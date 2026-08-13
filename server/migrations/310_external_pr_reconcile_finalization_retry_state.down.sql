-- Restore the pre-retry state constraint without silently discarding a
-- retry_wait row. The mapping is deterministic and releases any stale lease
-- before the old constraint is installed.
BEGIN;

ALTER TABLE external_pr_reconcile_finalization
    DROP CONSTRAINT IF EXISTS external_pr_reconcile_finalization_state_check;

UPDATE external_pr_reconcile_finalization
SET state = CASE WHEN attempt >= max_attempts THEN 'dead' ELSE 'pending' END,
    next_attempt_at = now(),
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_code = CASE
        WHEN attempt >= max_attempts THEN 'retry_state_rollback_attempts_exhausted'
        ELSE NULL
    END,
    last_redacted_error = CASE
        WHEN attempt >= max_attempts THEN 'retry_wait mapped to dead during state constraint rollback'
        ELSE NULL
    END,
    completed_at = CASE WHEN attempt >= max_attempts THEN now() ELSE NULL END,
    updated_at = now()
WHERE state = 'retry_wait';

ALTER TABLE external_pr_reconcile_finalization
    ADD CONSTRAINT external_pr_reconcile_finalization_state_check
    CHECK (state IN ('pending', 'succeeded', 'recorded', 'dead'));

COMMIT;
