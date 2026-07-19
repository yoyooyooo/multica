# Work Coordination Store V1–V2

## Problem

The fork needs passive, root-scoped coordination facts that Agents can read and mutate without turning the Store into a scheduler or coupling it to legacy issue dependency behavior. V1 established scopes, canonical request hashes, receipts, exact task-credential revalidation, and deletion guards. V2 adds one canonical dependency direction: `downstream blocked_by upstream`.

## Current source scope

The merged V1 source covers:

- root coordination scopes and request-hash receipts;
- exact authority revalidation for members and task-token Agents;
- route-scoped API/CLI product errors;
- lock-held Issue/Batch/Workspace deletion guards and typed deletion-handle lifecycle;
- the DB-required harness used by later slices.

The V2 source candidate adds:

- additive `coordination_dependency` storage, separate from legacy `issue_dependency`;
- add, list, and resolve operations under a coordination scope;
- workspace-global ownership of each unresolved downstream/upstream pair;
- cycle prevention, self-edge rejection, and a 1,000-active-row per-scope cap;
- scope revision CAS, canonical request hashes, immutable receipt replay, and revision-bound opaque pagination cursors;
- API, CLI, and built-in skill surfaces for dependency operations;
- Issue/Batch/Workspace deletion guards for all dependency history, including resolved rows.

It does not add blocker records, inspect, scheduling, wakeups, assignment, reconciliation, Store cleanup, or legacy dependency lifecycle behavior.

## Canonical model

A dependency has one meaning only:

```text
downstream blocked_by upstream
```

`blocks_issue_id` in the response is an explicit alias of `downstream_issue_id`; it does not define a second edge. Resolution is monotonic: it stamps `resolved_by_*` and `resolved_at`, retains history, and removes the row from active-list and cycle checks. The same pair may be added again only after the previous row is resolved.

The Store never reads or writes `issue_dependency` to implement this model. V2 migrations and tests preserve legacy rows and schema unchanged.

## Authority and concurrency boundaries

Every operation derives workspace and actor identity from server authentication. Agent mutations revalidate the exact task credential, require both endpoints to share the scope's actual root, and require one endpoint to equal the current task issue. Member mutations require current workspace membership.

Mutations share the workspace advisory-lock key space with Scope and delete operations. Under that lock, the service validates current authority, scope ownership, expected revision, pair ownership, capacity, and cycle safety; then it writes the dependency, advances the scope revision through CAS, and persists the receipt in one transaction. Replay first revalidates current authority and referenced resources, then returns the immutable saved projection.

Pagination cursors bind the scope id, current revision, creation timestamp, and dependency id. A revision change invalidates an older cursor rather than returning a mixed snapshot.

## API and CLI

Routes:

```text
POST /api/coordination/scopes/{scopeId}/dependencies
GET  /api/coordination/scopes/{scopeId}/dependencies
POST /api/coordination/scopes/{scopeId}/dependencies/{dependencyId}/resolve
```

CLI:

```bash
multica coordination dependency add \
  --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> \
  --expected-revision <n> --idempotency-key <key>
multica coordination dependency list --scope <uuid> [--cursor <opaque>] [--limit 1..100]
multica coordination dependency resolve \
  --scope <uuid> --dependency <uuid> \
  --expected-revision <n> --idempotency-key <key>
```

Use `--output json` for machine consumption. Conflict exit 6 remains restricted to exact method/route/code combinations. Self-dependency and cycle failures use validation exit 5. Unknown, malformed, mismatched, or future-route envelopes retain legacy handling.

## Passive boundary

V1–V2 do not alter Issue status, assignee, comments, metadata, task scheduling, Autopilot behavior, or legacy dependency rows. They add no foreign keys or cascading actions. Delete guards prevent dangling Store facts but do not claim automatic cleanup or rollback of external side effects.

## Deployment and portability

V1 was merged into this fork's `main`. V2 remains a source candidate until its exact head passes review, CI, and merge acceptance. A source commit alone does not prove migration apply, process restart, runtime deployment, or projection updates.

This feature is intended as a general upstream candidate rather than a permanent local-only contract. Until upstream owns an equivalent passive slice, this fork remains the source authority for its additive migrations and behavior.

## Implementation anchors

- migrations `202`–`210` for scopes and receipts;
- migrations `211`–`217` for canonical dependencies;
- `server/pkg/db/queries/coordination.sql` and generated sqlc types;
- coordination service, handler, router, CLI, and built-in skill wiring;
- typed deletion handles and guarded handler orchestration;
- `scripts/test-work-coordination-db-required.sh`.

## Verification and claim limit

The source includes migration up/down/up tests, DB-backed lifecycle and authority tests, concurrency and capacity tests, strict wire tests, CLI request/error/output tests, deletion endpoint tests, sqlc generation checks, and the DB-required harness. Exact-head repository gates, independent review, CI, merge, and deployment must be recorded separately; they are not claimed by this narrative.

## Rollback

The schema is additive. The V2 down path removes dependency constraints, indexes, and table in reverse order without touching legacy `issue_dependency`; the V1 down path separately removes receipt and scope storage. Runtime rollback must stop V2 writes before applying schema rollback.
