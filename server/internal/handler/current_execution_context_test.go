package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newCurrentExecutionContextRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/integrations/current-execution-context", nil)
}

func TestGetCurrentExecutionContextFallsBackGenerationToTaskID(t *testing.T) {
	agentID := createHandlerTestAgent(t, "fallback-generation-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgent(t, agentID)
	// Ensure execution_id is NULL so generation falls back to task id.
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET execution_id=NULL WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `
		UPDATE task_token SET execution_id=NULL
		WHERE token_hash=(SELECT token_hash FROM task_token WHERE task_id=$1 ORDER BY created_at DESC LIMIT 1)
	`, taskID); err != nil {
		// best-effort; lock path may not require token generation when null
		t.Logf("token execution_id clear: %v", err)
	}
	req := newCurrentExecutionContextRequest()
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()
	testHandler.GetCurrentExecutionContext(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	claim := response["claim"].(map[string]any)
	run := response["run"].(map[string]any)
	if claim["generation"] != taskID || claim["task_id"] != taskID || run["id"] != taskID || run["task_id"] != taskID {
		t.Fatalf("fallback generation claim=%#v run=%#v", claim, run)
	}
}

func authorizeCurrentExecutionContextTestTask(t *testing.T, req *http.Request, agentID, taskID string) string {
	t.Helper()
	tokenHash := uuid.NewString()
	if _, err := testHandler.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{
		TokenHash: tokenHash,
		TaskID:    parseUUID(taskID), AgentID: parseUUID(agentID),
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create current execution context task token: %v", err)
	}
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Task-Token-Hash", tokenHash)
	return tokenHash
}

func sortedJSONKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestGetCurrentExecutionContextRejectsStaleGenerationToken(t *testing.T) {
	agentID := createHandlerTestAgent(t, "stale-generation-context-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgent(t, agentID)
	executionID := uuid.NewString()
	staleGeneration := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET execution_id=$2 WHERE id=$1`, taskID, executionID); err != nil {
		t.Fatal(err)
	}
	// Bind token to a different generation than the running task row.
	if _, err := testPool.Exec(context.Background(), `
		UPDATE task_token SET execution_id=$2
		WHERE token_hash=(SELECT token_hash FROM task_token WHERE task_id=$1 ORDER BY created_at DESC LIMIT 1)
	`, taskID, staleGeneration); err != nil {
		t.Fatal(err)
	}
	req := newCurrentExecutionContextRequest()
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()
	testHandler.GetCurrentExecutionContext(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s, want 401 for generation mismatch", rr.Code, rr.Body.String())
	}
}

func TestGetCurrentExecutionContextRequiresTaskTokenActor(t *testing.T) {
	req := newCurrentExecutionContextRequest()
	req.Header.Set("X-Actor-Source", "member")
	rr := httptest.NewRecorder()
	testHandler.GetCurrentExecutionContext(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGetCurrentExecutionContextReturnsOnlyTokenBoundServerFacts(t *testing.T) {
	issueID := createExternalPRTestIssue(t, "current execution context issue", "in_review", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "current-execution-context-agent", []byte(`{}`))

	var squadID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, instructions)
		VALUES ($1, 'current-execution-context-squad', '', $2, $3, '')
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create context squad: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE id=$1`, squadID) })

	daemonID := uuid.NewString()
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, $2, 'current execution context runtime', 'local', 'pi', 'online', '{}'::jsonb, '{}'::jsonb, $3, now())
		RETURNING id
	`, testWorkspaceID, daemonID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create context runtime: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, runtimeID) })

	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET runtime_id=$2 WHERE id=$1`, taskID, runtimeID); err != nil {
		t.Fatalf("bind task to context runtime: %v", err)
	}
	req := newCurrentExecutionContextRequest()
	tokenHash := authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
	executionID := uuid.NewString()
	var triggerID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, 'current execution context trigger', 'comment')
		RETURNING id
	`, testWorkspaceID, issueID, testUserID).Scan(&triggerID); err != nil {
		t.Fatalf("create context trigger comment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE agent_task_queue SET trigger_comment_id=NULL WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE id=$1`, triggerID)
	})
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET execution_id=$2, squad_id=$3, trigger_comment_id=$4,
			originator_user_id=$5, accountable_user_id=$5,
			originator_source='direct_human',
			trigger_evidence_kind='comment', trigger_evidence_ref_id=$4,
			dispatched_at=now()-interval '2 seconds'
		WHERE id=$1
	`, taskID, executionID, squadID, triggerID, testUserID); err != nil {
		t.Fatalf("enrich context task: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE task_token SET execution_id=$2 WHERE token_hash=$1`, tokenHash, executionID); err != nil {
		t.Fatalf("bind context token to execution: %v", err)
	}

	// Caller headers are not identity inputs. The task token/task row remain authoritative.
	req.Header.Set("X-Agent-ID", uuid.NewString())
	rr := httptest.NewRecorder()
	testHandler.GetCurrentExecutionContext(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}

	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode context response: %v", err)
	}
	if got, want := sortedJSONKeys(response), []string{"agent", "attribution", "claim", "issue", "observed_at", "run", "runtime", "schema", "squad", "task", "trigger", "workspace"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level keys=%v want=%v", got, want)
	}
	if response["schema"] != "multica.current-execution-context.v2" {
		t.Fatalf("schema=%v", response["schema"])
	}
	if _, err := time.Parse(time.RFC3339Nano, response["observed_at"].(string)); err != nil {
		t.Fatalf("observed_at=%v: %v", response["observed_at"], err)
	}

	workspace := response["workspace"].(map[string]any)
	if workspace["id"] != testWorkspaceID || workspace["slug"] == "" || workspace["name"] != nil {
		t.Fatalf("workspace=%#v", workspace)
	}
	agent := response["agent"].(map[string]any)
	if agent["id"] != agentID || agent["name"] != nil || agent["status"] != nil {
		t.Fatalf("agent=%#v", agent)
	}
	task := response["task"].(map[string]any)
	if task["id"] != taskID || task["status"] != "running" || task["attempt"] == nil || task["max_attempts"] != nil {
		t.Fatalf("task=%#v", task)
	}
	claim := response["claim"].(map[string]any)
	if claim["generation"] != executionID || claim["task_id"] != taskID {
		t.Fatalf("claim=%#v", claim)
	}
	run := response["run"].(map[string]any)
	if run["id"] != executionID || run["task_id"] != taskID || run["status"] != nil {
		t.Fatalf("run=%#v", run)
	}
	issue := response["issue"].(map[string]any)
	if issue["id"] != issueID || issue["key"] == "" || issue["title"] != nil || issue["status"] != nil {
		t.Fatalf("issue=%#v", issue)
	}
	if squad := response["squad"].(map[string]any); squad["id"] != squadID || squad["name"] != nil || squad["details_available"] != nil {
		t.Fatalf("squad=%#v", squad)
	}
	if runtime := response["runtime"].(map[string]any); runtime["id"] != runtimeID || runtime["daemon_id"] != daemonID || runtime["name"] != nil || runtime["provider"] != nil || runtime["status"] != nil {
		t.Fatalf("runtime=%#v", runtime)
	}
	trigger := response["trigger"].(map[string]any)
	if trigger["kind"] != "comment" || trigger["id"] != triggerID || trigger["comment_id"] != nil {
		t.Fatalf("trigger=%#v", trigger)
	}
	attribution := response["attribution"].(map[string]any)
	if attribution["source"] != "direct_human" || attribution["precise"] != true {
		t.Fatalf("attribution=%#v", attribution)
	}
	if attribution["initiator"].(map[string]any)["id"] != testUserID || attribution["originator"].(map[string]any)["id"] != testUserID {
		t.Fatalf("attribution users=%#v", attribution)
	}
	if evidence := attribution["evidence"].(map[string]any); evidence["kind"] != "comment" || evidence["ref_id"] != triggerID {
		t.Fatalf("attribution evidence=%#v", evidence)
	}
	forbidden := []string{"assertion", "authority", "policy_class", "requested_operation", "requested_capabilities", "provider_credential", "session"}
	encoded := rr.Body.String()
	for _, key := range forbidden {
		if json.Valid([]byte(encoded)) && containsJSONKey(response, key) {
			t.Fatalf("response contains forbidden policy/credential key %q: %s", key, encoded)
		}
	}
}

