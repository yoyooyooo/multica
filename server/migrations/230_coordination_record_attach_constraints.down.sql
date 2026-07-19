ALTER TABLE coordination_record_issue_ref
    DROP CONSTRAINT IF EXISTS coordination_record_issue_ref_position_key,
    DROP CONSTRAINT IF EXISTS coordination_record_issue_ref_issue_key,
    DROP CONSTRAINT IF EXISTS coordination_record_issue_ref_pkey;

ALTER TABLE coordination_record
    DROP CONSTRAINT IF EXISTS coordination_record_pkey;
