package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	prMergeTestRepository = "jackie/agent-kit"
	prMergeTestHead       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func withDelegationRouteParams(req *http.Request, workspaceID, delegationID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", workspaceID)
	if delegationID != "" {
		routeContext.URLParams.Add("delegationId", delegationID)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func configurePRMergeAssertionTest(t *testing.T, now time.Time) {
	t.Helper()
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "merge-delegation-test-signing-value")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "urn:multica:deployment:merge-delegation-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-merge-delegation-test")
	testHandler.workloadAssertionNow = func() time.Time { return now }
	t.Cleanup(func() { testHandler.workloadAssertionNow = nil })
}

func newPRMergeAssertionRequest(repository string, pullRequestNumber, forgejoPullRequestNumber int64, head, method string) *http.Request {
	return newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose": "ags_session_exchange",
		"target": map[string]any{
			"provider": "ags", "instance": "mini", "repository": repository,
		},
		"requested_resource": map[string]any{"service": "ags", "repository": repository},
		"requested_operation": map[string]any{"name": "pr.merge", "constraints": map[string]any{
			"pull_request_number": pullRequestNumber, "forgejo_pull_request_number": forgejoPullRequestNumber,
			"expected_head_sha": head, "merge_method": method,
		}},
		"requested_capabilities": []string{"repo:read", "repo:write"},
	})
}

type prMergeDelegationFixture struct {
	workspaceID              string
	taskID                   string
	runID                    string
	repository               string
	pullRequestNumber        int64
	forgejoPullRequestNumber int64
	expectedHeadSHA          string
	mergeMethod              string
	grantedAt                time.Time
	expiresAt                time.Time
	revokedAt                *time.Time
}

func insertPRMergeDelegationFixture(t *testing.T, fixture prMergeDelegationFixture) string {
	t.Helper()
	var id string
	var revokedBy any
	var revocationReason any
	if fixture.revokedAt != nil {
		revokedBy = testUserID
		revocationReason = "test revocation"
	}
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO workload_pr_merge_delegation (
    workspace_id, task_id, run_id, repository, pull_request_number,
    forgejo_pull_request_number, expected_head_sha, merge_method,
    granted_by_user_id, granted_at, expires_at, revoked_at,
    revoked_by_user_id, revocation_reason
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
RETURNING id`, fixture.workspaceID, fixture.taskID, fixture.runID, fixture.repository,
		fixture.pullRequestNumber, fixture.forgejoPullRequestNumber, fixture.expectedHeadSHA,
		fixture.mergeMethod, testUserID, fixture.grantedAt, fixture.expiresAt,
		fixture.revokedAt, revokedBy, revocationReason).Scan(&id); err != nil {
		t.Fatalf("insert PR merge delegation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation WHERE id=$1`, id)
	})
	return id
}

func TestPRMergeDelegationOwnerCreateReadReplaceAndRevoke(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	configurePRMergeAssertionTest(t, now)
	issueID := createExternalPRTestIssue(t, "owner PR merge delegation", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "owner-pr-merge-delegation-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation WHERE task_id=$1`, taskID)
	})

	create := func() prMergeDelegationResponse {
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/workload-delegations/pr-merge", map[string]any{
			"task_id": taskID, "run_id": taskID, "repository": prMergeTestRepository,
			"pull_request_number": 41, "forgejo_pull_request_number": 52,
			"expected_head_sha": prMergeTestHead, "merge_method": "fast-forward-only", "ttl_seconds": 600,
		})
		req = withDelegationRouteParams(req, testWorkspaceID, "")
		req.Header.Set("X-User-ID", testUserID)
		rr := httptest.NewRecorder()
		testHandler.CreatePRMergeDelegation(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
		}
		var response prMergeDelegationResponse
		if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Schema != prMergeDelegationSchema || response.State != "active" || response.Operation != "pr.merge" || response.TaskID != taskID || response.RunID != taskID || response.AuthorityRevision == "" || response.GrantedByUserID != testUserID {
			t.Fatalf("create response=%#v", response)
		}
		return response
	}
	first := create()
	second := create()
	if first.ID == second.ID || first.AuthorityRevision == second.AuthorityRevision {
		t.Fatalf("replacement reused identity: first=%#v second=%#v", first, second)
	}

	read := func(id string) prMergeDelegationResponse {
		req := withDelegationRouteParams(newRequest(http.MethodGet, "/", nil), testWorkspaceID, id)
		req.Header.Set("X-User-ID", testUserID)
		rr := httptest.NewRecorder()
		testHandler.GetPRMergeDelegation(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("read status=%d body=%s", rr.Code, rr.Body.String())
		}
		var response prMergeDelegationResponse
		if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	if got := read(first.ID); got.State != "revoked" || got.RevocationReason == nil {
		t.Fatalf("replaced delegation=%#v", got)
	}

	unknownRevokeReq := withDelegationRouteParams(newRequest(http.MethodPost, "/", map[string]any{
		"reason": "operator cancelled merge authority", "authority_revision": second.AuthorityRevision,
	}), testWorkspaceID, second.ID)
	unknownRevokeReq.Header.Set("X-User-ID", testUserID)
	unknownRevoke := httptest.NewRecorder()
	testHandler.RevokePRMergeDelegation(unknownRevoke, unknownRevokeReq)
	if unknownRevoke.Code != http.StatusBadRequest || read(second.ID).State != "active" {
		t.Fatalf("unknown revoke status=%d body=%s", unknownRevoke.Code, unknownRevoke.Body.String())
	}

	revokeReq := withDelegationRouteParams(newRequest(http.MethodPost, "/", map[string]any{"reason": "operator cancelled merge authority"}), testWorkspaceID, second.ID)
	revokeReq.Header.Set("X-User-ID", testUserID)
	revoke := httptest.NewRecorder()
	testHandler.RevokePRMergeDelegation(revoke, revokeReq)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	if got := read(second.ID); got.State != "revoked" || got.RevokedByUserID == nil || *got.RevokedByUserID != testUserID {
		t.Fatalf("revoked delegation=%#v", got)
	}
}

