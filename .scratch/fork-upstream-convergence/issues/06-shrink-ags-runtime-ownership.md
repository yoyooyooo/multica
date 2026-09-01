Status: ready-for-agent
Blocked by: None - can start immediately
Track: optional sidecar; does not block the three-ticket clean rebuild DAG

# Shrink Multica ownership of AGS runtime policy

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

31 and 32.

## What to build

Move repository-operation authority, Access Grant routing, Git/GitHub shims, and platform Git identity policy to AGS bootstrap or runtime configuration. Simplify Multica execution identity to use the existing Task identity wherever sufficient, retaining only a narrowly proven claim or environment addition when AGS requires a distinct execution identifier.

The completed slice must preserve ordinary agent startup and the supported AGS workflow while removing Multica-owned provider policy and obsolete compatibility surfaces.

## Simplification guardrails

- Delete Multica-owned policy by default. Retain a compatibility route or field only when a current caller is identified and its removal condition is recorded.
- Do not replace the existing execution-context subsystem with a new subsystem. At most, retain one proven claim/environment value.

## Acceptance criteria

- [ ] AGS bootstrap or runtime configuration owns repository-provider policy and shim selection.
- [ ] Multica core no longer chooses Access Grant routing or platform Git identity on behalf of AGS.
- [ ] Ordinary Pi, Codex, and Claude task startup remains provider-neutral and does not require manual grant/session commands.
- [ ] Task ID is used as the execution correlation identity unless a failing contract test proves it insufficient.
- [ ] Any retained distinct execution identity is limited to claim and environment projection and has no parallel query/locking subsystem.
- [ ] Residual link-token and execution-context routes are removed in this generation when no current caller is found; any retained field names its current caller and removal gate. No compatibility shim or census subsystem is added.

## Blocked by

None - can start immediately.
