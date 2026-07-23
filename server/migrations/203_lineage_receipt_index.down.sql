-- Accepted lineage facts are append-only. A code rollback removes writers but
-- never deletes rows, so a later additive rollout can resume from this index.
SELECT 1;
