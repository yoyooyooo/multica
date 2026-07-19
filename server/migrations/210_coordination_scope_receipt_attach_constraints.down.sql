ALTER TABLE coordination_receipt
    DROP CONSTRAINT IF EXISTS coordination_receipt_scope_ordinal_key,
    DROP CONSTRAINT IF EXISTS coordination_receipt_workspace_idempotency_key,
    DROP CONSTRAINT IF EXISTS coordination_receipt_pkey;

ALTER TABLE coordination_scope
    DROP CONSTRAINT IF EXISTS coordination_scope_pkey;
