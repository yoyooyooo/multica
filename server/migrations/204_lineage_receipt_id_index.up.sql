CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_lineage_receipt_id
    ON lineage_receipt (id);
