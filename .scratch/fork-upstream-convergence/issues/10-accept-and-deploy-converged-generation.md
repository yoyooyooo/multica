Status: ready-for-agent
Blocked by: [01 Release lane](01-establish-clean-upstream-release-lane.md), [02 Migration stream](02-establish-fork-migration-stream.md), [03 External PR authority](03-harden-external-pr-binding-authority.md), [04 Unified PR projection](04-unify-pr-projection-and-conflicts.md), [05 Completion continuation](05-decouple-pr-completion-and-continuation.md), [06 AGS runtime ownership](06-shrink-ags-runtime-ownership.md), [07 Pi cancellation](07-correct-pi-process-group-cancellation.md), [08 Offline web build](08-build-web-offline-with-licensed-fonts.md), [09 Live External PR convergence](09-shadow-converge-live-external-prs.md)

# Accept and deploy the converged generation to Mini and imile-win

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

6-10 and 35.

## What to build

Accept one exact converged source revision, promote its single arm64/amd64 OCI manifest, and deploy it to Mini and imile-win as two independent production transactions. Each target receives its own backup, migration preflight, schema and image digest readback, runtime revision proof, smoke checks, rollback boundary, and owner-only redacted receipt.

This issue owns production authorization and rollout evidence. It must not begin from partial dependency completion or rebuild different source for either target.

## Simplification guardrails

- Use the established release scripts and two explicit target transactions. Do not build a rollout controller, fleet scheduler, or generalized environment manager.
- Run only acceptance checks tied to the four agreed seams and observed production classes. Stop after both targets pass readback; optional hardening and upstream follow-ups remain separate work.

## Acceptance criteria

- [ ] Every blocking issue is resolved with evidence and no unresolved stop rule or External PR disposition remains.
- [ ] The accepted source tree is clean, exact-head CI is green at that revision, and the promoted manifest digest was built only after acceptance.
- [ ] The manifest contains verified arm64 and amd64 descriptors from the same source revision.
- [ ] Mini and imile-win each receive an independent database backup and target-specific preflight before any switch.
- [ ] Upstream migrations run before fork migrations and post-migration ledger/schema readback matches the accepted plan.
- [ ] Each target reads back the expected immutable image digest and exact runtime source revision.
- [ ] Authenticated smoke checks cover unified PR reads, an authoritative External PR flow, completion-policy non-regression, uploads/storage continuity, and daemon health where applicable.
- [ ] Pi process-group cancellation is proven on a supported live or production-equivalent runtime before claiming the behavior deployed.
- [ ] A failure on one target leaves the other target's transaction and rollback authority intact.
- [ ] Receipts are owner-only, secret-safe, and include explicit database/image rollback boundaries without raw inspect output.
- [ ] The previous accepted images, binaries, database backups, and deployment evidence remain available for rollback.

## Blocked by

- [01 Establish the clean-upstream exact-SHA release lane](01-establish-clean-upstream-release-lane.md)
- [02 Establish the fork migration stream and accepted upgrade floor](02-establish-fork-migration-stream.md)
- [03 Harden explicit External PR binding authority](03-harden-external-pr-binding-authority.md)
- [04 Unify pull request projection and association conflict handling](04-unify-pr-projection-and-conflicts.md)
- [05 Decouple pull request completion and prove durable continuation](05-decouple-pr-completion-and-continuation.md)
- [06 Shrink Multica ownership of AGS runtime policy](06-shrink-ags-runtime-ownership.md)
- [07 Correct Pi process-group cancellation](07-correct-pi-process-group-cancellation.md)
- [08 Build the web application offline with licensed fonts](08-build-web-offline-with-licensed-fonts.md)
- [09 Shadow-converge every live External PR association](09-shadow-converge-live-external-prs.md)
