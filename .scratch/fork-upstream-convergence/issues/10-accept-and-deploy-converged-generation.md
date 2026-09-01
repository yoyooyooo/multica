Status: ready-for-agent
Blocked by: [01 Rebuild from latest upstream](01-establish-clean-upstream-release-lane.md), [03 Minimal External PR authority](03-harden-external-pr-binding-authority.md)

# Upgrade and deploy the clean generation to both targets

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

4-10, 34-35, and 38.

## What to build

Add the smallest sequential migration path needed to apply current upstream migrations and then Fork DDL without competing for upstream migration numbers. Prove fresh install, sanitized 2026-08-30 accepted-floor upgrade, and one interrupted retry. Account for all 240 live External PR rows: keep the 231 strict rows, normalize each of the nine non-strict rows when authority can be recovered, otherwise preserve its existing explicit fact read-only.

After exact-head acceptance, deploy artifacts from the same source SHA to Mini and imile-win as two explicit independent transactions with per-target backup, migration, digest/runtime readback, smoke checks, and rollback evidence.

## Simplification guardrails

- Implement only a sequential runner and small Fork ledger; do not build a migration platform or recreate every historical fixture.
- The live-row census and comparison are one-time operator/test tooling. Do not ship shadow traffic, dual-write, archive UI, repair service, migration dashboard, rollout controller, or fleet scheduler.
- A single multi-architecture manifest is optional. Pi cancellation, AGS ownership cleanup, and upstream submissions are independent sidecars and do not block deployment.

## Acceptance criteria

- [ ] Upstream migrations run before Fork migrations without consuming upstream numeric authority or rewriting applied full basenames.
- [ ] Fresh upstream and sanitized accepted-floor databases reach the same required schema.
- [ ] One interrupted Fork migration retries without duplicate effects or ledger corruption.
- [ ] All 240 live rows have a bounded disposition; the nine non-strict rows are normalized from authority or preserved read-only without invented facts or archive behavior.
- [ ] Exact-head CI is green before artifacts are built, and every deployed architecture reads back the same accepted source SHA.
- [ ] Mini and imile-win each receive an independent database backup and preflight before switching.
- [ ] Each target reads back the expected schema/ledger, image digest, runtime revision, uploads/storage continuity, and authenticated External PR behavior.
- [ ] A failure on one target does not alter the other target's deployment or rollback authority.
- [ ] Receipts are owner-only and secret-safe and record explicit database/image rollback boundaries.
- [ ] Work stops after the External PR contract, 240-row disposition, accepted-floor upgrade/rollback, two target readbacks, and exact-head CI pass.

## Blocked by

- [01 Rebuild from latest upstream with an additive release path](01-establish-clean-upstream-release-lane.md)
- [03 Reimplement the minimal External PR authority slice](03-harden-external-pr-binding-authority.md)
