ALTER TABLE coordination_record
    ADD CONSTRAINT coordination_record_pkey PRIMARY KEY USING INDEX coordination_record_pkey_idx;

ALTER TABLE coordination_record_issue_ref
    ADD CONSTRAINT coordination_record_issue_ref_pkey PRIMARY KEY USING INDEX coordination_record_issue_ref_pkey_idx,
    ADD CONSTRAINT coordination_record_issue_ref_issue_key UNIQUE USING INDEX coordination_record_issue_ref_issue_key_idx,
    ADD CONSTRAINT coordination_record_issue_ref_position_key UNIQUE USING INDEX coordination_record_issue_ref_position_key_idx;
