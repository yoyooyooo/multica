-- Context Continuity v1 receipt/index storage. This migration is additive and
-- intentionally has no foreign keys: source facts remain owned by their exact
-- authority, and a missing historical source must degrade to not_observed rather
-- than make an append-only receipt invalid. Unique and lookup indexes are added
-- in follow-up single-statement migrations so they can be built concurrently.

CREATE TABLE IF NOT EXISTS lineage_receipt (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    multica_instance_id TEXT NOT NULL,
    ags_instance_id TEXT NULL,
    schema_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    issuer_instance_id TEXT NOT NULL,
    source_authority TEXT NOT NULL,
    source_ref TEXT NOT NULL,
    source_version TEXT NOT NULL,
    source_digest TEXT NOT NULL,
    target_authority TEXT NOT NULL,
    target_ref TEXT NOT NULL,
    envelope_id TEXT NOT NULL,
    envelope_digest TEXT NOT NULL,
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('accepted', 'rejected', 'unknown')),
    policy_revision TEXT NULL,
    config_generation TEXT NULL,
    runtime_bundle_revision TEXT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    evidence_refs JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence_refs) = 'array'),
    integrity_issuer TEXT NOT NULL,
    integrity_key_id TEXT NOT NULL,
    integrity_payload_sha256 TEXT NOT NULL,
    supersedes_receipt_id UUID NULL,
    correction_reason_code TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((supersedes_receipt_id IS NULL) = (correction_reason_code IS NULL))
);

CREATE TABLE IF NOT EXISTS lineage_receipt_anchor (
    receipt_id UUID NOT NULL,
    anchor_kind TEXT NOT NULL,
    anchor_ref TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Observations contain only fact identity, version, digest, state, and time.
-- Raw source/provider payloads are deliberately not persisted here.
CREATE TABLE IF NOT EXISTS lineage_observation (
    receipt_id UUID NOT NULL,
    segment TEXT NOT NULL,
    observation_mode TEXT NOT NULL CHECK (observation_mode IN ('snapshot', 'live')),
    observation_state TEXT NOT NULL CHECK (observation_state IN (
        'resolved', 'not_applicable', 'not_observed', 'redacted',
        'unavailable', 'stale', 'conflict'
    )),
    fact_kind TEXT NOT NULL,
    fact_ref TEXT NOT NULL,
    fact_authority TEXT NOT NULL,
    fact_version TEXT NULL,
    fact_digest TEXT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Redactions record only a class and reason; they cannot become a side channel
-- for the secret-bearing content that was deliberately excluded from the index.
CREATE TABLE IF NOT EXISTS lineage_redaction (
    receipt_id UUID NOT NULL,
    field_class TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