func TestPRMergeDelegationCreateRejectsMachineUnknownAndCrossWorkspaceInputs(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	configurePRMergeAssertionTest(t, now)
	issueID := createExternalPRTestIssue(t, "PR merge delegation create denials", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "pr-merge-delegation-denial-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	validBody := map[string]any{
		"task_id": taskID, "run_id": taskID, "repository": prMergeTestRepository,
		"pull_request_number": 41, "forgejo_pull_request_number": 52,
		"expected_head_sha": prMergeTestHead, "merge_method": "fast-forward-only", "ttl_seconds": 600,
	}

	machineReq := withDelegationRouteParams(newRequest(http.MethodPost, "/", validBody), testWorkspaceID, "")
	machineReq.Header.Set("X-User-ID", testUserID)
	machineReq.Header.Set("X-Actor-Source", "task_token")
	machine := httptest.NewRecorder()
	testHandler.CreatePRMergeDelegation(machine, machineReq)
	if machine.Code != http.StatusForbidden {
		t.Fatalf("machine create status=%d body=%s", machine.Code, machine.Body.String())
	}

	unknownBody := make(map[string]any, len(validBody)+1)
	for key, value := range validBody {
		unknownBody[key] = value
	}
	unknownBody["policy_class"] = workspaceMaintainerPolicyClass
	unknownReq := withDelegationRouteParams(newRequest(http.MethodPost, "/", unknownBody), testWorkspaceID, "")
	unknownReq.Header.Set("X-User-ID", testUserID)
	unknown := httptest.NewRecorder()
	testHandler.CreatePRMergeDelegation(unknown, unknownReq)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown create status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	wrongRunBody := make(map[string]any, len(validBody))
	for key, value := range validBody {
		wrongRunBody[key] = value
	}
	wrongRunBody["run_id"] = uuid.NewString()
	wrongRunReq := withDelegationRouteParams(newRequest(http.MethodPost, "/", wrongRunBody), testWorkspaceID, "")
	wrongRunReq.Header.Set("X-User-ID", testUserID)
	wrongRun := httptest.NewRecorder()
	testHandler.CreatePRMergeDelegation(wrongRun, wrongRunReq)
	if wrongRun.Code != http.StatusBadRequest {
		t.Fatalf("wrong-run create status=%d body=%s", wrongRun.Code, wrongRun.Body.String())
	}

	crossWorkspaceReq := withDelegationRouteParams(newRequest(http.MethodPost, "/", validBody), uuid.NewString(), "")
	crossWorkspaceReq.Header.Set("X-User-ID", testUserID)
	crossWorkspace := httptest.NewRecorder()
	testHandler.CreatePRMergeDelegation(crossWorkspace, crossWorkspaceReq)
	if crossWorkspace.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace create status=%d body=%s", crossWorkspace.Code, crossWorkspace.Body.String())
	}
}

