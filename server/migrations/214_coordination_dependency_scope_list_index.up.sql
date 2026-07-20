CREATE INDEX CONCURRENTLY coordination_dependency_scope_active_created_idx ON coordination_dependency (workspace_id, coordination_scope_id, created_at, id) WHERE resolved_at IS NULL;
