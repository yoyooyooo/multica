-- Durable post-commit completion finalization intent. It is deliberately typed
-- to the external PR continuation and owns only replayable Multica lifecycle
-- closure; provider effects remain outside this table.
BEGIN;

CREATE TABLE IF NOT EXISTS external_pr_reconcile_finalization (
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL,
    issue_id              UUID NOT NULL,
    work_id               UUID,
    source_revision       TEXT NOT NULL,
    source                TEXT NOT NULL,
    previous_status       TEXT NOT NULL,
    terminal_status       TEXT NOT NULL,
    status_activity_id    UUID NOT NULL,
    intended_parent_id    UUID,
    activity_ids          UUID[] NOT NULL DEFAULT '{}',
    state                 TEXT NOT NULL DEFAULT 'pending',
    CONSTRAINT external_pr_reconcile_finalization_state_check
        CHECK (state IN ('pending', 'retry_wait', 'succeeded', 'recorded', 'dead')),
    parent_comment_id     UUID,
    activity_published    BOOLEAN NOT NULL DEFAULT FALSE,
    issue_published       BOOLEAN NOT NULL DEFAULT FALSE,
    comment_published     BOOLEAN NOT NULL DEFAULT FALSE,
    parent_wake_done      BOOLEAN NOT NULL DEFAULT FALSE,
    attempt               INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts          INTEGER NOT NULL DEFAULT 4 CHECK (max_attempts > 0),
    next_attempt_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code       TEXT,
    last_redacted_error   TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at          TIMESTAMPTZ
);

ALTER TABLE external_pr_reconcile_finalization
    ADD COLUMN IF NOT EXISTS intended_parent_id UUID;

ALTER TABLE comment
    ADD COLUMN IF NOT EXISTS finalization_key TEXT;

COMMIT;
