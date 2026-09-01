Status: ready-for-agent
Blocked by: [03 Harden explicit External PR binding authority](03-harden-external-pr-binding-authority.md), [04 Unify pull request projection](04-unify-pr-projection-and-conflicts.md)

# Decouple pull request completion and prove durable continuation

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

23-26.

## What to build

Separate the generic Issue completion evaluator from External PR reconciliation and finalization storage. Native GitHub/VCS paths must evaluate their own persisted associations without requiring fork-owned finalization records. The External adapter must continue to combine explicit authoritative facts with durable work, exactly-once terminal materialization, and independent parent/stage continuation.

Completion must fail closed on association conflicts and preserve every accepted fork policy across crashes, retries, concurrent Issue edits, and provider redelivery.

## Acceptance criteria

- [ ] The generic completion evaluator has no mandatory dependency on External PR work or finalization records.
- [ ] Native GitHub and VCS completion behavior remains compatible with upstream association semantics when no explicit conflict exists.
- [ ] External completion requires authoritative explicit binding, completion intent, merged state, and no conflicting association.
- [ ] `record_only`, unsupported policy, cancelled/done protection, eligible leaf-child checks, non-leaf rejection, and open authoritative PR blocking are proven transactionally.
- [ ] Status activity, External completion lineage, and terminal Issue transition commit atomically.
- [ ] A crash after terminal fact admission but before completion is recovered by durable reconcile without duplicate status changes or activities.
- [ ] A crash after Issue completion but before parent/stage continuation resumes finalization without closing a reopened Issue.
- [ ] Concurrent Issue status/topology edits and provider writes use a deterministic lock order and do not deadlock or complete against stale policy.
- [ ] Parent wake and stage continuation are exactly once from the user's observable perspective.
- [ ] Issue and Workspace deletion remain atomic with External work/finalization cleanup and cannot race a late callback into orphan state.

## Blocked by

- [03 Harden explicit External PR binding authority](03-harden-external-pr-binding-authority.md)
- [04 Unify pull request projection and association conflict handling](04-unify-pr-projection-and-conflicts.md)
