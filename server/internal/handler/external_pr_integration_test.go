package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestExternalPRProviderAllowedDefaultsOpen(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "")
	if !externalPRProviderAllowed("ags") || !externalPRProviderAllowed("custom") {
		t.Fatalf("externalPRProviderAllowed() should default to open when allowlist is empty")
	}
}

func TestExternalPRProviderAllowedHonorsAllowlist(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags, gitlab")
	if !externalPRProviderAllowed("AGS") {
		t.Fatalf("externalPRProviderAllowed() should normalize allowed provider names")
	}
	if externalPRProviderAllowed("custom") {
		t.Fatalf("externalPRProviderAllowed() accepted provider outside allowlist")
	}
}

type rejectingExternalPRTxStarter struct{ err error }

func (s rejectingExternalPRTxStarter) Begin(context.Context) (pgx.Tx, error) {
	return nil, s.err
}

func TestExternalPRErrorContractIsTypedAndSecretSafe(t *testing.T) {
	secret := "postgres password=internal-secret SQLSTATE 40P01"
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "validation", err: externalPRValidation("invalid state"), wantStatus: http.StatusBadRequest, wantBody: "invalid state"},
		{name: "conflict", err: externalPRConflictError{message: "binding changed"}, wantStatus: http.StatusConflict, wantBody: "binding changed"},
		{name: "infrastructure", err: fmt.Errorf("%s", secret), wantStatus: http.StatusServiceUnavailable, wantBody: "external PR integration temporarily unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := externalPRErrorResponse(tc.err)
			if status != tc.wantStatus || body != tc.wantBody {
				t.Fatalf("response=(%d,%q), want=(%d,%q)", status, body, tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(body, secret) || strings.Contains(body, "SQLSTATE") {
				t.Fatalf("response leaked infrastructure detail: %q", body)
			}
		})
	}
}

func TestExternalPRInfrastructureFailureReturnsGeneric503(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-infra-token")
	issueID := createExternalPRTestIssue(t, "external infra error", "todo", "", nil)
	failing := *testHandler
	failing.TxStarter = rejectingExternalPRTxStarter{err: errors.New("database secret SQLSTATE 40P01")}
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", externalPRCompletionReq(testWorkspaceID, issueID, 10001))
	req.Header.Set("Authorization", "Bearer external-pr-infra-token")
	w := httptest.NewRecorder()
	failing.RegisterExternalPullRequestLink(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "database secret") || strings.Contains(w.Body.String(), "SQLSTATE") {
		t.Fatalf("response leaked internal error: %s", w.Body.String())
	}
}

