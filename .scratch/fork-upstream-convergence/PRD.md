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
3. Fork deployment and release behavior lives in additive deployment tooling and produces one immutable multi-architecture artifact from one accepted source revision.
4. Future fork schema changes use a fork-owned migration stream and ledger, with the accepted 2026-08-30 generation as the minimum direct upgrade source.

External PR remains an authoritative control-plane capability while native and External pull requests converge on one user-facing pull request surface. Explicit associations take precedence over text-inferred associations. A conflict between explicit and inferred associations fails closed for completion and remains observable for repair.

Generic fixes that benefit every deployment are submitted upstream or carried only as small, named, temporary patches. Target-specific CLI installation, Compose overrides, offline build support, receipts, backup logic, and host deployment behavior no longer modify upstream-owned product or official deployment files.

## Clean Rebuild Constraints

- Implementation starts in a new worktree at the exact latest accepted upstream `main`; the current fork branch and prior squash are read-only donor evidence.
- The target has one owner and one write path per fact, one user-facing PR surface, and one generic completion evaluator. A new abstraction is admitted only when it removes more fork/upstream coupling than it adds.
- Compatibility code requires a named present obligation: live data, a still-used wire contract, or an installed client that cannot be upgraded atomically. Every admitted shim has an owner, removal condition, and bounded lifetime.
- No speculative fallback, permanent dual-write, permanent shadow reader, generic provider framework, or second lifecycle kernel is allowed. Shadow comparison is a finite migration check and is deleted before acceptance.
- The nine non-strict live links receive one-time normalize-or-preserve decisions. The plan must not create an archive service, archive UI, or general repair workflow for nine rows.
- Preparing an upstream contribution is never on the critical path for the fork generation. The fork carries a minimal named patch until upstream release actually supersedes it.
- Proof stops when the four accepted seams cover the real behavior contracts, all live-data classes are accounted for, and exact-head CI passes. Additional hypothetical matrices require a concrete escaped defect or production risk.
- When a requirement cannot justify its source delta, migration burden, test seam, and future sync cost, the default disposition is deletion or omission.

## User Stories

1. As a fork maintainer, I want every refresh to begin from current upstream, so that obsolete fork implementation is not mechanically replayed.
2. As a fork maintainer, I want every fork capability classified by behavior contract and owner, so that refresh decisions are evidence-based.
3. As a fork maintainer, I want little or no fork code in high-churn upstream files, so that routine upstream changes do not create broad conflicts.
4. As a fork maintainer, I want an explicit upgrade floor, so that the active branch does not retain every historical topology forever.
5. As a fork maintainer, I want future fork migrations in a separate stream and ledger, so that upstream migration numbers cannot collide with fork versions.
6. As an operator, I want Mini and imile-win to run artifacts built from the same accepted source revision, so that target differences cannot conceal source drift.
7. As an operator, I want one immutable arm64/amd64 OCI manifest, so that each host pulls the correct architecture without rebuilding source.
8. As an operator, I want source acceptance and exact-head CI before final artifact builds, so that stale verification cannot be reused after a source change.
9. As an operator, I want deployment receipts to be redacted and owner-readable, so that verification evidence does not expose runtime secrets.
10. As an operator, I want each target backed up, deployed, verified, and rolled back independently, so that one target failure does not force a coupled outage.
11. As an operator, I want target-specific Compose and storage behavior in additive fork overlays, so that official upstream deployment files remain replaceable.
12. As an AGS integration, I want to submit the exact Workspace and Issue UUID for an External PR, so that association does not depend on PR prose or branch names.
13. As an AGS integration, I want an external PR natural identity to be immutable once bound, so that retries and races cannot silently move it to another Issue.
14. As an AGS integration, I want idempotency keys bound to canonical payload revisions, so that exact replay succeeds and changed replay fails closed.
15. As an AGS integration, I want terminal provider facts durably accepted before asynchronous completion, so that a crash cannot lose a successful merge.

16. As an AGS integration, I want AGS PR identity and Forgejo merge identity preserved separately, so that source work and merge evidence remain auditable.
17. As an AGS integration, I want canonical repository, binding revision, head/base, merge method, and fact revision persisted, so that authoritative claims can be revalidated.
18. As a user, I want native and External pull requests shown in one Issue surface, so that provider internals do not create duplicate product sections.
19. As a user, I want association provenance retained, so that explicit binding, title, branch, closing keyword, and body reference are distinguishable.
20. As a user, I want body-only Issue mentions to remain non-blocking references, so that incidental prose cannot affect active work.
21. As a user, I want title or branch text collisions prevented from overriding an explicit binding, so that unrelated PRs do not appear authoritative.
22. As a user, I want explicit-versus-inferred conflicts to block automatic completion, so that ambiguity cannot silently finish the wrong Issue.
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
33. As an upstream maintainer, I want generic Git locale, child environment, process cancellation, and CI fixes isolated as upstream candidates, so that broadly useful behavior is not hidden in a private fork.
34. As a database operator, I want fresh install, accepted-floor upgrade, and interrupted-retry migration proofs, so that schema convergence is verified against realistic states.
35. As a fork maintainer, I want every retained capability represented by contract tests and every retired capability represented by an explicit disposition, so that future refreshes can be reconstructed from intent.
36. As a fork maintainer, I want compatibility admitted only for a current named obligation, so that speculative transition code does not become permanent architecture.
37. As a fork maintainer, I want finite migration checks removed after cutover, so that shadow and dual-path machinery does not survive its purpose.
38. As a fork maintainer, I want work to stop once real contracts, live-data classes, and release seams pass, so that diminishing-return proof does not delay the clean rebuild.

