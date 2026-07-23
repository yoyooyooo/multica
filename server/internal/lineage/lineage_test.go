package lineage

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryStore struct {
	byID          map[string]StoredReceipt
	byIdempotency map[string]StoredReceipt
	last          StoredReceipt
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		byID:          make(map[string]StoredReceipt),
		byIdempotency: make(map[string]StoredReceipt),
	}
}

func (s *memoryStore) FindReceipt(_ context.Context, id string) (StoredReceipt, bool, error) {
	receipt, ok := s.byID[id]
	return receipt, ok, nil
}

func (s *memoryStore) FindByIdempotency(_ context.Context, domain AuthorityDomain, issuerInstanceID, operation, key string) (StoredReceipt, bool, error) {
	receipt, ok := s.byIdempotency[idempotencyMapKey(domain, issuerInstanceID, operation, key)]
	return receipt, ok, nil
}

func (s *memoryStore) Ingest(_ context.Context, receipt StoredReceipt) (StoredReceipt, bool, error) {
	key := idempotencyMapKey(receipt.Domain, receipt.IssuerInstanceID, receipt.Operation, receipt.IdempotencyKey)
	if existing, ok := s.byIdempotency[key]; ok {
		return existing, false, nil
	}
	s.byID[receipt.ID] = receipt
	s.byIdempotency[key] = receipt
	s.last = receipt
	return receipt, true, nil
}

func idempotencyMapKey(domain AuthorityDomain, issuerInstanceID, operation, key string) string {
	return domain.MulticaInstanceID + ":" + domain.WorkspaceID + ":" + issuerInstanceID + ":" + operation + ":" + key
}

