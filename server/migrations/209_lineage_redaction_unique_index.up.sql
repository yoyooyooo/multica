CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_lineage_redaction_unique
    ON lineage_redaction (receipt_id, field_class, reason);