func containsJSONKey(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == target || containsJSONKey(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsJSONKey(child, target) {
				return true
			}
		}
	}
	return false
}

func TestGetCurrentExecutionContextOmitsUnavailableOptionalLineage(t *testing.T) {
	agentID := createHandlerTestAgent(t, "minimal-current-execution-context-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgent(t, agentID)
	req := newCurrentExecutionContextRequest()
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()
	testHandler.GetCurrentExecutionContext(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"issue", "squad", "trigger"} {
		if _, present := response[key]; present {
			t.Fatalf("optional %s should be absent: %#v", key, response[key])
		}
	}
	attribution := response["attribution"].(map[string]any)
	if attribution["source"] != "unattributed" || attribution["precise"] != false {
		t.Fatalf("attribution=%#v", attribution)
	}
	if _, present := attribution["initiator"]; present {
		t.Fatalf("unexpected initiator=%#v", attribution["initiator"])
	}
	if _, present := attribution["originator"]; present {
		t.Fatalf("unexpected originator=%#v", attribution["originator"])
	}
}

func TestCurrentExecutionContextAndLinkTokenStayOnSingleTransactionConnection(t *testing.T) {
	const linkSecret = "single-connection-link-token-secret"
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", linkSecret)
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE", "external-pr-link")
	issueID := createExternalPRTestIssue(t, "single connection current context", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "single-connection-current-context-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	cfg := testPool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	singlePool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(singlePool.Close)
	singleHandler := *testHandler
	singleHandler.Queries = db.New(singlePool)
	singleHandler.TxStarter = singlePool

	for _, tc := range []struct {
		name   string
		method string
		path   string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{name: "current context", method: http.MethodGet, path: "/api/integrations/current-execution-context", call: singleHandler.GetCurrentExecutionContext},
		{name: "external PR link token", method: http.MethodPost, path: "/api/integrations/external-pr/link-token", call: singleHandler.CreateExternalPRLinkToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
			ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
			defer cancel()
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			tc.call(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestGetCurrentExecutionContextKeepsUnavailableOptionalRuntimeAndSquadAsUnresolvedRefs(t *testing.T) {
	agentID := createHandlerTestAgent(t, "unresolved-current-execution-context-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgent(t, agentID)

	var foreignWorkspaceID, unavailableRuntimeID, unavailableSquadID string
	suffix := uuid.NewString()
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Current context foreign workspace', $1, '', 'CCF')
		RETURNING id
	`, "current-context-foreign-"+suffix).Scan(&foreignWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, 'Current context foreign runtime', 'cloud', 'test', 'online', '', '{}'::jsonb, $2, now())
		RETURNING id
	`, foreignWorkspaceID, testUserID).Scan(&unavailableRuntimeID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, instructions)
		VALUES ($1, 'Current context foreign squad', '', $2, $3, '')
		RETURNING id
	`, foreignWorkspaceID, agentID, testUserID).Scan(&unavailableSquadID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET runtime_id=$2, squad_id=$3 WHERE id=$1`, taskID, unavailableRuntimeID, unavailableSquadID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE agent_task_queue SET runtime_id=$2, squad_id=NULL WHERE id=$1`, taskID, testRuntimeID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE id=$1`, unavailableSquadID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, unavailableRuntimeID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, foreignWorkspaceID)
	})

	req := newCurrentExecutionContextRequest()
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()
	testHandler.GetCurrentExecutionContext(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	runtime := response["runtime"].(map[string]any)
	if runtime["id"] != unavailableRuntimeID || runtime["details_available"] != nil || len(runtime) != 1 {
		t.Fatalf("runtime=%#v", runtime)
	}
	squad := response["squad"].(map[string]any)
	if squad["id"] != unavailableSquadID || squad["details_available"] != nil || len(squad) != 1 {
		t.Fatalf("squad=%#v", squad)
	}
}

func TestGetCurrentExecutionContextLinearizesBeforeTaskCompletion(t *testing.T) {
	agentID := createHandlerTestAgent(t, "linearized-current-execution-context-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgent(t, agentID)
	req := newCurrentExecutionContextRequest()
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)

	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.CurrentExecutionContextHook = func(stage string) {
		if stage == "current_execution_context_locked" {
			close(locked)
			<-release
		}
	}
	t.Cleanup(func() { testHandler.CurrentExecutionContextHook = nil })

	rr := httptest.NewRecorder()
	contextDone := make(chan struct{})
	go func() {
		testHandler.GetCurrentExecutionContext(rr, req)
		close(contextDone)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("context read did not acquire task authority lock")
	}

	completionDone := make(chan error, 1)
	go func() {
		_, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1 AND status='running'`, taskID)
		completionDone <- err
	}()
	select {
	case err := <-completionDone:
		t.Fatalf("completion crossed context lock early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-contextDone:
	case <-time.After(5 * time.Second):
		t.Fatal("context read did not finish")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("context response=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case err := <-completionDone:
		if err != nil {
			t.Fatalf("complete task after context read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not resume after context read")
	}
	testHandler.CurrentExecutionContextHook = nil

	retry := httptest.NewRecorder()
	retryReq := newCurrentExecutionContextRequest()
	retryReq.Header = req.Header.Clone()
	testHandler.GetCurrentExecutionContext(retry, retryReq)
	if retry.Code != http.StatusUnauthorized {
		t.Fatalf("post-completion context response=%d body=%s", retry.Code, retry.Body.String())
	}
}

func TestGetCurrentExecutionContextRejectsTerminalRevokedAndCrossTaskAuthority(t *testing.T) {
	issueID := createExternalPRTestIssue(t, "current context authority", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "current-context-authority-agent", []byte(`{}`))
	otherAgentID := createHandlerTestAgent(t, "other-current-context-agent", []byte(`{}`))

	t.Run("cross task and agent", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		otherTaskID := createHandlerTestTaskForAgentOnIssue(t, otherAgentID, issueID)
		req := newCurrentExecutionContextRequest()
		authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		req.Header.Set("X-Task-ID", otherTaskID)
		req.Header.Set("X-Agent-ID", otherAgentID)
		rr := httptest.NewRecorder()
		testHandler.GetCurrentExecutionContext(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("cross-task status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("cross workspace", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		req := newCurrentExecutionContextRequest()
		authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		req.Header.Set("X-Workspace-ID", uuid.NewString())
		rr := httptest.NewRecorder()
		testHandler.GetCurrentExecutionContext(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("cross-workspace status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run("terminal "+status, func(t *testing.T) {
			taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
			req := newCurrentExecutionContextRequest()
			authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
			if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status=$2, completed_at=now() WHERE id=$1`, taskID, status); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			testHandler.GetCurrentExecutionContext(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("terminal %s status=%d body=%s", status, rr.Code, rr.Body.String())
			}
		})
	}

	t.Run("revoked token", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		req := newCurrentExecutionContextRequest()
		tokenHash := authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		if _, err := testPool.Exec(context.Background(), `DELETE FROM task_token WHERE token_hash=$1`, tokenHash); err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		testHandler.GetCurrentExecutionContext(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("revoked status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}
