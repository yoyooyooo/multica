Status: ready-for-agent
Blocked by: [02 Establish the fork migration stream](02-establish-fork-migration-stream.md)

# Harden explicit External PR binding authority

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

12-17.

## What to build

Strengthen External PR as the fork-owned authority for AGS-to-Issue association. Persist the complete canonical AGS and merge-provider binding revision, verify all redundant Workspace and Issue labels against their UUID records, bind canonical URLs to the configured provider instance, and make natural identity plus payload revision immutable under retries and races.

The slice must accept an authoritative typed terminal fact durably and leave completion to reconciliation. It must not replace explicit association with title, body, or branch parsing.

## Acceptance criteria

- [ ] Typed callbacks require valid Workspace and Issue UUIDs that belong together, and supplied slug/key labels match those records exactly.
- [ ] Provider, canonical repository, provider repository, target instance, binding identity/revision, expected head/base, merge method, and fact revision are complete and persisted for authoritative terminal facts.
- [ ] External and merge URLs are canonical, credential-free, query/fragment-free, and bound to the configured instance and repository identities.
- [ ] The provider allowlist and authoritative confidence/completion-intent rules fail closed for typed terminal admission.
- [ ] An exact idempotency replay succeeds without duplicate facts, activities, or work; the same key with a changed payload returns conflict.
- [ ] One external natural identity cannot be rebound to another Issue, including concurrent first-bind races.
- [ ] A committed terminal fact always has durable reconcile work before the HTTP success response; process failure after admission cannot lose it.
- [ ] Shared service-token authentication is documented as service identity, not as proof that an individual provider fact is correct.

## Blocked by

- [02 Establish the fork migration stream](02-establish-fork-migration-stream.md)
