BEGIN;
DROP TABLE IF EXISTS external_pr_reconcile_finalization;
ALTER TABLE comment DROP COLUMN IF EXISTS finalization_key;
COMMIT;
