package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkCoordinationCLIProcessesAggregatePassiveFlow(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("wcs-v5-owner-%d@multica.test", suffix)
	otherEmail := fmt.Sprintf("wcs-v5-other-%d@multica.test", suffix)
	workspaceSlug := fmt.Sprintf("wcs-v5-cli-%d", suffix)

	var ownerID, otherID, workspaceID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('WCS V5 Owner',$1) RETURNING id`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('WCS V5 Other',$1) RETURNING id`, otherEmail).Scan(&otherID); err != nil {
		t.Fatalf("insert other actor: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name,slug,description,issue_prefix)
VALUES ('WCS V5 CLI',$1,'passive aggregate CLI test','WCV') RETURNING id`, workspaceSlug).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id,user_id,role)
VALUES ($1,$2,'owner'),($1,$3,'member')`, workspaceID, ownerID, otherID); err != nil {
		t.Fatalf("insert members: %v", err)
	}

	var rootID, downstreamID, upstreamID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number)
VALUES ($1,'WCS V5 Root','member',$2,'none',1) RETURNING id`, workspaceID, ownerID).Scan(&rootID); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id)
VALUES ($1,'WCS V5 Downstream','member',$2,'none',2,$3) RETURNING id`, workspaceID, ownerID, rootID).Scan(&downstreamID); err != nil {
		t.Fatalf("insert downstream: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id)
VALUES ($1,'WCS V5 Upstream','member',$2,'none',3,$3) RETURNING id`, workspaceID, ownerID, rootID).Scan(&upstreamID); err != nil {
		t.Fatalf("insert upstream: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, statement := range []string{
			`DELETE FROM coordination_record_issue_ref WHERE workspace_id=$1`,
			`DELETE FROM coordination_record WHERE workspace_id=$1`,
			`DELETE FROM coordination_dependency WHERE workspace_id=$1`,
			`DELETE FROM coordination_receipt WHERE workspace_id=$1`,
			`DELETE FROM coordination_scope WHERE workspace_id=$1`,
			`DELETE FROM workspace WHERE id=$1`,
		} {
			if _, err := testPool.Exec(cleanupCtx, statement, workspaceID); err != nil {
				t.Errorf("cleanup workspace fixture: %v", err)
			}
		}
		if _, err := testPool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id=ANY($1::uuid[])`, []string{ownerID, otherID}); err != nil {
			t.Errorf("cleanup users: %v", err)
		}
	})

	issueIDs := []string{rootID, downstreamID, upstreamID}
	before := captureCoordinationCLIPassiveSnapshots(t, workspaceID, issueIDs)
	legacyBefore := captureCoordinationCLILegacyDependencies(t, workspaceID, issueIDs)

	ownerToken, err := generateTestJWT(ownerID, ownerEmail, "WCS V5 Owner")
	if err != nil {
		t.Fatalf("owner token: %v", err)
	}
	otherToken, err := generateTestJWT(otherID, otherEmail, "WCS V5 Other")
	if err != nil {
		t.Fatalf("other token: %v", err)
	}
	binary := buildCoordinationCLIBinary(t)
	rootDir := t.TempDir()
	ownerA := coordinationCLIProcessEnv(t, filepath.Join(rootDir, "owner-a"), workspaceID, ownerToken)
	ownerB := coordinationCLIProcessEnv(t, filepath.Join(rootDir, "owner-b"), workspaceID, ownerToken)
	other := coordinationCLIProcessEnv(t, filepath.Join(rootDir, "other"), workspaceID, otherToken)
	payloadPath := filepath.Join(rootDir, "blocker.json")
	resolutionPath := filepath.Join(rootDir, "resolution.json")
	if err := os.WriteFile(payloadPath, []byte(fmt.Sprintf(`{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"issue","id":%q}]}`, downstreamID)), 0o600); err != nil {
		t.Fatalf("write blocker payload: %v", err)
	}
	if err := os.WriteFile(resolutionPath, []byte(`{"resolution_code":"no_longer_blocking","evidence_refs":[]}`), 0o600); err != nil {
		t.Fatalf("write blocker resolution: %v", err)
	}

	ensure := runCoordinationCLIProcess(t, binary, ownerA,
		"coordination", "scope", "ensure", "--root", rootID, "--workflow-profile", "v5-cli-e2e",
		"--idempotency-key", "v5-cli-scope", "--output=json")
	requireCoordinationCLIExit(t, ensure, 0)
	var ensured struct {
		Scope struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"scope"`
		Receipt struct {
			ReceiptOrdinal int64 `json:"receipt_ordinal"`
		} `json:"receipt"`
	}
	decodeSingleCoordinationCLIJSON(t, ensure.Stdout, &ensured)
	if ensured.Scope.ID == "" || ensured.Scope.Revision != 0 || ensured.Receipt.ReceiptOrdinal != 1 {
		t.Fatalf("ensure output=%+v", ensured)
	}

	dependencyArgs := []string{
		"coordination", "dependency", "add", "--scope", ensured.Scope.ID,
		"--downstream", downstreamID, "--upstream", upstreamID,
		"--expected-revision", "0", "--idempotency-key", "v5-cli-dependency", "--output=json",
	}
	dependency := runCoordinationCLIProcess(t, binary, ownerA, dependencyArgs...)
	requireCoordinationCLIExit(t, dependency, 0)
	var dependencyCreated struct {
		Dependency struct {
			ID string `json:"id"`
		} `json:"dependency"`
		ScopeRevision int64  `json:"scope_revision"`
		Outcome       string `json:"outcome"`
		Receipt       struct {
			ReceiptOrdinal int64 `json:"receipt_ordinal"`
		} `json:"receipt"`
	}
	decodeSingleCoordinationCLIJSON(t, dependency.Stdout, &dependencyCreated)
	if dependencyCreated.Dependency.ID == "" || dependencyCreated.ScopeRevision != 1 || dependencyCreated.Outcome != "created" || dependencyCreated.Receipt.ReceiptOrdinal != 2 {
		t.Fatalf("dependency output=%+v", dependencyCreated)
	}

	blockerArgs := []string{
		"coordination", "blocker", "add", "--scope", ensured.Scope.ID,
		"--downstream", downstreamID, "--upstream", upstreamID, "--dependency", dependencyCreated.Dependency.ID,
		"--payload-file", payloadPath, "--expected-revision", "1", "--idempotency-key", "v5-cli-blocker", "--output=json",
	}
	blocker := runCoordinationCLIProcess(t, binary, ownerA, blockerArgs...)
	requireCoordinationCLIExit(t, blocker, 0)
	var blockerCreated coordinationCLIBlockerMutation
	decodeSingleCoordinationCLIJSON(t, blocker.Stdout, &blockerCreated)
	if blockerCreated.Resource.ID == "" || blockerCreated.ScopeRevision != 2 || !blockerCreated.Changed || blockerCreated.Replayed || blockerCreated.Receipt.ReceiptOrdinal != 3 {
		t.Fatalf("blocker output=%+v", blockerCreated)
	}

	active := inspectCoordinationCLIProcess(t, binary, ownerB, ensured.Scope.ID)
	if active.ScopeRevision != 2 || len(active.ActiveDependencies) != 1 || len(active.OpenBlockers) != 1 || len(active.ReceiptRefs) != 3 || active.NextReceiptCursor != nil {
		t.Fatalf("active inspection=%+v", active)
	}
	assertCoordinationCLIReceiptOrdinals(t, active.ReceiptRefs, 3, 2, 1)

	dependencyReplay := runCoordinationCLIProcess(t, binary, ownerB, dependencyArgs...)
	requireCoordinationCLIExit(t, dependencyReplay, 0)
	var replayedDependency struct {
		Dependency struct {
			ID string `json:"id"`
		} `json:"dependency"`
		ScopeRevision int64  `json:"scope_revision"`
		Outcome       string `json:"outcome"`
		Receipt       struct {
			ReceiptOrdinal int64 `json:"receipt_ordinal"`
		} `json:"receipt"`
	}
	decodeSingleCoordinationCLIJSON(t, dependencyReplay.Stdout, &replayedDependency)
	if replayedDependency.Outcome != "replay" || replayedDependency.ScopeRevision != 1 || replayedDependency.Receipt.ReceiptOrdinal != 2 || replayedDependency.Dependency.ID != dependencyCreated.Dependency.ID {
		t.Fatalf("dependency replay=%+v", replayedDependency)
	}
	blockerReplay := runCoordinationCLIProcess(t, binary, ownerB, blockerArgs...)
	requireCoordinationCLIExit(t, blockerReplay, 0)
	var replayedBlocker coordinationCLIBlockerMutation
	decodeSingleCoordinationCLIJSON(t, blockerReplay.Stdout, &replayedBlocker)
	if !replayedBlocker.Replayed || replayedBlocker.ScopeRevision != 2 || replayedBlocker.Receipt.ReceiptOrdinal != 3 || replayedBlocker.Resource.ID != blockerCreated.Resource.ID {
		t.Fatalf("blocker replay=%+v", replayedBlocker)
	}

	actorConflict := runCoordinationCLIProcess(t, binary, other, dependencyArgs...)
	requireCoordinationCLIExit(t, actorConflict, 6)
	if len(bytes.TrimSpace(actorConflict.Stdout)) != 0 || coordinationCLIErrorCode(t, actorConflict.Stderr) != "coordination_idempotency_conflict" {
		t.Fatalf("different-actor conflict stdout=%q stderr=%q", actorConflict.Stdout, actorConflict.Stderr)
	}

	resolveBlocker := runCoordinationCLIProcess(t, binary, ownerB,
		"coordination", "blocker", "resolve", "--scope", ensured.Scope.ID, "--blocker", blockerCreated.Resource.ID,
		"--resolution-file", resolutionPath, "--expected-revision", "2", "--idempotency-key", "v5-cli-resolve-blocker", "--output=json")
	requireCoordinationCLIExit(t, resolveBlocker, 0)
	var blockerResolved coordinationCLIBlockerMutation
	decodeSingleCoordinationCLIJSON(t, resolveBlocker.Stdout, &blockerResolved)
	if blockerResolved.ScopeRevision != 3 || !blockerResolved.Changed || blockerResolved.Replayed || blockerResolved.Receipt.ReceiptOrdinal != 4 {
		t.Fatalf("blocker resolve=%+v", blockerResolved)
	}
	middle := inspectCoordinationCLIProcess(t, binary, ownerA, ensured.Scope.ID)
	if middle.ScopeRevision != 3 || len(middle.ActiveDependencies) != 1 || len(middle.OpenBlockers) != 0 {
		t.Fatalf("middle inspection=%+v", middle)
	}

	resolveDependency := runCoordinationCLIProcess(t, binary, ownerA,
		"coordination", "dependency", "resolve", "--scope", ensured.Scope.ID, "--dependency", dependencyCreated.Dependency.ID,
		"--expected-revision", "3", "--idempotency-key", "v5-cli-resolve-dependency", "--output=json")
	requireCoordinationCLIExit(t, resolveDependency, 0)
	var dependencyResolved struct {
		ScopeRevision int64  `json:"scope_revision"`
		Outcome       string `json:"outcome"`
		Receipt       struct {
			ReceiptOrdinal int64 `json:"receipt_ordinal"`
		} `json:"receipt"`
	}
	decodeSingleCoordinationCLIJSON(t, resolveDependency.Stdout, &dependencyResolved)
	if dependencyResolved.ScopeRevision != 4 || dependencyResolved.Outcome != "resolved" || dependencyResolved.Receipt.ReceiptOrdinal != 5 {
		t.Fatalf("dependency resolve=%+v", dependencyResolved)
	}
	finalInspection := inspectCoordinationCLIProcess(t, binary, ownerB, ensured.Scope.ID)
	if finalInspection.ScopeRevision != 4 || len(finalInspection.ActiveDependencies) != 0 || len(finalInspection.OpenBlockers) != 0 || len(finalInspection.ReceiptRefs) != 5 {
		t.Fatalf("final inspection=%+v", finalInspection)
	}
	assertCoordinationCLIReceiptOrdinals(t, finalInspection.ReceiptRefs, 5, 4, 3, 2, 1)

	after := captureCoordinationCLIPassiveSnapshots(t, workspaceID, issueIDs)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("CLI coordination flow changed Issue/task/Autopilot facts\nbefore=%+v\nafter=%+v", before, after)
	}
	legacyAfter := captureCoordinationCLILegacyDependencies(t, workspaceID, issueIDs)
	if legacyBefore != legacyAfter {
		t.Fatalf("CLI coordination flow changed legacy issue_dependency\nbefore=%s\nafter=%s", legacyBefore, legacyAfter)
	}
}

type coordinationCLIProcessResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

type coordinationCLIBlockerMutation struct {
	Resource struct {
		ID string `json:"id"`
	} `json:"resource"`
	ScopeRevision int64 `json:"scope_revision"`
	Changed       bool  `json:"changed"`
	Replayed      bool  `json:"replayed"`
	Receipt       struct {
		ReceiptOrdinal int64 `json:"receipt_ordinal"`
	} `json:"receipt"`
}

type coordinationCLIInspection struct {
	ScopeRevision      int64             `json:"scope_revision"`
	ActiveDependencies []json.RawMessage `json:"active_dependencies"`
	OpenBlockers       []json.RawMessage `json:"open_blockers"`
	ReceiptRefs        []struct {
		ReceiptOrdinal int64 `json:"receipt_ordinal"`
	} `json:"receipt_refs"`
	NextReceiptCursor *string `json:"next_receipt_cursor"`
}

type coordinationCLIPassiveSnapshot struct {
	Status        string
	AssigneeType  string
	AssigneeID    string
	UpdatedAt     time.Time
	Metadata      string
	CommentCount  int64
	ActiveTasks   int64
	TotalTasks    int64
	AutopilotRuns int64
}

func buildCoordinationCLIBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "multica")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "github.com/multica-ai/multica/server/cmd/multica")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build multica CLI: %v output=%s", err, output)
	}
	return binary
}

func coordinationCLIProcessEnv(t *testing.T, home, workspaceID, token string) []string {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("create CLI home: %v", err)
	}
	overridden := map[string]struct{}{
		"HOME": {}, "XDG_CONFIG_HOME": {}, "MULTICA_SERVER_URL": {}, "MULTICA_WORKSPACE_ID": {}, "MULTICA_TOKEN": {},
		"MULTICA_AGENT_ID": {}, "MULTICA_TASK_ID": {}, "MULTICA_DAEMON_PORT": {}, "MULTICA_COORDINATION_HELPER": {},
	}
	env := make([]string, 0, len(os.Environ())+9)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := overridden[key]; !skip {
			env = append(env, entry)
		}
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"MULTICA_SERVER_URL="+testServer.URL,
		"MULTICA_WORKSPACE_ID="+workspaceID,
		"MULTICA_TOKEN="+token,
		"MULTICA_AGENT_ID=",
		"MULTICA_TASK_ID=",
		"MULTICA_DAEMON_PORT=",
		"MULTICA_COORDINATION_HELPER=",
	)
}

func runCoordinationCLIProcess(t *testing.T, binary string, env []string, args ...string) coordinationCLIProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI process timed out: %v", args)
	}
	result := coordinationCLIProcessResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run CLI process %v: %v", args, err)
	}
	result.ExitCode = exitErr.ExitCode()
	return result
}

func requireCoordinationCLIExit(t *testing.T, result coordinationCLIProcessResult, want int) {
	t.Helper()
	if result.ExitCode != want {
		t.Fatalf("CLI exit=%d want=%d stdout=%q stderr=%q", result.ExitCode, want, result.Stdout, result.Stderr)
	}
	if want == 0 && len(bytes.TrimSpace(result.Stderr)) != 0 {
		t.Fatalf("successful CLI wrote stderr=%q", result.Stderr)
	}
}

func decodeSingleCoordinationCLIJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode CLI JSON %q: %v", data, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("CLI stdout contains trailing JSON/prose %q: %v", data, err)
	}
}

func coordinationCLIErrorCode(t *testing.T, data []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeSingleCoordinationCLIJSON(t, data, &envelope)
	return envelope.Error.Code
}

func inspectCoordinationCLIProcess(t *testing.T, binary string, env []string, scopeID string) coordinationCLIInspection {
	t.Helper()
	result := runCoordinationCLIProcess(t, binary, env,
		"coordination", "inspect", "--scope", scopeID, "--output=json")
	requireCoordinationCLIExit(t, result, 0)
	var inspection coordinationCLIInspection
	decodeSingleCoordinationCLIJSON(t, result.Stdout, &inspection)
	return inspection
}

func assertCoordinationCLIReceiptOrdinals(t *testing.T, refs []struct {
	ReceiptOrdinal int64 `json:"receipt_ordinal"`
}, want ...int64) {
	t.Helper()
	if len(refs) != len(want) {
		t.Fatalf("receipt count=%d want=%d", len(refs), len(want))
	}
	for index := range want {
		if refs[index].ReceiptOrdinal != want[index] {
			t.Fatalf("receipt[%d]=%d want=%d", index, refs[index].ReceiptOrdinal, want[index])
		}
	}
}

func captureCoordinationCLIPassiveSnapshots(t *testing.T, workspaceID string, issueIDs []string) map[string]coordinationCLIPassiveSnapshot {
	t.Helper()
	result := make(map[string]coordinationCLIPassiveSnapshot, len(issueIDs))
	for _, issueID := range issueIDs {
		var snapshot coordinationCLIPassiveSnapshot
		if err := testPool.QueryRow(context.Background(), `
SELECT i.status,
       COALESCE(i.assignee_type,''),
       COALESCE(i.assignee_id::text,''),
       i.updated_at,
       COALESCE(i.metadata::text,'null'),
       (SELECT count(*) FROM comment c WHERE c.workspace_id=$1 AND c.issue_id=i.id),
       (SELECT count(*) FROM agent_task_queue q JOIN issue qi ON qi.id=q.issue_id WHERE qi.workspace_id=$1 AND q.issue_id=i.id AND q.status IN ('queued','dispatched','running','waiting_local_directory','deferred')),
       (SELECT count(*) FROM agent_task_queue q JOIN issue qi ON qi.id=q.issue_id WHERE qi.workspace_id=$1 AND q.issue_id=i.id),
       (SELECT count(*) FROM autopilot_run r JOIN issue ri ON ri.id=r.issue_id WHERE ri.workspace_id=$1 AND r.issue_id=i.id)
FROM issue i WHERE i.workspace_id=$1 AND i.id=$2`, workspaceID, issueID).Scan(
			&snapshot.Status, &snapshot.AssigneeType, &snapshot.AssigneeID, &snapshot.UpdatedAt, &snapshot.Metadata,
			&snapshot.CommentCount, &snapshot.ActiveTasks, &snapshot.TotalTasks, &snapshot.AutopilotRuns,
		); err != nil {
			t.Fatalf("capture passive issue snapshot: %v", err)
		}
		result[issueID] = snapshot
	}
	return result
}

func captureCoordinationCLILegacyDependencies(t *testing.T, workspaceID string, issueIDs []string) string {
	t.Helper()
	var snapshot string
	if err := testPool.QueryRow(context.Background(), `
SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY d.id)::text,'[]')
FROM issue_dependency d
JOIN issue downstream ON downstream.id=d.issue_id
JOIN issue upstream ON upstream.id=d.depends_on_issue_id
WHERE downstream.workspace_id=$1
  AND upstream.workspace_id=$1
  AND (d.issue_id=ANY($2::uuid[]) OR d.depends_on_issue_id=ANY($2::uuid[]))`, workspaceID, issueIDs).Scan(&snapshot); err != nil {
		t.Fatalf("capture legacy dependencies: %v", err)
	}
	return snapshot
}
