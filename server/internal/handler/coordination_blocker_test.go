package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkCoordinationBlockerStrictWireLifecycle(t *testing.T) {
	ctx := context.Background()
	var rootID, downstreamID, upstreamID, evidenceID, resolutionEvidenceID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,$2,'member',$3,'none',991101) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS blocker root %d", time.Now().UnixNano()), testUserID).Scan(&rootID); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	for number, target := range map[int]*string{991102: &downstreamID, 991103: &upstreamID, 991104: &evidenceID, 991105: &resolutionEvidenceID} {
		if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,$2,'member',$3,'none',$4,$5) RETURNING id`, testWorkspaceID, fmt.Sprintf("WCS blocker child %d", time.Now().UnixNano()), testUserID, number, rootID).Scan(target); err != nil {
			t.Fatalf("insert child %d: %v", number, err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_record_issue_ref WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_record WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_dependency WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []string{downstreamID, upstreamID, evidenceID, resolutionEvidenceID, rootID})
	})

	ensureReq := newRequest(http.MethodPost, "/api/coordination/scopes", map[string]string{"root_issue_id": rootID, "workflow_profile_key": "blocker-wire"})
	ensureReq.Header.Set("Idempotency-Key", "blocker-wire-scope")
	ensureW := httptest.NewRecorder()
	testHandler.EnsureCoordinationScope(ensureW, ensureReq)
	if ensureW.Code != http.StatusCreated {
		t.Fatalf("ensure status=%d body=%s", ensureW.Code, ensureW.Body.String())
	}
	var ensured coordinationEnsureCLIResponseForHandlerTest
	if err := json.Unmarshal(ensureW.Body.Bytes(), &ensured); err != nil || ensured.Scope.ID == "" {
		t.Fatalf("ensure response=%s err=%v", ensureW.Body.String(), err)
	}

	dependencyBody := fmt.Sprintf(`{"expected_revision":0,"downstream_issue_id":%q,"upstream_issue_id":%q}`, downstreamID, upstreamID)
	dependencyReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", dependencyBody, "blocker-wire-dependency")
	dependencyReq = withURLParam(dependencyReq, "scopeId", ensured.Scope.ID)
	dependencyW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(dependencyW, dependencyReq)
	if dependencyW.Code != http.StatusCreated {
		t.Fatalf("add dependency status=%d body=%s", dependencyW.Code, dependencyW.Body.String())
	}
	var dependency coordinationDependencyMutationResponse
	if err := json.Unmarshal(dependencyW.Body.Bytes(), &dependency); err != nil {
		t.Fatalf("decode dependency: %v", err)
	}

	tooManyRefs := strings.TrimSuffix(strings.Repeat(fmt.Sprintf(`{"kind":"issue","id":%q},`, evidenceID), 33), ",")
	invalidBodies := []string{
		fmt.Sprintf(`{"expected_revision":1,"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"Expected_Revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"Reason_Code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","Reason_Code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","reason_code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"issue","id":%q,"id":%q}]}}`, downstreamID, upstreamID, evidenceID, evidenceID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[{"Kind":"issue","id":%q}]}}`, downstreamID, upstreamID, evidenceID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"root_issue_id":%q,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID, rootID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[],"note":"DO_NOT_ECHO_BLOCKER_INPUT"}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":null,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":null,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}}`, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":null}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":null}}`, downstreamID, upstreamID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"issue","id":%q},{"kind":"issue","id":%q}]}}`, downstreamID, upstreamID, evidenceID, evidenceID),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[%s]}}`, downstreamID, upstreamID, tooManyRefs),
		fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}} {}`, downstreamID, upstreamID),
	}
	for index, body := range invalidBodies {
		req := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers", body, "blocker-wire-invalid")
		req = withURLParam(req, "scopeId", ensured.Scope.ID)
		w := httptest.NewRecorder()
		testHandler.AppendCoordinationBlocker(w, req)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) || strings.Contains(w.Body.String(), "DO_NOT_ECHO_BLOCKER_INPUT") {
			t.Fatalf("invalid append %d status=%d body=%s", index, w.Code, w.Body.String())
		}
	}
	var recordCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM coordination_record WHERE workspace_id=$1`, testWorkspaceID).Scan(&recordCount); err != nil || recordCount != 0 {
		t.Fatalf("invalid append wrote records=%d err=%v", recordCount, err)
	}

	before := workCoordinationIssueFacts(t, ctx, downstreamID)
	appendBody := fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"dependency_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"issue","id":%q}]}}`, downstreamID, upstreamID, dependency.Dependency.ID, evidenceID)
	appendReq := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers", appendBody, "blocker-wire-append")
	appendReq = withURLParam(appendReq, "scopeId", ensured.Scope.ID)
	appendW := httptest.NewRecorder()
	testHandler.AppendCoordinationBlocker(appendW, appendReq)
	if appendW.Code != http.StatusCreated {
		t.Fatalf("append status=%d body=%s", appendW.Code, appendW.Body.String())
	}
	var appendObject map[string]json.RawMessage
	if err := json.Unmarshal(appendW.Body.Bytes(), &appendObject); err != nil {
		t.Fatalf("decode append: %v", err)
	}
	assertExactJSONKeys(t, appendObject, "changed", "receipt", "replayed", "resource", "scope_revision")
	var resourceObject map[string]json.RawMessage
	if err := json.Unmarshal(appendObject["resource"], &resourceObject); err != nil {
		t.Fatalf("decode append resource: %v", err)
	}
	assertExactJSONKeys(t, resourceObject, "id", "workspace_id", "scope_id", "kind", "schema_version", "status", "root_issue_id", "downstream_issue_id", "upstream_issue_id", "dependency_id", "reason_code", "resolution_code", "create_evidence_refs", "resolution_evidence_refs", "created_by", "resolved_by", "created_at", "resolved_at")
	var createdByObject map[string]json.RawMessage
	if err := json.Unmarshal(resourceObject["created_by"], &createdByObject); err != nil {
		t.Fatalf("decode created_by: %v", err)
	}
	assertExactJSONKeys(t, createdByObject, "type", "id", "task_id")
	var appended coordinationBlockerMutationResponse
	if err := json.Unmarshal(appendW.Body.Bytes(), &appended); err != nil {
		t.Fatalf("decode append response: %v", err)
	}
	createdAt, createdAtErr := time.Parse(time.RFC3339Nano, appended.Resource.CreatedAt)
	if !appended.Changed || appended.Replayed || appended.ScopeRevision != 2 || appended.Resource.Status != "open" ||
		appended.Resource.DependencyID == nil || *appended.Resource.DependencyID != dependency.Dependency.ID || appended.Resource.ResolutionCode != nil ||
		appended.Resource.ResolvedBy != nil || appended.Resource.ResolvedAt != nil || len(appended.Resource.CreateEvidenceRefs) != 1 || len(appended.Resource.ResolutionEvidenceRefs) != 0 ||
		appended.Resource.CreatedBy.TaskID != nil || createdAtErr != nil || createdAt.Location() != time.UTC || strings.Contains(appendW.Body.String(), `"payload"`) {
		t.Fatalf("append response=%+v", appended)
	}

	replayReq := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers", appendBody, "blocker-wire-append")
	replayReq = withURLParam(replayReq, "scopeId", ensured.Scope.ID)
	replayW := httptest.NewRecorder()
	testHandler.AppendCoordinationBlocker(replayW, replayReq)
	var replayed coordinationBlockerMutationResponse
	if replayW.Code != http.StatusOK || json.Unmarshal(replayW.Body.Bytes(), &replayed) != nil || !replayed.Replayed || replayed.Receipt.ID != appended.Receipt.ID {
		t.Fatalf("append replay status=%d response=%+v body=%s", replayW.Code, replayed, replayW.Body.String())
	}

	listReq := withURLParam(newRequest(http.MethodGet, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers?status=open&limit=100", nil), "scopeId", ensured.Scope.ID)
	listW := httptest.NewRecorder()
	testHandler.ListCoordinationBlockers(listW, listReq)
	var page coordinationBlockerPageResponse
	if listW.Code != http.StatusOK || json.Unmarshal(listW.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.ScopeRevision != 2 || page.StatusFilter != "open" || page.NextCursor != nil {
		t.Fatalf("list status=%d page=%+v body=%s", listW.Code, page, listW.Body.String())
	}
	for index, rawQuery := range []string{"status=x", "status=open&status=all", "limit=0", "cursor=%zz", "unknown=x"} {
		req := newRequest(http.MethodGet, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers", nil)
		req.URL.RawQuery = rawQuery
		req = withURLParam(req, "scopeId", ensured.Scope.ID)
		w := httptest.NewRecorder()
		testHandler.ListCoordinationBlockers(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid list %d status=%d body=%s", index, w.Code, w.Body.String())
		}
	}

	invalidResolveBodies := []string{
		`{"expected_revision":2,"expected_revision":2,"schema_version":1,"resolution":{"resolution_code":"no_longer_blocking","evidence_refs":[]}}`,
		`{"expected_revision":2,"schema_version":1,"resolution":{"resolution_code":"no_longer_blocking","resolution_code":"superseded","evidence_refs":[]}}`,
		`{"expected_revision":2,"schema_version":1,"resolution":{"resolution_code":"no_longer_blocking","evidence_refs":[],"note":"x"}}`,
		`{"expected_revision":2,"schema_version":null,"resolution":{"resolution_code":"no_longer_blocking","evidence_refs":[]}}`,
		`{"expected_revision":2,"schema_version":1,"resolution":{"resolution_code":"no_longer_blocking","evidence_refs":null}}`,
		`{"expected_revision":2,"schema_version":1,"resolution":{"resolution_code":"no_longer_blocking","evidence_refs":[]}} {}`,
	}
	for index, body := range invalidResolveBodies {
		req := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers/"+appended.Resource.ID+"/resolve", body, "blocker-wire-invalid-resolve")
		req = withURLParams(req, "scopeId", ensured.Scope.ID, "recordId", appended.Resource.ID)
		w := httptest.NewRecorder()
		testHandler.ResolveCoordinationBlocker(w, req)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
			t.Fatalf("invalid resolve %d status=%d body=%s", index, w.Code, w.Body.String())
		}
	}
	resolveBody := fmt.Sprintf(`{"expected_revision":2,"schema_version":1,"resolution":{"resolution_code":"no_longer_blocking","evidence_refs":[{"kind":"issue","id":%q}]}}`, resolutionEvidenceID)
	resolveReq := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers/"+appended.Resource.ID+"/resolve", resolveBody, "blocker-wire-resolve")
	resolveReq = withURLParams(resolveReq, "scopeId", ensured.Scope.ID, "recordId", appended.Resource.ID)
	resolveW := httptest.NewRecorder()
	testHandler.ResolveCoordinationBlocker(resolveW, resolveReq)
	var resolved coordinationBlockerMutationResponse
	if resolveW.Code != http.StatusOK || json.Unmarshal(resolveW.Body.Bytes(), &resolved) != nil || !resolved.Changed || resolved.Replayed ||
		resolved.ScopeRevision != 3 || resolved.Resource.Status != "resolved" || resolved.Resource.ResolutionCode == nil ||
		resolved.Resource.ResolvedBy == nil || resolved.Resource.ResolvedAt == nil || len(resolved.Resource.ResolutionEvidenceRefs) != 1 ||
		resolved.Resource.CreatedAt != appended.Resource.CreatedAt || resolved.Resource.CreatedBy != appended.Resource.CreatedBy {
		t.Fatalf("resolve status=%d response=%+v body=%s", resolveW.Code, resolved, resolveW.Body.String())
	}
	noopBody := `{"expected_revision":3,"schema_version":1,"resolution":{"resolution_code":"superseded","evidence_refs":[]}}`
	noopReq := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers/"+appended.Resource.ID+"/resolve", noopBody, "blocker-wire-resolve-noop")
	noopReq = withURLParams(noopReq, "scopeId", ensured.Scope.ID, "recordId", appended.Resource.ID)
	noopW := httptest.NewRecorder()
	testHandler.ResolveCoordinationBlocker(noopW, noopReq)
	var noop coordinationBlockerMutationResponse
	if noopW.Code != http.StatusOK || json.Unmarshal(noopW.Body.Bytes(), &noop) != nil || noop.Changed || noop.Replayed || noop.ScopeRevision != 3 || noop.Receipt.RevisionBefore != 3 || noop.Receipt.RevisionAfter != 3 {
		t.Fatalf("resolve noop status=%d response=%+v body=%s", noopW.Code, noop, noopW.Body.String())
	}
	resolveReplayReq := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers/"+appended.Resource.ID+"/resolve", resolveBody, "blocker-wire-resolve")
	resolveReplayReq = withURLParams(resolveReplayReq, "scopeId", ensured.Scope.ID, "recordId", appended.Resource.ID)
	resolveReplayW := httptest.NewRecorder()
	testHandler.ResolveCoordinationBlocker(resolveReplayW, resolveReplayReq)
	var resolveReplay coordinationBlockerMutationResponse
	if resolveReplayW.Code != http.StatusOK || json.Unmarshal(resolveReplayW.Body.Bytes(), &resolveReplay) != nil || !resolveReplay.Changed || !resolveReplay.Replayed || resolveReplay.ScopeRevision != 3 || resolveReplay.Receipt.ID != resolved.Receipt.ID {
		t.Fatalf("resolve replay status=%d response=%+v body=%s", resolveReplayW.Code, resolveReplay, resolveReplayW.Body.String())
	}
	after := workCoordinationIssueFacts(t, ctx, downstreamID)
	if before != after {
		t.Fatalf("passive blocker changed issue facts: before=%+v after=%+v", before, after)
	}

	for phase, issueID := range map[string]string{"create": evidenceID, "resolution": resolutionEvidenceID} {
		guardReq := withURLParam(newRequest(http.MethodDelete, "/api/issues/"+issueID, nil), "id", issueID)
		guardW := httptest.NewRecorder()
		testHandler.DeleteIssue(guardW, guardReq)
		if guardW.Code != http.StatusConflict || !bytes.Contains(guardW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
			t.Fatalf("%s evidence guard status=%d body=%s", phase, guardW.Code, guardW.Body.String())
		}
	}
	batchReq := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{"issue_ids": []string{downstreamID, resolutionEvidenceID}})
	batchW := httptest.NewRecorder()
	testHandler.BatchDeleteIssues(batchW, batchReq)
	if batchW.Code != http.StatusConflict || !bytes.Contains(batchW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
		t.Fatalf("blocker batch guard status=%d body=%s", batchW.Code, batchW.Body.String())
	}
	workspaceReq := withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID, nil), "id", testWorkspaceID)
	workspaceW := httptest.NewRecorder()
	testHandler.DeleteWorkspace(workspaceW, workspaceReq)
	if workspaceW.Code != http.StatusConflict || !bytes.Contains(workspaceW.Body.Bytes(), []byte(`"code":"coordination_delete_blocked"`)) {
		t.Fatalf("blocker workspace guard status=%d body=%s", workspaceW.Code, workspaceW.Body.String())
	}
}

type coordinationEnsureCLIResponseForHandlerTest struct {
	Scope coordinationScopeDTO `json:"scope"`
}

func newBlockerRawRequest(method, path, body, key string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req
}
