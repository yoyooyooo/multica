-- Typed, provider-neutral workflow continuation for external PR terminal facts.
-- This is deliberately not a generic continuation/effect ledger. Relationships
-- are application-owned so Issue/workspace cleanup can remain transactional
-- without foreign keys or cascades.
BEGIN;

CREATE TABLE IF NOT EXISTS external_pr_reconcile_work (
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL,
    issue_id           UUID NOT NULL,
    link_id            UUID NOT NULL,
    kind               TEXT NOT NULL CHECK (kind = 'external_pr_terminal'),
    provider           TEXT NOT NULL,
    external_repo      TEXT NOT NULL,
    external_number    INTEGER NOT NULL,
    source_revision    TEXT NOT NULL,
    source_idempotency_key TEXT,
    state              TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'claimed', 'retry_wait', 'succeeded', 'recorded', 'dead')),
    attempt            INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts       INTEGER NOT NULL DEFAULT 4 CHECK (max_attempts > 0),
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner        TEXT,
    lease_token        UUID,
    lease_expires_at   TIMESTAMPTZ,
    last_error_code    TEXT,
    last_redacted_error TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at       TIMESTAMPTZ
);

COMMIT;
