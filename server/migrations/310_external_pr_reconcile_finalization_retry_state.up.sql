-- Repair the pre-publication 289 state constraint on databases that already
-- recorded that migration before retry_wait was included. Keep the operation
-- scoped to the exact constraint owned by this migration.
BEGIN;

ALTER TABLE external_pr_reconcile_finalization
    DROP CONSTRAINT IF EXISTS external_pr_reconcile_finalization_state_check;

ALTER TABLE external_pr_reconcile_finalization
    ADD CONSTRAINT external_pr_reconcile_finalization_state_check
    CHECK (state IN ('pending', 'retry_wait', 'succeeded', 'recorded', 'dead'));

COMMIT;
