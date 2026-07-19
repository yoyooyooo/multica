# Work Coordination Store V1

## Problem

The fork needs a passive coordination store that can persist root-scoped coordination facts, canonical request hashes, and receipts without depending on legacy issue dependency rows or scheduling side effects. The observed gap is the lack of a server-backed scope/receipt slice that can be exercised from DB to service to API to CLI with exact credential revalidation and stable error mapping.

## Scope

V1 only covers:

- root coordination scopes;
- request-hash receipts;
- exact credential revalidation for task tokens;
- issue-root validation;
- the coordination scope API, CLI, and built-in skill surface;
- route-scoped CLI product errors and token/arity-aware output parsing;
- lock-held Issue/Batch/Workspace deletion guards with typed `Delete`/`Finish` lifecycle;
- the DB-required harness used by the later slices.

It does not add dependency, blocker, inspect, or scheduling authority.

## Authority and failure boundaries

V1 stores only passive coordination facts. It does not alter Issue status, assignee, comments, metadata, task scheduling, or Autopilot behavior. It does not use foreign keys or cascading actions. Delete-path handling remains a narrow guard seam around existing delete flows; V1 does not introduce Store cleanup or external-effect durability guarantees.

Issue and Workspace deletion now use one pinned connection, a workspace session lock, one transaction, and at-most-once `Finish(commit)`. Workspace membership teardown is explicit in that transaction and checked against the locked pre-delete census, rather than being left only to FK cascade. Batch deletion preserves the existing partial-success boundary with per-target savepoints: only `entity_delete` SQLSTATE `23503` is recoverable after verified rollback-to/release; all other failures abort the batch. Compact typed effects run only after commit and verified lock release. Commit-unknown or release failure returns `coordination_internal` and runs no effects.

Failures are closed through typed `coordination_*` errors and stable CLI exit mapping. V1 only upgrades exact method/route/code combinations; future-slice conflict codes on V1 routes retain legacy exit 1. Receipt replay revalidates current authority before returning saved data.

## Deployment and portability

V1 is merged into fork `main`. The merged implementation and source-test authority is commit `b9c9d9f635aed1206e46fd13b2d81d819ede84c4`, tree `ed242167e920620a9937f1b206e1009f93beb768`. Migrations `202` through `210` are allocated and merged as source, but migration apply, service/CLI restart, deployment, and live operation have not occurred and are not claimed. No runtime or projection target has been updated by the source merge alone.

V2 through V5 are not implemented and remain locked behind the V1 accepted exact-head review gate. This feature is a general upstream candidate rather than a permanent local-only contract. Until upstream owns an equivalent passive slice, this fork remains the source authority for the implementation and its additive migrations.

## Implementation anchors

- migrations `202` through `210` for `coordination_scope` and `coordination_receipt`;
- `server/pkg/db/queries/coordination.sql` and the generated sqlc types;
- coordination service, handler, middleware, CLI, and built-in skill wiring;
- typed deletion handles in `server/internal/service/coordination_delete.go` and guarded handler orchestration in `server/internal/handler/coordination_delete.go`;
- `scripts/test-work-coordination-db-required.sh` as the DB-required harness.

## Tests

The merged V1 source is covered by the coordination DB harness, migration lint, sqlc generation, focused Go tests, and CLI/API behavior tests. Deletion coverage includes the sealed phase/failure classification matrix, savepoint partial success, whole-batch fatal rollback, definite and unknown commit outcomes, lock/unlock terminalization, caller-side at-most-once `Finish` guards, duplicate/outside-target rejection, and post-`Finish` effect ordering. These are source-test claims, not runtime apply or live evidence.

## Rollback

The schema is additive and the down path drops the V1 tables and indexes in reverse order. The fork delta can retire once upstream owns the same passive coordination slice and the registry points to that upstream source.
