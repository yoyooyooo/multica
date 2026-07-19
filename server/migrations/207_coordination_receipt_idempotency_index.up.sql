CREATE UNIQUE INDEX CONCURRENTLY coordination_receipt_workspace_idempotency_idx
    ON coordination_receipt (workspace_id, idempotency_key);
