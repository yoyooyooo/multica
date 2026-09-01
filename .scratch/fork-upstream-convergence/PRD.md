Status: ready-for-agent

# Fork Upstream Convergence Program

## Problem Statement

Maintaining the Multica fork across upstream generations currently costs too much time and carries too much delivery risk. Fork behavior is spread across upstream-owned handlers, migrations, frontend surfaces, official deployment files, CI workflows, runtime launch code, and target-specific operational scripts. Each upstream refresh therefore requires broad conflict resolution and repeated proof that live behavior and data were not lost.

Some fork capabilities now look similar to upstream features but still have materially different authority semantics. External PR is the clearest example: upstream associates pull requests with Issues by parsing human-readable Issue identifiers from PR titles, bodies, and branches, while the fork receives an explicit Workspace UUID, Issue UUID, external PR identity, completion intent, and idempotency revision from a trusted control plane. Replacing that explicit association with text inference would reduce correctness even if it removed fork-owned tables.

The project needs a new generation strategy that starts from current upstream, preserves only proven fork product contracts, moves target-specific operations out of upstream-owned surfaces, and establishes stable ownership boundaries that can be re-derived and retested during future upstream refreshes.

## Solution

Create a converged fork generation from current upstream with four ownership boundaries:

1. The fork owns explicit External PR association facts, AGS correlation, durable terminal reconciliation, fork-only completion policy, and live-data compatibility.
2. Upstream owns generic pull request metadata, provider adapters, CI status aggregation, shared pull request response shapes, and the common Issue pull request presentation.
3. Fork deployment and release behavior lives in additive deployment tooling; every target artifact comes from one accepted source revision.
4. Future fork schema changes use the smallest fork-owned sequential migration stream and ledger needed to avoid upstream number collisions, with the accepted 2026-08-30 generation as the minimum direct upgrade source.

External PR remains an authoritative control-plane capability while native and External pull requests converge on one user-facing pull request surface. Explicit associations take precedence over text-inferred associations. A conflict between explicit and inferred associations fails closed for completion and remains observable for repair.

Generic fixes that benefit every deployment are submitted upstream or carried only as small, named, temporary patches. Target-specific CLI installation, Compose overrides, offline build support, receipts, backup logic, and host deployment behavior no longer modify upstream-owned product or official deployment files.

## Clean Rebuild Constraints

- Implementation starts in a new worktree at the exact latest accepted upstream `main`; the current fork branch and prior squash are read-only donor evidence.
- The target has one owner and one write path per fact, one user-facing PR surface, and the existing completion path with only a narrow ownership correction. A new abstraction is admitted only when it removes more fork/upstream coupling than it adds.
- Compatibility code requires a named present obligation: live data, a still-used wire contract, or an installed client that cannot be upgraded atomically. Every admitted shim has an owner, removal condition, and bounded lifetime.
- No speculative fallback, permanent dual-write, permanent shadow reader, generic provider framework, or second lifecycle kernel is allowed. Shadow comparison is a finite migration check and is deleted before acceptance.
- The nine non-strict live links receive one-time normalize-or-preserve decisions. The plan must not create an archive service, archive UI, or general repair workflow for nine rows.
- Preparing an upstream contribution is never on the critical path for the fork generation. The fork carries a minimal named patch until upstream release actually supersedes it.
- Proof stops when the External PR contract and upgrade/deployment acceptance paths cover the real behavior contracts, all live-data classes are accounted for, and exact-head CI passes. Pi and AGS ownership changes use their own narrow tests and do not block the rebuild.
- When a requirement cannot justify its source delta, migration burden, test seam, and future sync cost, the default disposition is deletion or omission.

## User Stories

