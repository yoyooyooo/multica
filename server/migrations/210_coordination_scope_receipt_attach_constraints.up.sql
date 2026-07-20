ALTER TABLE coordination_scope
    ADD CONSTRAINT coordination_scope_pkey PRIMARY KEY USING INDEX coordination_scope_pkey_idx;

ALTER TABLE coordination_receipt
    ADD CONSTRAINT coordination_receipt_pkey PRIMARY KEY USING INDEX coordination_receipt_pkey_idx,
    ADD CONSTRAINT coordination_receipt_workspace_idempotency_key UNIQUE USING INDEX coordination_receipt_workspace_idempotency_idx,
    ADD CONSTRAINT coordination_receipt_scope_ordinal_key UNIQUE USING INDEX coordination_receipt_scope_ordinal_idx;
