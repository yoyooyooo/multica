CREATE TABLE coordination_dependency (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    coordination_scope_id UUID NOT NULL,
    downstream_issue_id UUID NOT NULL,
    upstream_issue_id UUID NOT NULL,
    created_by_type TEXT NOT NULL,
    created_by_id UUID NOT NULL,
    created_task_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    resolved_by_type TEXT NULL,
    resolved_by_id UUID NULL,
    resolved_task_id UUID NULL,
    resolved_at TIMESTAMPTZ NULL,
    CONSTRAINT coordination_dependency_self_check CHECK (downstream_issue_id <> upstream_issue_id),
    CONSTRAINT coordination_dependency_created_by_type_check CHECK (created_by_type IN ('member', 'agent')),
    CONSTRAINT coordination_dependency_created_by_task_check CHECK (
        (created_by_type = 'member' AND created_task_id IS NULL)
        OR (created_by_type = 'agent' AND created_task_id IS NOT NULL)
    ),
    CONSTRAINT coordination_dependency_resolved_by_type_check CHECK (
        resolved_by_type IS NULL OR resolved_by_type IN ('member', 'agent')
    ),
    CONSTRAINT coordination_dependency_resolution_check CHECK (
        (
            resolved_at IS NULL
            AND resolved_by_type IS NULL
            AND resolved_by_id IS NULL
            AND resolved_task_id IS NULL
        )
        OR (
            resolved_at IS NOT NULL
            AND resolved_by_type IS NOT NULL
            AND resolved_by_id IS NOT NULL
            AND (
                (resolved_by_type = 'member' AND resolved_task_id IS NULL)
                OR (resolved_by_type = 'agent' AND resolved_task_id IS NOT NULL)
            )
        )
    )
);