## Implementation Decisions

- Current upstream is the implementation baseline. Prior fork code is donor evidence, not a merge source. Capabilities are reimplemented from behavior contracts and live-data obligations.
- External PR remains a fork-owned authoritative fact model. Its natural identity is Workspace, provider, repository, and PR number; its Issue association uses explicit UUIDs and cannot be silently rebound.
- AGS PR identity and the actual merge-provider PR identity remain separate facts. Canonical instance, repository binding, binding revision, expected head/base, merge method, and projection fact revision are persisted rather than accepted only transiently.
- The callback contract verifies that supplied Workspace slug and Issue key agree with the authoritative UUID records. Redundant display identifiers are either verified or removed from the authority envelope.
- Typed terminal callbacks require a configured provider allowlist, canonical body idempotency key, canonical URLs bound to the configured instance, authoritative confidence, explicit completion intent, and a complete projection fact set.
- A shared service token authenticates the AGS service but is not treated as per-event proof. Repository binding and immutable fact revision provide the narrower event authority.
- External PR terminal admission and durable reconciliation remain. The terminal fact transaction commits before completion work and exact replay nudges unfinished work without duplicating side effects.
- Native pull request metadata and External PR facts converge through a unified read projection. The product exposes one Issue pull request surface while retaining association provenance and dual AGS/merge-provider identity where relevant.
- Association provenance is modeled explicitly. Supported values distinguish explicit External binding, title inference, branch inference, closing-keyword inference, and body-only reference.
- Explicit External binding has precedence over text inference. If one provider PR is explicitly bound to one Issue but text inference points to another, the system records a conflict, suppresses automatic completion, and exposes a repairable diagnostic.
- Body-only references remain hidden and non-blocking. Title and branch inference remain available for ordinary native workflows but cannot override an explicit binding.
- Managed AGS repositories or PRs use explicit completion authority. Text-derived closing intent for the same provider identity is ignored when an External binding exists.
- The generic pull request completion evaluator is independent of External PR finalization records. External reconciliation wraps the evaluator with durable work and finalization; native GitHub/VCS paths use the generic evaluator without fork storage ownership.
- Completion preserves exact policies for `record_only`, supported leaf-child completion, non-leaf rejection, open authoritative PR blocking, merged completion intent, terminal Issue protection, parent wake, stage continuation, idempotency, and crash recovery.
- Current execution context reuses upstream Task, Workspace, Agent, Issue, Runtime, trigger, and claim facts. Task ID is preferred; one small claim/environment addition is allowed only if AGS proves a distinct execution identity is required.

- Repository authority, Access Grant routing, Git/GitHub shims, and platform Git identity move to AGS bootstrap or runtime configuration. Multica does not own provider-operation policy.
- Pi process cancellation is reimplemented as a narrow process-group patch with bounded TERM wait, process-group liveness polling, and SIGKILL escalation even when the leader exits first.
- Offline web builds use a local-font or build-overlay mechanism that does not require network access. Any redistributed font bundle includes its required licenses and copyright notices.
- Generic fixes for deterministic Git diagnostics, child environment duplicate-key replacement, process-group cancellation, CI path gating, and offline font supply-chain behavior are proposed upstream independently. Temporary fork patches are named and removable.
- Fork deployment behavior lives in additive tooling. Official upstream Compose, Dockerfiles, Make targets, and branch filters are not used as target configuration stores.
- One accepted source revision produces one multi-architecture OCI manifest. A source change invalidates exact-head CI, build directories, tags, manifests, and deployment receipts.
- Mini and imile-win are separate deployment transactions with their own backups, schema readback, image digest readback, runtime revision readback, rollback boundary, and redacted evidence.
- Raw container inspection output is not retained as evidence. Receipt directories and files are owner-only and contain allowlisted projections.
- Future fork migrations use a fork-owned directory and ledger. Deployment applies upstream migrations first and fork migrations second.
- Previously applied full migration basenames and live ledger rows are immutable. The accepted 2026-08-30 generation is the minimum direct upgrade source; older forks first upgrade through that generation.
- Migration proof covers a fresh upstream database, a sanitized accepted-floor schema with live External PR facts, and interrupted convergence retry. Historical active fixtures are retired only after those paths and backup restoration are proven.
- The 240 observed live External PR associations are preserved as authoritative fork facts. The 231 strict mappings are eligible for unified projection immediately; the remaining nine require provider normalization or explicit archive disposition. They are not silently coerced into text-derived native links.
- The convergence program uses exact-head CI and contract evidence before deployment. Runtime deployments remain unchanged until an implementation generation is accepted and explicitly authorized.
- Transitional reads exist only in tests or a bounded operator-run comparison. The accepted runtime has no permanent old/new dual-write, shadow processor, or fallback association path.
- The nine non-strict links are normalized when authority can be recovered; otherwise their existing explicit facts are preserved read-only. No new archive subsystem is created.
- Upstream pull requests are optional follow-up work. Fork acceptance depends on the local minimal patch and its tests, not on upstream review timing.
- Scope is complete when the four agreed seams pass and all current live-data classes have a disposition. Extra abstraction or fault matrices need evidence of a real uncovered risk.

