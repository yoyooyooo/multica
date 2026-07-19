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

func TestWorkCoordinationDependencyStrictWireLifecycle(t *testing.T) {
	ctx := context.Background()
	var rootID, downstreamID, upstreamID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,$2,'member',$3,'none',990101) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS dependency root %d", time.Now().UnixNano()), testUserID).Scan(&rootID); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,$2,'member',$3,'none',990102,$4) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS downstream %d", time.Now().UnixNano()), testUserID, rootID).Scan(&downstreamID); err != nil {
		t.Fatalf("insert downstream: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,$2,'member',$3,'none',990103,$4) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS upstream %d", time.Now().UnixNano()), testUserID, rootID).Scan(&upstreamID); err != nil {
		t.Fatalf("insert upstream: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_dependency WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []string{downstreamID, upstreamID, rootID})
	})

	ensureReq := newRequest(http.MethodPost, "/api/coordination/scopes", map[string]string{"root_issue_id": rootID, "workflow_profile_key": "matt-loop"})
	ensureReq.Header.Set("Idempotency-Key", "dependency-wire-scope")
	ensureW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(ensureW, ensureReq)
	if ensureW.Code != http.StatusCreated {
		t.Fatalf("ensure status=%d body=%s", ensureW.Code, ensureW.Body.String())
	}
	var ensured struct {
		Scope struct {
			ID string `json:"id"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(ensureW.Body.Bytes(), &ensured); err != nil || ensured.Scope.ID == "" {
		t.Fatalf("ensure response=%s err=%v", ensureW.Body.String(), err)
	}

	before := workCoordinationIssueFacts(t, ctx, downstreamID)
	invalidBodies := []string{
		`{"expected_revision":0,"expected_revision":0,"downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `"}`,
		`{"expected_revision":0,"downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `","actor_id":"` + testUserID + `"}`,
		`{"downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `"}`,
		`{"expected_revision":0,"downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `"} {}`,
	}
	for i, body := range invalidBodies {
		req := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", body, "dependency-invalid")
		req = withURLParam(req, "scopeId", ensured.Scope.ID)
		w := httptest.NewRecorder()
		testHandler.AddCoordinationDependency(w, req)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
			t.Fatalf("invalid case %d status=%d body=%s", i, w.Code, w.Body.String())
		}
	}
	var dependencyCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM coordination_dependency WHERE workspace_id=$1`, testWorkspaceID).Scan(&dependencyCount); err != nil || dependencyCount != 0 {
		t.Fatalf("invalid requests wrote dependencies=%d err=%v", dependencyCount, err)
	}
	selfBody := fmt.Sprintf(`{"expected_revision":0,"downstream_issue_id":%q,"upstream_issue_id":%q}`, downstreamID, downstreamID)
	selfReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", selfBody, "dependency-self")
	selfReq = withURLParam(selfReq, "scopeId", ensured.Scope.ID)
	selfW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(selfW, selfReq)
	if selfW.Code != http.StatusUnprocessableEntity || !bytes.Contains(selfW.Body.Bytes(), []byte(`"code":"coordination_self_dependency"`)) {
		t.Fatalf("self status=%d body=%s", selfW.Code, selfW.Body.String())
	}

	body := fmt.Sprintf(`{"expected_revision":0,"downstream_issue_id":%q,"upstream_issue_id":%q}`, downstreamID, upstreamID)
	addReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", body, "dependency-add")
	addReq.Header.Set("X-Agent-ID", rootID)
	addReq.Header.Set("X-Task-ID", upstreamID)
	addReq = withURLParam(addReq, "scopeId", ensured.Scope.ID)
	addW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(addW, addReq)
	if addW.Code != http.StatusCreated {
		t.Fatalf("add status=%d body=%s", addW.Code, addW.Body.String())
	}
	var addObject map[string]json.RawMessage
	if err := json.Unmarshal(addW.Body.Bytes(), &addObject); err != nil {
		t.Fatalf("decode add: %v", err)
	}
	assertExactJSONKeys(t, addObject, "dependency", "outcome", "receipt", "scope_revision")
	var dependencyObject map[string]json.RawMessage
	if err := json.Unmarshal(addObject["dependency"], &dependencyObject); err != nil {
		t.Fatalf("decode dependency object: %v", err)
	}
	assertExactJSONKeys(t, dependencyObject, "blocks_issue_id", "coordination_scope_id", "created_at", "created_by", "downstream_issue_id", "id", "resolved_at", "resolved_by", "upstream_issue_id", "workspace_id")
	var addResponse coordinationDependencyMutationResponse
	if err := json.Unmarshal(addW.Body.Bytes(), &addResponse); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if addResponse.Outcome != "created" || addResponse.ScopeRevision != 1 || addResponse.Receipt.ReceiptOrdinal != 2 || addResponse.Dependency.BlocksIssueID != downstreamID || addResponse.Dependency.CreatedBy.ActorType != "member" || addResponse.Dependency.ResolvedBy != nil || addResponse.Dependency.ResolvedAt != nil {
		t.Fatalf("add response=%+v", addResponse)
	}
	if bytes.Contains(addW.Body.Bytes(), []byte("request_hash")) || bytes.Contains(addW.Body.Bytes(), []byte("next_receipt_ordinal")) {
		t.Fatalf("internal data leaked: %s", addW.Body.String())
	}

	replayReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", body, "dependency-add")
	replayReq = withURLParam(replayReq, "scopeId", ensured.Scope.ID)
	replayW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(replayW, replayReq)
	if replayW.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayW.Code, replayW.Body.String())
	}
	var replay coordinationDependencyMutationResponse
	if err := json.Unmarshal(replayW.Body.Bytes(), &replay); err != nil || replay.Outcome != "replay" || replay.Receipt.ID != addResponse.Receipt.ID || replay.Receipt.ReceiptOrdinal != addResponse.Receipt.ReceiptOrdinal {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	noopBody := fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q}`, downstreamID, upstreamID)
	noopReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", noopBody, "dependency-noop")
	noopReq = withURLParam(noopReq, "scopeId", ensured.Scope.ID)
	noopW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(noopW, noopReq)
	if noopW.Code != http.StatusOK {
		t.Fatalf("noop status=%d body=%s", noopW.Code, noopW.Body.String())
	}
	var noop coordinationDependencyMutationResponse
	if err := json.Unmarshal(noopW.Body.Bytes(), &noop); err != nil || noop.Outcome != "noop" || noop.ScopeRevision != 1 || noop.Receipt.ReceiptOrdinal != 3 {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
	cycleBody := fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q}`, upstreamID, downstreamID)
	cycleReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", cycleBody, "dependency-cycle")
	cycleReq = withURLParam(cycleReq, "scopeId", ensured.Scope.ID)
	cycleW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(cycleW, cycleReq)
	if cycleW.Code != http.StatusUnprocessableEntity || !bytes.Contains(cycleW.Body.Bytes(), []byte(`"code":"coordination_cycle"`)) {
		t.Fatalf("cycle status=%d body=%s", cycleW.Code, cycleW.Body.String())
	}

	listReq := withURLParam(newRequest(http.MethodGet, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies?limit=100", nil), "scopeId", ensured.Scope.ID)
	listW := httptest.NewRecorder()
	testHandler.ListCoordinationDependencies(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listW.Code, listW.Body.String())
	}
	var pageObject map[string]json.RawMessage
	if err := json.Unmarshal(listW.Body.Bytes(), &pageObject); err != nil {
		t.Fatalf("decode page object: %v", err)
	}
	assertExactJSONKeys(t, pageObject, "dependencies", "next_cursor", "scope_revision")
	var page coordinationDependencyPageResponse
	if err := json.Unmarshal(listW.Body.Bytes(), &page); err != nil || len(page.Dependencies) != 1 || page.ScopeRevision != 1 || page.NextCursor != nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}

	for i, rawPath := range []string{
		"/api/coordination/scopes/" + ensured.Scope.ID + "/dependencies?unknown=x",
		"/api/coordination/scopes/" + ensured.Scope.ID + "/dependencies?cursor=a&cursor=b",
		"/api/coordination/scopes/" + ensured.Scope.ID + "/dependencies?limit=0",
	} {
		invalidListReq := withURLParam(newRequest(http.MethodGet, rawPath, nil), "scopeId", ensured.Scope.ID)
		invalidListW := httptest.NewRecorder()
		testHandler.ListCoordinationDependencies(invalidListW, invalidListReq)
		if invalidListW.Code != http.StatusBadRequest || !bytes.Contains(invalidListW.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
			t.Fatalf("invalid list case %d status=%d body=%s", i, invalidListW.Code, invalidListW.Body.String())
		}
	}
	invalidResolveBodies := []string{
		`{"expected_revision":1,"dependency_id":"` + addResponse.Dependency.ID + `"}`,
		`{"expected_revision":1,"expected_revision":1}`,
		`{}`,
		`{"expected_revision":1} {}`,
	}
	for i, invalidBody := range invalidResolveBodies {
		invalidResolveReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies/"+addResponse.Dependency.ID+"/resolve", invalidBody, "dependency-resolve-invalid")
		invalidResolveReq = withURLParams(invalidResolveReq, "scopeId", ensured.Scope.ID, "dependencyId", addResponse.Dependency.ID)
		invalidResolveW := httptest.NewRecorder()
		testHandler.ResolveCoordinationDependency(invalidResolveW, invalidResolveReq)
		if invalidResolveW.Code != http.StatusBadRequest || !bytes.Contains(invalidResolveW.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
			t.Fatalf("invalid resolve case %d status=%d body=%s", i, invalidResolveW.Code, invalidResolveW.Body.String())
		}
	}
	var preResolveRevision int64
	var preResolveReceipts int
	if err := testPool.QueryRow(ctx, `SELECT revision FROM coordination_scope WHERE id=$1`, ensured.Scope.ID).Scan(&preResolveRevision); err != nil || preResolveRevision != 1 {
		t.Fatalf("pre-resolve revision=%d err=%v", preResolveRevision, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, ensured.Scope.ID).Scan(&preResolveReceipts); err != nil || preResolveReceipts != 3 {
		t.Fatalf("pre-resolve receipts=%d err=%v", preResolveReceipts, err)
	}

	resolveBody := `{"expected_revision":1}`
	resolveReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies/"+addResponse.Dependency.ID+"/resolve", resolveBody, "dependency-resolve")
	resolveReq = withURLParams(resolveReq, "scopeId", ensured.Scope.ID, "dependencyId", addResponse.Dependency.ID)
	resolveW := httptest.NewRecorder()
	testHandler.ResolveCoordinationDependency(resolveW, resolveReq)
	if resolveW.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", resolveW.Code, resolveW.Body.String())
	}
	var resolved coordinationDependencyMutationResponse
	if err := json.Unmarshal(resolveW.Body.Bytes(), &resolved); err != nil || resolved.Outcome != "resolved" || resolved.ScopeRevision != 2 || resolved.Dependency.ResolvedBy == nil || resolved.Dependency.ResolvedAt == nil {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}

	listReq = withURLParam(newRequest(http.MethodGet, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies?limit=100", nil), "scopeId", ensured.Scope.ID)
	listW = httptest.NewRecorder()
	testHandler.ListCoordinationDependencies(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list resolved status=%d body=%s", listW.Code, listW.Body.String())
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &page); err != nil || len(page.Dependencies) != 0 || page.ScopeRevision != 2 {
		t.Fatalf("resolved page=%+v err=%v", page, err)
	}

	after := workCoordinationIssueFacts(t, ctx, downstreamID)
	if before != after {
		t.Fatalf("passive dependency changed issue facts: before=%+v after=%+v", before, after)
	}
	deleteReq := withURLParam(newRequest(http.MethodDelete, "/api/issues/"+downstreamID, nil), "id", downstreamID)
	deleteW := httptest.NewRecorder()
	testHandler.DeleteIssue(deleteW, deleteReq)
	if deleteW.Code != http.StatusConflict || !bytes.Contains(deleteW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
		t.Fatalf("dependency guard status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}
	batchReq := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{"issue_ids": []string{downstreamID, upstreamID}})
	batchW := httptest.NewRecorder()
	testHandler.BatchDeleteIssues(batchW, batchReq)
	if batchW.Code != http.StatusConflict || !bytes.Contains(batchW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
		t.Fatalf("dependency batch guard status=%d body=%s", batchW.Code, batchW.Body.String())
	}
	workspaceReq := withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID, nil), "id", testWorkspaceID)
	workspaceW := httptest.NewRecorder()
	testHandler.DeleteWorkspace(workspaceW, workspaceReq)
	if workspaceW.Code != http.StatusConflict || !bytes.Contains(workspaceW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
		t.Fatalf("dependency workspace guard status=%d body=%s", workspaceW.Code, workspaceW.Body.String())
	}
	var remaining int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=ANY($1::uuid[])`, []string{downstreamID, upstreamID}).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("guarded endpoint count=%d err=%v", remaining, err)
	}
}

func newDependencyRawRequest(method, path, body, key string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req
}
