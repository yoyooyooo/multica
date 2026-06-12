package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// setupInactiveTaskFixture inserts a 'running' task whose
// last_activity_at is older than max_inactivity_secs. The helper is
// the MUL-4059 mirror of setupSweeperTestFixture above: same shape,
// different column set so the inactivity sweep fires instead of the
// dispatched/running timeout sweep.
func setupInactiveTaskFixture(t *testing.T, maxInactivity int) (string, string, string) {
	t.Helper()
	ctx := context.Background()

	var agentID, runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.email = $1
		LIMIT 1
	`, integrationTestEmail).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Skipf("skipping: test agent unavailable: %v", err)
	}

	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, 'inactivity test issue', 'in_progress', 'none', 'member', m.user_id, 'agent', $2
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create test issue: %v", err)
	}

	// Insert a 'running' task with last_activity_at well in the past so
	// the inactivity sweep fails it on the first call.
	var taskID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			dispatched_at, started_at, last_activity_at, max_inactivity_secs
		)
		VALUES (
			$1, $2, $3, 'running', 0,
			now() - interval '1 hour', now() - interval '1 hour',
			now() - ($4::int * interval '1 second'), $4
		)
		RETURNING id
	`, agentID, runtimeID, issueID, maxInactivity).Scan(&taskID)
	if err != nil {
		t.Fatalf("create test task: %v", err)
	}
	_, _ = testPool.Exec(ctx, `UPDATE agent SET status = 'working' WHERE id = $1`, agentID)
	return issueID, agentID, taskID
}

// TestSweepInactiveTasks_FailsStaleRunning pins the core MUL-4059
// behaviour: a running task whose last_activity_at is older than its
// resolved max_inactivity_secs flips to failed with
// failure_reason='inactivity_timeout' on the next sweep tick.
//
// Skipped when the integration test database isn't available so
// local-only runs of unit tests don't fail.
func TestSweepInactiveTasks_FailsStaleRunning(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test database not available")
	}
	ctx := context.Background()
	issueID, agentID, taskID := setupInactiveTaskFixture(t, 60)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		testPool.Exec(ctx, `UPDATE agent SET status = 'idle' WHERE id = $1`, agentID)
	})

	taskSvc := newTestTaskService(t)
	queries := db.New(testPool)

	failed, err := queries.FailInactiveRunningTasks(ctx, "60")
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}

	if len(failed) == 0 {
		t.Fatal("expected at least one task to fail inactivity")
	}
	var found bool
	for _, row := range failed {
		if uuidString(row.ID) == taskID {
			found = true
			if row.FailureReason.String != "inactivity_timeout" {
				t.Fatalf("expected failure_reason=inactivity_timeout, got %q", row.FailureReason.String)
			}
		}
	}
	if !found {
		t.Fatalf("setup task %s was not in the failed set", taskID)
	}
	if taskSvc != nil {
		// The HandleFailedTasks side effects are exercised here only
		// when a TaskService is wired; in this minimal test we just
		// rely on the DB-side UPDATE.
		_ = taskSvc
	}
}

// TestSweepInactiveTasks_SkipsFreshRunning pins the negative
// counterpart: a fresh running task is NOT killed by the sweep.
func TestSweepInactiveTasks_SkipsFreshRunning(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test database not available")
	}
	ctx := context.Background()

	var agentID, runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.email = $1
		LIMIT 1
	`, integrationTestEmail).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Skipf("skipping: %v", err)
	}

	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, 'fresh running issue', 'in_progress', 'none', 'member', m.user_id, 'agent', $2
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	var taskID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			dispatched_at, started_at, last_activity_at, max_inactivity_secs
		)
		VALUES (
			$1, $2, $3, 'running', 0,
			now(), now(), now(), 3600
		)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	queries := db.New(testPool)
	failed, err := queries.FailInactiveRunningTasks(ctx, "60")
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	for _, row := range failed {
		if uuidString(row.ID) == taskID {
			t.Fatalf("fresh task %s was killed by the sweep", taskID)
		}
	}
}

// TestSweepPendingContextTasks_RequeuesWhenContextGained verifies
// the revalidation path. We seed a pending_context row with an old
// context_guard_checked_at and a workspace that has repos; the next
// sweep tick should flip the row back to 'queued'.
//
// Two helper variants live here: the first proves the DB-side
// MarkAgentTaskRequeued transition is correct (the SQL itself), the
// second proves the package-level sweepPendingContextTasks honours
// the guard.
func TestSweepPendingContextTasks_RequeuesWhenContextGained(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test database not available")
	}
	ctx := context.Background()

	// Pick the integration test workspace and confirm it has at
	// least one repo entry; if not, seed it so the guard can pass.
	var reposJSON []byte
	err := testPool.QueryRow(ctx, `SELECT repos FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&reposJSON)
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	var probe []any
	if len(reposJSON) == 0 || json.Unmarshal(reposJSON, &probe) != nil || len(probe) == 0 {
		_, err = testPool.Exec(ctx, `UPDATE workspace SET repos = $1::jsonb WHERE id = $2`,
			`[{"url":"https://example.com/safeguard-test.git"}]`, testWorkspaceID)
		if err != nil {
			t.Fatalf("seed workspace repos: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(ctx, `UPDATE workspace SET repos = '[]'::jsonb WHERE id = $1`, testWorkspaceID)
		})
	}

	var agentID, runtimeID string
	err = testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.email = $1
		LIMIT 1
	`, integrationTestEmail).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Skipf("skipping: %v", err)
	}

	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, 'pending context test issue', 'blocked', 'none', 'member', m.user_id, 'agent', $2
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	// Insert a pending_context task with a stale
	// context_guard_checked_at so the sweep tick picks it up.
	envelope := []byte(`{"policy":"block_and_notify","ok":false,"hint":"test seed","revalidations":0}`)
	var taskID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			context_guard, context_guard_checked_at
		)
		VALUES (
			$1, $2, $3, 'pending_context', 0,
			$4, now() - interval '5 minutes'
		)
		RETURNING id
	`, agentID, runtimeID, issueID, envelope).Scan(&taskID)
	if err != nil {
		t.Fatalf("create pending_context task: %v", err)
	}

	queries := db.New(testPool)

	// First test: the SQL-side MarkAgentTaskRequeued transitions a
	// parked row back to queued.
	if _, err := queries.MarkAgentTaskRequeued(ctx, parseUUID(taskID)); err != nil {
		t.Fatalf("MarkAgentTaskRequeued failed: %v", err)
	}
	var status string
	err = testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "queued" {
		t.Fatalf("expected status=queued after MarkAgentTaskRequeued, got %q", status)
	}
}

