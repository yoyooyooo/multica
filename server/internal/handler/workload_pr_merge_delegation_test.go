package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
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
		Schema   string `json:"schema"`
		Workload struct {
			MergeDelegation prMergeDelegationFacts `json:"merge_delegation"`
		} `json:"workload"`
		Introspect prMergeDelegationServiceRequest `json:"introspect_request"`
		Consume    prMergeDelegationServiceRequest `json:"consume_request"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode canonical fixture: %v", err)
	}
	if fixture.Schema != "multica.ags.pr-merge-delegation-fixture.v2" || fixture.Workload.MergeDelegation.Schema != prMergeDelegationSchema {
		t.Fatalf("unexpected fixture schema: %#v", fixture)
	}
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "imile-win")
	if err := validatePRMergeDelegationServiceRequest(fixture.Introspect, false); err != nil {
		t.Fatalf("fixture introspect: %v", err)
	}
	if err := validatePRMergeDelegationServiceRequest(fixture.Consume, true); err != nil {
		t.Fatalf("fixture consume: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got, want := hex.EncodeToString(sum[:]), "fc6bf25cefe84cb55e2bec9976e94e333543974747de5561a7a9cda50a9a2416"; got != want {
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
		SessionID:               "ags-session-safe", Phase: "exchange",
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
}
