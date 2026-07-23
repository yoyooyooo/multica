CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_lineage_receipt_anchor_unique
    ON lineage_receipt_anchor (receipt_id, anchor_kind, anchor_ref);
