CREATE INDEX CONCURRENTLY coordination_record_issue_ref_issue_guard_idx
    ON coordination_record_issue_ref (workspace_id, issue_id);
