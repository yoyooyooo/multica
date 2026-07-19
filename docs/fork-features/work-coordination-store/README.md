# Work Coordination Store V1–V5

## Problem

The fork needs passive, root-scoped coordination facts that Agents can read and mutate without turning the Store into a scheduler or coupling it to legacy issue dependency behavior. V1 established scopes, canonical request hashes, receipts, exact task-credential revalidation, and deletion guards. V2 added canonical dependencies. V3 added strict typed blocker discovery and resolution facts. V4 closes the source read contract with one consistent inspection snapshot and bounded receipt-history pagination.

## Current source scope

Accepted source on the branch base covers:

- V1 root coordination scopes, request-hash receipts, exact member/task-token authority, and guarded deletion;
- V2 independent `coordination_dependency` storage and canonical `downstream blocked_by upstream` lifecycle;
- V3 strict schema-v1 blocker records, issue-only evidence refs, independent blocker/dependency resolution, API/CLI surfaces, and deletion guards;
- V4 consistent inspection, bounded safe receipt pagination, batched blocker evidence reads, strict API/CLI surfaces, and passive conformance;
- V5 a real CLI-process/router/PostgreSQL aggregate proof plus a DB-required named-pass gate for passive non-interference.

The accepted V4 source adds:

- public `CoordinationService.InspectScope` using one read-only repeatable-read transaction;
- one response containing the scope revision, every active owner-scope dependency, every open blocker, and a fixed page of 100 safe receipt refs;
- receipt ordering by `receipt_ordinal DESC` with an opaque workspace/scope/revision/collection/upper/last cursor;
- first-page committed ordinal upper bounds, so later no-op receipts cannot enter an older pagination window;
- revision-conflict invalidation for later pages after a fact mutation;
- strict `GET /api/coordination/scopes/{scopeId}/inspect` and `multica coordination inspect` JSON/table surfaces;
- batched blocker evidence-ref reads for list and inspect paths;
- consistent-snapshot, hard-bound, ordering, cursor, Agent authority, no-side-effect, router, CLI, and error-classifier conformance tests.

V4 needs no schema migration. Existing V1–V3 indexes cover active dependency order, open blocker order, evidence reads, and receipt ordinal windows.

The accepted V5 source slice adds one hermetic aggregate proof: a built `multica` CLI binary runs from independent A/B process homes through the real HTTP router into a real database, proving same-member replay, different-member idempotency conflict, independent blocker/dependency resolution, receipt ordinals, and exact Issue/task/Autopilot/legacy-dependency non-interference. The DB-required harness requires named pass evidence from this test. This is test and narrative hardening only; it adds no production route, schema, deployment tooling, or scheduling behavior.

The frozen ticket directory remains the delivery-contract baseline. Its preimplementation capsule is historical text; this narrative plus exact source, PR, CI, and acceptance evidence carry current status.

## Canonical model

A dependency has one meaning:

```text
downstream blocked_by upstream
```

A blocker is a separate immutable discovery fact with monotonic resolution provenance:

```text
kind=blocker
schema_version=1
status=open|resolved
reason_code=waiting_on_issue
resolution_code=null|no_longer_blocking|superseded
```

Its endpoints share the scope's actual root. Evidence is a bounded set of typed issue UUID references, not free text, URLs, comments, or arbitrary JSON. An optional `dependency_id` is only a validated link to an active dependency; resolving a blocker never resolves or mutates that dependency. Existing blocker evidence remains valid if that dependency is later resolved, while a later append or append replay cannot bind the now-resolved dependency.

An exact-key append replay returns the saved blocker only after current authority and referenced-resource validation. Reusing the same typed payload with a fresh idempotency key creates a distinct evidence record because V3 defines no blocker business identity for content-based deduplication.

The Store never reads or writes legacy `issue_dependency` for this model.

## Consistent inspection

`InspectScope` returns one bounded source snapshot:

- complete active dependencies in `(created_at ASC,id ASC)` order;
- complete open blockers in `(created_at DESC,id DESC)` order;
- at most 100 receipt refs in `receipt_ordinal DESC` order;
- one nullable next receipt cursor.

The service opens PostgreSQL with `REPEATABLE READ READ ONLY` before authority, scope, fact, and receipt reads. A mutation that commits while inspection is in flight is therefore visible either entirely before or entirely after the inspected snapshot; it cannot produce an old scope revision with new dependency or blocker rows. If storage somehow exceeds either 1,000-row active/open invariant, inspect returns `coordination_internal` and no partial graph.

The first receipt page captures the committed maximum ordinal. A later page is limited to that upper ordinal and the previous page's last ordinal. New no-op receipts do not advance scope revision, but their larger ordinals still cannot enter an older window. A real fact mutation advances revision and makes the cursor fail with `coordination_revision_conflict`.

Receipt refs expose only typed operation/resource identity, ordinal, before/after revision, safe actor type, and timestamp. Request hashes, result snapshots, idempotency keys, payloads, actor IDs, and unbounded history are not returned.

Inspect does not infer frontier, actionable work, terminal state, ownership transfer, wakeup, or scheduling.

## Authority and concurrency boundaries

