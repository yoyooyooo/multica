package lineage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TxStarter is the narrow transaction seam needed to make a receipt and all of
// its index rows atomic. It is intentionally local to this package so later
// query/API work can use the primitive without depending on a service package.
type TxStarter interface {
	queryRower
	Begin(context.Context) (pgx.Tx, error)
}

// PostgresStore is the production Store implementation. SQL stays here rather
// than in a handler because lineage receipt ingestion is an atomic domain write,
// not an HTTP transport concern.
type PostgresStore struct {
	db TxStarter
}

func NewPostgresStore(db TxStarter) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) FindReceipt(ctx context.Context, id string) (StoredReceipt, bool, error) {
	return loadStoredReceipt(ctx, s.db, receiptSelect+` WHERE id = $1`, id)
}

func (s *PostgresStore) FindByIdempotency(ctx context.Context, domain AuthorityDomain, issuerInstanceID, operation, key string) (StoredReceipt, bool, error) {
	return loadStoredReceipt(ctx, s.db, receiptSelect+`
WHERE workspace_id = $1
  AND multica_instance_id = $2
  AND issuer_instance_id = $3
  AND operation = $4
  AND idempotency_key = $5`, domain.WorkspaceID, domain.MulticaInstanceID, issuerInstanceID, operation, key)
}

