package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkCoordinationHandlerScopeAndDeleteGuard(t *testing.T) {
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, priority, number)
		VALUES ($1, $2, 'member', $3, 'none', 990001)
		RETURNING id`, testWorkspaceID, "WCS handler root "+fmt.Sprint(time.Now().UnixNano()), testUserID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	requestBody := map[string]string{"root_issue_id": issueID, "workflow_profile_key": "matt-loop"}
	req := newRequest(http.MethodPost, "/api/coordination/scopes", requestBody)
	req.Header.Set("Idempotency-Key", "handler-ensure-1")
	w := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("ensure status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Scope struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"scope"`
		Receipt struct {
			ID      string `json:"id"`
			Ordinal int64  `json:"receipt_ordinal"`
		} `json:"receipt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode ensure: %v", err)
	}
	if created.Scope.ID == "" || created.Scope.Revision != 0 || created.Receipt.ID == "" || created.Receipt.Ordinal != 1 {
		t.Fatalf("unexpected ensure response: %+v", created)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"outcome"`)) || bytes.Contains(w.Body.Bytes(), []byte(`"request_hash"`)) {
		t.Fatalf("internal fields leaked: %s", w.Body.String())
	}

	replayReq := newRequest(http.MethodPost, "/api/coordination/scopes", requestBody)
	replayReq.Header.Set("Idempotency-Key", "handler-ensure-1")
	replayW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(replayW, replayReq)
	if replayW.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayW.Code, replayW.Body.String())
	}
	var replay struct {
		Receipt struct {
			ID      string `json:"id"`
			Ordinal int64  `json:"receipt_ordinal"`
		} `json:"receipt"`
	}
	if err := json.Unmarshal(replayW.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replay.Receipt.ID != created.Receipt.ID || replay.Receipt.Ordinal != created.Receipt.Ordinal {
		t.Fatalf("replay changed receipt: created=%+v replay=%+v", created.Receipt, replay.Receipt)
	}

	getReq := withURLParam(newRequest(http.MethodGet, "/api/coordination/scopes/"+created.Scope.ID, nil), "scopeId", created.Scope.ID)
	getW := httptest.NewRecorder()
	testHandler.GetCoordinationScope(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getW.Code, getW.Body.String())
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/coordination/scopes", bytes.NewBufferString(`{"root_issue_id":"`+issueID+`","root_issue_id":"`+issueID+`","workflow_profile_key":"matt-loop"}`))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-User-ID", testUserID)
	badReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	badReq.Header.Set("Idempotency-Key", "duplicate-field")
	badW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(badW, badReq)
	if badW.Code != http.StatusBadRequest || !bytes.Contains(badW.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
		t.Fatalf("duplicate field status=%d body=%s", badW.Code, badW.Body.String())
	}

	deleteReq := withURLParam(newRequest(http.MethodDelete, "/api/issues/"+issueID, nil), "id", issueID)
	deleteW := httptest.NewRecorder()
	testHandler.DeleteIssue(deleteW, deleteReq)
	if deleteW.Code != http.StatusConflict || !bytes.Contains(deleteW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
		t.Fatalf("guard status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}
	var issueExists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id = $1)`, issueID).Scan(&issueExists); err != nil || !issueExists {
		t.Fatalf("guarded issue missing=%v err=%v", !issueExists, err)
	}

	if _, err := testPool.Exec(ctx, `DELETE FROM coordination_receipt WHERE workspace_id = $1`, testWorkspaceID); err != nil {
		t.Fatalf("clear coordination receipts: %v", err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM coordination_scope WHERE workspace_id = $1`, testWorkspaceID); err != nil {
		t.Fatalf("clear coordination scopes: %v", err)
	}
	deleteReq = withURLParam(newRequest(http.MethodDelete, "/api/issues/"+issueID, nil), "id", issueID)
	deleteW = httptest.NewRecorder()
	testHandler.DeleteIssue(deleteW, deleteReq)
	if deleteW.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}
}
