CREATE UNIQUE INDEX CONCURRENTLY coordination_record_issue_ref_issue_key_idx
    ON coordination_record_issue_ref (record_id, phase, issue_id);
