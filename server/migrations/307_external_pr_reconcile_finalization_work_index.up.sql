CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS external_pr_reconcile_finalization_work_uidx
    ON external_pr_reconcile_finalization (work_id)
    WHERE work_id IS NOT NULL;
