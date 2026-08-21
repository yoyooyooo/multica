CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS external_pr_reconcile_work_identity_uidx
    ON external_pr_reconcile_work (workspace_id, kind, link_id, source_revision);