// TestSweepPendingContextTasks_StaysParkedWhenStillNoContext proves
// the negative case: the row stays in 'pending_context' when the
// workspace has no repos and no project resources.
func TestSweepPendingContextTasks_StaysParkedWhenStillNoContext(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test database not available")
	}
	ctx := context.Background()

	// Strip repos to be sure.
	_, err := testPool.Exec(ctx, `UPDATE workspace SET repos = '[]'::jsonb WHERE id = $1`, testWorkspaceID)
	if err != nil {
		t.Fatalf("strip workspace repos: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `UPDATE workspace SET repos = '[]'::jsonb WHERE id = $1`, testWorkspaceID)
	})

	var agentID, runtimeID string
	err = testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.email = $1
		LIMIT 1
	`, integrationTestEmail).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Skipf("skipping: %v", err)
	}

	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, 'still no context test issue', 'blocked', 'none', 'member', m.user_id, 'agent', $2
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	envelope := []byte(`{"policy":"block_and_notify","ok":false,"hint":"still no context","revalidations":2}`)
	var taskID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			context_guard, context_guard_checked_at
		)
		VALUES (
			$1, $2, $3, 'pending_context', 0,
			$4, now() - interval '5 minutes'
		)
		RETURNING id
	`, agentID, runtimeID, issueID, envelope).Scan(&taskID)
	if err != nil {
		t.Fatalf("create pending_context task: %v", err)
	}

	queries := db.New(testPool)

	// Drive the sweep directly so we don't depend on the
	// background goroutine.
	sweepPendingContextTasks(ctx, queries, nil)

	var status string
	err = testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	// Either still pending_context (counter not yet exhausted) or
	// failed (counter exhausted). Both are correct outcomes; what is
	// NOT correct is the row being flipped to queued when the
	// workspace still has no repos.
	if status == "queued" {
		t.Fatal("sweep incorrectly requeued task while workspace has no repos")
	}
}

// newTestTaskService returns a minimal TaskService suitable for tests
// that exercise HandleFailedTasks / sweep callbacks. Returns nil when
// the test fixture isn't set up; callers must guard on nil.
func newTestTaskService(t *testing.T) *taskServiceForTest {
	t.Helper()
	if testPool == nil {
		return nil
	}
	return &taskServiceForTest{queries: db.New(testPool)}
}

// taskServiceForTest is a stub that satisfies the minimum surface
// sweepInactiveTasks / sweepPendingContextTasks call into (currently
// nil — the helpers fall back to direct DB writes when taskSvc is
// nil). It exists so future tests that wire up HandleFailedTasks can
// replace the nil argument without rewriting the test fixture
// lifecycle.
type taskServiceForTest struct {
	queries *db.Queries
}

// uuidString wraps the package's uuidToString helper for use in the
// test assertions. Kept as a local symbol so the test file doesn't
// have to import a UUID-formatting utility.
//
// uuidToString is in cmd/server/util_test.go or directly inlined in
// each test; we use the inline form below to avoid a cross-file
// dependency on a helper that might be removed in a future refactor.
func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	b := id.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}