# Work Coordination Store V1–V3

## Problem

The fork needs passive, root-scoped coordination facts that Agents can read and mutate without turning the Store into a scheduler or coupling it to legacy issue dependency behavior. V1 established scopes, canonical request hashes, receipts, exact task-credential revalidation, and deletion guards. V2 added canonical dependencies. V3 adds strict typed blocker discovery, evidence-reference, and resolution facts.

## Current source scope

Accepted source on the branch base covers:

- V1 root coordination scopes, request-hash receipts, exact member/task-token authority, and guarded deletion;
- V2 independent `coordination_dependency` storage and canonical `downstream blocked_by upstream` lifecycle;
- V2 API, CLI, built-in Skill, cycle/capacity controls, immutable replay, and revision-bound pagination.

The V3 source candidate adds:

- additive `coordination_record` and `coordination_record_issue_ref` storage without foreign keys or cascades;
- schema-v1 blocker append, list, and monotonic resolve operations;
- fixed reason `waiting_on_issue` and resolution codes `no_longer_blocking|superseded`;
- sorted, unique, issue-only create and resolution evidence references;
- optional validated linkage to an active canonical dependency with the same scope and exact endpoints;
- scope revision CAS, canonical request hashes, immutable receipt replay, a 1,000-open-blocker cap with retained resolved history, and status/revision-bound opaque cursors;
- member and exact task-token Agent authority across append, list, resolve, and replay;
- API, CLI, built-in Skill, and Issue/Batch/Workspace deletion guards for all blocker history and evidence refs.

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

The Store never reads or writes legacy `issue_dependency` for this model. V3 does not reinterpret V2 dependency state.

## Authority and concurrency boundaries

Every operation derives workspace and actor identity from server authentication. Member operations revalidate current workspace membership. Agent operations revalidate the exact task credential, current task-to-Agent binding, current task issue, and scope root. Agent mutations require the current task issue to be one blocker endpoint; Agent list returns only blockers containing that issue.

Mutations share the workspace advisory-lock key space with scope, dependency, blocker, and delete operations. Under that lock the service validates current authority and resources before checking receipts. A receipt is therefore not an authorization cache. Receipt misses continue with scope and row locking, expected-revision CAS, capacity checks, writes, revision advance, and receipt persistence in one transaction.

Blocker list cursors bind scope id, current scope revision, status filter, creation timestamp, and record id. Any revision or status-filter change invalidates an older cursor rather than returning a mixed page.

## API and CLI

Routes:

```text
POST /api/coordination/scopes/{scopeId}/blockers
GET  /api/coordination/scopes/{scopeId}/blockers
POST /api/coordination/scopes/{scopeId}/blockers/{recordId}/resolve
```

CLI:

```bash
multica coordination blocker add \
  --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> \
  [--dependency <uuid>] --payload-file <strict-json> \
  --expected-revision <n> --idempotency-key <key>
multica coordination blocker list \
  --scope <uuid> [--status open|resolved|all] [--cursor <opaque>] [--limit 1..100]
multica coordination blocker resolve \
  --scope <uuid> --blocker <uuid> --resolution-file <strict-json> \
  --expected-revision <n> --idempotency-key <key>
```

Payload and resolution files are strict JSON capped at 4,096 bytes. Unknown or duplicate fields, explicit nulls for required values, trailing JSON, unsupported codes, non-issue evidence, duplicate evidence, and more than 32 refs fail closed. Use `--output json` for machine consumption.

## Passive boundary

V1–V3 do not alter Issue status, assignee, comments, metadata, task scheduling, Autopilot behavior, dependency lifecycle, or Store cleanup. They do not wake Agents or dispatch tasks. Blocker records are evidence facts, not scheduling commands. Historical assisted workflow classification remains unchanged.

Deletion guards prevent dangling Store facts for scopes, dependencies, blocker endpoints, and evidence refs. They prove only database soft-reference safety; they do not claim rollback of external effects.

## Deployment and portability

V1 and V2 are accepted in this fork's `main`. V3 remains a source candidate until its exact head passes local gates, independent review, PR CI, merge, merged-main CI, and acceptance. This narrative does not claim mini deployment, migration apply, process restart, runtime availability, or live tracer acceptance.

The capability is intended as a general upstream candidate. Until upstream owns an equivalent passive slice, this fork remains source authority for the additive migrations and behavior.

## Implementation anchors

- migrations `202`–`210`: scopes and receipts;
- migrations `211`–`217`: canonical dependencies;
- migrations `218`–`230`: typed blockers and evidence refs;
- `server/pkg/db/queries/coordination.sql` and generated sqlc types;
- coordination service, handler, router, CLI, and built-in Skill wiring;
- typed deletion handles and guarded handler orchestration;
- `scripts/test-work-coordination-db-required.sh`.

## Verification and claim limit

The source candidate includes migration up/down/up tests, DB-backed lifecycle/canonical-hash/authority/pagination/capacity tests, strict wire tests, CLI file/request/error/output tests, deletion endpoint tests, sqlc generation checks, and the DB-required harness. Exact-head repository gates, independent review, CI, merge, and deployment must be recorded separately and are not claimed here.

## Rollback

The schema is additive. V3 down migrations remove blocker constraints, indexes, evidence-ref storage, and record storage in reverse order without touching V2 dependencies or legacy `issue_dependency`. Runtime rollback must stop V3 writes before schema rollback. V2 and V1 rollback remain separate lower layers.
