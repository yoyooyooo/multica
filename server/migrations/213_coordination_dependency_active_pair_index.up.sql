CREATE UNIQUE INDEX CONCURRENTLY coordination_dependency_active_pair_idx ON coordination_dependency (workspace_id, downstream_issue_id, upstream_issue_id) WHERE resolved_at IS NULL;
