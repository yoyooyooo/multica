CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_lineage_observation_unique
    ON lineage_observation (receipt_id, segment, observation_mode, fact_kind, fact_ref);
