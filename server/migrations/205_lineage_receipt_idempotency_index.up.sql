CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_lineage_receipt_idempotency
    ON lineage_receipt (workspace_id, multica_instance_id, issuer_instance_id, operation, idempotency_key);
