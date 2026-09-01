Status: ready-for-agent
Blocked by: [02 Migration stream](02-establish-fork-migration-stream.md), [03 External PR authority](03-harden-external-pr-binding-authority.md), [04 Unified PR projection](04-unify-pr-projection-and-conflicts.md), [05 Completion continuation](05-decouple-pr-completion-and-continuation.md)

# Shadow-converge every live External PR association

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

14-22, 34, and 35.

## What to build

Run a read-only-first shadow convergence campaign over the accepted production-data shape. Preserve every live External PR as an explicit fork-owned fact, enrich strict associations into the unified projection, and produce an authoritative disposition for each non-strict association. Compare old and unified reads plus completion eligibility before any production authority switch.

This slice may prepare migration and repair artifacts against sanitized data. It must not mutate production or classify a missing fact by textual inference.

## Acceptance criteria

- [ ] A sanitized accepted-floor fixture represents all observed live association classes and realistic migration ledger state.
- [ ] All 240 live associations appear in a census with stable Workspace, Issue, external identity, state, confidence, completion intent, and merge projection disposition.
- [ ] The 231 strict associations preserve explicit Issue authority and produce equivalent or intentionally improved unified projections.
- [ ] Each of the nine non-strict associations is resolved by authoritative provider lookup or receives an explicit read-only archive disposition.
- [ ] No missing canonical field is inferred from PR title, body, branch, or an Issue-shaped string.
- [ ] Old External reads, native reads, unified reads, and completion eligibility are compared for every live Issue class with machine-readable discrepancies.
- [ ] Explicit-versus-inferred conflicts are enumerated and automatic completion remains disabled until repaired.
- [ ] Reconcile/finalization state is proven terminal or replayable; no in-flight lease is silently discarded.
- [ ] The prepared migration is retryable on sanitized data and records a rollback/restore boundary.
- [ ] Production mutation remains gated on a separate accepted generation and explicit deployment authorization.

## Blocked by

- [02 Establish the fork migration stream](02-establish-fork-migration-stream.md)
- [03 Harden explicit External PR binding authority](03-harden-external-pr-binding-authority.md)
- [04 Unify pull request projection and association conflict handling](04-unify-pr-projection-and-conflicts.md)
- [05 Decouple pull request completion and prove durable continuation](05-decouple-pr-completion-and-continuation.md)
