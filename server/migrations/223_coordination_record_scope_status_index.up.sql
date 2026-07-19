CREATE INDEX CONCURRENTLY coordination_record_scope_status_created_idx
    ON coordination_record (workspace_id, coordination_scope_id, status, created_at DESC, id DESC);