func TestCreateWorkloadAssertionPRMergeRequiresExactActiveDelegation(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	configurePRMergeAssertionTest(t, now)
	issueID := createExternalPRTestIssue(t, "exact PR merge assertion delegation", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "exact-pr-merge-assertion-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	insertPRMergeDelegationFixture(t, prMergeDelegationFixture{
		workspaceID: testWorkspaceID, taskID: taskID, runID: taskID, repository: prMergeTestRepository,
		pullRequestNumber: 41, forgejoPullRequestNumber: 52, expectedHeadSHA: prMergeTestHead,
		mergeMethod: "fast-forward-only", grantedAt: now.Add(-time.Minute), expiresAt: now.Add(2 * time.Minute),
	})

	req := newPRMergeAssertionRequest(prMergeTestRepository, 41, 52, prMergeTestHead, "fast-forward-only")
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workloadAssertionResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Workload.Authority == nil || response.Workload.Authority.PolicyClass != workspaceMaintainerPolicyClass {
		t.Fatalf("authority=%#v", response.Workload.Authority)
	}
	if response.ExpiresAt != now.Add(2*time.Minute).Format(time.RFC3339) {
		t.Fatalf("assertion expiry=%s want delegation cap=%s", response.ExpiresAt, now.Add(2*time.Minute).Format(time.RFC3339))
	}
}

func TestCreateWorkloadAssertionPRMergeRejectsMissingOrMismatchedDelegation(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	configurePRMergeAssertionTest(t, now)
	issueID := createExternalPRTestIssue(t, "PR merge assertion delegation denials", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "pr-merge-assertion-denial-agent", []byte(`{}`))

	tests := []struct {
		name   string
		mutate func(*prMergeDelegationFixture, *string)
		insert bool
	}{
		{name: "missing", insert: false},
		{name: "arbitrary task", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.taskID = uuid.NewString() }},
		{name: "wrong run", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.runID = uuid.NewString() }},
		{name: "wrong repository", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.repository = "jackie/other" }},
		{name: "wrong AGS PR", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.pullRequestNumber = 42 }},
		{name: "wrong Forgejo PR", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.forgejoPullRequestNumber = 53 }},
		{name: "wrong head", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) {
			f.expectedHeadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{name: "wrong method", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.mergeMethod = "rebase" }},
		{name: "revoked", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { revoked := now.Add(-time.Second); f.revokedAt = &revoked }},
		{name: "expired", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.expiresAt = now.Add(-time.Second) }},
		{name: "cross workspace", insert: true, mutate: func(f *prMergeDelegationFixture, _ *string) { f.workspaceID = uuid.NewString() }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
			fixture := prMergeDelegationFixture{
				workspaceID: testWorkspaceID, taskID: taskID, runID: taskID, repository: prMergeTestRepository,
				pullRequestNumber: 41, forgejoPullRequestNumber: 52, expectedHeadSHA: prMergeTestHead,
				mergeMethod: "fast-forward-only", grantedAt: now.Add(-time.Minute), expiresAt: now.Add(5 * time.Minute),
			}
			requestRepository := prMergeTestRepository
			if tc.mutate != nil {
				tc.mutate(&fixture, &requestRepository)
			}
			if tc.insert {
				insertPRMergeDelegationFixture(t, fixture)
			}
			req := newPRMergeAssertionRequest(requestRepository, 41, 52, prMergeTestHead, "fast-forward-only")
			authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
			rr := httptest.NewRecorder()
			testHandler.CreateWorkloadAssertion(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if json.Valid(rr.Body.Bytes()) && string(rr.Body.Bytes()) != "" {
				var body map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err == nil {
					if _, ok := body["assertion"]; ok {
						t.Fatalf("denial returned assertion: %s", rr.Body.String())
					}
				}
			}
		})
	}
}

func TestLockActivePRMergeDelegationQueryBindsAuthorityRevisionAndActor(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	issueID := createExternalPRTestIssue(t, "PR merge delegation query", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "pr-merge-delegation-query-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	id := insertPRMergeDelegationFixture(t, prMergeDelegationFixture{
		workspaceID: testWorkspaceID, taskID: taskID, runID: taskID, repository: prMergeTestRepository,
		pullRequestNumber: 41, forgejoPullRequestNumber: 52, expectedHeadSHA: prMergeTestHead,
		mergeMethod: "fast-forward-only", grantedAt: now.Add(-time.Minute), expiresAt: now.Add(5 * time.Minute),
	})
	row, err := testHandler.Queries.GetPRMergeDelegationInWorkspace(context.Background(), db.GetPRMergeDelegationInWorkspaceParams{
		ID: parseUUID(id), WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !row.AuthorityRevision.Valid || !row.GrantedByUserID.Valid || uuidToString(row.GrantedByUserID) != testUserID || row.Operation != "pr.merge" {
		t.Fatalf("delegation audit binding=%#v", row)
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("expires_at=%v", row.ExpiresAt)
	}
}
