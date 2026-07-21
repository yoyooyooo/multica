CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_lineage_receipt_anchor_lookup
    ON lineage_receipt_anchor (anchor_kind, anchor_ref, receipt_id);
