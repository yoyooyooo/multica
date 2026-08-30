-- Add an independent lease to finalization intent. The lease is separate from
-- external_pr_reconcile_work so a post-commit crash is recoverable even when
-- the finalization has no linked work row.
ALTER TABLE external_pr_reconcile_finalization
    ADD COLUMN IF NOT EXISTS lease_owner TEXT,
    ADD COLUMN IF NOT EXISTS lease_token UUID,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;
