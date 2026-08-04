package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestDelegatedPRMergeFeatureFlagDefaultsOff(t *testing.T) {
	t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "")
	if delegatedPRMergeEnabled() {
		t.Fatal("delegated pr.merge must default off")
	}
	t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "1")
	if !delegatedPRMergeEnabled() {
		t.Fatal("delegated pr.merge should require exact enabled value")
	}
	for _, value := range []string{"true", " 1", "1 ", "0"} {
		t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", value)
		if delegatedPRMergeEnabled() {
			t.Fatalf("noncanonical feature flag %q enabled merge", value)
		}
	}
}

func TestNormalizeExternalPRMergeProjectionRequiresCompleteServerFacts(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "imile-win")
	complete := externalPullRequestLinkRequest{
		ExternalRepo: "ux/smip", MergeProvider: "forgejo", MergeRepo: "ux/smip", MergeNumber: 2,
		TargetInstance:          "imile-win",
		CanonicalRepositoryID:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalRepository:     "ux/smip",
		ProviderBindingID:       "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ProviderBindingRevision: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ProviderRepository:      "ux/smip",
		ExpectedHeadSHA:         "1111111111111111111111111111111111111111",
		ExpectedBaseSHA:         "2222222222222222222222222222222222222222",
		BaseRef:                 "main", DelegatedMergeMethod: "rebase",
		ProjectionFactsRevision: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	projection, err := normalizeExternalPRMergeProjection(complete, "forgejo")
	if err != nil || !projection.present {
		t.Fatalf("complete projection rejected: projection=%#v err=%v", projection, err)
	}
	partial := complete
	partial.ExpectedBaseSHA = ""
	if _, err := normalizeExternalPRMergeProjection(partial, "forgejo"); err == nil {
		t.Fatal("partial projection facts accepted")
	}
	wrongInstance := complete
	wrongInstance.TargetInstance = "mini"
	if _, err := normalizeExternalPRMergeProjection(wrongInstance, "forgejo"); err == nil {
		t.Fatal("cross-instance projection accepted")
	}
	alias := complete
	alias.CanonicalRepository = "ux/smip.git"
	if _, err := normalizeExternalPRMergeProjection(alias, "forgejo"); err == nil {
		t.Fatal("repository alias accepted")
	}
}