func TestRegisterExternalPRPublishesProjectionRefresh(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-refresh-token")
	issueID := createExternalPRTestIssue(t, "external projection refresh", "todo", "", nil)
	eventsCh := make(chan events.Event, 1)
	testHandler.Bus.Subscribe(protocol.EventPullRequestUpdated, func(e events.Event) {
		payload, _ := e.Payload.(map[string]any)
		if payload["issue_id"] == issueID && payload["provider"] == "ags" {
			eventsCh <- e
		}
	})

	reqBody := externalPRCompletionReq(testWorkspaceID, issueID, 10002)
	reqBody.State = "open"
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", reqBody)
	req.Header.Set("Authorization", "Bearer external-pr-refresh-token")
	w := httptest.NewRecorder()
	testHandler.RegisterExternalPullRequestLink(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	select {
	case <-eventsCh:
	case <-time.After(time.Second):
		t.Fatalf("expected %s after External PR projection commit", protocol.EventPullRequestUpdated)
	}

	replayReq := newRequest(http.MethodPost, "/api/integrations/external-pr/links", reqBody)
	replayReq.Header.Set("Authorization", "Bearer external-pr-refresh-token")
	replayW := httptest.NewRecorder()
	testHandler.RegisterExternalPullRequestLink(replayW, replayReq)
	if replayW.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayW.Code, replayW.Body.String())
	}
	select {
	case <-eventsCh:
		t.Fatalf("idempotency replay published a duplicate %s", protocol.EventPullRequestUpdated)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestExternalPRLinkTokenAudienceConfig(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE", "")
	if got := externalPRLinkTokenAudience(); got != defaultExternalPRLinkTokenAudience {
		t.Fatalf("externalPRLinkTokenAudience() = %q, want default", got)
	}
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE", "custom-audience")
	if got := externalPRLinkTokenAudience(); got != "custom-audience" {
		t.Fatalf("externalPRLinkTokenAudience() = %q, want custom-audience", got)
	}
}

func TestCompleteIssueFromExternalPRPublicGuardMatrix(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")

	noParent := createExternalPRTestIssue(t, "external-pr no parent", "todo", "", nil)
	got := completeExternalPRViaHandler(t, noParent, 1001)
	if got.Outcome != "recorded" || got.Reason != "guard_not_satisfied" {
		t.Fatalf("no-parent outcome = %#v, want recorded/guard_not_satisfied", got)
	}
	assertIssueStatus(t, noParent, "todo")

	parentForContainer := createExternalPRTestIssue(t, "external-pr container parent", "todo", "", nil)
	container := createExternalPRTestIssue(t, "external-pr child with children", "todo", parentForContainer, int32Ptr(1))
	_ = createExternalPRTestIssue(t, "external-pr grandchild", "todo", container, int32Ptr(1))
	got = completeExternalPRViaHandler(t, container, 1002)
	if got.Outcome != "recorded" || got.Reason != "guard_not_satisfied" {
		t.Fatalf("has-children outcome = %#v, want recorded/guard_not_satisfied", got)
	}
	assertIssueStatus(t, container, "todo")

	parentForOpenPR := createExternalPRTestIssue(t, "external-pr open-pr parent", "todo", "", nil)
	blocked := createExternalPRTestIssue(t, "external-pr open-pr child", "todo", parentForOpenPR, int32Ptr(1))
	openReq := externalPRCompletionReq(testWorkspaceID, blocked, 1003)
	openReq.State = "open"
	if status := registerExternalPRViaHandlerWithToken(openReq, "completion-policy-test-token"); status != http.StatusOK {
		t.Fatalf("register open blocker status=%d", status)
	}
	got = completeExternalPRViaHandler(t, blocked, 1004)
	if got.Outcome != "recorded" || got.Reason != "guard_not_satisfied" {
		t.Fatalf("open-pr outcome = %#v, want recorded/guard_not_satisfied", got)
	}
	assertIssueStatus(t, blocked, "todo")
}

func TestCompleteIssueFromExternalPRCompletesLeafChildAndPublishes(t *testing.T) {
	ctx := context.Background()
	workspaceID := testWorkspaceID
	parent := createExternalPRTestIssue(t, "external-pr success parent", "todo", "", nil)
	child := createExternalPRTestIssue(t, "external-pr success child", "todo", parent, int32Ptr(1))

	eventsCh := make(chan events.Event, 8)
	testHandler.Bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		if payload, ok := e.Payload.(map[string]any); ok && payload["source"] == "external_pr_merged" {
			eventsCh <- e
		}
	})

	completion := externalPRCompletionReq(workspaceID, child, 1005)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "completion-policy-test-token")
	got := completeExternalPRViaHandler(t, child, completion.ExternalNumber)
	if got.Outcome != "completed" || got.IssueID != child {
		t.Fatalf("success outcome = %#v, want completed for child", got)
	}
	assertIssueStatus(t, child, "done")

	select {
	case <-eventsCh:
	default:
		t.Fatalf("expected %s event with source external_pr_merged", protocol.EventIssueUpdated)
	}

	var systemComments int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM comment WHERE issue_id=$1 AND author_type='system' AND type='system'`, parent).Scan(&systemComments); err != nil {
		t.Fatalf("count parent system comments: %v", err)
	}
	if systemComments == 0 {
		t.Fatalf("expected parent child-done system comment")
	}
}

func TestRegisterExternalTerminalFactReevaluatesEarlierMergedSibling(t *testing.T) {
	parent := createExternalPRTestIssue(t, "external registration reeval parent", "in_progress", "", nil)
	child := createExternalPRTestIssue(t, "external registration reeval child", "in_progress", parent, int32Ptr(1))
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "registration-reeval-token")
	blocker := externalPRCompletionReq(testWorkspaceID, child, 1605)
	blocker.State = "open"
	blocker.IdempotencyKey = ""
	intent := false
	blocker.CompletionIntent = &intent
	if status := registerExternalPRViaHandlerWithToken(blocker, "registration-reeval-token"); status != http.StatusOK {
		t.Fatalf("register blocker status=%d", status)
	}
	merged := externalPRCompletionReq(testWorkspaceID, child, 1604)
	merged.IdempotencyKey = ""
	if status := registerExternalPRViaHandlerWithToken(merged, "registration-reeval-token"); status != http.StatusOK {
		t.Fatalf("register merged sibling status=%d", status)
	}
	assertIssueStatus(t, child, "in_progress")

	blocker.State = "closed"
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", blocker)
	req.Header.Set("Authorization", "Bearer registration-reeval-token")
	w := httptest.NewRecorder()
	testHandler.RegisterExternalPullRequestLink(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register closed blocker status=%d body=%s", w.Code, w.Body.String())
	}
	assertIssueStatus(t, child, "done")
}

func TestListExternalPullRequestsForIssue(t *testing.T) {
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "external-pr list parent", "todo", "", nil)
	child := createExternalPRTestIssue(t, "external-pr list child", "todo", parent, int32Ptr(1))

	authoritative := externalPRCompletionReq(testWorkspaceID, child, 1101)
	authoritative.State = "merged"
	authoritative.MergedSHA = "11384b43b138b2a2d79cd7eb3c8c2e533900cfeb"
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, authoritative); err != nil {
		t.Fatalf("seed authoritative link: %v", err)
	}
	inferred := externalPRCompletionReq(testWorkspaceID, child, 1102)
	inferred.LinkConfidence = "inferred"
	inferred.State = "open"
	intent := false
	inferred.CompletionIntent = &intent
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, inferred); err != nil {
		t.Fatalf("seed inferred link: %v", err)
	}

	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+child+"/external-prs", nil), "id", child)
	rr := httptest.NewRecorder()
	testHandler.ListExternalPullRequestsForIssue(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		ExternalPullRequests []externalPullRequestLinkResponse `json:"external_pull_requests"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.ExternalPullRequests) != 2 {
		t.Fatalf("external_pull_requests length = %d, want 2", len(payload.ExternalPullRequests))
	}
	var foundAuthoritative, foundInferred bool
	for _, pr := range payload.ExternalPullRequests {
		if pr.ExternalNumber == 1101 {
			foundAuthoritative = pr.Provider == "ags" && strings.HasPrefix(pr.ExternalRepo, "handler-tests/external-pr-") && pr.State == "merged" && pr.LinkConfidence == "authoritative" && pr.MergedSHA != nil && *pr.MergedSHA == "11384b43b138b2a2d79cd7eb3c8c2e533900cfeb"
		}
		if pr.ExternalNumber == 1102 {
			foundInferred = pr.LinkConfidence == "inferred" && !pr.CompletionIntent
		}
	}
	if !foundAuthoritative || !foundInferred {
		t.Fatalf("response missing authoritative/inferred coverage: %#v", payload.ExternalPullRequests)
	}

	parentReq := withURLParam(newRequest(http.MethodGet, "/api/issues/"+parent+"/external-prs", nil), "id", parent)
	parentRR := httptest.NewRecorder()
	testHandler.ListExternalPullRequestsForIssue(parentRR, parentReq)
	if parentRR.Code != http.StatusOK {
		t.Fatalf("parent status = %d body=%s", parentRR.Code, parentRR.Body.String())
	}
	var parentPayload struct {
		ExternalPullRequests []externalPullRequestLinkResponse `json:"external_pull_requests"`
	}
	if err := json.NewDecoder(parentRR.Body).Decode(&parentPayload); err != nil {
		t.Fatalf("decode parent response: %v", err)
	}
	if len(parentPayload.ExternalPullRequests) != 0 {
		t.Fatalf("parent inherited %d child external PRs, want exact-issue empty list", len(parentPayload.ExternalPullRequests))
	}
}

