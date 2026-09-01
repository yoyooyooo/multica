Status: ready-for-agent
Blocked by: [01 Rebuild from latest upstream](01-establish-clean-upstream-release-lane.md)

# Reimplement the minimal External PR authority slice

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

12-26 and 35-38.

## What to build

Reimplement External PR on the clean upstream generation as one narrow vertical slice: typed AGS callback admission, explicit Workspace/Issue UUID binding, immutable external natural identity, payload-hash idempotency, terminal fact durability, existing completion policies, crash continuation, and one Issue pull request UI section.

Validate the complete canonical callback envelope and include it in the idempotency hash, but persist only fields consumed by runtime reads, completion, or recovery. Reuse the current completion path with the smallest ownership correction: native calls without External work must not create External finalization state; External reconcile keeps its durable work and continuation.

## Simplification guardrails

- Do not persist request-only canonical envelope fields, create a provenance taxonomy, conflict table, repair UI, shadow API, generic provider framework, second PR model, or second lifecycle kernel.
- External authority never parses title, body, or branch. Ordinary upstream native association remains unchanged and is not configured as a second authority for AGS-managed PRs in this generation.
- Require transactionally idempotent observable behavior, not a generalized distributed exactly-once protocol. Test only retained contracts and reproduced authority races.

## Acceptance criteria

- [ ] Typed callbacks verify Workspace and Issue UUID ownership; redundant slug/key values are checked against those records or removed from the request contract.
- [ ] The complete canonical envelope is validated and hashed without adding permanent columns that have no runtime consumer.
- [ ] Exact replay creates no duplicate fact, activity, or work; the same key with changed payload returns conflict.
- [ ] One external natural identity cannot be rebound to another Issue, including a concurrent first-bind race.
- [ ] A terminal fact commits with durable reconcile work before HTTP success and survives a process crash.
- [ ] `record_only`, eligible leaf completion, non-leaf rejection, terminal Issue protection, open authoritative PR blocking, parent wake, and stage continuation retain their current observable behavior.
- [ ] Native completion without External work writes no External finalization row; External retry after completion does not duplicate status or lineage.
- [ ] Issue and Workspace deletion remain atomic with External facts and pending continuation.
- [ ] The existing Issue pull request API/UI renders External rows in one section without provenance or conflict-management product UI.
- [ ] External contract tests prove PR prose is not consulted; no exhaustive upstream provider/text matrix is added.

## Blocked by

- [01 Rebuild from latest upstream with an additive release path](01-establish-clean-upstream-release-lane.md)
