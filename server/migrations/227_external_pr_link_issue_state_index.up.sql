CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_external_pr_link_issue_state
    ON external_pull_request_link(workspace_id, issue_id, state)
    WHERE state IN ('open', 'draft') AND link_confidence = 'authoritative';
