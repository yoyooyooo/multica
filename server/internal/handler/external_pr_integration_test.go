package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
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

func TestCompleteIssueFromExternalPRGuardMatrix(t *testing.T) {
	ctx := context.Background()
	workspaceID := testWorkspaceID

	noParent := createExternalPRTestIssue(t, "external-pr no parent", "todo", "", nil)
	got := testHandler.completeLeafChildIssueFromExternalPR(httptest.NewRequest(http.MethodPost, "/", nil), externalPRCompletionReq(workspaceID, noParent, 1001))
	if got.Outcome != "skipped" || got.Reason != "no_parent" {
		t.Fatalf("no-parent outcome = %#v, want skipped/no_parent", got)
	}
	assertIssueStatus(t, noParent, "todo")

	parentForContainer := createExternalPRTestIssue(t, "external-pr container parent", "todo", "", nil)
	container := createExternalPRTestIssue(t, "external-pr child with children", "todo", parentForContainer, int32Ptr(1))
	_ = createExternalPRTestIssue(t, "external-pr grandchild", "todo", container, int32Ptr(1))
	got = testHandler.completeLeafChildIssueFromExternalPR(httptest.NewRequest(http.MethodPost, "/", nil), externalPRCompletionReq(workspaceID, container, 1002))
	if got.Outcome != "skipped" || got.Reason != "has_children" {
		t.Fatalf("has-children outcome = %#v, want skipped/has_children", got)
	}
	assertIssueStatus(t, container, "todo")

	parentForOpenPR := createExternalPRTestIssue(t, "external-pr open-pr parent", "todo", "", nil)
	blocked := createExternalPRTestIssue(t, "external-pr open-pr child", "todo", parentForOpenPR, int32Ptr(1))
	openReq := externalPRCompletionReq(workspaceID, blocked, 1003)
	openReq.State = "open"
	if err := testHandler.upsertExternalPullRequestLink(ctx, openReq); err != nil {
		t.Fatalf("seed open PR link: %v", err)
	}
	got = testHandler.completeLeafChildIssueFromExternalPR(httptest.NewRequest(http.MethodPost, "/", nil), externalPRCompletionReq(workspaceID, blocked, 1004))
	if got.Outcome != "skipped" || got.Reason != "open_pr_exists" {
		t.Fatalf("open-pr outcome = %#v, want skipped/open_pr_exists", got)
	}
	assertIssueStatus(t, blocked, "todo")

	parentForUnknownPolicy := createExternalPRTestIssue(t, "external-pr unknown-policy parent", "todo", "", nil)
	unknownPolicy := createExternalPRTestIssue(t, "external-pr unknown-policy child", "todo", parentForUnknownPolicy, int32Ptr(1))
	if _, err := testPool.Exec(ctx, `
UPDATE issue
SET metadata = jsonb_build_object('external_pr_completion_policy', 'future_policy')
WHERE id=$1
`, unknownPolicy); err != nil {
		t.Fatalf("set unknown completion policy: %v", err)
	}
	got = testHandler.completeLeafChildIssueFromExternalPR(httptest.NewRequest(http.MethodPost, "/", nil), externalPRCompletionReq(workspaceID, unknownPolicy, 1007))
	if got.Outcome != "skipped" || got.Reason != "completion_policy_unsupported" {
		t.Fatalf("unknown-policy outcome = %#v, want skipped/completion_policy_unsupported", got)
	}
	assertIssueStatus(t, unknownPolicy, "todo")

	parentForNonStringPolicy := createExternalPRTestIssue(t, "external-pr non-string-policy parent", "todo", "", nil)
	nonStringPolicy := createExternalPRTestIssue(t, "external-pr non-string-policy child", "todo", parentForNonStringPolicy, int32Ptr(1))
	if _, err := testPool.Exec(ctx, `
UPDATE issue
SET metadata = jsonb_build_object('external_pr_completion_policy', true)
WHERE id=$1
`, nonStringPolicy); err != nil {
		t.Fatalf("set non-string completion policy: %v", err)
	}
	got = testHandler.completeLeafChildIssueFromExternalPR(httptest.NewRequest(http.MethodPost, "/", nil), externalPRCompletionReq(workspaceID, nonStringPolicy, 1008))
	if got.Outcome != "skipped" || got.Reason != "completion_policy_unsupported" {
		t.Fatalf("non-string-policy outcome = %#v, want skipped/completion_policy_unsupported", got)
	}
	assertIssueStatus(t, nonStringPolicy, "todo")
}

