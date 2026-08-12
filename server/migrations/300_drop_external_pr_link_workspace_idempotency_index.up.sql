-- T017: link-row idempotency is retired; receipt table remains the idempotency owner.
DROP INDEX CONCURRENTLY IF EXISTS idx_external_pr_link_workspace_idempotency;
