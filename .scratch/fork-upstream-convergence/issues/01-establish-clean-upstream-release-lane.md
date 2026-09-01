Status: ready-for-agent
Blocked by: None - can start immediately

# Rebuild from latest upstream with an additive release path

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

1-3, 6-11, 29-30, 33, and 36-38.

## What to build

Create a new generation in a clean worktree from the exact latest accepted upstream `main`. Reimplement only retained Fork contracts; treat prior Fork source as read-only donor evidence. Move target-specific build, Compose, storage, CLI activation, backup, and receipt behavior behind thin additive Fork tooling. Make the web production build offline with the smallest legally distributable font overlay.

Build each required architecture from the same accepted source SHA. A single multi-architecture manifest is optional. This ticket ends with exact-head CI and artifact/dry-run evidence; it does not switch production.

## Simplification guardrails

- Do not create a release platform, deployment daemon, environment abstraction, font pipeline, theme system, or visual redesign.
- Reuse existing registry, Compose, host, and build primitives behind thin scripts. Do not edit official upstream deployment files for target policy.
- Upstream contributions are optional follow-up and never block this ticket.

## Acceptance criteria

- [ ] The generation starts from a recorded latest-upstream SHA in a new clean worktree; no prior Fork squash is merged or cherry-picked.
- [ ] Every retained source change points to a current behavior contract; unrelated prior Fork changes are omitted.
- [ ] Target-specific release behavior is invoked from additive Fork tooling and official upstream deployment files remain replaceable.
- [ ] Exact-head CI passes before architecture artifacts are built.
- [ ] Every target artifact reads back the same accepted source SHA; registry packaging may be separate tags or one manifest.
- [ ] The production web build succeeds offline and every redistributed font has required license and copyright material.
- [ ] Receipts contain only allowlisted redacted data with owner-only permissions; raw container environments are not retained.
- [ ] A disposable or dry-run proof covers artifact selection, digest readback, and rollback inputs without modifying production.

## Blocked by

None - can start immediately.