func TestCompleteExternalPRLeafStatusCompletionPolicyPredicate(t *testing.T) {
	ctx := context.Background()
	workspaceID, err := parseExternalPRUUID(testWorkspaceID)
	if err != nil {
		t.Fatalf("parse test workspace id: %v", err)
	}
	normalizedLeafMetadata, err := json.Marshal(map[string]any{
		"external_pr_completion_policy": externalPRCompletionPolicyTrimCutset + "LeAf_ChIlD_OnLy" + externalPRCompletionPolicyTrimCutset,
	})
	if err != nil {
		t.Fatalf("marshal normalized leaf metadata: %v", err)
	}
	cases := []struct {
		name     string
		metadata string
		wantDone bool
	}{
		{name: "absent", metadata: `{}`, wantDone: true},
		{name: "normalized complete ASCII cutset", metadata: string(normalizedLeafMetadata), wantDone: true},
		{name: "record only", metadata: `{"external_pr_completion_policy":"record_only"}`},
		{name: "unknown", metadata: `{"external_pr_completion_policy":"future_policy"}`},
		{name: "letter-v bounded unknown", metadata: `{"external_pr_completion_policy":"vleaf_child_onlyv"}`},
		{name: "json null", metadata: `{"external_pr_completion_policy":null}`},
		{name: "boolean", metadata: `{"external_pr_completion_policy":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := createExternalPRTestIssue(t, "external-pr predicate parent "+tc.name, "todo", "", nil)
			child := createExternalPRTestIssue(t, "external-pr predicate child "+tc.name, "todo", parent, int32Ptr(1))
			if _, err := testPool.Exec(ctx, `UPDATE issue SET metadata=$2::jsonb WHERE id=$1`, child, tc.metadata); err != nil {
				t.Fatalf("set predicate metadata: %v", err)
			}
			issueID, err := parseExternalPRUUID(child)
			if err != nil {
				t.Fatalf("parse child issue id: %v", err)
			}
			_, err = testHandler.completeExternalPRLeafStatus(ctx, issueID, workspaceID)
			if tc.wantDone {
				if err != nil {
					t.Fatalf("completeExternalPRLeafStatus() error = %v, want success", err)
				}
				assertIssueStatus(t, child, "done")
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("completeExternalPRLeafStatus() error = %v, want pgx.ErrNoRows", err)
			}
			assertIssueStatus(t, child, "todo")
		})
	}
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

	got := testHandler.completeLeafChildIssueFromExternalPR(httptest.NewRequest(http.MethodPost, "/", nil), externalPRCompletionReq(workspaceID, child, 1005))
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

func TestCompleteIssueFromExternalPRRecordOnlyKeepsLeafAndStageActive(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "test-external-pr-token")
	parent := createExternalPRTestIssue(t, "external-pr record-only parent", "todo", "", nil)
	child := createExternalPRTestIssue(t, "external-pr record-only child", "in_progress", parent, int32Ptr(1))
	if _, err := testPool.Exec(ctx, `
UPDATE issue
SET metadata = jsonb_build_object('external_pr_completion_policy', ' ReCoRd_OnLy ')
WHERE id=$1
`, child); err != nil {
		t.Fatalf("set record-only completion policy: %v", err)
	}

	reqBody := externalPRCompletionReq(testWorkspaceID, child, 1006)
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", reqBody)
	req.Header.Set("Authorization", "Bearer test-external-pr-token")
	rr := httptest.NewRecorder()
	testHandler.CompleteIssueFromExternalPR(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got externalCompleteFromPRResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode completion response: %v", err)
	}
	if got.Outcome != "skipped" || got.Reason != "completion_policy_record_only" || got.IssueID != child {
		t.Fatalf("record-only outcome = %#v, want skipped/completion_policy_record_only for child", got)
	}
	assertIssueStatus(t, child, "in_progress")

	var linkState string
	var completionIntent bool
	if err := testPool.QueryRow(ctx, `
SELECT state, completion_intent
FROM external_pull_request_link
WHERE workspace_id=$1 AND issue_id=$2 AND external_number=$3
`, testWorkspaceID, child, int32(1006)).Scan(&linkState, &completionIntent); err != nil {
		t.Fatalf("read merged record-only PR link: %v", err)
	}
	if linkState != "merged" || !completionIntent {
		t.Fatalf("record-only PR link = state %q completion_intent %v, want merged/true", linkState, completionIntent)
	}

	var parentSystemComments int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM comment WHERE issue_id=$1 AND author_type='system' AND type='system'`, parent).Scan(&parentSystemComments); err != nil {
		t.Fatalf("count record-only parent system comments: %v", err)
	}
	if parentSystemComments != 0 {
		t.Fatalf("record-only merge emitted %d parent Stage comments, want 0", parentSystemComments)
	}

	for action, want := range map[string]int{
		"external_pr_linked":             1,
		"external_pr_merged":             1,
		"issue_completed_by_external_pr": 0,
	} {
		var count int
		if err := testPool.QueryRow(ctx, `SELECT COUNT(*)::int FROM activity_log WHERE issue_id=$1 AND action=$2 AND actor_type='system'`, child, action).Scan(&count); err != nil {
			t.Fatalf("count record-only activity %s: %v", action, err)
		}
		if count != want {
			t.Fatalf("record-only activity %s count = %d, want %d", action, count, want)
		}
	}
}

func TestListExternalPullRequestsForIssue(t *testing.T) {
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "external-pr list parent", "todo", "", nil)
	child := createExternalPRTestIssue(t, "external-pr list child", "todo", parent, int32Ptr(1))

	authoritative := externalPRCompletionReq(testWorkspaceID, child, 1101)
	authoritative.State = "merged"
	authoritative.MergedSHA = "11384b43b138b2a2d79cd7eb3c8c2e533900cfeb"
	if err := testHandler.upsertExternalPullRequestLink(ctx, authoritative); err != nil {
		t.Fatalf("seed authoritative link: %v", err)
	}
	inferred := externalPRCompletionReq(testWorkspaceID, child, 1102)
	inferred.LinkConfidence = "inferred"
	inferred.State = "open"
	intent := false
	inferred.CompletionIntent = &intent
	if err := testHandler.upsertExternalPullRequestLink(ctx, inferred); err != nil {
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
			foundAuthoritative = pr.Provider == "ags" && pr.ExternalRepo == "handler-tests/external-pr" && pr.State == "merged" && pr.LinkConfidence == "authoritative" && pr.MergedSHA != nil && *pr.MergedSHA == "11384b43b138b2a2d79cd7eb3c8c2e533900cfeb"
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

func TestUpsertExternalPRRejectsUnsafeURLs(t *testing.T) {
	issueID := createExternalPRTestIssue(t, "external-pr unsafe URL", "todo", "", nil)
	req := externalPRCompletionReq(testWorkspaceID, issueID, 1106)
	req.ExternalURL = "javascript:alert(1)"
	if err := testHandler.upsertExternalPullRequestLink(context.Background(), req); err == nil {
		t.Fatal("upsert accepted unsafe external_url")
	}
	req.ExternalURL = "https://ags.example/repo/pull/1106"
	req.MergeURL = "data:text/html,unsafe"
	if err := testHandler.upsertExternalPullRequestLink(context.Background(), req); err == nil {
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

func externalPRCompletionReq(workspaceID, issueID string, number int32) externalPullRequestLinkRequest {
	intent := true
	return externalPullRequestLinkRequest{
		Provider:         "ags",
		IssueID:          issueID,
		WorkspaceID:      workspaceID,
		Workspace:        handlerTestWorkspaceSlug,
		IssueKey:         "HAN-" + fmt.Sprint(number),
		ExternalRepo:     "handler-tests/external-pr",
		ExternalNumber:   number,
		ExternalURL:      fmt.Sprintf("http://ags.local/pull/%d", number),
		MergeProvider:    "forgejo",
		MergeRepo:        "handler-tests/external-pr",
		MergeNumber:      number,
		MergeURL:         fmt.Sprintf("http://forgejo.local/pulls/%d", number),
		MergedSHA:        fmt.Sprintf("sha-%d", number),
		CompletionIntent: &intent,
		LinkConfidence:   "authoritative",
		State:            "merged",
		IdempotencyKey:   fmt.Sprintf("external-pr-test-%d", number),
	}
}

func createExternalPRTestIssue(t *testing.T, title, status, parentID string, stage *int32) string {
	t.Helper()
	var id string
	var err error
	if parentID == "" && stage == nil {
		err = testPool.QueryRow(context.Background(), `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number)
			VALUES ($1, $2, $3, 'member', $4, (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id=$1))
			RETURNING id
		`, testWorkspaceID, title, status, testUserID).Scan(&id)
	} else if stage == nil {
		err = testPool.QueryRow(context.Background(), `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, parent_issue_id, number)
			VALUES ($1, $2, $3, 'member', $4, $5, (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id=$1))
			RETURNING id
		`, testWorkspaceID, title, status, testUserID, parentID).Scan(&id)
	} else {
		err = testPool.QueryRow(context.Background(), `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, parent_issue_id, stage, number)
			VALUES ($1, $2, $3, 'member', $4, $5, $6, (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id=$1))
			RETURNING id
		`, testWorkspaceID, title, status, testUserID, parentID, *stage).Scan(&id)
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
