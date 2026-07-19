---
name: multica-work-coordination
description: "Use when working with coordination scope ensure/get flows, request-hash receipts, or the passive V1 coordination API and CLI surface."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Work Coordination

## Quick start

Use the coordination scope commands for passive scope and receipt reads:

```bash
multica coordination scope ensure --root <issue-ref> --workflow-profile <key> --idempotency-key <key>
multica coordination scope get --scope <uuid>
multica coordination scope get --root <issue-ref> --workflow-profile <key>
```

Prefer `--output json` for machine consumption. Use `--help` before writes.

## Core model

Work coordination scope is passive. It records a root-scoped coordination fact, a server-stamped receipt, and a canonical request hash. It does not own scheduling, dependency fan-out, blocker evidence, or lifecycle cleanup.

Important consequences:

- scope ensure is idempotent for the same canonical request;
- replay is only valid after current authority revalidation;
- member and task identity come from the server, not from CLI-provided fields;
- the CLI only speaks to the coordination endpoints that exist in the source tree;
- V1 conflict exit 6 is route-scoped: scope ensure accepts only idempotency conflict, while guarded Issue/Batch/Workspace deletes accept only delete-blocked; future-slice conflicts remain legacy exit 1 until their routes ship.

## Scope fields

- `id` - coordination scope UUID.
- `workspace_id` - workspace that owns the scope.
- `scope_kind` - `root` in V1.
- `state` - `active` in V1.
- `root_issue_id` - actual root issue UUID.
- `workflow_profile_key` - workflow profile identifier.
- `revision` - server-side revision counter.
- `created_by` - nested server-stamped `actor_type`, `actor_id`, and nullable `task_id`.
- `created_at` / `updated_at` - RFC 3339 server timestamps.

`next_receipt_ordinal` is internal storage state and is not part of the API or CLI scope projection.

## Receipts

Receipts are persisted server-side and return the saved scope result together with a receipt ordinal. They are not a cache for authority, and they must be revalidated before replay.

## References

```text
references/work-coordination-source-map.md
```
