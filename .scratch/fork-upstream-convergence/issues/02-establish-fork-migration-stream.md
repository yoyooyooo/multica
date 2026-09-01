Status: ready-for-agent
Blocked by: None - can start immediately

# Establish the fork migration stream and accepted upgrade floor

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

4, 5, and 34.

## What to build

Introduce a fork-owned migration stream and ledger that runs after upstream migrations. Establish the accepted 2026-08-30 generation as the minimum direct upgrade source and replace broad historical migration replay with three executable acceptance paths: fresh upstream install, sanitized accepted-floor upgrade, and interrupted convergence retry.

Existing full migration basenames and live ledger rows remain immutable. This slice proves the migration mechanism without performing a production migration.

## Simplification guardrails

- Implement only a sequential upstream-then-fork runner and ledger; do not build a migration orchestration framework or second schema-management product.
- Keep exactly the three accepted test states. Do not recreate every historical migration directory or compatibility topology in the active tree.

## Acceptance criteria

- [ ] Fork migrations no longer consume upstream numeric migration authority.
- [ ] Deployment order is deterministically upstream migrations followed by fork migrations.
- [ ] A fresh database reaches the converged schema through the supported runner.
- [ ] A sanitized accepted-floor database reaches the same schema without rewriting historical ledger rows.
- [ ] An interrupted or failed fork migration can be retried without duplicate effects or corrupted ledger state.
- [ ] Concurrent tests use unique schemas or databases and assert complete migration identities rather than numeric prefixes.
- [ ] Rollback fences and backup requirements are explicit wherever image-only rollback is insufficient.

## Blocked by

None - can start immediately.