func TestDeleteIssueRemovesExternalPRLinksAtomically(t *testing.T) {
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "external-pr delete cleanup", "todo", "", nil)
	req := externalPRCompletionReq(testWorkspaceID, issueID, 1501)
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, req); err != nil {
		t.Fatalf("seed external PR link: %v", err)
	}
	if err := testHandler.Queries.DeleteIssue(ctx, db.DeleteIssueParams{
		ID:          parseUUID(issueID),
		WorkspaceID: parseUUID(testWorkspaceID),
	}); err != nil {
		t.Fatalf("delete issue with external PR link: %v", err)
	}

	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM external_pull_request_link WHERE issue_id = $1`,
		parseUUID(issueID),
	).Scan(&count); err != nil {
		t.Fatalf("count external PR links after issue delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("external PR links after issue delete = %d, want 0", count)
	}
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM external_pull_request_receipt WHERE issue_id = $1`,
		parseUUID(issueID),
	).Scan(&count); err != nil {
		t.Fatalf("count external PR receipts after issue delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("external PR receipts after issue delete = %d, want 0", count)
	}
	// T016 retired workload_pr_merge_delegation* tables; External PR link/receipt
	// cleanup above is the remaining product surface for this test.
}

func TestExternalPRWritesRejectCrossWorkspaceIssue(t *testing.T) {
	ctx := context.Background()
	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "External PR Foreign Workspace", "external-pr-foreign-"+uuid.New().String()[:8], "Cross-workspace External PR test", "EPF").Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	var foreignIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, number, title, status, position, creator_type, creator_id)
		VALUES ($1, 1, 'foreign external PR issue', 'todo', 1, 'member', $2)
		RETURNING id
	`, foreignWorkspaceID, testUserID).Scan(&foreignIssueID); err != nil {
		t.Fatalf("create foreign issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM activity_log WHERE issue_id=$1`, foreignIssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, foreignIssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, foreignIssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, foreignWorkspaceID)
	})

	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "test-external-pr-token")
	cases := []struct {
		name   string
		path   string
		number int32
		call   func(http.ResponseWriter, *http.Request)
	}{
		{name: "register", path: "/api/integrations/external-pr/links", number: 1104, call: testHandler.RegisterExternalPullRequestLink},
		{name: "complete", path: "/api/integrations/external-pr/complete-from-merge", number: 1105, call: testHandler.CompleteIssueFromExternalPR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := externalPRCompletionReq(testWorkspaceID, foreignIssueID, tc.number)
			req := newRequest(http.MethodPost, tc.path, body)
			req.Header.Set("Authorization", "Bearer test-external-pr-token")
			rr := httptest.NewRecorder()
			tc.call(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
			}
		})
	}

	var linkCount, activityCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM external_pull_request_link WHERE issue_id=$1`, foreignIssueID).Scan(&linkCount); err != nil {
		t.Fatalf("count cross-workspace links: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM activity_log WHERE issue_id=$1 AND action LIKE 'external_pr_%'`, foreignIssueID).Scan(&activityCount); err != nil {
		t.Fatalf("count cross-workspace activity: %v", err)
	}
	if linkCount != 0 || activityCount != 0 {
		t.Fatalf("cross-workspace write leaked link/activity: links=%d activity=%d", linkCount, activityCount)
	}
}

