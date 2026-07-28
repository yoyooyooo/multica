CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_external_pr_link_workspace_idempotency
    ON external_pull_request_link(workspace_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
