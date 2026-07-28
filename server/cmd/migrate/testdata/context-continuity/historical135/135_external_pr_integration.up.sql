-- External PR integration: provider-neutral links from outside code hosts to Multica issues.
-- Service peers (AGS is the first provider) write authoritative task-token-derived
-- links here so Multica can make the final issue-status transition with local
-- hierarchy guards.
CREATE TABLE external_pull_request_link (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id          UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL,
    external_repo     TEXT NOT NULL,
    external_number   INTEGER NOT NULL,
    external_url      TEXT,
    merge_provider    TEXT,
    merge_repo        TEXT,
    merge_number      INTEGER,
    merge_url         TEXT,
    merged_sha        TEXT,
    link_confidence   TEXT NOT NULL DEFAULT 'authoritative'
        CHECK (link_confidence IN ('authoritative', 'inferred')),
    completion_intent BOOLEAN NOT NULL DEFAULT TRUE,
    state             TEXT NOT NULL DEFAULT 'open'
        CHECK (state IN ('open', 'draft', 'closed', 'merged')),
    idempotency_key   TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider, external_repo, external_number)
);

CREATE INDEX idx_external_pr_link_issue_state
    ON external_pull_request_link(workspace_id, issue_id, state)
    WHERE completion_intent AND link_confidence = 'authoritative';

CREATE UNIQUE INDEX idx_external_pr_link_idempotency
    ON external_pull_request_link(idempotency_key)
    WHERE idempotency_key IS NOT NULL;
