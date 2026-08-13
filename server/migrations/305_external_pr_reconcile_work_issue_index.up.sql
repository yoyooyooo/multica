CREATE INDEX CONCURRENTLY IF NOT EXISTS external_pr_reconcile_work_issue_idx
    ON external_pr_reconcile_work (workspace_id, issue_id, state, updated_at);
