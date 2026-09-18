package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func externalPRTestRequest(t *testing.T, issueID string, number int32) externalPRRequest {
	t.Helper()
	var issueNumber int32
	dbfx.QueryRow(t, `SELECT number FROM issue WHERE id=$1`, issueID).Scan(&issueNumber)
	repo := fmt.Sprintf("owner/repo-%d", number)
	mergeRepo := fmt.Sprintf("forgejo/repo-%d", number)
	intent := true
	return externalPRRequest{
		Provider: "ags", WorkspaceID: testWorkspaceID, IssueID: issueID,
		Workspace: handlerTestWorkspaceSlug, IssueKey: fmt.Sprintf("HAN-%d", issueNumber),
		ExternalRepo: repo, ExternalNumber: number,
		// A different Issue-shaped string in the URL must never influence binding.
		ExternalURL:   fmt.Sprintf("https://ags.example/HAN-999/%s/pull/%d", repo, number),
		MergeProvider: "forgejo", MergeRepo: mergeRepo, MergeNumber: number,
		MergeURL:                fmt.Sprintf("https://forgejo.example/%s/pulls/%d", mergeRepo, number),
		MergedSHA:               "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TargetInstance:          "test-instance",
		CanonicalRepositoryID:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalRepository:     repo,
		ProviderBindingID:       "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ProviderBindingRevision: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ProviderRepository:      mergeRepo,
		ExpectedHeadSHA:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedBaseSHA:         "cccccccccccccccccccccccccccccccccccccccc",
		BaseRef:                 "main", DelegatedMergeMethod: "squash",
		ProjectionFactsRevision: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		CompletionIntent:        &intent, LinkConfidence: "authoritative", State: "merged",
		IdempotencyKey: fmt.Sprintf("external-pr:test:%d", number),
	}
}

func callExternalPR(t *testing.T, handler http.HandlerFunc, body externalPRRequest) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", body)
	req.Header.Set("Authorization", "Bearer external-pr-test-token")
	response := httptest.NewRecorder()
	handler(response, req)
	return response
}

func claimExternalPRTestWork(t *testing.T, issueID string) externalPRWork {
	t.Helper()
	var work externalPRWork
	dbfx.QueryRow(t, `
UPDATE external_pr_reconcile_work
SET state='claimed', attempt=attempt+1, lease_owner='test', lease_token=gen_random_uuid(),
    lease_expires_at=now()+interval '5 minutes'
WHERE id=(
    SELECT id FROM external_pr_reconcile_work
    WHERE issue_id=$1 AND state IN ('pending','retry_wait')
    ORDER BY created_at DESC LIMIT 1
)
RETURNING id, workspace_id, issue_id, link_id, source_revision, state, attempt,
    max_attempts, lease_token, previous_status, status_activity_id,
    intended_parent_id, activity_published, issue_published, parent_comment_id,
    parent_wake_done`, issueID).Scan(
		&work.ID, &work.WorkspaceID, &work.IssueID, &work.LinkID, &work.SourceRevision,
		&work.State, &work.Attempt, &work.MaxAttempts, &work.LeaseToken,
		&work.PreviousStatus, &work.StatusActivityID, &work.IntendedParentID,
		&work.ActivityPublished, &work.IssuePublished, &work.ParentCommentID,
		&work.ParentWakeDone,
	)
	return work
}