func TestPRMergeDelegationFactsDigestIsCanonicalAndExecutionBound(t *testing.T) {
	facts := prMergeDelegationBindingFacts{
		WorkspaceID:           "11111111-1111-1111-1111-111111111111",
		IssueID:               "22222222-2222-2222-2222-222222222222",
		ExternalPRLinkID:      "33333333-3333-3333-3333-333333333333",
		TaskID:                "44444444-4444-4444-4444-444444444444",
		ExecutionID:           "55555555-5555-5555-5555-555555555555",
		RuntimeID:             "66666666-6666-6666-6666-666666666666",
		TargetInstance:        "imile-win",
		CanonicalRepositoryID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalRepository:   "ux/smip", Provider: "forgejo",
		ProviderBindingID:       "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ProviderBindingRevision: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ProviderRepository:      "ux/smip", AGSPRNumber: 2, ProviderPRNumber: 2,
		ExpectedHeadSHA: "1111111111111111111111111111111111111111",
		ExpectedBaseSHA: "2222222222222222222222222222222222222222",
		BaseRef:         "main", MergeMethod: "rebase",
		ProjectionFactsRevision: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	first := digestPRMergeDelegationFacts(facts)
	if first != digestPRMergeDelegationFacts(facts) || !canonicalExternalPRDigestPattern.MatchString(first) {
		t.Fatalf("facts digest is not stable: %q", first)
	}
	facts.ExecutionID = "77777777-7777-7777-7777-777777777777"
	if first == digestPRMergeDelegationFacts(facts) {
		t.Fatal("execution drift did not change facts digest")
	}
}

func TestPRMergeDelegationCanonicalWireFixture(t *testing.T) {
	path := "../../../docs/features/fork/workload-assertions/fixtures/pr-merge-delegation-v2.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical fixture: %v", err)
	}
	var fixture struct {
		Schema       string                          `json:"schema"`
		BindingFacts prMergeDelegationBindingFacts   `json:"binding_facts"`
		Workload     workloadAssertionWorkload       `json:"workload"`
		Introspect   prMergeDelegationServiceRequest `json:"introspect_request"`
		Consume      prMergeDelegationServiceRequest `json:"consume_request"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode closed canonical fixture: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatal("canonical fixture contains a second JSON value")
	}
	merge := fixture.Workload.MergeDelegation
	if fixture.Schema != "multica.ags.pr-merge-delegation-fixture.v2" || merge == nil || merge.Schema != prMergeDelegationSchema {
		t.Fatalf("unexpected fixture schema: %#v", fixture)
	}
	if fixture.Workload.WorkspaceID != merge.WorkspaceID || fixture.Workload.TaskID != merge.TaskID || fixture.Workload.RunID != merge.RunID {
		t.Fatalf("workload identity does not match merge delegation: workload=%#v delegation=%#v", fixture.Workload, merge)
	}
	if fixture.BindingFacts.WorkspaceID != merge.WorkspaceID || fixture.BindingFacts.IssueID != merge.IssueID ||
		fixture.BindingFacts.TaskID != merge.TaskID || fixture.BindingFacts.ExecutionID != merge.RunID ||
		fixture.BindingFacts.RuntimeID != merge.RuntimeID || fixture.BindingFacts.TargetInstance != merge.TargetInstance ||
		fixture.BindingFacts.CanonicalRepositoryID != merge.CanonicalRepositoryID || fixture.BindingFacts.CanonicalRepository != merge.CanonicalRepository ||
		fixture.BindingFacts.Provider != merge.Provider || fixture.BindingFacts.ProviderBindingID != merge.ProviderBindingID ||
		fixture.BindingFacts.ProviderBindingRevision != merge.ProviderBindingRevision || fixture.BindingFacts.ProviderRepository != merge.ProviderRepository ||
		fixture.BindingFacts.AGSPRNumber != merge.AGSPRNumber || fixture.BindingFacts.ProviderPRNumber != merge.ProviderPRNumber ||
		fixture.BindingFacts.ExpectedHeadSHA != merge.ExpectedHeadSHA || fixture.BindingFacts.ExpectedBaseSHA != merge.ExpectedBaseSHA ||
		fixture.BindingFacts.BaseRef != merge.BaseRef || fixture.BindingFacts.MergeMethod != merge.MergeMethod ||
		fixture.BindingFacts.ProjectionFactsRevision != merge.ProjectionFactsRevision {
		t.Fatalf("binding facts do not match merge delegation: binding=%#v delegation=%#v", fixture.BindingFacts, merge)
	}
	if got := digestPRMergeDelegationFacts(fixture.BindingFacts); got != merge.FactsDigest {
		t.Fatalf("fixture facts digest=%s want recomputed=%s", merge.FactsDigest, got)
	}
	requestFromMerge := func(sessionID, intentID, phase string) prMergeDelegationServiceRequest {
		return prMergeDelegationServiceRequest{
			AuthorityRevision: merge.AuthorityRevision, FactsDigest: merge.FactsDigest,
			TargetInstance: merge.TargetInstance, CanonicalRepositoryID: merge.CanonicalRepositoryID,
			CanonicalRepository: merge.CanonicalRepository, ProviderBindingID: merge.ProviderBindingID,
			ProviderBindingRevision: merge.ProviderBindingRevision, ProviderRepository: merge.ProviderRepository,
			AGSPRNumber: merge.AGSPRNumber, ProviderPRNumber: merge.ProviderPRNumber,
			ExpectedHeadSHA: merge.ExpectedHeadSHA, ExpectedBaseSHA: merge.ExpectedBaseSHA,
			BaseRef: merge.BaseRef, MergeMethod: merge.MergeMethod, ProjectionFactsRevision: merge.ProjectionFactsRevision,
			TaskID: merge.TaskID, RunID: merge.RunID, SessionID: sessionID, IntentID: intentID, Phase: phase,
		}
	}
	if want := requestFromMerge("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", "exchange"); fixture.Introspect != want {
		t.Fatalf("introspect request drift: got=%#v want=%#v", fixture.Introspect, want)
	}
	if want := requestFromMerge("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "99999999-9999-9999-9999-999999999999", "pre_effect"); fixture.Consume != want {
		t.Fatalf("consume request drift: got=%#v want=%#v", fixture.Consume, want)
	}
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "imile-win")
	if err := validatePRMergeDelegationServiceRequest(fixture.Introspect, false); err != nil {
		t.Fatalf("fixture introspect: %v", err)
	}
	if err := validatePRMergeDelegationServiceRequest(fixture.Consume, true); err != nil {
		t.Fatalf("fixture consume: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got, want := hex.EncodeToString(sum[:]), "d4b3efe6faf82984a5da1006feafb283063988a12404278f9c490aa58c2dfead"; got != want {
		t.Fatalf("fixture digest=%s want=%s", got, want)
	}
}

func TestValidatePRMergeDelegationServiceRequestRequiresExactInstanceAndIntent(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "imile-win")
	req := prMergeDelegationServiceRequest{
		AuthorityRevision:       "11111111-1111-1111-1111-111111111111",
		FactsDigest:             "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TargetInstance:          "imile-win",
		CanonicalRepositoryID:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CanonicalRepository:     "ux/smip",
		ProviderBindingID:       "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ProviderBindingRevision: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		ProviderRepository:      "ux/smip", AGSPRNumber: 2, ProviderPRNumber: 2,
		ExpectedHeadSHA: "1111111111111111111111111111111111111111",
		ExpectedBaseSHA: "2222222222222222222222222222222222222222",
		BaseRef:         "main", MergeMethod: "rebase",
		ProjectionFactsRevision: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		TaskID:                  "22222222-2222-2222-2222-222222222222",
		RunID:                   "33333333-3333-3333-3333-333333333333",
		SessionID:               "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Phase: "exchange",
	}
	if err := validatePRMergeDelegationServiceRequest(req, false); err != nil {
		t.Fatalf("valid introspection rejected: %v", err)
	}
	req.Phase = "pre_effect"
	req.IntentID = "44444444-4444-4444-4444-444444444444"
	if err := validatePRMergeDelegationServiceRequest(req, true); err != nil {
		t.Fatalf("valid consume rejected: %v", err)
	}
	req.TargetInstance = "mini"
	if err := validatePRMergeDelegationServiceRequest(req, true); err == nil {
		t.Fatal("cross-instance consume accepted")
	}
	req.TargetInstance = "imile-win"
	for name, sessionID := range map[string]string{
		"ags session secret": "ags_sess_do-not-persist",
		"JWT":                "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzZWNyZXQifQ.signature",
		"private key":        "-----BEGIN PRIVATE KEY-----",
		"overlong":           strings.Repeat("a", 256),
	} {
		t.Run(name, func(t *testing.T) {
			invalid := req
			invalid.SessionID = sessionID
			if err := validatePRMergeDelegationServiceRequest(invalid, true); err == nil || strings.Contains(err.Error(), sessionID) {
				t.Fatalf("unsafe session identifier validation err=%q", err)
			}
		})
	}
	const sessionSecret = "ags_sess_response-must-not-echo"
	unsafe := req
	unsafe.SessionID = sessionSecret
	t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "1")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "session-validation-service-token")
	request := newRequest(http.MethodPost, "/", unsafe)
	request.Header.Set("Authorization", "Bearer session-validation-service-token")
	route := chi.NewRouteContext()
	route.URLParams.Add("delegationId", "55555555-5555-5555-5555-555555555555")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	testHandler.ConsumePRMergeDelegation(recorder, request)
	if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), sessionSecret) {
		t.Fatalf("unsafe session response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestApproveAndRevokeRequireStrictEmptyJSONObject(t *testing.T) {
	for _, handler := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{{"approve", testHandler.ApprovePRMergeDelegation}, {"revoke", testHandler.RevokePRMergeDelegation}} {
		for name, body := range map[string]string{
			"empty": "", "null": "null", "array": "[]", "scalar": `"value"`,
			"unknown": `{"unexpected":true}`, "second value": `{} {}`,
		} {
			t.Run(handler.name+"/reject/"+name, func(t *testing.T) {
				t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "1")
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				req.Header.Set("X-User-ID", testUserID)
				route := chi.NewRouteContext()
				route.URLParams.Add("id", testWorkspaceID)
				route.URLParams.Add("delegationId", "55555555-5555-5555-5555-555555555555")
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
				recorder := httptest.NewRecorder()
				handler.call(recorder, req)
				if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "{\"error\":\"invalid request body\"}\n" {
					t.Fatalf("body %q status=%d response=%s", body, recorder.Code, recorder.Body.String())
				}
			})
		}
	}
}
