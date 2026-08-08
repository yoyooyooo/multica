CREATE INDEX CONCURRENTLY IF NOT EXISTS workload_pr_merge_delegation_event_history_idx ON workload_pr_merge_delegation_event (delegation_id, created_at, id);
