CREATE UNIQUE INDEX CONCURRENTLY idx_external_pr_link_idempotency
    ON external_pull_request_link(idempotency_key)
    WHERE idempotency_key IS NOT NULL;
