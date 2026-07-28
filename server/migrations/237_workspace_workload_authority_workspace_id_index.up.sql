CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_workload_authority_workspace_id_uidx
    ON workspace_workload_authority(workspace_id);
