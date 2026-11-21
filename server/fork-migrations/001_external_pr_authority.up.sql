BEGIN;

CREATE TABLE IF NOT EXISTS external_pull_request_link (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider = 'ags'),
    external_repo TEXT NOT NULL,
    external_number INTEGER NOT NULL CHECK (external_number > 0),
    external_url TEXT NOT NULL,
    merge_provider TEXT NOT NULL CHECK (merge_provider = 'forgejo'),
    merge_repo TEXT NOT NULL,
    merge_number INTEGER NOT NULL CHECK (merge_number > 0),
    merge_url TEXT NOT NULL,
    merged_sha TEXT,
    link_confidence TEXT NOT NULL DEFAULT 'authoritative' CHECK (link_confidence = 'authoritative'),
    completion_intent BOOLEAN NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('open', 'draft', 'closed', 'merged')),
    fact_revision TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider, external_repo, external_number)
);

-- Accepted-floor tables predate the separate fork stream. Add only fields the
-- new runtime consumes; request-only canonical projection fields stay out.
ALTER TABLE external_pull_request_link
    ADD COLUMN IF NOT EXISTS fact_revision TEXT;
UPDATE external_pull_request_link
SET fact_revision = encode(public.digest(concat_ws(chr(31), workspace_id::text, issue_id::text,
    provider, external_repo, external_number::text, COALESCE(external_url, ''),
    COALESCE(merge_provider, ''), COALESCE(merge_repo, ''), COALESCE(merge_number::text, ''),
    COALESCE(merge_url, ''), COALESCE(merged_sha, ''), completion_intent::text, state), 'sha256'), 'hex')
WHERE fact_revision IS NULL OR fact_revision = '';
ALTER TABLE external_pull_request_link ALTER COLUMN fact_revision SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS external_pull_request_link_identity_uidx
    ON external_pull_request_link (workspace_id, provider, external_repo, external_number);
CREATE INDEX IF NOT EXISTS external_pull_request_link_issue_state_idx
    ON external_pull_request_link (workspace_id, issue_id, state, updated_at DESC);

CREATE TABLE IF NOT EXISTS external_pull_request_receipt (
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    link_id UUID REFERENCES external_pull_request_link(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    external_repo TEXT NOT NULL,
    external_number INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, idempotency_key)
);
ALTER TABLE external_pull_request_receipt ADD COLUMN IF NOT EXISTS link_id UUID;
UPDATE external_pull_request_receipt AS receipt
SET link_id = link.id
FROM external_pull_request_link AS link
WHERE receipt.link_id IS NULL
  AND link.workspace_id = receipt.workspace_id
  AND link.issue_id = receipt.issue_id
  AND link.provider = receipt.provider
  AND link.external_repo = receipt.external_repo
  AND link.external_number = receipt.external_number;
WITH latest_receipt AS (
    SELECT DISTINCT ON (link_id) link_id, payload_hash
    FROM external_pull_request_receipt
    WHERE link_id IS NOT NULL
    ORDER BY link_id, created_at DESC, idempotency_key DESC
)
UPDATE external_pull_request_link AS link
SET fact_revision = latest_receipt.payload_hash
FROM latest_receipt
WHERE latest_receipt.link_id = link.id
  AND latest_receipt.payload_hash ~ '^[0-9a-f]{64}$';
CREATE INDEX IF NOT EXISTS external_pull_request_receipt_issue_idx
    ON external_pull_request_receipt (workspace_id, issue_id);

