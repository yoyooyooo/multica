CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_external_pr_receipt_idempotency
    ON external_pull_request_receipt(workspace_id, idempotency_key);
