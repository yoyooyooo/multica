package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRerunIssueFreshPrivateAgentDeniedBeforeMutation(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID, _, memberID := privateAgentTestFixture(t)
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id,
			assignee_type, assignee_id, number
		)
		VALUES (
			$1, 'Private rerun gate fixture', 'in_progress', 'none', 'member', $2,
			'agent', $3,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1)
		)
		RETURNING id
	`, testWorkspaceID, memberID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	var sourceTaskID, queuedTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			started_at, completed_at, failure_reason
		)
		SELECT id, runtime_id, $2, 'failed', 0, now(), now(), 'agent_error'
		FROM agent WHERE id = $1
		RETURNING id
	`, agentID, issueID).Scan(&sourceTaskID); err != nil {
		t.Fatalf("create source task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		SELECT id, runtime_id, $2, 'queued', 0 FROM agent WHERE id = $1
		RETURNING id
	`, agentID, issueID).Scan(&queuedTaskID); err != nil {
		t.Fatalf("create existing queued task: %v", err)
	}

	req := newRequestAs(memberID, http.MethodPost, "/api/issues/"+issueID+"/rerun-fresh", map[string]any{"task_id": sourceTaskID})
	req = withURLParam(req, "id", issueID)
	w := httptest.NewRecorder()
	testHandler.RerunIssueFresh(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, queuedTaskID).Scan(&status); err != nil {
		t.Fatalf("read existing task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("existing task status = %q, want queued", status)
	}
}

func TestDecodeRerunIssueRequest(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		req, err := decodeRerunIssueRequest(strings.NewReader(""))
		if err != nil {
			t.Fatalf("decode empty body: %v", err)
		}
		if req.TaskID != "" {
			t.Fatalf("TaskID = %q, want empty", req.TaskID)
		}
	})

	t.Run("unknown-length body content", func(t *testing.T) {
		req, err := decodeRerunIssueRequest(strings.NewReader(`{"task_id":"task-123"}`))
		if err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.TaskID != "task-123" {
			t.Fatalf("TaskID = %q, want task-123", req.TaskID)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if _, err := decodeRerunIssueRequest(strings.NewReader(`{"task_id":`)); err == nil {
			t.Fatal("expected invalid JSON error")
		}
	})

	t.Run("trailing garbage", func(t *testing.T) {
		if _, err := decodeRerunIssueRequest(strings.NewReader(`{"task_id":"task-123"}garbage`)); err == nil {
			t.Fatal("expected trailing garbage error")
		}
	})

	t.Run("multiple JSON values", func(t *testing.T) {
		if _, err := decodeRerunIssueRequest(strings.NewReader(`{"task_id":"task-123"}{}`)); err == nil {
			t.Fatal("expected multiple JSON values error")
		}
	})
}