## Testing Decisions

- Tests assert externally observable contracts rather than private helper structure. Database-backed handler tests, real migration runners, real subprocess groups, and digest/runtime readback are preferred to mocks at ownership boundaries.
- **External PR product contract seam:** exercise authenticated callback admission through persisted explicit binding, immutable natural identity, idempotent replay, conflict rejection, unified pull request reads, completion policy, parent/stage continuation, deletion, and crash recovery using the real handler and PostgreSQL. Include title/body/branch lookalike counterexamples and explicit-versus-inferred conflicts.
- **Upgrade seam:** apply upstream then fork migrations to both a fresh upstream schema and a sanitized accepted-floor schema. Verify immutable ledgers, preservation or explicit disposition of every live External PR class, retry after interruption, and rollback fences.
- **Runtime seam:** execute real Unix process groups and prove graceful TERM, bounded wait, SIGKILL escalation, leader-first exit, descendant survival detection, cancellation races, and no collateral process termination.
- **Release seam:** after exact-head CI, build one arm64/amd64 OCI manifest and verify architecture descriptors, immutable digest, source revision, secret-safe receipts, independent target backup/deploy/readback, and rollback evidence.
- Narrow tests remain for deployment overlays, CLI version admission, local installer behavior, deterministic Git locale, child environment replacement, CI path gating, offline font builds, and font license presence.
- Existing upstream GitHub/VCS webhook, pull request list, CI status, Issue deletion, Workspace deletion, daemon, and frontend tests remain regression prior art. New tests extend those public seams instead of duplicating implementation-level suites.
- Do not build exhaustive historical, provider, or concurrency matrices after the accepted seams cover the observed classes. Add a case only for a distinct authority boundary, live-data shape, or reproduced failure.

## Out of Scope

- Replacing explicit External PR association with PR title, body, or branch parsing.
- Removing External PR source facts, idempotency receipts, or durable reconciliation before unified projection and recovery contracts are proven.
- Treating the existing nine non-strict live links as strict mappings without authoritative provider lookup or an archive decision.
- A big-bang production database rewrite or destructive cleanup without accepted backups and readback.
- Replaying the prior fork squash or preserving old implementation solely because it existed in a previous generation.
- Fork-specific modifications to official upstream deployment files when an additive overlay can express the requirement.
- Building separate source revisions or architecture-specific source trees for Mini and imile-win.
- Restoring retired Workload Assertion, delegated Session/merge, legacy gateway, or repository-authority surfaces.
- Adding a broad execution-context query and locking subsystem for one environment variable.
- Claiming an upstream contribution is complete before it is accepted and released upstream.
- Permanent dual-write, shadow traffic, fallback reads, compatibility routers, generic archive/repair services, or provider-neutral frameworks created only for this convergence.
- Preserving an old route, field, table, worker, fixture, or UI branch without a named current consumer or live-data obligation.
- Expanding the proof matrix after the agreed seams and observed production classes pass without a concrete escaped defect.
- Deploying, migrating production data, merging the planning pull request, or rotating credentials as part of this specification publication.

## Further Notes

- The analysis baseline was refreshed to upstream `main` revision `1dd6b9ecdbaa991bfd51b48ef5c269056045d547`. The relevant text-inference implementation is unchanged from the previously frozen baseline.
- The accepted running fork source remains `ad25aa66bb3ab9b8c55f0cc2825523c0e72c0be7`; this planning work does not change either target runtime.
- Read-only production census found 240 live External PR associations: 178 on Mini and 62 on imile-win. Of these, 231 have strict mapping facts and nine need bounded normalization or archive decisions.
- The planning pull request and convergence blueprint are proposal authority only. Implementation must use new isolated worktrees, fast-forward-only integration, exact-head evidence, and explicit deployment authorization.
- Success means the next upstream refresh can reconstruct the fork from stable product contracts, fork-owned data authority, additive operations, and bounded patches without rediscovering hidden behavior in a broad diff.