CREATE TABLE IF NOT EXISTS external_pr_reconcile_work (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    link_id UUID NOT NULL REFERENCES external_pull_request_link(id) ON DELETE CASCADE,
    kind TEXT NOT NULL DEFAULT 'external_pr_terminal' CHECK (kind = 'external_pr_terminal'),
    provider TEXT NOT NULL DEFAULT 'ags',
    external_repo TEXT NOT NULL,
    external_number INTEGER NOT NULL,
    source_revision TEXT NOT NULL,
    source_idempotency_key TEXT,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'claimed', 'retry_wait', 'succeeded', 'recorded', 'dead')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 4 CHECK (max_attempts > 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner TEXT,
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    previous_status TEXT,
    status_activity_id UUID,
    intended_parent_id UUID,
    activity_published BOOLEAN NOT NULL DEFAULT FALSE,
    issue_published BOOLEAN NOT NULL DEFAULT FALSE,
    parent_comment_id UUID,
    parent_wake_done BOOLEAN NOT NULL DEFAULT FALSE,
    last_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (link_id, source_revision)
);
ALTER TABLE external_pr_reconcile_work
    ADD COLUMN IF NOT EXISTS previous_status TEXT,
    ADD COLUMN IF NOT EXISTS status_activity_id UUID,
    ADD COLUMN IF NOT EXISTS intended_parent_id UUID,
    ADD COLUMN IF NOT EXISTS activity_published BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS issue_published BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS parent_comment_id UUID,
    ADD COLUMN IF NOT EXISTS parent_wake_done BOOLEAN NOT NULL DEFAULT FALSE;

-- Fold only External-owned pending finalization progress into the work row. Old
-- native rows have no work_id and intentionally remain outside the new runtime.
DO $$
BEGIN
    IF to_regclass('external_pr_reconcile_finalization') IS NOT NULL THEN
        EXECUTE $sql$
            UPDATE external_pr_reconcile_work AS work
            SET previous_status = COALESCE(work.previous_status, finalization.previous_status),
                status_activity_id = COALESCE(work.status_activity_id, finalization.status_activity_id),
                intended_parent_id = COALESCE(work.intended_parent_id, finalization.intended_parent_id),
                activity_published = work.activity_published OR finalization.activity_published,
                issue_published = work.issue_published OR finalization.issue_published,
                parent_comment_id = COALESCE(work.parent_comment_id, finalization.parent_comment_id),
                parent_wake_done = work.parent_wake_done OR finalization.parent_wake_done
            FROM external_pr_reconcile_finalization AS finalization
            WHERE finalization.work_id = work.id
        $sql$;
    END IF;
END
$$;

-- Accepted-floor tables intentionally had application-owned relationships.
-- These narrow delete triggers make future Issue/Workspace removal atomic
-- without modifying upstream delete queries or requiring orphan cleanup first.
CREATE OR REPLACE FUNCTION delete_external_pr_issue_facts()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM external_pr_reconcile_work WHERE issue_id = OLD.id;
    DELETE FROM external_pull_request_receipt WHERE issue_id = OLD.id;
    DELETE FROM external_pull_request_link WHERE issue_id = OLD.id;
    RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS external_pr_issue_delete ON issue;
CREATE TRIGGER external_pr_issue_delete
BEFORE DELETE ON issue FOR EACH ROW EXECUTE FUNCTION delete_external_pr_issue_facts();

CREATE OR REPLACE FUNCTION delete_external_pr_workspace_facts()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM external_pr_reconcile_work WHERE workspace_id = OLD.id;
    DELETE FROM external_pull_request_receipt WHERE workspace_id = OLD.id;
    DELETE FROM external_pull_request_link WHERE workspace_id = OLD.id;
    RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS external_pr_workspace_delete ON workspace;
CREATE TRIGGER external_pr_workspace_delete
BEFORE DELETE ON workspace FOR EACH ROW EXECUTE FUNCTION delete_external_pr_workspace_facts();

CREATE UNIQUE INDEX IF NOT EXISTS external_pr_reconcile_work_revision_uidx
    ON external_pr_reconcile_work (link_id, source_revision);
CREATE INDEX IF NOT EXISTS external_pr_reconcile_work_claim_idx
    ON external_pr_reconcile_work (state, next_attempt_at, updated_at, id)
    WHERE state IN ('pending', 'claimed', 'retry_wait');
CREATE INDEX IF NOT EXISTS external_pr_reconcile_work_issue_idx
    ON external_pr_reconcile_work (workspace_id, issue_id);

COMMIT;
