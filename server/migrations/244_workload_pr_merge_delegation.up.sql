CREATE TABLE workload_pr_merge_delegation (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    task_id UUID NOT NULL,
    run_id UUID NOT NULL,
    operation TEXT NOT NULL DEFAULT 'pr.merge' CHECK (operation = 'pr.merge'),
    repository TEXT NOT NULL CHECK (repository = btrim(repository) AND repository <> ''),
    pull_request_number BIGINT NOT NULL CHECK (pull_request_number > 0),
    forgejo_pull_request_number BIGINT NOT NULL CHECK (forgejo_pull_request_number > 0),
    expected_head_sha TEXT NOT NULL CHECK (expected_head_sha ~ '^[a-f0-9]{40}$'),
    merge_method TEXT NOT NULL CHECK (merge_method IN ('merge', 'rebase', 'rebase-merge', 'squash', 'fast-forward-only')),
    authority_revision UUID NOT NULL DEFAULT gen_random_uuid(),
    granted_by_user_id UUID NOT NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > granted_at),
    revoked_at TIMESTAMPTZ,
    revoked_by_user_id UUID,
    revocation_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (revoked_at IS NULL AND revoked_by_user_id IS NULL AND revocation_reason IS NULL)
        OR
        (revoked_at IS NOT NULL AND revoked_by_user_id IS NOT NULL AND revocation_reason IS NOT NULL AND revocation_reason <> '')
    )
);
