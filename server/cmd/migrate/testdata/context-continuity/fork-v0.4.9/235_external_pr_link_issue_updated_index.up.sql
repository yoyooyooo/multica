CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_external_pr_link_workspace_issue_updated
    ON external_pull_request_link(workspace_id, issue_id, updated_at);
