// Package lineage defines the append-only, instance-scoped receipt index used
// by later lineage query surfaces. It deliberately stores references, digests,
// and explicit redaction declarations instead of raw provider payloads.
package lineage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ReceiptSchemaV1 = "context-continuity.boundary-receipt.v1"

var (
	ErrCrossInstance       = errors.New("lineage receipt authority domain is outside the request scope")
	ErrIdempotencyConflict = errors.New("lineage idempotency key was already used with a different payload")
	ErrInvalidCorrection   = errors.New("lineage correction does not supersede a receipt in the same authority domain and kind")
	ErrUnsafeReference     = errors.New("lineage receipt contains an unsafe reference")
)

// Scope is the authenticated Multica authority domain for one ingest request.
// Handlers must construct it from their workspace loader rather than request
// body fields, so callers cannot choose another instance or workspace.
type Scope struct {
	MulticaInstanceID string
	WorkspaceID       string
}

// AuthorityDomain identifies the exact fact-acceptance domain of a receipt.
type AuthorityDomain struct {
	MulticaInstanceID string
	WorkspaceID       string
	AGSInstanceID     string
}

// Reference identifies a fact owned by another authority. It intentionally
// carries no raw response body or credentials.
type Reference struct {
	Authority string
	Ref       string
	Version   string
	Digest    string
}

type Envelope struct {
	ID     string
	Digest string
}

type Epochs struct {
	PolicyRevision        string
	ConfigGeneration      string
	RuntimeBundleRevision string
}

type Integrity struct {
	Issuer        string
	KeyID         string
	PayloadSHA256 string
}

type Correction struct {
	SupersedesReceiptID string
	ReasonCode          string
}

type Anchor struct {
	Kind string
	Ref  string
}

type ObservationMode string

const (
	ObservationModeSnapshot ObservationMode = "snapshot"
	ObservationModeLive     ObservationMode = "live"
)

type ObservationState string

const (
	ObservationStateResolved      ObservationState = "resolved"
	ObservationStateNotApplicable ObservationState = "not_applicable"
	ObservationStateNotObserved   ObservationState = "not_observed"
	ObservationStateRedacted      ObservationState = "redacted"
	ObservationStateUnavailable   ObservationState = "unavailable"
	ObservationStateStale         ObservationState = "stale"
	ObservationStateConflict      ObservationState = "conflict"
)

// Observation indexes a snapshot or a current read without storing the raw
// authority payload. A legacy row stays not_observed until an owning authority
// supplies a real fact; callers must not synthesize an anchor from it.
type Observation struct {
	Segment    string
	Mode       ObservationMode
	State      ObservationState
	FactKind   string
	FactRef    string
	Authority  string
	Version    string
	Digest     string
	ObservedAt time.Time
}

type Redaction struct {
	FieldClass string
	Reason     string
}

// Receipt is the only accepted write shape. There is intentionally no generic
// metadata or participant field: workloads, principals, and provider executors
// remain facts of their owning receipts rather than Issue metadata.
type Receipt struct {
	ID               string
	Schema           string
	Domain           AuthorityDomain
	Kind             string
	IssuerInstanceID string
	Source           Reference
	Target           Reference
	Envelope         Envelope
	Operation        string
	IdempotencyKey   string
	CorrelationID    string
	Outcome          string
	Epochs           Epochs
	ObservedAt       time.Time
	EvidenceRefs     []string
	Integrity        Integrity
	Correction       Correction
	Anchors          []Anchor
	Observations     []Observation
	Redactions       []Redaction
}

// StoredReceipt is the normalized, persistence-safe form passed to a Store.
// It contains no bearer token, session credential, provider payload, or Issue
// metadata mutation.
type StoredReceipt struct {
	Receipt
}

// Store owns atomic persistence. Its Ingest method must atomically persist the
// receipt and every anchor, observation, and redaction, then return an existing
// receipt when the idempotency tuple already exists.
type Store interface {
	FindReceipt(ctx context.Context, id string) (StoredReceipt, bool, error)
	FindByIdempotency(ctx context.Context, domain AuthorityDomain, issuerInstanceID, operation, key string) (StoredReceipt, bool, error)
	Ingest(ctx context.Context, receipt StoredReceipt) (stored StoredReceipt, created bool, err error)
}

type Service struct {
	Scope Scope
	store Store
}

func NewService(scope Scope, store Store) *Service {
	return &Service{Scope: scope, store: store}
}

type IngestResult struct {
	Receipt StoredReceipt
	Created bool
}

