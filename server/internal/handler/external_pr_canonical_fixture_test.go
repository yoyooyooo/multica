package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
)

const canonicalExternalPRTerminalFixtureSHA256 = "71989de6b77160b84e267b3d5213c90e83289674ed3d2ff38e6eb3376780f7dc"

func readCanonicalExternalPRTerminalFixture(t *testing.T) (externalPullRequestLinkRequest, []byte) {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/multica/external-pr-terminal-request.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != canonicalExternalPRTerminalFixtureSHA256 {
		t.Fatalf("canonical fixture SHA-256=%s, want %s", got, canonicalExternalPRTerminalFixtureSHA256)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var fixture externalPullRequestLinkRequest
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode canonical external PR fixture: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("canonical external PR fixture has trailing JSON: %v", err)
	}
	return fixture, data
}

func TestTypedAGSExternalPRTerminalAdmissionRejectsMatrixMismatches(t *testing.T) {
	fixture, _ := readCanonicalExternalPRTerminalFixture(t)
	cases := []struct {
		name   string
		path   string
		mutate func(*externalPullRequestLinkRequest)
	}{
		{"merged_on_links", "/api/integrations/external-pr/links", func(req *externalPullRequestLinkRequest) {}},
		{"closed_with_sha", "/api/integrations/external-pr/links", func(req *externalPullRequestLinkRequest) { req.State = "closed" }},
		{"closed_with_completion_intent", "/api/integrations/external-pr/links", func(req *externalPullRequestLinkRequest) {
			req.State = "closed"
			req.MergedSHA = ""
			intent := true
			req.CompletionIntent = &intent
		}},
		{"missing_completion_intent", "/api/integrations/external-pr/complete-from-merge", func(req *externalPullRequestLinkRequest) { req.CompletionIntent = nil }},

		{"missing_idempotency_key", "/api/integrations/external-pr/complete-from-merge", func(req *externalPullRequestLinkRequest) { req.IdempotencyKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := fixture
			tc.mutate(&req)
			if tc.name == "closed_with_sha" {
				req.MergedSHA = ""
				req.State = "closed"
				req.MergedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			if err := validateTypedAGSExternalPRTerminal(req, tc.path); err == nil {
				t.Fatalf("typed terminal admission accepted %s", tc.name)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*externalPullRequestLinkRequest)
	}{
		{"missing_workspace", func(req *externalPullRequestLinkRequest) { req.Workspace = "" }},
		{"missing_issue_key", func(req *externalPullRequestLinkRequest) { req.IssueKey = "" }},
		{"missing_external_url", func(req *externalPullRequestLinkRequest) { req.ExternalURL = "" }},
		{"invalid_external_url", func(req *externalPullRequestLinkRequest) { req.ExternalURL = "not-a-url" }},
		{"external_url_userinfo", func(req *externalPullRequestLinkRequest) { req.ExternalURL = "https://user:pass@ags.example/pulls/1" }},
		{"external_url_query", func(req *externalPullRequestLinkRequest) {
			req.ExternalURL = "https://ags.example/pulls/1?token=secret"
		}},
		{"external_url_fragment", func(req *externalPullRequestLinkRequest) { req.ExternalURL = "https://ags.example/pulls/1#fragment" }},
		{"merge_url_query", func(req *externalPullRequestLinkRequest) {
			req.MergeURL = "https://forgejo.example/pulls/1?token=secret"
		}},
		{"missing_workspace_id", func(req *externalPullRequestLinkRequest) { req.WorkspaceID = "" }},
		{"missing_issue_id", func(req *externalPullRequestLinkRequest) { req.IssueID = "" }},
		{"missing_external_repo", func(req *externalPullRequestLinkRequest) { req.ExternalRepo = "" }},
		{"missing_external_number", func(req *externalPullRequestLinkRequest) { req.ExternalNumber = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := fixture
			tc.mutate(&req)
			if err := validateTypedAGSExternalPRTerminal(req, "/api/integrations/external-pr/complete-from-merge"); err == nil {
				t.Fatalf("typed terminal admission accepted %s", tc.name)
			}
		})
	}
}

func TestMulticaCanonicalExternalPRFixtureUsesRealNormalizerAndRouter(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "mini-prod")
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "canonical-fixture-token")

	fixture, _ := readCanonicalExternalPRTerminalFixture(t)
	if err := validateTypedAGSExternalPRTerminal(fixture, "/api/integrations/external-pr/complete-from-merge"); err != nil {
		t.Fatalf("canonical merged fixture admission: %v", err)
	}
	projection, err := validateExternalPRMergeProjection(fixture, normalizeExternalPRProvider(fixture.MergeProvider))
	if err != nil || !projection.present {
		t.Fatalf("canonical projection normalization=(%#v,%v)", projection, err)
	}

	closed := fixture
	closed.State = "closed"
	closed.MergedSHA = ""
	closedIntent := false
	closed.CompletionIntent = &closedIntent
	closed.IdempotencyKey += ":closed"
	if err := validateTypedAGSExternalPRTerminal(closed, "/api/integrations/external-pr/links"); err != nil {
		t.Fatalf("canonical closed fixture admission: %v", err)
	}
	if closed.MergedSHA != "" || closed.State != "closed" {
		t.Fatalf("closed fixture normalization changed terminal matrix: state=%q sha=%q", closed.State, closed.MergedSHA)
	}

	parent := createExternalPRTestIssue(t, "canonical fixture parent", "in_progress", "", nil)
	fixture.IssueID = createExternalPRTestIssue(t, "canonical fixture child", "in_progress", parent, int32Ptr(1))
	cleanupExternalPRReconcileIssueFixtures(t, parent, fixture.IssueID)
	fixture.WorkspaceID = testWorkspaceID

	router := chi.NewRouter()
	router.Post("/api/integrations/external-pr/links", testHandler.RegisterExternalPullRequestLink)
	router.Post("/api/integrations/external-pr/complete-from-merge", testHandler.CompleteIssueFromExternalPR)
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", fixture)
	req.Header.Set("Authorization", "Bearer canonical-fixture-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("canonical fixture router status=%d body=%s", response.Code, response.Body.String())
	}

	unknown := newRequest(http.MethodPost, "/api/integrations/external-pr/terminal-facts", fixture)
	unknown.Header.Set("Authorization", "Bearer canonical-fixture-token")
	unknownResponse := httptest.NewRecorder()
	router.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusNotFound {
		t.Fatalf("unexpected /terminal-facts route status=%d", unknownResponse.Code)
	}
}