func (s *PostgresStore) Ingest(ctx context.Context, receipt StoredReceipt) (StoredReceipt, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return StoredReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	evidence, err := json.Marshal(receipt.EvidenceRefs)
	if err != nil {
		return StoredReceipt{}, false, fmt.Errorf("marshal lineage evidence references: %w", err)
	}

	_, err = tx.Exec(ctx, `
INSERT INTO lineage_receipt (
    id, workspace_id, multica_instance_id, ags_instance_id,
    schema_name, kind, issuer_instance_id,
    source_authority, source_ref, source_version, source_digest,
    target_authority, target_ref,
    envelope_id, envelope_digest,
    operation, idempotency_key, correlation_id, outcome,
    policy_revision, config_generation, runtime_bundle_revision,
    observed_at, evidence_refs,
    integrity_issuer, integrity_key_id, integrity_payload_sha256,
    supersedes_receipt_id, correction_reason_code
) VALUES (
    $1, $2, $3, NULLIF($4, ''),
    $5, $6, $7,
    $8, $9, $10, $11,
    $12, $13,
    $14, $15,
    $16, $17, $18, $19,
    NULLIF($20, ''), NULLIF($21, ''), NULLIF($22, ''),
    $23, $24::jsonb,
    $25, $26, $27,
    NULLIF($28, '')::uuid, NULLIF($29, '')
)
ON CONFLICT (workspace_id, multica_instance_id, issuer_instance_id, operation, idempotency_key)
DO NOTHING`,
		receipt.ID, receipt.Domain.WorkspaceID, receipt.Domain.MulticaInstanceID, receipt.Domain.AGSInstanceID,
		receipt.Schema, receipt.Kind, receipt.IssuerInstanceID,
		receipt.Source.Authority, receipt.Source.Ref, receipt.Source.Version, receipt.Source.Digest,
		receipt.Target.Authority, receipt.Target.Ref,
		receipt.Envelope.ID, receipt.Envelope.Digest,
		receipt.Operation, receipt.IdempotencyKey, receipt.CorrelationID, receipt.Outcome,
		receipt.Epochs.PolicyRevision, receipt.Epochs.ConfigGeneration, receipt.Epochs.RuntimeBundleRevision,
		receipt.ObservedAt, evidence,
		receipt.Integrity.Issuer, receipt.Integrity.KeyID, receipt.Integrity.PayloadSHA256,
		receipt.Correction.SupersedesReceiptID, receipt.Correction.ReasonCode,
	)
	if err != nil {
		return StoredReceipt{}, false, err
	}

	stored, found, err := loadStoredReceipt(ctx, tx, receiptSelect+`
WHERE workspace_id = $1
  AND multica_instance_id = $2
  AND issuer_instance_id = $3
  AND operation = $4
  AND idempotency_key = $5`,
		receipt.Domain.WorkspaceID, receipt.Domain.MulticaInstanceID, receipt.IssuerInstanceID, receipt.Operation, receipt.IdempotencyKey)
	if err != nil {
		return StoredReceipt{}, false, err
	}
	if !found {
		return StoredReceipt{}, false, errors.New("lineage receipt insert returned no row")
	}
	if stored.ID != receipt.ID {
		if err := tx.Commit(ctx); err != nil {
			return StoredReceipt{}, false, err
		}
		return stored, false, nil
	}

	for _, anchor := range receipt.Anchors {
		if _, err := tx.Exec(ctx, `
INSERT INTO lineage_receipt_anchor (receipt_id, anchor_kind, anchor_ref)
VALUES ($1, $2, $3)
ON CONFLICT (receipt_id, anchor_kind, anchor_ref) DO NOTHING`, receipt.ID, anchor.Kind, anchor.Ref); err != nil {
			return StoredReceipt{}, false, err
		}
	}
	for _, observation := range receipt.Observations {
		if _, err := tx.Exec(ctx, `
INSERT INTO lineage_observation (
    receipt_id, segment, observation_mode, observation_state,
    fact_kind, fact_ref, fact_authority, fact_version, fact_digest, observed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10
)
ON CONFLICT (receipt_id, segment, observation_mode, fact_kind, fact_ref)
DO NOTHING`, receipt.ID, observation.Segment, observation.Mode, observation.State,
			observation.FactKind, observation.FactRef, observation.Authority, observation.Version, observation.Digest, observation.ObservedAt); err != nil {
			return StoredReceipt{}, false, err
		}
	}
	for _, redaction := range receipt.Redactions {
		if _, err := tx.Exec(ctx, `
INSERT INTO lineage_redaction (receipt_id, field_class, reason)
VALUES ($1, $2, $3)
ON CONFLICT (receipt_id, field_class, reason) DO NOTHING`, receipt.ID, redaction.FieldClass, redaction.Reason); err != nil {
			return StoredReceipt{}, false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return StoredReceipt{}, false, err
	}
	return stored, true, nil
}

const receiptSelect = `
SELECT
    id::text, workspace_id::text, multica_instance_id, COALESCE(ags_instance_id, ''),
    schema_name, kind, issuer_instance_id,
    source_authority, source_ref, source_version, source_digest,
    target_authority, target_ref,
    envelope_id, envelope_digest,
    operation, idempotency_key, correlation_id, outcome,
    COALESCE(policy_revision, ''), COALESCE(config_generation, ''), COALESCE(runtime_bundle_revision, ''),
    observed_at, evidence_refs,
    integrity_issuer, integrity_key_id, integrity_payload_sha256,
    COALESCE(supersedes_receipt_id::text, ''), COALESCE(correction_reason_code, '')
FROM lineage_receipt`

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadStoredReceipt(ctx context.Context, q queryRower, query string, args ...any) (StoredReceipt, bool, error) {
	var receipt StoredReceipt
	var evidence []byte
	err := q.QueryRow(ctx, query, args...).Scan(
		&receipt.ID, &receipt.Domain.WorkspaceID, &receipt.Domain.MulticaInstanceID, &receipt.Domain.AGSInstanceID,
		&receipt.Schema, &receipt.Kind, &receipt.IssuerInstanceID,
		&receipt.Source.Authority, &receipt.Source.Ref, &receipt.Source.Version, &receipt.Source.Digest,
		&receipt.Target.Authority, &receipt.Target.Ref,
		&receipt.Envelope.ID, &receipt.Envelope.Digest,
		&receipt.Operation, &receipt.IdempotencyKey, &receipt.CorrelationID, &receipt.Outcome,
		&receipt.Epochs.PolicyRevision, &receipt.Epochs.ConfigGeneration, &receipt.Epochs.RuntimeBundleRevision,
		&receipt.ObservedAt, &evidence,
		&receipt.Integrity.Issuer, &receipt.Integrity.KeyID, &receipt.Integrity.PayloadSHA256,
		&receipt.Correction.SupersedesReceiptID, &receipt.Correction.ReasonCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredReceipt{}, false, nil
	}
	if err != nil {
		return StoredReceipt{}, false, err
	}
	if err := json.Unmarshal(evidence, &receipt.EvidenceRefs); err != nil {
		return StoredReceipt{}, false, fmt.Errorf("decode lineage evidence references: %w", err)
	}
	return receipt, true, nil
}

// Keep a compiler check close to the narrow transaction abstraction.
var _ queryRower = (pgx.Tx)(nil)