1. As a fork maintainer, I want every refresh to begin from current upstream, so that obsolete fork implementation is not mechanically replayed.
2. As a fork maintainer, I want every fork capability classified by behavior contract and owner, so that refresh decisions are evidence-based.
3. As a fork maintainer, I want little or no fork code in high-churn upstream files, so that routine upstream changes do not create broad conflicts.
4. As a fork maintainer, I want an explicit upgrade floor, so that the active branch does not retain every historical topology forever.
5. As a fork maintainer, I want future fork migrations in a separate stream and ledger, so that upstream migration numbers cannot collide with fork versions.
6. As an operator, I want Mini and imile-win to run artifacts built from the same accepted source revision, so that target differences cannot conceal source drift.
7. As an operator, I want every architecture-specific artifact built from the same exact accepted SHA, so that a registry packaging choice cannot create source drift.
8. As an operator, I want source acceptance and exact-head CI before final artifact builds, so that stale verification cannot be reused after a source change.
9. As an operator, I want deployment receipts to be redacted and owner-readable, so that verification evidence does not expose runtime secrets.
10. As an operator, I want each target backed up, deployed, verified, and rolled back independently, so that one target failure does not force a coupled outage.
11. As an operator, I want target-specific Compose and storage behavior in additive fork overlays, so that official upstream deployment files remain replaceable.
12. As an AGS integration, I want to submit the exact Workspace and Issue UUID for an External PR, so that association does not depend on PR prose or branch names.
13. As an AGS integration, I want an external PR natural identity to be immutable once bound, so that retries and races cannot silently move it to another Issue.
14. As an AGS integration, I want idempotency keys bound to canonical payload revisions, so that exact replay succeeds and changed replay fails closed.
15. As an AGS integration, I want terminal provider facts durably accepted before asynchronous completion, so that a crash cannot lose a successful merge.

16. As an AGS integration, I want AGS PR identity and Forgejo merge evidence preserved in the existing authoritative fact, so that source work and merge outcome remain auditable without a second provider model.
17. As an AGS integration, I want the complete canonical callback envelope validated and included in the idempotency hash without turning every request field into permanent product schema.
18. As a user, I want native and External pull requests shown in one Issue surface, so that provider internals do not create duplicate product sections.
19. As a user, I want ordinary upstream PR behavior left unchanged when no External PR fact exists, so that the clean rebuild does not redesign upstream association semantics.
20. As a user, I want External completion to use only its explicit UUID binding, so that incidental PR prose cannot become External authority.
21. As a user, I want any ambiguity encountered during bounded migration validation to fail closed, so that migration tooling cannot silently finish the wrong Issue.
22. As a user, I want conflict diagnostics limited to tests and operator output, so that a one-time convergence concern does not become permanent product UI.
23. As a user, I want merged authoritative External PRs to respect `record_only`, so that observation-only Issues are never completed automatically.
24. As a user, I want PR completion limited to eligible leaf child Issues, so that parent containers and non-leaf work are not closed accidentally.
25. As a user, I want parent and stage continuation to survive server crashes, so that durable work resumes without manual repair.
26. As a maintainer, I want native PR completion independent from External PR finalization storage, so that generic upstream behavior does not depend on fork tables.
27. As a runtime maintainer, I want Pi cancellation to terminate the entire process group, so that descendants cannot survive after the leader exits.
28. As a runtime maintainer, I want the process-group fix expressed as a small upstream-oriented patch, so that it can eventually disappear from the fork.
29. As a builder, I want web assets to build without network font downloads, so that release builds are deterministic in restricted environments.
30. As a compliance owner, I want vendored fonts to include distributable license and copyright material, so that offline builds remain legally auditable.
31. As an AGS operator, I want repository authority, Git shims, and platform identity configured by AGS bootstrap, so that Multica runtime code does not own provider policy.
32. As an agent runtime, I want to use the existing Task identity when sufficient, so that a parallel execution-context subsystem is not maintained for one environment value.
33. As a fork maintainer, I want broadly useful fixes kept as small named patches with optional upstream follow-up, so that upstream review timing cannot block the rebuild.
34. As a database operator, I want fresh install, accepted-floor upgrade, and interrupted-retry migration proofs, so that schema convergence is verified against realistic states.
35. As a fork maintainer, I want every retained capability represented by contract tests and every retired capability represented by an explicit disposition, so that future refreshes can be reconstructed from intent.
36. As a fork maintainer, I want compatibility admitted only for a current named obligation, so that speculative transition code does not become permanent architecture.
37. As a fork maintainer, I want finite migration checks removed after cutover, so that shadow and dual-path machinery does not survive its purpose.
38. As a fork maintainer, I want work to stop once the External PR contract, live-data disposition, upgrade/rollback path, and exact-head CI pass, so that diminishing-return proof does not delay the clean rebuild.

## Implementation Decisions

