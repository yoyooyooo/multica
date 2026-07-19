CREATE TABLE coordination_record (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    coordination_scope_id UUID NOT NULL,
    kind TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    status TEXT NOT NULL,
    root_issue_id UUID NOT NULL,
    downstream_issue_id UUID NOT NULL,
    upstream_issue_id UUID NOT NULL,
    dependency_id UUID NULL,
    reason_code TEXT NOT NULL,
    resolution_code TEXT NULL,
    created_by_type TEXT NOT NULL,
    created_by_id UUID NOT NULL,
    created_task_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    resolved_by_type TEXT NULL,
    resolved_by_id UUID NULL,
    resolved_task_id UUID NULL,
    resolved_at TIMESTAMPTZ NULL,
    CONSTRAINT coordination_record_kind_check CHECK (kind = 'blocker'),
    CONSTRAINT coordination_record_schema_version_check CHECK (schema_version = 1),
    CONSTRAINT coordination_record_status_check CHECK (status IN ('open', 'resolved')),
    CONSTRAINT coordination_record_endpoints_check CHECK (downstream_issue_id <> upstream_issue_id),
    CONSTRAINT coordination_record_reason_code_check CHECK (reason_code = 'waiting_on_issue'),
    CONSTRAINT coordination_record_resolution_code_check CHECK (
        resolution_code IS NULL OR resolution_code IN ('no_longer_blocking', 'superseded')
    ),
    CONSTRAINT coordination_record_created_by_type_check CHECK (created_by_type IN ('member', 'agent')),
    CONSTRAINT coordination_record_created_by_task_check CHECK (
        (created_by_type = 'member' AND created_task_id IS NULL)
        OR (created_by_type = 'agent' AND created_task_id IS NOT NULL)
    ),
    CONSTRAINT coordination_record_resolution_state_check CHECK (
        (
            status = 'open'
            AND resolution_code IS NULL
            AND resolved_by_type IS NULL
            AND resolved_by_id IS NULL
            AND resolved_task_id IS NULL
            AND resolved_at IS NULL
        )
        OR (
            status = 'resolved'
            AND resolution_code IS NOT NULL
            AND resolved_by_type IN ('member', 'agent')
            AND resolved_by_id IS NOT NULL
            AND resolved_at IS NOT NULL
            AND (
                (resolved_by_type = 'member' AND resolved_task_id IS NULL)
                OR (resolved_by_type = 'agent' AND resolved_task_id IS NOT NULL)
            )
        )
    )
);

CREATE TABLE coordination_record_issue_ref (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    coordination_scope_id UUID NOT NULL,
    record_id UUID NOT NULL,
    phase TEXT NOT NULL,
    issue_id UUID NOT NULL,
    position INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT coordination_record_issue_ref_phase_check CHECK (phase IN ('create', 'resolution')),
    CONSTRAINT coordination_record_issue_ref_position_check CHECK (position BETWEEN 0 AND 31)
);