func (s *Service) Ingest(ctx context.Context, receipt Receipt) (IngestResult, error) {
	if err := validateScope(s.Scope, receipt.Domain); err != nil {
		return IngestResult{}, err
	}
	if err := validateReceipt(receipt); err != nil {
		return IngestResult{}, err
	}

	digest, err := PayloadSHA256(receipt)
	if err != nil {
		return IngestResult{}, err
	}
	if receipt.Integrity.PayloadSHA256 != digest {
		return IngestResult{}, fmt.Errorf("lineage integrity payload_sha256 does not match canonical payload")
	}

	if receipt.Correction.SupersedesReceiptID != "" {
		superseded, found, err := s.store.FindReceipt(ctx, receipt.Correction.SupersedesReceiptID)
		if err != nil {
			return IngestResult{}, fmt.Errorf("load superseded receipt: %w", err)
		}
		if !found || superseded.Domain != receipt.Domain || superseded.Kind != receipt.Kind {
			return IngestResult{}, ErrInvalidCorrection
		}
	}

	existing, found, err := s.store.FindByIdempotency(ctx, receipt.Domain, receipt.IssuerInstanceID, receipt.Operation, receipt.IdempotencyKey)
	if err != nil {
		return IngestResult{}, fmt.Errorf("lookup idempotency receipt: %w", err)
	}
	if found {
		if existing.Integrity.PayloadSHA256 != digest {
			return IngestResult{}, ErrIdempotencyConflict
		}
		return IngestResult{Receipt: existing, Created: false}, nil
	}

	stored, created, err := s.store.Ingest(ctx, StoredReceipt{Receipt: normalize(receipt)})
	if err != nil {
		return IngestResult{}, fmt.Errorf("store lineage receipt: %w", err)
	}
	if !created {
		if stored.Integrity.PayloadSHA256 != digest {
			return IngestResult{}, ErrIdempotencyConflict
		}
		return IngestResult{Receipt: stored, Created: false}, nil
	}
	return IngestResult{Receipt: stored, Created: true}, nil
}

func validateScope(scope Scope, domain AuthorityDomain) error {
	if strings.TrimSpace(scope.MulticaInstanceID) == "" || strings.TrimSpace(scope.WorkspaceID) == "" {
		return fmt.Errorf("lineage scope is incomplete")
	}
	if domain.MulticaInstanceID != scope.MulticaInstanceID || domain.WorkspaceID != scope.WorkspaceID {
		return ErrCrossInstance
	}
	if _, err := uuid.Parse(domain.WorkspaceID); err != nil {
		return fmt.Errorf("lineage workspace id: %w", err)
	}
	return nil
}

func validateReceipt(receipt Receipt) error {
	if _, err := uuid.Parse(receipt.ID); err != nil {
		return fmt.Errorf("lineage receipt id: %w", err)
	}
	if receipt.Schema != ReceiptSchemaV1 {
		return fmt.Errorf("unsupported lineage receipt schema %q", receipt.Schema)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"kind", receipt.Kind},
		{"issuer instance", receipt.IssuerInstanceID},
		{"source authority", receipt.Source.Authority},
		{"source ref", receipt.Source.Ref},
		{"source version", receipt.Source.Version},
		{"source digest", receipt.Source.Digest},
		{"target authority", receipt.Target.Authority},
		{"target ref", receipt.Target.Ref},
		{"envelope id", receipt.Envelope.ID},
		{"envelope digest", receipt.Envelope.Digest},
		{"operation", receipt.Operation},
		{"idempotency key", receipt.IdempotencyKey},
		{"correlation id", receipt.CorrelationID},
		{"integrity issuer", receipt.Integrity.Issuer},
		{"integrity key id", receipt.Integrity.KeyID},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("lineage receipt %s is required", field.name)
		}
	}
	if receipt.Outcome != "accepted" && receipt.Outcome != "rejected" && receipt.Outcome != "unknown" {
		return fmt.Errorf("unsupported lineage outcome %q", receipt.Outcome)
	}
	if receipt.Integrity.Issuer != receipt.Target.Authority {
		return errors.New("lineage integrity issuer must be the target authority")
	}
	if strings.Contains(strings.ToLower(receipt.Source.Authority), "ags") || strings.Contains(strings.ToLower(receipt.Target.Authority), "ags") {
		if strings.TrimSpace(receipt.Domain.AGSInstanceID) == "" {
			return errors.New("lineage AGS receipt requires an ags_instance_id")
		}
	}
	for _, digest := range []string{receipt.Source.Digest, receipt.Envelope.Digest, receipt.Integrity.PayloadSHA256} {
		if !isSHA256(digest) {
			return fmt.Errorf("lineage digest is not a SHA-256 value")
		}
	}
	if receipt.ObservedAt.IsZero() {
		return errors.New("lineage observed_at is required")
	}
	if (receipt.Correction.SupersedesReceiptID == "") != (receipt.Correction.ReasonCode == "") {
		return errors.New("lineage correction requires both supersedes_receipt_id and reason_code")
	}
	if receipt.Correction.SupersedesReceiptID != "" {
		if receipt.Correction.SupersedesReceiptID == receipt.ID {
			return errors.New("lineage correction cannot supersede itself")
		}
		if _, err := uuid.Parse(receipt.Correction.SupersedesReceiptID); err != nil {
			return fmt.Errorf("lineage supersedes receipt id: %w", err)
		}
	}
	for _, ref := range append([]string{receipt.Source.Ref, receipt.Target.Ref, receipt.Envelope.ID, receipt.CorrelationID}, receipt.EvidenceRefs...) {
		if unsafeReference(ref) {
			return ErrUnsafeReference
		}
	}
	for _, anchor := range receipt.Anchors {
		if !allowedAnchorKind(anchor.Kind) || strings.TrimSpace(anchor.Ref) == "" {
			return fmt.Errorf("invalid lineage anchor %q", anchor.Kind)
		}
		if unsafeReference(anchor.Ref) {
			return ErrUnsafeReference
		}
	}
	for _, observation := range receipt.Observations {
		if err := validateObservation(observation); err != nil {
			return err
		}
	}
	for _, redaction := range receipt.Redactions {
		if strings.TrimSpace(redaction.FieldClass) == "" || strings.TrimSpace(redaction.Reason) == "" {
			return errors.New("lineage redaction requires field_class and reason")
		}
	}
	return nil
}

