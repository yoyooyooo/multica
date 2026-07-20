CREATE INDEX CONCURRENTLY coordination_record_root_issue_idx
    ON coordination_record (workspace_id, root_issue_id);
