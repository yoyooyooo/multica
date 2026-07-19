CREATE UNIQUE INDEX CONCURRENTLY coordination_record_issue_ref_position_key_idx
    ON coordination_record_issue_ref (record_id, phase, position);