func TestPublicExternalAuthoritativeOpenWithoutIntentBlocksMergedGitHub(t *testing.T) {
	ctx := context.Background()
	_, child, _ := createCompletionPolicyIssuePairViaHandlers(t, "mixed-provider-public")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	open := externalPRCompletionReq(testWorkspaceID, child.ID, 1601)
	open.State = "open"
	intent := false
	open.CompletionIntent = &intent
	if status := registerExternalPRViaHandler(open); status != http.StatusOK {
		t.Fatalf("register authoritative external blocker status=%d", status)
	}

	secret := "mixed-provider-public-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	t.Setenv("GITHUB_APP_SLUG", "mixed-provider-public")
	installationID := time.Now().UnixNano()
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID: parseUUID(testWorkspaceID), InstallationID: installationID,
		AccountLogin: "mixed-provider-public", AccountType: "User",
	}); err != nil {
		t.Fatalf("create GitHub installation fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM github_installation WHERE installation_id=$1`, installationID)
	})
	firePRWebhook(t, secret, installationID, 1601, "Fix "+child.Identifier, "Closes "+child.Identifier, "fix/mixed-provider-public", "merged")
	assertIssueStatus(t, child.ID, "todo")
}

func TestExternalPRPublicIdentityAndIdempotencyAreImmutable(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	issueA := createExternalPRTestIssue(t, "external immutable A", "todo", "", nil)
	issueB := createExternalPRTestIssue(t, "external immutable B", "todo", "", nil)
	first := externalPRCompletionReq(testWorkspaceID, issueA, 1602)
	if status := registerExternalPRViaHandler(first); status != http.StatusOK {
		t.Fatalf("first public registration status=%d", status)
	}
	if status := registerExternalPRViaHandler(first); status != http.StatusOK {
		t.Fatalf("exact public replay status=%d", status)
	}

	changed := first
	changed.MergedSHA = "different-payload"
	if status := registerExternalPRViaHandler(changed); status != http.StatusConflict {
		t.Fatalf("changed payload status=%d, want 409", status)
	}

	rebound := first
	rebound.IssueID = issueB
	if status := registerExternalPRViaHandler(rebound); status != http.StatusConflict {
		t.Fatalf("cross-issue rebind status=%d, want 409", status)
	}
}

func TestExternalPRConcurrentFirstBindCannotCrossIssue(t *testing.T) {
	ctx := context.Background()
	issueA := createExternalPRTestIssue(t, "external concurrent bind A", "todo", "", nil)
	issueB := createExternalPRTestIssue(t, "external concurrent bind B", "todo", "", nil)
	number := int32(1699)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	reqA := externalPRCompletionReq(testWorkspaceID, issueA, number)
	reqA.State = "open"
	reqB := externalPRCompletionReq(testWorkspaceID, issueB, number)
	reqB.State = "open"
	reqB.ExternalRepo = reqA.ExternalRepo
	reqB.IdempotencyKey = reqA.IdempotencyKey + "-other-issue"

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	statuses := make([]int, 2)
	for i, request := range []externalPullRequestLinkRequest{reqA, reqB} {
		go func(index int, body externalPullRequestLinkRequest) {
			defer wg.Done()
			<-start
			statuses[index] = registerExternalPRViaHandler(body)
		}(i, request)
	}
	close(start)
	wg.Wait()

	successes, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("concurrent bind statuses=%v", statuses)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent first bind successes=%d conflicts=%d statuses=%v", successes, conflicts, statuses)
	}

	var persistedIssueID string
	if err := testPool.QueryRow(ctx, `SELECT issue_id::text FROM external_pull_request_link
WHERE workspace_id=$1 AND provider=$2 AND external_repo=$3 AND external_number=$4`,
		testWorkspaceID, "ags", reqA.ExternalRepo, number).Scan(&persistedIssueID); err != nil {
		t.Fatal(err)
	}
	var wrongReceipts int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM external_pull_request_receipt
