CREATE UNIQUE INDEX CONCURRENTLY coordination_receipt_scope_ordinal_idx
    ON coordination_receipt (coordination_scope_id, receipt_ordinal);
