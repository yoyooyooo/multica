-- External PR integration: provider-neutral links from outside code hosts to Multica issues.
-- Service peers (AGS is the first provider) write authoritative task-token-derived
-- links here so Multica can make the final issue-status transition with local
-- hierarchy guards.
CREATE TABLE external_pull_request_link (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL,
    issue_id          UUID NOT NULL,
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
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