WHERE workspace_id=$1 AND external_repo=$2 AND external_number=$3 AND issue_id::text<>$4`,
		testWorkspaceID, reqA.ExternalRepo, number, persistedIssueID).Scan(&wrongReceipts); err != nil {
		t.Fatal(err)
	}
	if wrongReceipts != 0 {
		t.Fatalf("concurrent loser wrote %d wrong-Issue receipts", wrongReceipts)
	}
}

func TestExternalPRConcurrentReverseIssueOrderDoesNotDeadlock(t *testing.T) {
	issueA := createExternalPRTestIssue(t, "external reverse lock A", "todo", "", nil)
	issueB := createExternalPRTestIssue(t, "external reverse lock B", "todo", "", nil)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	firstA := externalPRCompletionReq(testWorkspaceID, issueA, 1700)
	firstA.State = "open"
	firstB := externalPRCompletionReq(testWorkspaceID, issueB, 1701)
	firstB.State = "open"
	if status := registerExternalPRViaHandler(firstA); status != http.StatusOK {
		t.Fatalf("seed A status=%d", status)
	}
	if status := registerExternalPRViaHandler(firstB); status != http.StatusOK {
		t.Fatalf("seed B status=%d", status)
	}
	crossA, crossB := firstA, firstB
	crossA.IssueID = issueB
	crossA.IdempotencyKey += "-cross"
	crossB.IssueID = issueA
	crossB.IdempotencyKey += "-cross"
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for _, request := range []externalPullRequestLinkRequest{crossA, crossB} {
		go func(body externalPullRequestLinkRequest) {
			<-start
			statuses <- registerExternalPRViaHandler(body)
		}(request)
	}
	close(start)
	for range 2 {
		select {
		case status := <-statuses:
			if status != http.StatusConflict {
				t.Fatalf("reverse-order conflict status=%d", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reverse-order public requests deadlocked")
		}
	}
}

func TestExternalPRConcurrentExactReplayWritesSingleActivity(t *testing.T) {
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "external concurrent activity", "todo", "", nil)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	request := externalPRCompletionReq(testWorkspaceID, issueID, 1702)
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			statuses <- registerExternalPRViaHandler(request)
		}()
	}
	close(start)
	for range 2 {
		if status := <-statuses; status != http.StatusOK {
			t.Fatalf("exact replay status=%d", status)
		}
	}
	for _, action := range []string{"external_pr_linked", "external_pr_merged"} {
		var count int
		if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM activity_log WHERE issue_id=$1 AND action=$2`, issueID, action).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("concurrent exact replay activity %s count=%d, want 1", action, count)
		}
	}
}

func TestExternalPRActivityFailureRollsBackFact(t *testing.T) {
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "external activity rollback", "todo", "", nil)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	request := externalPRCompletionReq(testWorkspaceID, issueID, 1703)
	request.State = "open"
	failing := *testHandler
	failing.ExternalPRActivityWriter = func(context.Context, dbExecutor, string, externalPullRequestLinkRequest, string) error {
		return errors.New("injected external activity failure")
	}
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", request)
	req.Header.Set("Authorization", "Bearer external-pr-handler-test-token")
	w := httptest.NewRecorder()
	failing.RegisterExternalPullRequestLink(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("activity failure status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "injected external activity failure") {
		t.Fatalf("activity failure leaked internal error: %s", w.Body.String())
	}
	for table := range map[string]struct{}{"external_pull_request_link": {}, "external_pull_request_receipt": {}, "activity_log": {}} {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*)::int FROM %s WHERE issue_id=$1", table)
		if err := testPool.QueryRow(ctx, query, issueID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("activity failure left %s rows=%d", table, count)
		}
	}
}

