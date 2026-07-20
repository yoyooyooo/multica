CREATE INDEX CONCURRENTLY coordination_record_issue_ref_read_idx
    ON coordination_record_issue_ref (workspace_id, record_id, phase, position, issue_id);
