CREATE INDEX CONCURRENTLY IF NOT EXISTS workload_pr_merge_delegation_issue_state_idx ON workload_pr_merge_delegation (workspace_id, issue_id, state, updated_at);
