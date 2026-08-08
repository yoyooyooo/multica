-- The pre-migration hook performs the final exact catalog reconciliation.
-- Ledgering this no-op fences historical repair hooks so later migrations may
-- intentionally evolve these indexes without an old definition being restored.
SELECT 1;
