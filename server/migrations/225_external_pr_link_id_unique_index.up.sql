CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_external_pr_link_id
    ON external_pull_request_link(id);
