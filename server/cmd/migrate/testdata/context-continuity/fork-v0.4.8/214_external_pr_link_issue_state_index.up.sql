CREATE INDEX CONCURRENTLY idx_external_pr_link_issue_state
    ON external_pull_request_link(workspace_id, issue_id, state)
    WHERE completion_intent AND link_confidence = 'authoritative';
