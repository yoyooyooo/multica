CREATE INDEX CONCURRENTLY IF NOT EXISTS external_pr_reconcile_work_claim_idx
    ON external_pr_reconcile_work (state, next_attempt_at, lease_expires_at, updated_at);
