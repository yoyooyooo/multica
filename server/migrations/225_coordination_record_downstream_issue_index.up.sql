CREATE INDEX CONCURRENTLY coordination_record_downstream_issue_idx
    ON coordination_record (workspace_id, downstream_issue_id);
