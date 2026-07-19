package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestWorkCoordinationInspectStrictWireAndReceiptCursor(t *testing.T) {
	ctx := context.Background()
	var rootID, downstreamID, upstreamID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number)
VALUES ($1,$2,'member',$3,'none',992001) RETURNING id`,
		testWorkspaceID, fmt.Sprintf("WCS inspect root %d", time.Now().UnixNano()), testUserID).Scan(&rootID); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	for number, target := range map[int]*string{992002: &downstreamID, 992003: &upstreamID} {
		if err := testPool.QueryRow(ctx, `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id)
VALUES ($1,$2,'member',$3,'none',$4,$5) RETURNING id`,
			testWorkspaceID, fmt.Sprintf("WCS inspect child %d", time.Now().UnixNano()), testUserID, number, rootID).Scan(target); err != nil {
			t.Fatalf("insert child %d: %v", number, err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_record_issue_ref WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_record WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_dependency WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []string{downstreamID, upstreamID, rootID})
	})

	ensureReq := newRequest(http.MethodPost, "/api/coordination/scopes", map[string]string{"root_issue_id": rootID, "workflow_profile_key": "inspect-wire"})
	ensureReq.Header.Set("Idempotency-Key", "inspect-wire-scope")
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
	dependencyReq := newDependencyRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/dependencies", dependencyBody, "inspect-wire-dependency")
	dependencyReq = withURLParam(dependencyReq, "scopeId", ensured.Scope.ID)
	dependencyW := httptest.NewRecorder()
	testHandler.AddCoordinationDependency(dependencyW, dependencyReq)
	if dependencyW.Code != http.StatusCreated {
		t.Fatalf("dependency status=%d body=%s", dependencyW.Code, dependencyW.Body.String())
	}
	var dependency coordinationDependencyMutationResponse
	if err := json.Unmarshal(dependencyW.Body.Bytes(), &dependency); err != nil {
		t.Fatalf("decode dependency: %v", err)
	}

	blockerBody := fmt.Sprintf(`{"expected_revision":1,"downstream_issue_id":%q,"upstream_issue_id":%q,"dependency_id":%q,"schema_version":1,"payload":{"reason_code":"waiting_on_issue","evidence_refs":[]}}`, downstreamID, upstreamID, dependency.Dependency.ID)
	blockerReq := newBlockerRawRequest(http.MethodPost, "/api/coordination/scopes/"+ensured.Scope.ID+"/blockers", blockerBody, "inspect-wire-blocker")
	blockerReq = withURLParam(blockerReq, "scopeId", ensured.Scope.ID)
	blockerW := httptest.NewRecorder()
	testHandler.AppendCoordinationBlocker(blockerW, blockerReq)
	if blockerW.Code != http.StatusCreated {
		t.Fatalf("blocker status=%d body=%s", blockerW.Code, blockerW.Body.String())
	}

	if _, err := testPool.Exec(ctx, `
INSERT INTO coordination_receipt (
  id,workspace_id,coordination_scope_id,receipt_ordinal,operation,idempotency_key,
  request_hash,resource_type,resource_id,revision_before,revision_after,result_snapshot,
  actor_type,actor_id,created_at
)
SELECT gen_random_uuid(),$1,$2,ordinal,'resolve_dependency','inspect-wire-seed-'||ordinal,
       decode(repeat('00',32),'hex'),'dependency',$3,2,2,'{}'::jsonb,
       'member',$4,'2030-01-01T00:00:00Z'::timestamptz - ordinal * interval '1 second'
FROM generate_series(4,103) AS ordinal`, testWorkspaceID, ensured.Scope.ID, dependency.Dependency.ID, testUserID); err != nil {
		t.Fatalf("seed receipt page: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE coordination_scope SET next_receipt_ordinal=103 WHERE id=$1`, ensured.Scope.ID); err != nil {
		t.Fatalf("advance next receipt ordinal: %v", err)
	}

	inspectReq := withURLParam(newRequest(http.MethodGet, "/api/coordination/scopes/"+ensured.Scope.ID+"/inspect", nil), "scopeId", ensured.Scope.ID)
	inspectW := httptest.NewRecorder()
	testHandler.InspectCoordinationScope(inspectW, inspectReq)
	if inspectW.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", inspectW.Code, inspectW.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(inspectW.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode inspection: %v", err)
	}
	assertExactJSONKeys(t, raw, "scope", "scope_revision", "active_dependencies", "open_blockers", "receipt_refs", "next_receipt_cursor")
	var inspection coordinationScopeInspectionResponse
	if err := json.Unmarshal(inspectW.Body.Bytes(), &inspection); err != nil {
		t.Fatalf("decode typed inspection: %v", err)
	}
	if inspection.ScopeRevision != 2 || inspection.Scope.Revision != 2 || len(inspection.ActiveDependencies) != 1 || len(inspection.OpenBlockers) != 1 || len(inspection.ReceiptRefs) != 100 || inspection.NextReceiptCursor == nil {
		t.Fatalf("inspection=%+v", inspection)
	}
	if inspection.ReceiptRefs[0].ReceiptOrdinal != 103 || inspection.ReceiptRefs[99].ReceiptOrdinal != 4 || inspection.ReceiptRefs[0].ActorType != "member" {
		t.Fatalf("receipt refs=%+v", inspection.ReceiptRefs)
	}
	if bytes.Contains(inspectW.Body.Bytes(), []byte("request_hash")) || bytes.Contains(inspectW.Body.Bytes(), []byte("result_snapshot")) || bytes.Contains(inspectW.Body.Bytes(), []byte("idempotency_key")) {
		t.Fatalf("inspection leaked receipt internals: %s", inspectW.Body.String())
	}

	invalidQueries := []string{"receipt_cursor=a&receipt_cursor=b", "receipt_cursor=%zz", "unknown=x"}
	for index, query := range invalidQueries {
		req := withURLParam(newRequest(http.MethodGet, "/api/coordination/scopes/"+ensured.Scope.ID+"/inspect?"+query, nil), "scopeId", ensured.Scope.ID)
		w := httptest.NewRecorder()
		testHandler.InspectCoordinationScope(w, req)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"coordination_invalid_payload"`)) {
			t.Fatalf("invalid query %d status=%d body=%s", index, w.Code, w.Body.String())
		}
	}

	if _, err := testPool.Exec(ctx, `UPDATE coordination_scope SET revision=revision+1 WHERE id=$1`, ensured.Scope.ID); err != nil {
		t.Fatalf("advance scope revision: %v", err)
	}
	stalePath := "/api/coordination/scopes/" + ensured.Scope.ID + "/inspect?receipt_cursor=" + url.QueryEscape(*inspection.NextReceiptCursor)
	staleReq := withURLParam(newRequest(http.MethodGet, stalePath, nil), "scopeId", ensured.Scope.ID)
	staleW := httptest.NewRecorder()
	testHandler.InspectCoordinationScope(staleW, staleReq)
	if staleW.Code != http.StatusConflict || !bytes.Contains(staleW.Body.Bytes(), []byte(`"code":"coordination_revision_conflict"`)) {
		t.Fatalf("stale cursor status=%d body=%s", staleW.Code, staleW.Body.String())
	}
}