func TestExternalPRExplicitAuthorityReplayConflictAndCompletion(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-test-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "test-instance")
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")

	parentID := dbfx.Issue(t, "External PR parent")
	childID := dbfx.Issue(t, "External PR child", testutil.Cols{"parent_issue_id": parentID})
	otherChildID := dbfx.Issue(t, "External PR other issue")
	request := externalPRTestRequest(t, childID, 700001)

	first := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, request)
	if first.Code != http.StatusOK {
		t.Fatalf("first callback status=%d body=%s", first.Code, first.Body.String())
	}
	assertExternalPRCounts(t, childID, 1, 1, 1, 1)
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(childID))
	if err != nil {
		t.Fatal(err)
	}
	projection, err := testHandler.listExternalPullRequestsForIssue(context.Background(), issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection) != 1 || projection[0].Provider != "ags" || projection[0].HtmlURL != request.ExternalURL {
		t.Fatalf("unified External PR projection=%+v", projection)
	}

	replay := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, request)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	assertExternalPRCounts(t, childID, 1, 1, 1, 1)

	alias := request
	alias.IdempotencyKey += ":alias"
	aliasReplay := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, alias)
	if aliasReplay.Code != http.StatusOK {
		t.Fatalf("alias replay status=%d body=%s", aliasReplay.Code, aliasReplay.Body.String())
	}
	assertExternalPRCounts(t, childID, 1, 2, 1, 1)

	changed := request
	changed.MergeURL += "?changed=1"
	conflict := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	terminalChanged := request
	terminalChanged.IdempotencyKey += ":changed-terminal"
	terminalChanged.MergedSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	terminalConflict := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, terminalChanged)
	if terminalConflict.Code != http.StatusConflict {
		t.Fatalf("changed terminal status=%d body=%s", terminalConflict.Code, terminalConflict.Body.String())
	}

	rebound := externalPRTestRequest(t, otherChildID, request.ExternalNumber)
	rebound.ExternalRepo = request.ExternalRepo
	rebound.CanonicalRepository = request.ExternalRepo
	rebound.IdempotencyKey += ":rebind"
	rebindConflict := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, rebound)
	if rebindConflict.Code != http.StatusConflict {
		t.Fatalf("rebind status=%d body=%s", rebindConflict.Code, rebindConflict.Body.String())
	}
	var otherReceipts int
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pull_request_receipt WHERE issue_id=$1`, otherChildID).Scan(&otherReceipts)
	if otherReceipts != 0 {
		t.Fatalf("losing rebind wrote %d receipts", otherReceipts)
	}

	work := claimExternalPRTestWork(t, childID)
	if err := testHandler.processExternalPRWork(context.Background(), work); err != nil {
		t.Fatalf("process External PR work: %v", err)
	}
	var status, workState string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id=$1`, childID).Scan(&status)
	dbfx.QueryRow(t, `SELECT state FROM external_pr_reconcile_work WHERE id=$1`, work.ID).Scan(&workState)
	if status != "done" || workState != "succeeded" {
		t.Fatalf("status/work state = %s/%s, want done/succeeded", status, workState)
	}
	var statusActivities, parentComments int
	dbfx.QueryRow(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='status_changed' AND details->>'source'='external_pr_merged'`, childID).Scan(&statusActivities)
	dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='system'`, parentID).Scan(&parentComments)
	if statusActivities != 1 || parentComments != 1 {
		t.Fatalf("status activities/parent comments = %d/%d, want 1/1", statusActivities, parentComments)
	}
	var parentCommentID string
	dbfx.QueryRow(t, `SELECT parent_comment_id::text FROM external_pr_reconcile_work WHERE id=$1`, work.ID).Scan(&parentCommentID)
	if parentCommentID != uuidToString(work.ID) {
		t.Fatalf("durable parent comment=%s want work id %s", parentCommentID, uuidToString(work.ID))
	}

	// Recreate the crash window after the deterministic comment was inserted but
	// before its work flag committed. The retry must reuse the same comment ID.
	dbfx.Exec(t, `
UPDATE external_pr_reconcile_work
SET state='pending', parent_comment_id=NULL, parent_wake_done=FALSE,
    lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL,
    next_attempt_at=now(), completed_at=NULL
WHERE id=$1`, work.ID)
	retryWork := claimExternalPRTestWork(t, childID)
	if err := testHandler.processExternalPRWork(context.Background(), retryWork); err != nil {
		t.Fatalf("retry durable parent continuation: %v", err)
	}
	dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='system'`, parentID).Scan(&parentComments)
	if parentComments != 1 {
		t.Fatalf("parent comments after crash retry=%d want 1", parentComments)
	}
}

func TestExternalPRCompletionPolicyAndLeafGuard(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-test-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "test-instance")
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")

	parentID := dbfx.Issue(t, "External policy parent")
	recordOnlyID := dbfx.Issue(t, "External record only", testutil.Cols{
		"parent_issue_id": parentID,
		"metadata":        testutil.Raw(`'{"external_pr_completion_policy":"record_only"}'::jsonb`),
	})
	nonLeafID := dbfx.Issue(t, "External non-leaf", testutil.Cols{"parent_issue_id": parentID})
	dbfx.Issue(t, "External grandchild", testutil.Cols{"parent_issue_id": nonLeafID})

	for index, issueID := range []string{recordOnlyID, nonLeafID} {
		request := externalPRTestRequest(t, issueID, int32(700010+index))
		response := callExternalPR(t, testHandler.CompleteIssueFromExternalPR, request)
		if response.Code != http.StatusOK {
			t.Fatalf("callback %d status=%d body=%s", index, response.Code, response.Body.String())
		}
		work := claimExternalPRTestWork(t, issueID)
		if err := testHandler.processExternalPRWork(context.Background(), work); err != nil {
			t.Fatalf("process %d: %v", index, err)
		}
		var issueStatus, workState, reason string
		dbfx.QueryRow(t, `SELECT status FROM issue WHERE id=$1`, issueID).Scan(&issueStatus)
		dbfx.QueryRow(t, `SELECT state, last_error_code FROM external_pr_reconcile_work WHERE id=$1`, work.ID).Scan(&workState, &reason)
		if issueStatus != "todo" || workState != "recorded" {
			t.Fatalf("case %d status/work=%s/%s reason=%s", index, issueStatus, workState, reason)
		}
	}
}

func assertExternalPRCounts(t *testing.T, issueID string, links, receipts, activities, works int) {
	t.Helper()
	var gotLinks, gotReceipts, gotActivities, gotWorks int
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pull_request_link WHERE issue_id=$1`, issueID).Scan(&gotLinks)
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pull_request_receipt WHERE issue_id=$1`, issueID).Scan(&gotReceipts)
	dbfx.QueryRow(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='external_pr_recorded'`, issueID).Scan(&gotActivities)
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pr_reconcile_work WHERE issue_id=$1`, issueID).Scan(&gotWorks)
	if gotLinks != links || gotReceipts != receipts || gotActivities != activities || gotWorks != works {
		t.Fatalf("link/receipt/activity/work counts=%d/%d/%d/%d want=%d/%d/%d/%d",
			gotLinks, gotReceipts, gotActivities, gotWorks, links, receipts, activities, works)
	}
}

func TestExternalPRConcurrentFirstBindHasOneWinner(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "external-pr-test-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "test-instance")
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")

	firstIssue := dbfx.Issue(t, "External concurrent first")
	secondIssue := dbfx.Issue(t, "External concurrent second")
	first := externalPRTestRequest(t, firstIssue, 700020)
	second := externalPRTestRequest(t, secondIssue, 700020)
	second.ExternalRepo = first.ExternalRepo
	second.CanonicalRepository = first.CanonicalRepository
	second.IdempotencyKey += ":second"

	start := make(chan struct{})
	responses := make(chan int, 2)
	for _, request := range []externalPRRequest{first, second} {
		request := request
		go func() {
			<-start
			responses <- callExternalPR(t, testHandler.CompleteIssueFromExternalPR, request).Code
		}()
	}
	close(start)
	statuses := []int{<-responses, <-responses}
	winners, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			winners++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent winners/conflicts=%d/%d", winners, conflicts)
	}
	var links, receipts, works int
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pull_request_link WHERE external_number=700020 AND external_repo=$1`, first.ExternalRepo).Scan(&links)
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pull_request_receipt WHERE external_number=700020 AND external_repo=$1`, first.ExternalRepo).Scan(&receipts)
	dbfx.QueryRow(t, `SELECT count(*) FROM external_pr_reconcile_work WHERE external_number=700020 AND external_repo=$1`, first.ExternalRepo).Scan(&works)
	if links != 1 || receipts != 1 || works != 1 {
		t.Fatalf("concurrent link/receipt/work=%d/%d/%d", links, receipts, works)
	}
}

func TestExternalPRCompletionPolicyParser(t *testing.T) {
	cases := []struct {
		name     string
		metadata []byte
		allowed  bool
		reason   string
	}{
		{"missing", nil, true, ""},
		{"leaf", []byte(`{"external_pr_completion_policy":"leaf_child_only"}`), true, ""},
		{"record only", []byte(`{"external_pr_completion_policy":"record_only"}`), false, "record_only"},
		{"wrong type", []byte(`{"external_pr_completion_policy":true}`), false, "unsupported_completion_policy"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			allowed, reason := externalPRCompletionPolicy(test.metadata)
			if allowed != test.allowed || reason != test.reason {
				t.Fatalf("got %v/%q want %v/%q", allowed, reason, test.allowed, test.reason)
			}
		})
	}
}
