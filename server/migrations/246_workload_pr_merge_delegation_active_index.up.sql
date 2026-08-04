CREATE UNIQUE INDEX CONCURRENTLY workload_pr_merge_delegation_active_task_uidx ON workload_pr_merge_delegation (workspace_id, task_id, run_id, operation) WHERE revoked_at IS NULL;
