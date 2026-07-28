CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_external_pr_receipt_workspace_issue
    ON external_pull_request_receipt(workspace_id, issue_id);