- Current upstream is the implementation baseline. Prior fork code is donor evidence, not a merge source. Capabilities are reimplemented from behavior contracts and live-data obligations.
- External PR remains a fork-owned authoritative fact model. Its natural identity is Workspace, provider, repository, and PR number; its Issue association uses explicit UUIDs and cannot be silently rebound.
- AGS PR identity and Forgejo merge evidence remain in the existing External PR authority shape. The full canonical callback envelope is validated and included in the immutable payload hash, but fields that have no runtime query or recovery consumer are not promoted into permanent columns.
- The callback contract verifies that supplied Workspace slug and Issue key agree with the authoritative UUID records. Redundant display identifiers are either verified or removed from the authority envelope.
- Typed terminal callbacks require a configured provider allowlist, canonical body idempotency key, canonical URLs bound to the configured instance, authoritative confidence, explicit completion intent, and a complete projection fact set.
- A shared service token authenticates the AGS service but is not treated as per-event proof. Repository binding and immutable fact revision provide the narrower event authority.
- External PR terminal admission and durable reconciliation remain. The terminal fact transaction commits before completion work and exact replay nudges unfinished work without duplicating side effects.
- The existing Issue pull request response and UI are reused to render External rows in one section. No association-provenance taxonomy, permanent conflict record, repair console, shadow API, or second provider model is added.
- External PR completion trusts only its explicit binding. Native upstream association remains unchanged and is not enabled as a second authority for AGS-managed PRs in this generation.
- Completion uses the existing completion path with a narrow ownership correction: native calls without External work do not create External finalization state; External reconcile retains its durable work and continuation.
- Completion preserves exact policies for `record_only`, supported leaf-child completion, non-leaf rejection, open authoritative PR blocking, merged completion intent, terminal Issue protection, parent wake, stage continuation, idempotency, and crash recovery.
- Current execution context reuses upstream Task, Workspace, Agent, Issue, Runtime, trigger, and claim facts. Task ID is preferred; one small claim/environment addition is allowed only if AGS proves a distinct execution identity is required.

- Repository authority, Access Grant routing, Git/GitHub shims, and platform Git identity move to AGS bootstrap or runtime configuration. Multica does not own provider-operation policy.
- Pi process cancellation is reimplemented as a narrow process-group patch with bounded TERM wait, process-group liveness polling, and SIGKILL escalation even when the leader exits first.
- Offline web builds use a local-font or build-overlay mechanism that does not require network access. Any redistributed font bundle includes its required licenses and copyright notices.
- Generic fixes for deterministic Git diagnostics, child environment duplicate-key replacement, process-group cancellation, CI path gating, and offline font behavior are carried as minimal named patches when required. Preparing or merging upstream contributions is optional follow-up.
- Fork deployment behavior lives in additive tooling. Official upstream Compose, Dockerfiles, Make targets, and branch filters are not used as target configuration stores.
- Each architecture-specific artifact is built from the same accepted source revision. A multi-architecture manifest may be published when convenient, but registry packaging is not an acceptance requirement. A source change invalidates exact-head CI, build directories, tags, and deployment receipts.
- Mini and imile-win are separate deployment transactions with their own backups, schema readback, image digest readback, runtime revision readback, rollback boundary, and redacted evidence.
- Raw container inspection output is not retained as evidence. Receipt directories and files are owner-only and contain allowlisted projections.
- Future fork migrations use a fork-owned directory and ledger. Deployment applies upstream migrations first and fork migrations second.
- Previously applied full migration basenames and live ledger rows are immutable. The accepted 2026-08-30 generation is the minimum direct upgrade source; older forks first upgrade through that generation.
- Migration proof covers a fresh upstream database, a sanitized accepted-floor schema with live External PR facts, and interrupted convergence retry. The sequential runner and small fork ledger are part of deployment, not an independent migration platform.
- The 240 observed live External PR associations keep their explicit facts. The 231 strict rows require no product migration beyond read compatibility; the remaining nine are normalized once when authority is recoverable or preserved read-only. No archive subsystem is created.
- The convergence program uses exact-head CI and contract evidence before deployment. Runtime deployments remain unchanged until an implementation generation is accepted and explicitly authorized.
- Transitional reads exist only in tests or a bounded operator-run comparison. The accepted runtime has no permanent old/new dual-write, shadow processor, fallback association path, or conflict product.
- The nine non-strict links are normalized when authority can be recovered; otherwise their existing explicit facts are preserved read-only. No new archive subsystem is created.
- Upstream pull requests are optional follow-up work. Fork acceptance depends on local minimal patches and tests, not on upstream review timing.
- Scope is complete when the External PR contract, 240-row disposition, accepted-floor upgrade/rollback, and exact-head CI pass. Pi and AGS sidecars have independent acceptance and do not block deployment.

## Testing Decisions