func TestIngestIsIdempotentAndRejectsChangedPayload(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(Scope{MulticaInstanceID: "multica-mini", WorkspaceID: uuid.NewString()}, store)
	receipt := validReceipt(svc.Scope)
	seal(t, &receipt)

	first, err := svc.Ingest(context.Background(), receipt)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if !first.Created {
		t.Fatal("first ingestion must create a receipt")
	}
	if len(store.last.Anchors) != 5 {
		t.Fatalf("want five anchors, got %d", len(store.last.Anchors))
	}

	second, err := svc.Ingest(context.Background(), receipt)
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if second.Created || second.Receipt.ID != first.Receipt.ID {
		t.Fatalf("replay = %#v, want existing receipt %#v", second, first.Receipt)
	}

	changed := receipt
	changed.ID = uuid.NewString()
	changed.Target.Ref = "ags-pr:92"
	seal(t, &changed)
	_, err = svc.Ingest(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestIngestRejectsCrossInstanceAndUnsafeReferences(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(Scope{MulticaInstanceID: "multica-mini", WorkspaceID: uuid.NewString()}, store)

	crossInstance := validReceipt(svc.Scope)
	crossInstance.Domain.MulticaInstanceID = "multica-imile"
	seal(t, &crossInstance)
	_, err := svc.Ingest(context.Background(), crossInstance)
	if !errors.Is(err, ErrCrossInstance) {
		t.Fatalf("cross-instance error = %v, want ErrCrossInstance", err)
	}

	unsafe := validReceipt(svc.Scope)
	unsafe.EvidenceRefs = append(unsafe.EvidenceRefs, "https://authority.example/receipt?token=secret")
	seal(t, &unsafe)
	_, err = svc.Ingest(context.Background(), unsafe)
	if !errors.Is(err, ErrUnsafeReference) {
		t.Fatalf("unsafe evidence error = %v, want ErrUnsafeReference", err)
	}
}

func TestCorrectionMustStayInSameAuthorityDomainAndReceiptKind(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(Scope{MulticaInstanceID: "multica-mini", WorkspaceID: uuid.NewString()}, store)
	base := validReceipt(svc.Scope)
	seal(t, &base)
	if _, err := svc.Ingest(context.Background(), base); err != nil {
		t.Fatalf("ingest base: %v", err)
	}

	wrongKind := validReceipt(svc.Scope)
	wrongKind.ID = uuid.NewString()
	wrongKind.Kind = "backup"
	wrongKind.Correction = Correction{SupersedesReceiptID: base.ID, ReasonCode: "source_changed"}
	seal(t, &wrongKind)
	_, err := svc.Ingest(context.Background(), wrongKind)
	if !errors.Is(err, ErrInvalidCorrection) {
		t.Fatalf("wrong-kind correction error = %v, want ErrInvalidCorrection", err)
	}

	foreign := base
	foreign.ID = uuid.NewString()
	foreign.Domain.WorkspaceID = uuid.NewString()
	foreign.Correction = Correction{SupersedesReceiptID: base.ID, ReasonCode: "source_changed"}
	seal(t, &foreign)
	foreignSvc := NewService(Scope{MulticaInstanceID: foreign.Domain.MulticaInstanceID, WorkspaceID: foreign.Domain.WorkspaceID}, store)
	_, err = foreignSvc.Ingest(context.Background(), foreign)
	if !errors.Is(err, ErrInvalidCorrection) {
		t.Fatalf("foreign correction error = %v, want ErrInvalidCorrection", err)
	}
}

func TestLegacyObservationStaysNotObservedWithoutFabricatedIdentity(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(Scope{MulticaInstanceID: "multica-mini", WorkspaceID: uuid.NewString()}, store)
	receipt := validReceipt(svc.Scope)
	receipt.Observations = []Observation{{
		Segment:    "dispatch_runtime",
		Mode:       ObservationModeSnapshot,
		State:      ObservationStateNotObserved,
		FactKind:   "task",
		FactRef:    "legacy-task-row-42",
		Authority:  "multica",
		ObservedAt: receipt.ObservedAt,
	}}
	seal(t, &receipt)

	if _, err := svc.Ingest(context.Background(), receipt); err != nil {
		t.Fatalf("ingest legacy receipt: %v", err)
	}
	if len(store.last.Observations) != 1 || store.last.Observations[0].State != ObservationStateNotObserved {
		t.Fatalf("legacy observation = %#v, want unmodified not_observed state", store.last.Observations)
	}
	for _, anchor := range store.last.Anchors {
		if anchor.Ref == "legacy-task-row-42" {
			t.Fatalf("legacy observation must not manufacture a task anchor: %#v", anchor)
		}
	}
}

func TestConflictAndRedactionObservationsAreAcceptedAsIndexFacts(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(Scope{MulticaInstanceID: "multica-mini", WorkspaceID: uuid.NewString()}, store)
	receipt := validReceipt(svc.Scope)
	receipt.Observations = append(receipt.Observations, Observation{
		Segment:    "pr_review_ci",
		Mode:       ObservationModeLive,
		State:      ObservationStateConflict,
		FactKind:   "forgejo_pr",
		FactRef:    "forgejo://jackie/agent-git-service/pulls/91",
		Authority:  "forgejo-mini",
		Version:    "2026-07-21T15:00:00Z",
		Digest:     "d5df9547c0ee5c5f1cac176d720c84eab6a8b2b2e3997f27be4b3dff3c047498",
		ObservedAt: receipt.ObservedAt,
	})
	receipt.Redactions = []Redaction{{FieldClass: "provider_payload", Reason: "secret_safe"}}
	seal(t, &receipt)

	if _, err := svc.Ingest(context.Background(), receipt); err != nil {
		t.Fatalf("ingest conflict receipt: %v", err)
	}
	var conflict Observation
	for _, observation := range store.last.Observations {
		if observation.State == ObservationStateConflict {
			conflict = observation
			break
		}
	}
	if conflict.State != ObservationStateConflict {
		t.Fatalf("conflict observation missing: %#v", store.last.Observations)
	}
	if got := store.last.Redactions; len(got) != 1 || got[0].FieldClass != "provider_payload" {
		t.Fatalf("redactions = %#v", got)
	}
}

func validReceipt(scope Scope) Receipt {
	observedAt := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	return Receipt{
		ID:     uuid.NewString(),
		Schema: ReceiptSchemaV1,
		Domain: AuthorityDomain{
			MulticaInstanceID: scope.MulticaInstanceID,
			WorkspaceID:       scope.WorkspaceID,
			AGSInstanceID:     "ags-mini",
		},
		Kind:             "source_operation",
		IssuerInstanceID: "ags-mini",
		Source: Reference{
			Authority: "ags-mini",
			Ref:       "ags-pr:91",
			Version:   "policy-7",
			Digest:    "1c4dd07d25e1b6b742ba4ef0abdbb9a87bf33c9929c6f3fb1b760f3948d2fbc2",
		},
		Target: Reference{
			Authority: "ags-mini",
			Ref:       "ags-pr:91",
		},
		Envelope:       Envelope{ID: "envelope-91", Digest: "7e55d4c2057b19de2b7b87a90c91f059ac0ea4162ed2a71f2a91f19eb16f48fd"},
		Operation:      "pr.create.v1",
		IdempotencyKey: "run-9:pr-create",
		CorrelationID:  "run-9",
		Outcome:        "accepted",
		Epochs: Epochs{
			PolicyRevision:        "policy-7",
			ConfigGeneration:      "config-11",
			RuntimeBundleRevision: "bundle-3",
		},
		ObservedAt:   observedAt,
		EvidenceRefs: []string{"multica:task:9", "ags:pr:91"},
		Integrity: Integrity{
			Issuer: "ags-mini",
			KeyID:  "ags-key-7",
		},
		Anchors: []Anchor{
			{Kind: "issue", Ref: uuid.NewString()},
			{Kind: "task", Ref: uuid.NewString()},
			{Kind: "run", Ref: uuid.NewString()},
			{Kind: "ags_pr", Ref: "ags-pr:91"},
			{Kind: "forgejo_pr", Ref: "forgejo://jackie/agent-git-service/pulls/91"},
		},
		Observations: []Observation{{
			Segment:    "source_artifact",
			Mode:       ObservationModeSnapshot,
			State:      ObservationStateResolved,
			FactKind:   "ags_pr",
			FactRef:    "ags-pr:91",
			Authority:  "ags-mini",
			Version:    "policy-7",
			Digest:     "1c4dd07d25e1b6b742ba4ef0abdbb9a87bf33c9929c6f3fb1b760f3948d2fbc2",
			ObservedAt: observedAt,
		}},
	}
}

func seal(t *testing.T, receipt *Receipt) {
	t.Helper()
	digest, err := PayloadSHA256(*receipt)
	if err != nil {
		t.Fatalf("payload digest: %v", err)
	}
	receipt.Integrity.PayloadSHA256 = digest
}

func TestPayloadDigestIsOrderIndependentForIndexSets(t *testing.T) {
	scope := Scope{MulticaInstanceID: "multica-mini", WorkspaceID: uuid.NewString()}
	first := validReceipt(scope)
	second := first
	second.Anchors = append([]Anchor(nil), first.Anchors...)
	sort.Slice(second.Anchors, func(i, j int) bool { return second.Anchors[i].Kind > second.Anchors[j].Kind })
	second.EvidenceRefs = append([]string(nil), first.EvidenceRefs...)
	sort.Sort(sort.Reverse(sort.StringSlice(second.EvidenceRefs)))

	firstDigest, err := PayloadSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := PayloadSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("set ordering changed digest: %s != %s", firstDigest, secondDigest)
	}
}
