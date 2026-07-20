CREATE INDEX CONCURRENTLY coordination_record_upstream_issue_idx
    ON coordination_record (workspace_id, upstream_issue_id);
