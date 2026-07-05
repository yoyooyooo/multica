package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
