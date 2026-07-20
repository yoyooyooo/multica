CREATE TABLE coordination_scope (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    scope_kind TEXT NOT NULL,
    state TEXT NOT NULL,
    root_issue_id UUID NOT NULL,
    workflow_profile_key TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 0,
    next_receipt_ordinal BIGINT NOT NULL DEFAULT 0,
    created_by_type TEXT NOT NULL,
    created_by_id UUID NOT NULL,
    created_task_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT coordination_scope_scope_kind_check CHECK (scope_kind = 'root'),
    CONSTRAINT coordination_scope_state_check CHECK (state = 'active'),
    CONSTRAINT coordination_scope_workflow_profile_key_check CHECK (
        char_length(workflow_profile_key) BETWEEN 1 AND 128
        AND workflow_profile_key ~ '^[a-z0-9][a-z0-9._-]{0,127}$'
    ),
    CONSTRAINT coordination_scope_revision_check CHECK (revision >= 0),
    CONSTRAINT coordination_scope_next_receipt_ordinal_check CHECK (next_receipt_ordinal >= 0),
    CONSTRAINT coordination_scope_created_by_type_check CHECK (created_by_type IN ('member', 'agent')),
    CONSTRAINT coordination_scope_created_by_task_check CHECK (
        (created_by_type = 'member' AND created_task_id IS NULL)
        OR (created_by_type = 'agent' AND created_task_id IS NOT NULL)
    )
);

CREATE TABLE coordination_receipt (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    coordination_scope_id UUID NOT NULL,
    receipt_ordinal BIGINT NOT NULL,
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash BYTEA NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id UUID NOT NULL,
    revision_before BIGINT NOT NULL,
    revision_after BIGINT NOT NULL,
    result_snapshot JSONB NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id UUID NOT NULL,
    actor_task_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT coordination_receipt_receipt_ordinal_check CHECK (receipt_ordinal >= 1),
    CONSTRAINT coordination_receipt_operation_check CHECK (char_length(operation) BETWEEN 1 AND 64),
    CONSTRAINT coordination_receipt_idempotency_key_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200),
    CONSTRAINT coordination_receipt_request_hash_check CHECK (octet_length(request_hash) = 32),
    CONSTRAINT coordination_receipt_resource_type_check CHECK (char_length(resource_type) BETWEEN 1 AND 32),
    CONSTRAINT coordination_receipt_revision_check CHECK (revision_before >= 0 AND revision_after >= revision_before),
    CONSTRAINT coordination_receipt_result_snapshot_check CHECK (
        jsonb_typeof(result_snapshot) = 'object'
        AND octet_length(result_snapshot::text) <= 16384
    ),
    CONSTRAINT coordination_receipt_actor_type_check CHECK (actor_type IN ('member', 'agent')),
    CONSTRAINT coordination_receipt_actor_task_check CHECK (
        (actor_type = 'member' AND actor_task_id IS NULL)
        OR (actor_type = 'agent' AND actor_task_id IS NOT NULL)
    )
);
