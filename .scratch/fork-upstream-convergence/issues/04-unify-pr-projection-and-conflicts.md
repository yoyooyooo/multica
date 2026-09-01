Status: ready-for-agent
Blocked by: [03 Harden explicit External PR binding authority](03-harden-external-pr-binding-authority.md)

# Unify pull request projection and association conflict handling

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

18-22.

## What to build

Create one user-facing pull request projection for native GitHub/VCS metadata and fork-owned External PR authority. Preserve AGS and merge-provider identity, expose association provenance, and apply deterministic precedence: explicit External binding wins over title, branch, closing-keyword, and body-reference inference.

When explicit and inferred sources point the same provider PR at different Issues, persist an observable conflict, exclude the ambiguous association from automatic completion, and give operators enough identity to repair it. Replace duplicate Issue PR sections with one shared surface without moving External write authority into the UI or native webhook parser.

## Acceptance criteria

- [ ] One Issue pull request API response can represent native, External, and AGS-to-Forgejo projected PRs without losing source identity.
- [ ] Association provenance distinguishes explicit External binding, title inference, branch inference, closing-keyword inference, and body-only reference.
- [ ] Explicit binding has deterministic precedence over every text-derived association for the same provider PR identity.
- [ ] Conflicting explicit and inferred Issue associations are persisted or otherwise durably observable and cannot carry completion authority.
- [ ] Body-only references remain hidden and non-blocking; ordinary native title/branch workflows retain their upstream behavior when no explicit binding exists.
- [ ] The Issue UI renders one coherent pull request section with provider, state, checks, merge projection, confidence, and conflict state where applicable.
- [ ] Realtime pull request updates invalidate or refresh the unified server-state query without duplicating state in client-owned stores.
- [ ] API compatibility parsing and malformed-response tests protect installed clients from projection evolution.
- [ ] Database-backed tests include PR title, body, branch, code-example, and cross-Workspace lookalikes that must not override explicit authority.

## Blocked by

- [03 Harden explicit External PR binding authority](03-harden-external-pr-binding-authority.md)