Every operation derives workspace and actor identity from server authentication. Member operations revalidate current workspace membership. Agent mutations revalidate exact task credential, current task-to-Agent binding, task issue, scope root, and endpoint authority. Agent list remains endpoint-filtered. Agent inspect requires the current task issue's actual root to equal the scope root and then returns the complete owner-scope snapshot required for graph reasoning.

Mutations share the workspace advisory-lock key space with scope, dependency, blocker, and delete operations. Under that lock the service validates current authority and resources before checking receipts. A receipt is therefore not an authorization cache. Inspect is read-only and uses snapshot isolation instead of constructing a second coordination lock owner.

## API and CLI

Route:

```text
GET /api/coordination/scopes/{scopeId}/inspect?receipt_cursor=<opaque>
```

CLI:

```bash
multica coordination inspect --scope <uuid> [--receipt-cursor <opaque>] [--output json|table]
```

JSON preserves the scope revision, complete active/open fact arrays, receipt refs, and nullable next cursor. Table output is display-only. The CLI's strict ProductError classifier adds only the inspect-route `coordination_revision_conflict` 409 combination; other conflict codes on that route retain fail-closed legacy handling.

Mutation payload and resolution files remain strict JSON capped at 4,096 bytes. Unknown or duplicate fields, explicit nulls for required values, trailing JSON, unsupported codes, non-issue evidence, duplicate evidence, and more than 32 refs fail closed. A network retry of an unchanged mutation reuses the same idempotency key; a changed payload requires a fresh key.

## Passive boundary

V1–V5 do not alter Issue status, assignee, comments, metadata, task scheduling, Autopilot behavior, dependency lifecycle, or Store cleanup. They do not wake Agents or dispatch tasks. Blocker records and inspection results are evidence facts, not scheduling commands. Historical assisted workflow classification remains unchanged.

Deletion guards prevent dangling Store facts for scopes, dependencies, blocker endpoints, and evidence refs. Receipt history alone is not a deletion guard. V1–V5 provide no Store cleanup, archive, or retention operation. The guards prove database soft-reference safety only; they do not claim rollback of external effects.

Program scopes, goal contracts, leases, fencing, wake claims, Reconciler, Autopilot control, UI, and performance/SLO remain out of scope.

## Deployment and portability

V1–V5 source is accepted in this fork's `main`. V5 authority is squash merge `7137910838a90d4b29193151c42be54f79ef41cc`, whose sole parent is accepted V4 authority `c08e7c5dd2d662f7722406b10ebb7176f4e75bc8`; its tree `3d9db246539b83628c68604c7ecdb32d5795813e` equals reviewed head `fafc15094744ece51d141ff6fca605a5cb814a5e`. PR `#60`, final-head CI `29694807759`, and merged-main CI `29694915008` are successful.

V5 source acceptance does not authorize deployment. Mini migration apply, server/CLI replacement, process restart, runtime availability, and a two-client live tracer still require a fresh target-specific plan and explicit approval. `passive-live-evidence.md` is therefore intentionally absent until an approved deployment/tracer produces real evidence; source acceptance must not pre-populate a document that could be mistaken for live acceptance.

The capability is intended as a general upstream candidate. Until upstream owns an equivalent passive slice, this fork remains source authority for the additive storage and behavior.

## Implementation anchors

- migrations `202`–`210`: scopes and receipts;
- migrations `211`–`217`: canonical dependencies;
- migrations `218`–`230`: typed blockers and evidence refs;
- `server/pkg/db/queries/coordination.sql` and generated sqlc types;
- `server/internal/service/coordination_inspect.go`;
- `server/internal/handler/coordination_inspect.go` and router wiring;
- `server/cmd/multica/cmd_coordination_inspect.go`;
- built-in `multica-work-coordination` Skill and source map;
- typed deletion handles and guarded handler orchestration;
- `server/cmd/server/work_coordination_cli_e2e_test.go` for the real CLI-process/router/database aggregate flow;
- `scripts/test-work-coordination-db-required.sh`.

## Verification and claim limit

Accepted V4 includes deterministic repeatable-read barrier proof, 1,000/1,001 fact-bound checks, receipt ordinal/window pagination, Agent root/revocation checks, cross-scope isolation, no-side-effect snapshots, strict handler/router tests, CLI JSON/table/single-envelope tests, and exact inspect route/code classification. Existing V1–V3 migration, lifecycle, capacity, cycle, replay, deletion, race, and generated-code checks remain part of the aggregate gate.

Accepted V5 adds `TestWorkCoordinationCLIProcessesAggregatePassiveFlow` and mechanically requires its pass evidence in the DB-required harness. Its independent process homes prove process-local CLI state is not the source of replay behavior; canonical server identity remains authoritative.

Exact-head repository gates, independent review, CI, merge, and any later deployment must be recorded separately. Source acceptance cannot be presented as mini live availability. The maximum source-only claim is that the repository's passive Store aggregate has passed the recorded deterministic test, review, and CI gates.

## Rollback

V4 adds no schema. Application rollback restores the prior server and CLI while preserving V1–V3 schema, facts, and receipts. V3 and lower schema rollback rules remain separate and require writes to stop before destructive down migrations. Accepted V5 changes tests and narrative only, so it introduces no additional runtime rollback action.