func TestExternalPRCompletionFailureReturnsGeneric503(t *testing.T) {
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "external completion infra parent", "todo", "", nil)
	child := createExternalPRTestIssue(t, "external completion infra child", "todo", parent, int32Ptr(1))
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-completion-infra-token")
	failing := *testHandler
	failing.CompletionActivityWriter = func(context.Context, *db.Queries, db.CreateActivityParams) (db.ActivityLog, error) {
		return db.ActivityLog{}, errors.New("database password=secret SQLSTATE 40P01")
	}
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", externalPRCompletionReq(testWorkspaceID, child, 10002))
	req.Header.Set("Authorization", "Bearer external-completion-infra-token")
	w := httptest.NewRecorder()
	failing.CompleteIssueFromExternalPR(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("completion failure status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "SQLSTATE") {
		t.Fatalf("completion failure leaked internal detail: %s", w.Body.String())
	}
	assertIssueStatus(t, child, "todo")
	var completionActivities int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM activity_log WHERE issue_id=$1 AND action='issue_completed_by_external_pr'`, child).Scan(&completionActivities); err != nil {
		t.Fatal(err)
	}
	if completionActivities != 0 {
		t.Fatalf("failed completion persisted activities=%d", completionActivities)
	}
}

func TestDeleteIssueSerializesBeforeExternalPRFact(t *testing.T) {
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "external delete lock", "todo", "", nil)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-delete-lock-token")
	request := externalPRCompletionReq(testWorkspaceID, issueID, nextCompletionPolicyPRNumber())
	request.State = "open"
	if status := registerExternalPRViaHandlerWithToken(request, "external-delete-lock-token"); status != http.StatusOK {
		t.Fatalf("seed external PR status=%d", status)
	}
	t.Cleanup(func() {
		testHandler.IssueDeleteHook = nil
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pull_request_receipt WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pull_request_link WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, issueID)
	})

	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.IssueDeleteHook = func(stage string) {
		if stage == "completion_lock_acquired" {
			close(locked)
			<-release
		}
	}
	deleteDone := make(chan int, 1)
	go func() {
		req := newRequest(http.MethodDelete, "/api/issues/"+issueID+"?workspace_id="+testWorkspaceID, nil)
		req = withURLParam(req, "id", issueID)
		w := httptest.NewRecorder()
		testHandler.DeleteIssue(w, req)
		deleteDone <- w.Code
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not acquire external Issue lock")
	}
	request.State = "merged"
	request.IdempotencyKey = "external-delete-lock-merged"
	type providerResponse struct {
		status int
		body   string
	}
	providerDone := make(chan providerResponse, 1)
	go func() {
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", request)
		req.Header.Set("Authorization", "Bearer external-delete-lock-token")
		w := httptest.NewRecorder()
		testHandler.RegisterExternalPullRequestLink(w, req)
		providerDone <- providerResponse{status: w.Code, body: w.Body.String()}
	}()
	select {
	case result := <-providerDone:
		t.Fatalf("external provider crossed delete lock status=%d body=%s", result.status, result.body)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if status := <-deleteDone; status != http.StatusNoContent {
		t.Fatalf("delete status=%d", status)
	}
	testHandler.IssueDeleteHook = nil
	result := <-providerDone
	if result.status != http.StatusBadRequest {
		t.Fatalf("post-delete external callback status=%d body=%s, want 400", result.status, result.body)
	}
	if strings.Contains(result.body, "deadlock") || strings.Contains(result.body, "40P01") || strings.Contains(result.body, "SQLSTATE") {
		t.Fatalf("post-delete external callback exposed database deadlock: %s", result.body)
	}
	var linkCount, receiptCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM external_pull_request_link WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issueID).Scan(&linkCount); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM external_pull_request_receipt WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issueID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if linkCount != 0 || receiptCount != 0 {
		t.Fatalf("deleted Issue retained external facts links=%d receipts=%d", linkCount, receiptCount)
	}
}

func TestBatchDeleteSerializesBeforeExternalPRFact(t *testing.T) {
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "external batch delete lock", "todo", "", nil)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-batch-delete-token")
	request := externalPRCompletionReq(testWorkspaceID, issueID, nextCompletionPolicyPRNumber())
	request.State = "open"
	if status := registerExternalPRViaHandlerWithToken(request, "external-batch-delete-token"); status != http.StatusOK {
		t.Fatalf("seed external PR status=%d", status)
	}
	t.Cleanup(func() {
		testHandler.IssueDeleteHook = nil
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pull_request_receipt WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pull_request_link WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, issueID)
	})
	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.IssueDeleteHook = func(stage string) {
		if stage == "batch_completion_locks_acquired" {
			close(locked)
			<-release
		}
	}
	deleteDone := make(chan int, 1)
	go func() {
		req := newRequest(http.MethodPost, "/api/issues/batch-delete?workspace_id="+testWorkspaceID, BatchDeleteIssuesRequest{IssueIDs: []string{issueID}})
		w := httptest.NewRecorder()
		testHandler.BatchDeleteIssues(w, req)
		deleteDone <- w.Code
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("batch delete did not acquire Issue locks")
	}
	request.State = "merged"
	request.IdempotencyKey += "-batch-delete"
	providerDone := make(chan int, 1)
	go func() { providerDone <- registerExternalPRViaHandlerWithToken(request, "external-batch-delete-token") }()
	select {
	case status := <-providerDone:
		t.Fatalf("external provider crossed batch delete lock status=%d", status)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if status := <-deleteDone; status != http.StatusOK {
		t.Fatalf("batch delete status=%d", status)
	}
	testHandler.IssueDeleteHook = nil
	if status := <-providerDone; status != http.StatusBadRequest {
		t.Fatalf("post-batch-delete callback status=%d, want 400", status)
	}
}

func TestWorkspaceDeleteSerializesBeforeExternalPRFact(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	var workspaceID, issueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1,$2,'','EWD') RETURNING id`, "External workspace delete "+suffix, "external-workspace-delete-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, workspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,number,title,status,position,creator_type,creator_id) VALUES ($1,1,'external workspace delete','todo',1,'member',$2) RETURNING id`, workspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testHandler.IssueDeleteHook = nil
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, workspaceID)
	})
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-workspace-delete-token")
	request := externalPRCompletionReq(workspaceID, issueID, nextCompletionPolicyPRNumber())
	request.State = "open"
	if status := registerExternalPRViaHandlerWithToken(request, "external-workspace-delete-token"); status != http.StatusOK {
		t.Fatalf("seed external PR status=%d", status)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.IssueDeleteHook = func(stage string) {
		if stage == "workspace_completion_locks_acquired" {
			close(locked)
			<-release
		}
	}
	deleteDone := make(chan int, 1)
	go func() {
		req := withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+workspaceID, nil), "id", workspaceID)
		w := httptest.NewRecorder()
		testHandler.DeleteWorkspace(w, req)
		deleteDone <- w.Code
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace delete did not acquire provider/Issue locks")
	}
	request.State = "merged"
	request.IdempotencyKey += "-workspace-delete"
	providerDone := make(chan int, 1)
	go func() {
		providerDone <- registerExternalPRViaHandlerWithToken(request, "external-workspace-delete-token")
	}()
	select {
	case status := <-providerDone:
		t.Fatalf("external provider crossed workspace delete lock status=%d", status)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if status := <-deleteDone; status != http.StatusNoContent {
		t.Fatalf("workspace delete status=%d", status)
	}
	testHandler.IssueDeleteHook = nil
	if status := <-providerDone; status != http.StatusBadRequest {
		t.Fatalf("post-workspace-delete callback status=%d, want 400", status)
	}
}