- Tests assert externally observable contracts rather than private helper structure. Database-backed handler tests, real migration runners, real subprocess groups, and digest/runtime readback are preferred to mocks at ownership boundaries.
- **External PR contract seam:** exercise typed callback admission through explicit Workspace/Issue binding, immutable natural identity, exact replay, changed-payload conflict, terminal fact durability, completion policy, parent/stage continuation, deletion, and crash recovery using the real handler and PostgreSQL. Do not add provenance or text-inference test matrices beyond proving External authority does not parse PR prose.
- **Upgrade and deployment seam:** apply upstream then the small fork migration stream to a fresh schema and a sanitized accepted-floor schema, account for all 240 live rows, retry one interrupted migration, and verify independent backup/digest/runtime/rollback readback on both targets. Architecture artifacts must share one exact SHA; a single manifest is optional.
- Pi cancellation and AGS ownership use independent narrow sidecar tests. They are not prerequisites for External PR data migration or target deployment.
- Narrow tests remain for the additive deployment path, CLI version admission, offline font build/license presence, and any minimal generic patch actually carried by the fork.
- Existing upstream tests remain regression prior art. Add a case only for a retained Fork contract, observed live-data shape, or reproduced failure; do not duplicate upstream provider matrices.
- Do not build exhaustive historical, provider, or concurrency matrices after the accepted seams cover the observed classes. Add a case only for a distinct authority boundary, live-data shape, or reproduced failure.

## Active Ticket DAG

```text
01 clean upstream rebuild and additive release path
  -> 03 minimal External PR authority slice
    -> 10 accepted-floor upgrade and two independent target deployments
```

Issues 06 and 07 are optional sidecars and do not block the DAG. Issues 02, 04,
05, 08, and 09 are `wontfix`: their minimal required work was merged into 01,
03, or 10, while their migration-platform, provenance/conflict, generic
completion, font-pipeline, and shadow/archive scope was deleted.

## Out of Scope

- Replacing explicit External PR association with PR title, body, or branch parsing.
- Removing External PR source facts, idempotency receipts, or durable reconciliation without a proven simpler implementation of the same current contract.
- Treating the existing nine non-strict live links as strict mappings without authoritative provider lookup; when lookup cannot recover authority, preserve the existing row read-only without building archive behavior.
- A big-bang production database rewrite or destructive cleanup without accepted backups and readback.
- Replaying the prior fork squash or preserving old implementation solely because it existed in a previous generation.
- Fork-specific modifications to official upstream deployment files when an additive overlay can express the requirement.
- Building separate source revisions or architecture-specific source trees for Mini and imile-win.
- Restoring retired Workload Assertion, delegated Session/merge, legacy gateway, or repository-authority surfaces.
- Adding a broad execution-context query and locking subsystem for one environment variable.
- Claiming an upstream contribution is complete before it is accepted and released upstream.
- Permanent dual-write, shadow traffic, fallback reads, compatibility routers, generic archive/repair services, or provider-neutral frameworks created only for this convergence.
- Preserving an old route, field, table, worker, fixture, or UI branch without a named current consumer or live-data obligation.
- Expanding the proof matrix after the External PR contract, 240-row disposition, accepted-floor upgrade/rollback, and exact-head CI pass without a concrete escaped defect.
- Deploying, migrating production data, merging the planning pull request, or rotating credentials as part of this specification publication.

## Further Notes

- The analysis baseline was refreshed to upstream `main` revision `1dd6b9ecdbaa991bfd51b48ef5c269056045d547`. The relevant text-inference implementation is unchanged from the previously frozen baseline.
- The accepted running fork source remains `ad25aa66bb3ab9b8c55f0cc2825523c0e72c0be7`; this planning work does not change either target runtime.
- Read-only production census found 240 live External PR associations: 178 on Mini and 62 on imile-win. Of these, 231 have strict mapping facts and nine need bounded normalization or archive decisions.
- The planning pull request and convergence blueprint are proposal authority only. Implementation must use new isolated worktrees, fast-forward-only integration, exact-head evidence, and explicit deployment authorization.
- The `mini-sub2api-xai/grok-4.6:xhigh` review removed the provenance/conflict product, canonical-envelope columns, permanent shadow/archive work, generic completion rewrite, mandatory single OCI manifest, and 10-ticket critical path. The active implementation DAG is three tickets; Pi and AGS ownership are independent sidecars.
- Success means the next upstream refresh can reconstruct the fork from stable product contracts, fork-owned data authority, additive operations, and bounded patches without rediscovering hidden behavior in a broad diff.