func validateObservation(observation Observation) error {
	if !allowedSegment(observation.Segment) || !allowedObservationMode(observation.Mode) || !allowedObservationState(observation.State) {
		return fmt.Errorf("invalid lineage observation state for segment %q", observation.Segment)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"fact kind", observation.FactKind},
		{"fact ref", observation.FactRef},
		{"authority", observation.Authority},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("lineage observation %s is required", field.name)
		}
	}
	if observation.ObservedAt.IsZero() {
		return errors.New("lineage observation observed_at is required")
	}
	if unsafeReference(observation.FactRef) {
		return ErrUnsafeReference
	}
	if observation.Digest != "" && !isSHA256(observation.Digest) {
		return errors.New("lineage observation digest is not a SHA-256 value")
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func unsafeReference(value string) bool {
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"bearer ", "token=", "secret=", "password=", "authorization=", "credential="} {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	return false
}

func allowedAnchorKind(kind string) bool {
	switch kind {
	case "issue", "task", "run", "ags_pr", "forgejo_pr", "commit", "support_case", "incident":
		return true
	default:
		return false
	}
}

func allowedSegment(segment string) bool {
	switch segment {
	case "decision_spec", "work_item_stage", "dispatch_runtime", "source_artifact", "authorization_operation", "pr_review_ci", "projection_backup_terminal", "support_incident_recovery":
		return true
	default:
		return false
	}
}

func allowedObservationMode(mode ObservationMode) bool {
	return mode == ObservationModeSnapshot || mode == ObservationModeLive
}

func allowedObservationState(state ObservationState) bool {
	switch state {
	case ObservationStateResolved, ObservationStateNotApplicable, ObservationStateNotObserved, ObservationStateRedacted, ObservationStateUnavailable, ObservationStateStale, ObservationStateConflict:
		return true
	default:
		return false
	}
}

// PayloadSHA256 returns the SHA-256 digest of a stable, normalized receipt
// payload. The integrity digest itself is excluded to avoid a circular hash.
func PayloadSHA256(receipt Receipt) (string, error) {
	payload := canonicalPayload(receipt)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("canonical lineage payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type canonicalReceipt struct {
	Schema           string          `json:"schema"`
	ID               string          `json:"receipt_id"`
	Domain           AuthorityDomain `json:"authority_domain"`
	Kind             string          `json:"kind"`
	IssuerInstanceID string          `json:"issuer_instance_id"`
	Source           Reference       `json:"source"`
	Target           Reference       `json:"target"`
	Envelope         Envelope        `json:"envelope"`
	Operation        string          `json:"operation"`
	IdempotencyKey   string          `json:"idempotency_key"`
	CorrelationID    string          `json:"correlation_id"`
	Outcome          string          `json:"outcome"`
	Epochs           Epochs          `json:"epochs"`
	ObservedAt       string          `json:"observed_at"`
	EvidenceRefs     []string        `json:"evidence_refs"`
	IntegrityIssuer  string          `json:"integrity_issuer"`
	IntegrityKeyID   string          `json:"integrity_key_id"`
	Correction       Correction      `json:"correction"`
	Anchors          []Anchor        `json:"anchors"`
	Observations     []Observation   `json:"observations"`
	Redactions       []Redaction     `json:"redactions"`
}

func canonicalPayload(receipt Receipt) canonicalReceipt {
	normalized := normalize(receipt)
	return canonicalReceipt{
		Schema:           normalized.Schema,
		ID:               normalized.ID,
		Domain:           normalized.Domain,
		Kind:             normalized.Kind,
		IssuerInstanceID: normalized.IssuerInstanceID,
		Source:           normalized.Source,
		Target:           normalized.Target,
		Envelope:         normalized.Envelope,
		Operation:        normalized.Operation,
		IdempotencyKey:   normalized.IdempotencyKey,
		CorrelationID:    normalized.CorrelationID,
		Outcome:          normalized.Outcome,
		Epochs:           normalized.Epochs,
		ObservedAt:       normalized.ObservedAt.UTC().Format(time.RFC3339Nano),
		EvidenceRefs:     normalized.EvidenceRefs,
		IntegrityIssuer:  normalized.Integrity.Issuer,
		IntegrityKeyID:   normalized.Integrity.KeyID,
		Correction:       normalized.Correction,
		Anchors:          normalized.Anchors,
		Observations:     normalized.Observations,
		Redactions:       normalized.Redactions,
	}
}

func normalize(receipt Receipt) Receipt {
	normalized := receipt
	normalized.EvidenceRefs = append([]string(nil), receipt.EvidenceRefs...)
	sort.Strings(normalized.EvidenceRefs)
	normalized.EvidenceRefs = uniqueStrings(normalized.EvidenceRefs)
	normalized.Anchors = append([]Anchor(nil), receipt.Anchors...)
	sort.Slice(normalized.Anchors, func(i, j int) bool {
		if normalized.Anchors[i].Kind == normalized.Anchors[j].Kind {
			return normalized.Anchors[i].Ref < normalized.Anchors[j].Ref
		}
		return normalized.Anchors[i].Kind < normalized.Anchors[j].Kind
	})
	normalized.Anchors = uniqueAnchors(normalized.Anchors)
	normalized.Observations = append([]Observation(nil), receipt.Observations...)
	sort.Slice(normalized.Observations, func(i, j int) bool {
		left, right := normalized.Observations[i], normalized.Observations[j]
		return observationSortKey(left) < observationSortKey(right)
	})
	normalized.Observations = uniqueObservations(normalized.Observations)
	normalized.Redactions = append([]Redaction(nil), receipt.Redactions...)
	sort.Slice(normalized.Redactions, func(i, j int) bool {
		if normalized.Redactions[i].FieldClass == normalized.Redactions[j].FieldClass {
			return normalized.Redactions[i].Reason < normalized.Redactions[j].Reason
		}
		return normalized.Redactions[i].FieldClass < normalized.Redactions[j].FieldClass
	})
	normalized.Redactions = uniqueRedactions(normalized.Redactions)
	return normalized
}

func uniqueStrings(values []string) []string {
	return uniqueBy(values, func(value string) string { return value })
}

func uniqueAnchors(values []Anchor) []Anchor {
	return uniqueBy(values, func(value Anchor) string { return value.Kind + "\x00" + value.Ref })
}

func uniqueObservations(values []Observation) []Observation {
	return uniqueBy(values, observationSortKey)
}

func uniqueRedactions(values []Redaction) []Redaction {
	return uniqueBy(values, func(value Redaction) string { return value.FieldClass + "\x00" + value.Reason })
}

func uniqueBy[T any](values []T, key func(T) string) []T {
	if len(values) < 2 {
		return values
	}
	unique := values[:0]
	var previous string
	for index, value := range values {
		current := key(value)
		if index == 0 || current != previous {
			unique = append(unique, value)
			previous = current
		}
	}
	return unique
}

func observationSortKey(observation Observation) string {
	return strings.Join([]string{
		observation.Segment,
		string(observation.Mode),
		string(observation.State),
		observation.FactKind,
		observation.FactRef,
		observation.Authority,
		observation.Version,
		observation.Digest,
		observation.ObservedAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")
}
