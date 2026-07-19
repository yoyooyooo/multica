package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestWorkCoordinationHandlerStrictWireContract(t *testing.T) {
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, priority, number, status)
		VALUES ($1, $2, 'member', $3, 'none', 990101, 'backlog')
		RETURNING id`, testWorkspaceID, "WCS wire root "+fmt.Sprint(time.Now().UnixNano()), testUserID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})

	before := workCoordinationIssueFacts(t, ctx, issueID)
	body := map[string]string{"root_issue_id": issueID, "workflow_profile_key": "matt-loop"}
	req := newRequest(http.MethodPost, "/api/coordination/scopes", body)
	req.Header.Set("Idempotency-Key", "wire-contract-1")
	w := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	after := workCoordinationIssueFacts(t, ctx, issueID)
	if before != after {
		t.Fatalf("ensure mutated issue-side facts: before=%+v after=%+v", before, after)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertExactJSONKeys(t, top, "receipt", "scope")
	var scope map[string]json.RawMessage
	if err := json.Unmarshal(top["scope"], &scope); err != nil {
		t.Fatalf("decode scope: %v", err)
	}
	assertExactJSONKeys(t, scope, "created_at", "created_by", "id", "revision", "root_issue_id", "scope_kind", "state", "updated_at", "workflow_profile_key", "workspace_id")
	var createdBy map[string]json.RawMessage
	if err := json.Unmarshal(scope["created_by"], &createdBy); err != nil {
		t.Fatalf("decode created_by: %v", err)
	}
	assertExactJSONKeys(t, createdBy, "actor_id", "actor_type", "task_id")
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(top["receipt"], &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	assertExactJSONKeys(t, receipt, "created_at", "id", "operation", "receipt_ordinal", "resource_id", "resource_type", "revision_after", "revision_before")
	for _, field := range []json.RawMessage{scope["created_at"], scope["updated_at"], receipt["created_at"]} {
		var value string
		if err := json.Unmarshal(field, &value); err != nil {
			t.Fatalf("decode timestamp: %v", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatalf("timestamp %q is not RFC3339Nano: %v", value, err)
		}
		_, offset := parsed.Zone()
		if offset != 0 || !strings.HasSuffix(value, "Z") {
			t.Fatalf("timestamp is not canonical UTC: %q", value)
		}
	}

	var snapshot []byte
	if err := testPool.QueryRow(ctx, `SELECT result_snapshot::text FROM coordination_receipt WHERE workspace_id=$1 AND idempotency_key='wire-contract-1'`, testWorkspaceID).Scan(&snapshot); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	var snapshotObject map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &snapshotObject); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	assertExactJSONKeys(t, snapshotObject, "created_at", "created_by", "id", "revision", "root_issue_id", "scope_kind", "state", "updated_at", "workflow_profile_key", "workspace_id")
	if bytes.Contains(snapshot, []byte("wire-contract-1")) || bytes.Contains(snapshot, []byte("request_hash")) || bytes.Contains(snapshot, []byte("receipt")) {
		t.Fatalf("snapshot leaked request or receipt data: %s", snapshot)
	}

	noopReq := newRequest(http.MethodPost, "/api/coordination/scopes", body)
	noopReq.Header.Set("Idempotency-Key", "wire-contract-2")
	noopW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(noopW, noopReq)
	if noopW.Code != http.StatusOK {
		t.Fatalf("no-op status=%d body=%s", noopW.Code, noopW.Body.String())
	}

	invalidBodies := []string{
		`{"root_issue_id":"` + issueID + `","workflow_profile_key":"matt-loop","actor_id":"` + testUserID + `"}`,
		`{"root_issue_id":"` + issueID + `","workflow_profile_key":"matt-loop","unknown":true}`,
		`{"root_issue_id":"` + issueID + `","workflow_profile_key":"matt-loop"} {}`,
	}
	for i, raw := range invalidBodies {
		badReq := newWorkCoordinationRawRequest(raw, "wire-invalid")
		badW := httptest.NewRecorder()
		testHandler.EnsureCoordinationScope(badW, badReq)
		if badW.Code != http.StatusBadRequest || !bytes.Contains(badW.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
			t.Fatalf("invalid body %d status=%d body=%s", i, badW.Code, badW.Body.String())
		}
	}
	spaceKeyReq := newRequest(http.MethodPost, "/api/coordination/scopes", body)
	spaceKeyReq.Header.Set("Idempotency-Key", " wire-key ")
	spaceKeyW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(spaceKeyW, spaceKeyReq)
	if spaceKeyW.Code != http.StatusBadRequest {
		t.Fatalf("whitespace idempotency key status=%d body=%s", spaceKeyW.Code, spaceKeyW.Body.String())
	}

	foreignWorkspace := fmt.Sprintf("wcs-wire-foreign-%d", time.Now().UnixNano())
	var foreignWorkspaceID, foreignIssueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('WCS Foreign',$1) RETURNING id`, foreignWorkspace).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("insert foreign workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS Foreign Root','member',$2,'none',1) RETURNING id`, foreignWorkspaceID, testUserID).Scan(&foreignIssueID); err != nil {
		t.Fatalf("insert foreign issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, foreignWorkspaceID)
	})
	foreignReq := newRequest(http.MethodPost, "/api/coordination/scopes", map[string]string{"root_issue_id": foreignIssueID, "workflow_profile_key": "matt-loop"})
	foreignReq.Header.Set("Idempotency-Key", "wire-foreign")
	foreignW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(foreignW, foreignReq)
	if foreignW.Code != http.StatusForbidden || !bytes.Contains(foreignW.Body.Bytes(), []byte(`"code":"coordination_cross_workspace"`)) || strings.Contains(foreignW.Body.String(), foreignIssueID) {
		t.Fatalf("cross-workspace status=%d body=%s", foreignW.Code, foreignW.Body.String())
	}

	var batchIssueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS batch actual','member',$2,'none',990102) RETURNING id`, testWorkspaceID, testUserID).Scan(&batchIssueID); err != nil {
		t.Fatalf("insert batch issue: %v", err)
	}
	batchReq := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{"issue_ids": []string{"not-a-uuid", "ffffffff-ffff-ffff-ffff-ffffffffffff", foreignIssueID, batchIssueID, batchIssueID}})
	batchW := httptest.NewRecorder()
	testHandler.BatchDeleteIssues(batchW, batchReq)
	if batchW.Code != http.StatusOK || strings.TrimSpace(batchW.Body.String()) != `{"deleted":1}` {
		t.Fatalf("batch status=%d body=%s", batchW.Code, batchW.Body.String())
	}
	var batchExists, foreignExists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1),EXISTS(SELECT 1 FROM issue WHERE id=$2)`, batchIssueID, foreignIssueID).Scan(&batchExists, &foreignExists); err != nil || batchExists || !foreignExists {
		t.Fatalf("batch actual=%v foreign=%v err=%v", batchExists, foreignExists, err)
	}
	zeroReq := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{"issue_ids": []string{"not-a-uuid", "ffffffff-ffff-ffff-ffff-ffffffffffff", foreignIssueID}})
	zeroW := httptest.NewRecorder()
	testHandler.BatchDeleteIssues(zeroW, zeroReq)
	if zeroW.Code != http.StatusOK || strings.TrimSpace(zeroW.Body.String()) != `{"deleted":0}` {
		t.Fatalf("zero-actual batch status=%d body=%s", zeroW.Code, zeroW.Body.String())
	}
}

type workCoordinationIssueFactSnapshot struct {
	Status        string
	AssigneeType  string
	AssigneeID    string
	Comments      int
	Tasks         int
	AutopilotRuns int
}

func workCoordinationIssueFacts(t *testing.T, ctx context.Context, issueID string) workCoordinationIssueFactSnapshot {
	t.Helper()
	var result workCoordinationIssueFactSnapshot
	if err := testPool.QueryRow(ctx, `SELECT status,COALESCE(assignee_type,''),COALESCE(assignee_id::text,'') FROM issue WHERE id=$1`, issueID).Scan(&result.Status, &result.AssigneeType, &result.AssigneeID); err != nil {
		t.Fatalf("load issue facts: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id=$1`, issueID).Scan(&result.Comments); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, issueID).Scan(&result.Tasks); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_run WHERE issue_id=$1`, issueID).Scan(&result.AutopilotRuns); err != nil {
		t.Fatalf("count autopilot runs: %v", err)
	}
	return result
}

func newWorkCoordinationRawRequest(body, key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/coordination/scopes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("Idempotency-Key", key)
	return req
}

func assertExactJSONKeys(t *testing.T, object map[string]json.RawMessage, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("JSON keys=%v want=%v", got, want)
	}
}