func TestExternalPRServiceTokenRequiresExactBearerScheme(t *testing.T) {
	issueID := createExternalPRTestIssue(t, "external strict bearer", "todo", "", nil)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "strict-bearer-token")
	body := externalPRCompletionReq(testWorkspaceID, issueID, 1704)
	for _, authorization := range []string{
		"strict-bearer-token",
		"Basic strict-bearer-token",
		"bearer strict-bearer-token",
		"Bearer  strict-bearer-token",
		"Bearer strict-bearer-token ",
		" Bearer strict-bearer-token",
		"Bearer",
	} {
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", body)
		req.Header.Set("Authorization", authorization)
		w := httptest.NewRecorder()
		testHandler.RegisterExternalPullRequestLink(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status=%d, want 401", authorization, w.Code)
		}
	}
	if status := registerExternalPRViaHandlerWithToken(body, "strict-bearer-token"); status != http.StatusOK {
		t.Fatalf("exact bearer status=%d", status)
	}
}

func TestExternalPRPublicMergedStateIsAbsorbingWithoutProviderVersion(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-handler-test-token")
	issueID := createExternalPRTestIssue(t, "external merged absorbing", "todo", "", nil)
	merged := externalPRCompletionReq(testWorkspaceID, issueID, 1603)
	merged.IdempotencyKey = ""
	if status := registerExternalPRViaHandler(merged); status != http.StatusOK {
		t.Fatalf("register merged fact status=%d", status)
	}
	stale := merged
	stale.State = "open"
	stale.MergedSHA = ""
	if status := registerExternalPRViaHandler(stale); status != http.StatusOK {
		t.Fatalf("register stale open replay status=%d", status)
	}

	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/external-prs", nil), "id", issueID)
	w := httptest.NewRecorder()
	testHandler.ListExternalPullRequestsForIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list absorbing state status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		ExternalPullRequests []externalPullRequestLinkResponse `json:"external_pull_requests"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ExternalPullRequests) != 1 || payload.ExternalPullRequests[0].State != "merged" {
		t.Fatalf("public readback=%#v, want one merged link", payload.ExternalPullRequests)
	}
}

func TestUpsertExternalPRRejectsUnsafeURLs(t *testing.T) {
	issueID := createExternalPRTestIssue(t, "external-pr unsafe URL", "todo", "", nil)
	req := externalPRCompletionReq(testWorkspaceID, issueID, 1106)
	req.ExternalURL = "javascript:alert(1)"
	if _, err := testHandler.upsertExternalPullRequestLink(context.Background(), req); err == nil {
		t.Fatal("upsert accepted unsafe external_url")
	}
	req.ExternalURL = "https://ags.example/repo/pull/1106"
	req.MergeURL = "data:text/html,unsafe"
	if _, err := testHandler.upsertExternalPullRequestLink(context.Background(), req); err == nil {
		t.Fatal("upsert accepted unsafe merge_url")
	}
}

func TestCompleteIssueFromExternalPRWritesActivityNotIssueComments(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "test-external-pr-token")
	parent := createExternalPRTestIssue(t, "external-pr activity parent", "todo", "", nil)
	child := createExternalPRTestIssue(t, "external-pr activity child", "todo", parent, int32Ptr(1))
	reqBody := externalPRCompletionReq(testWorkspaceID, child, 1103)

	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", reqBody)
	req.Header.Set("Authorization", "Bearer test-external-pr-token")
	rr := httptest.NewRecorder()
	testHandler.CompleteIssueFromExternalPR(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var childComments int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM comment WHERE issue_id=$1`, child).Scan(&childComments); err != nil {
		t.Fatalf("count child comments: %v", err)
	}
	if childComments != 0 {
		t.Fatalf("external PR complete wrote %d child comments, want 0", childComments)
	}

	for _, action := range []string{"external_pr_linked", "external_pr_merged", "issue_completed_by_external_pr"} {
		var count int
		if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM activity_log WHERE issue_id=$1 AND action=$2 AND actor_type='system'`, child, action).Scan(&count); err != nil {
			t.Fatalf("count activity %s: %v", action, err)
		}
		if count != 1 {
			t.Fatalf("activity %s count = %d, want 1", action, count)
		}
	}
}

func registerExternalPRViaHandler(body externalPullRequestLinkRequest) int {
	return registerExternalPRViaHandlerWithToken(body, "external-pr-handler-test-token")
}

func registerExternalPRViaHandlerWithToken(body externalPullRequestLinkRequest, token string) int {
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/links", body)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	testHandler.RegisterExternalPullRequestLink(w, req)
	return w.Code
}

func externalPRCompletionReq(workspaceID, issueID string, number int32) externalPullRequestLinkRequest {
	intent := true
	repository := "handler-tests/external-pr-" + issueID
	return externalPullRequestLinkRequest{
		Provider:         "ags",
		IssueID:          issueID,
		WorkspaceID:      workspaceID,
		Workspace:        handlerTestWorkspaceSlug,
		IssueKey:         "HAN-" + fmt.Sprint(number),
		ExternalRepo:     repository,
		ExternalNumber:   number,
		ExternalURL:      fmt.Sprintf("http://ags.local/pull/%d", number),
		MergeProvider:    "forgejo",
		MergeRepo:        repository,
		MergeNumber:      number,
		MergeURL:         fmt.Sprintf("http://forgejo.local/pulls/%d", number),
		MergedSHA:        fmt.Sprintf("sha-%d", number),
		CompletionIntent: &intent,
		LinkConfidence:   "authoritative",
		State:            "merged",
		IdempotencyKey:   fmt.Sprintf("external-pr-test-%s-%d", issueID, number),
	}
}

func createExternalPRTestIssue(t *testing.T, title, status, parentID string, stage *int32) string {
	t.Helper()
	ctx := context.Background()
	var number int32
	if err := testPool.QueryRow(ctx, `
UPDATE workspace
SET issue_counter = GREATEST(
    issue_counter,
    (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id=$1)
) + 1
WHERE id=$1
RETURNING issue_counter`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatalf("allocate test issue number: %v", err)
	}

	var id string
	var err error
	if parentID == "" && stage == nil {
		err = testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number)
			VALUES ($1, $2, $3, 'member', $4, $5)
			RETURNING id
		`, testWorkspaceID, title, status, testUserID, number).Scan(&id)
	} else if stage == nil {
		err = testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, parent_issue_id, number)
			VALUES ($1, $2, $3, 'member', $4, $5, $6)
			RETURNING id
		`, testWorkspaceID, title, status, testUserID, parentID, number).Scan(&id)
	} else {
		err = testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, parent_issue_id, stage, number)
			VALUES ($1, $2, $3, 'member', $4, $5, $6, $7)
			RETURNING id
		`, testWorkspaceID, title, status, testUserID, parentID, *stage, number).Scan(&id)
	}
	if err != nil {
		t.Fatalf("create test issue %q: %v", title, err)
	}
	return id
}

func assertIssueStatus(t *testing.T, issueID, want string) {
	t.Helper()
	var got string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id=$1`, issueID).Scan(&got); err != nil {
		t.Fatalf("load issue status: %v", err)
	}
	if got != want {
		t.Fatalf("issue %s status = %q, want %q", issueID, got, want)
	}
}

func int32Ptr(v int32) *int32 { return &v }
