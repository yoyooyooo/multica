---
name: multica-work-coordination
description: "Use when working with passive coordination scopes, canonical blocked-by dependencies, request-hash receipts, or the V1–V2 coordination API and CLI surface."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Work Coordination

## Quick start

Use the coordination commands for passive scope and dependency facts:

```bash
multica coordination scope ensure --root <issue-ref> --workflow-profile <key> --idempotency-key <key>
multica coordination scope get --scope <uuid>
multica coordination scope get --root <issue-ref> --workflow-profile <key>

multica coordination dependency add \
  --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> \
  --expected-revision <n> --idempotency-key <key>
multica coordination dependency list --scope <uuid> [--cursor <opaque>] [--limit 1..100]
multica coordination dependency resolve \
  --scope <uuid> --dependency <uuid> \
  --expected-revision <n> --idempotency-key <key>
```

Prefer `--output json` for machine consumption. Use `--help` before writes. Treat `--expected-revision` and `--idempotency-key` as required mutation inputs; do not infer either from stale output.

## Core model

Work coordination is passive. A scope records root ownership, revision, server-stamped identity, canonical request hashes, and receipts. A dependency has one canonical direction:

```text
downstream blocked_by upstream
```

The response field `blocks_issue_id` is an alias of `downstream_issue_id`. It is not a second edge.

The Store does not own scheduling, wakeups, Issue status, assignee, comments, metadata, blocker evidence, Autopilot behavior, legacy dependency lifecycle, or Store cleanup.

Important consequences:

- scope ensure and dependency mutations are idempotent for the same canonical request;
- an exact-key replay returns the saved receipt and does not allocate another ordinal;
- a fresh-key duplicate add in the same owner scope returns `noop`, leaves revision unchanged, and allocates a new receipt ordinal;
- an unresolved pair owned by another scope returns `coordination_dependency_scope_conflict` and changes neither scope revision;
- replay revalidates current authority and referenced resources before returning saved data;
- member and task identity come from the server, not CLI-provided fields;
- Agent dependency mutations require both endpoints to share the scope's actual root and one endpoint to equal the current task issue; Agent list returns only active pairs containing that task issue;
- one unresolved downstream/upstream pair has one owner scope across the workspace;
- each scope may have at most 1,000 active dependencies; resolving a row removes it from that active count but retains its history;
- self edges and active cycles are rejected;
- resolve is monotonic and retains history;
- active-list cursors are opaque and revision-bound; restart from page one after a revision conflict;
- on a mutation revision conflict, read the scope again and retry from its current revision with a fresh idempotency key instead of looping the stale request;
- all dependency history, including resolved rows, blocks Issue/Batch/Workspace deletion;
- `coordination_dependency` is independent of legacy `issue_dependency`.

## Scope fields

- `id` - coordination scope UUID.
- `workspace_id` - workspace that owns the scope.
- `scope_kind` - `root` in V1–V2.
- `state` - `active` in V1–V2.
- `root_issue_id` - actual root issue UUID.
- `workflow_profile_key` - workflow profile identifier.
- `revision` - server-side CAS revision advanced by dependency state changes.
- `created_by` - nested server-stamped `actor_type`, `actor_id`, and nullable `task_id`.
- `created_at` / `updated_at` - RFC 3339 server timestamps.

`next_receipt_ordinal` is internal storage state and is not part of the API or CLI scope projection.

## Dependency fields

- `id` - dependency UUID.
- `workspace_id` / `coordination_scope_id` - server-owned placement.
- `downstream_issue_id` - blocked issue.
- `upstream_issue_id` - prerequisite issue.
- `blocks_issue_id` - explicit alias of `downstream_issue_id`.
- `created_by` / `created_at` - server-stamped provenance.
- `resolved_by` / `resolved_at` - nullable monotonic resolution provenance.

List returns active rows only plus `scope_revision` and nullable `next_cursor`. Add/resolve returns the dependency, resulting scope revision, receipt, and outcome (`created`, `resolved`, `noop`, or `replay`).

## Error handling

Expected coordination exits are:

- `3` - current authority denied;
- `4` - scope, issue, or dependency not found;
- `5` - invalid payload, self dependency, or cycle;
- `6` - exact-route capacity, revision, idempotency, owner-scope, or delete-blocked conflict.

Only exact method/route/status/code JSON envelopes receive product exits. Unknown fields, duplicate keys, trailing JSON, non-empty details, wrong content type, or wrong route/code combinations retain legacy handling.

## Receipts

Receipts are persisted server-side. They preserve one canonical request hash and saved result projection. They are audit facts, not an authority cache and not a scheduling signal.

## References

```text
references/work-coordination-source-map.md
```
