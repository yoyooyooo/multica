CREATE INDEX CONCURRENTLY coordination_record_dependency_idx
    ON coordination_record (workspace_id, dependency_id)
    WHERE dependency_id IS NOT NULL;
