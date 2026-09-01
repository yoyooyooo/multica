# Fork convergence blueprint

## Status

This is an implementation proposal, not current runtime authority. Mini and
imile-win continue to run accepted source `ad25aa66b` until a separately tested
generation completes the transitions below.

The proposal deliberately starts again from current upstream behavior. Prior
fork source is evidence for requirements and live-data compatibility, not an
implementation that must be replayed.

## Baseline

- current upstream analysis base: `upstream/main@1dd6b9ecdbaa991bfd51b48ef5c269056045d547`
- frozen 2026-08-30 delta base: `upstream/main@15280617bc264e367ca9c5e5e5cefdb0988246b7`
- accepted fork runtime source: `ad25aa66bb3ab9b8c55f0cc2825523c0e72c0be7`
- frozen-base raw delta: 341 files, 21,753 insertions, 928 deletions
- frozen-base server share: 226 changed files
- upstream movement absorbed by the 2026-08-30 generation: 112 commits

The relevant upstream PR-to-Issue association implementation is unchanged between
these two upstream revisions. The raw delta overstates product ownership:
historical migrations, sqlc output, vendored fonts, migration fixtures, tests,
and generation records account for most files. The maintenance problem is
nevertheless real because the fork also modifies upstream's Issue, Workspace,
GitHub/VCS, migration runner, daemon, Makefile, Compose, and CI ownership
surfaces.

## Current implementation inventory

| Capability | Fork-owned anchors | Modified upstream hotspots |
| --- | --- | --- |
| External PR facts and reconciliation | `server/internal/handler/external_pr_*`, `pull_request_completion.go`, `server/internal/completionpolicy/`, `server/pkg/db/queries/external_pr_*`, `pull_request_completion.sql` | `issue.go`, `workspace.go`, `github.go`, `vcs.go`, `vcs_webhook.go`, server listeners/router, shared PR API and UI |
| Historical database compatibility | fork migrations `272` through `316`, `context_continuity_convergence_test.go`, historical migration testdata | `server/cmd/migrate/main.go`, migration lint, sqlc output, Issue/Workspace cleanup queries |
| Execution context and AGS task identity | `current_execution_context.go`, `external_pr_link_token.go`, daemon `git_identity.go` | auth middleware, task-token queries, daemon claim/finalize, task environment and PATH assembly |
| Pi cancellation | `pi_cancel_unix_test.go` | `server/pkg/agent/pi.go` |
| Offline web build | `apps/web/offline-fonts/**` | app layouts, web package script, `Dockerfile.web` |
| Owner deployment and storage | local CLI/version/upload scripts and generation receipts | Makefile, official self-host Compose/build files, installers, CI workflow branch/path gates |

This inventory is the retirement checklist. Generated files are regenerated or
removed with their owning source; they are not independent capabilities. The
current frontend's separate External PR and native Pull Request sections are a
migration artifact, not a target product distinction.

## Live-data observation

Read-only census on 2026-08-30 found:

| Target | External links | Links whose Issue still exists | Strict Forgejo mapping facts | Native VCS connections / PRs | Reconcile state |
| --- | ---: | ---: | ---: | ---: | --- |
| Mini | 333 | 178 in one live workspace | 173 live; 5 need normalization | 0 / 0 | 181 recorded, 88 succeeded; finalization 2 recorded, 96 succeeded |
| imile-win | 62 | 62 in one live workspace | 58 live; 4 need normalization | 0 / 0 | 29 recorded; no pending work observed |

Mini's other 155 links point at removed Issue/workspace fixtures. They are
cleanup candidates, not product history. All observed reconcile rows were
terminal, so the new design does not need to inherit in-flight leases. The nine
live links without complete strict merge facts require a bounded normalization
or archive decision before cutover.

## Greenfield rules

1. Preserve behavior, external wire contracts still used, live user data, and
   immutable migration ledger rows.
2. Adopt upstream implementation whenever it now owns equivalent behavior.
3. Put target operations in additive deployment overlays, not official upstream
   Compose, Makefile, installer, or CI files.
4. Put future fork migrations in a fork-owned directory and ledger.
5. Upstream generally useful fixes, but never block the fork generation on an
   upstream review. Keep only a small named patch until an upstream release
   actually supersedes it.
6. Set an explicit upgrade floor at the accepted 2026-08-30 generation. Older
   forks upgrade through that generation rather than keeping every historical
   topology executable forever.
7. Admit compatibility only for a named current client, wire contract, or live
   data obligation. Every shim has an owner and a removal gate.
8. The accepted runtime has no permanent dual-write, shadow reader, generic
   repair/archive service, or second lifecycle kernel. Migration comparison is
   finite operator/test tooling and is removed before acceptance.
9. Prefer one owner and one write path per fact, one PR surface, and the existing
   completion path with the smallest ownership correction. New abstractions must
   remove more upstream conflict surface than they add.
10. Stop when the External PR contract, 240-row disposition, accepted-floor
    upgrade/rollback, and exact-head CI pass. Pi and AGS sidecars use their own
    narrow acceptance and do not block the rebuild.

## Capability decisions

| Current fork surface | Current upstream observation | Target decision |
| --- | --- | --- |
| External PR authority, storage, Issue association, and AGS/Forgejo dual identity | Upstream has strong generic PR metadata and provider support, but associates PRs to Issues by parsing `PREFIX-NUMBER` from title, body, and branch. Title/branch matches become working links and closing-keyword matches can complete an Issue. | Keep External PR as the fork-owned authoritative fact and recovery model. Reuse upstream PR metadata, CI aggregation, DTO, and common UI without replacing explicit UUID association with text inference. |
| AGS `/links` and `/complete-from-merge` callbacks | Upstream accepts provider webhooks, but not the AGS service envelope, explicit Issue UUID, immutable external identity, or AGS-to-Forgejo projection facts. | Retain the typed authenticated callback. Verify redundant Workspace/Issue labels against UUIDs, validate the complete canonical envelope, include it in the immutable payload hash, and persist only fields used by runtime reads or recovery. |
| Durable External PR work/finalization scheduler | Upstream webhook redelivery does not cover an AGS provider operation that succeeded before its Multica callback or Issue lifecycle finalization completed. | Keep durable terminal admission, reconcile, and finalization for External PR only. Native completion without External work must not create External finalization state. |
| Completion policy, parent/stage wake, Issue locking | The fork already has a completion path with `record_only`, leaf checks, locks, and durable continuation. | Reuse that path with a narrow ownership correction. Do not extract a new generic evaluator or second lifecycle kernel. Preserve retained behavior with contract tests. |
| Current execution context endpoint and task-bound link token | Upstream claim data already carries task, workspace, agent, Issue, runtime, and trigger facts; exact execution identity is a small missing claim field, while link token is already marked residual | Retire link token. Add exact execution identity to the claim only if AGS requires it, then materialize a claim-time context file or environment projection in the daemon. Avoid a new server query/lock/route; keep an endpoint only for a proven post-claim consumer. |
| Access Grant routing, git/gh shims, platform Git identity | Repository authority belongs to AGS/Agent Kit rather than Multica product state | Move launcher/path/identity policy to AGS bootstrap or runtime configuration. Do not patch Multica task execution for repository-provider policy. |
| `MULTICA_RUN_ID` | Upstream supplies `MULTICA_TASK_ID` but not the fork's exact execution identity field | Prefer task ID if AGS accepts it. Otherwise upstream a small claim plus environment addition; no execution-context subsystem is needed for one variable. |
| Pi process-tree cancellation | Upstream has cross-platform process-group primitives and equivalent handling in several backends, but Pi still lacks complete TERM-to-KILL ownership | Reimplement as a narrow upstream fix using `waitProcessGroupGone`; the current fork's `procDone` race can skip SIGKILL after the leader exits while a descendant remains. Retain only the corrected small patch and Unix test until released upstream. |
| Offline Google Fonts | Upstream web build still uses `next/font/google`; the current vendored bundle does not yet carry distributable font license/copyright material | Put the smallest licensed offline font overlay under Fork deployment tooling. Upstream submission is optional and no font pipeline or product-layout change is added. |
| Local fork CLI/version/deployment scripts | These are owner operations, not product behavior | Move under `deploy/fork/**` and invoke directly from the project skill. Stop adding Makefile targets or editing official self-host files. |
| Uploads bind and old named-volume copy | Both live targets already crossed the boundary and retain receipts | Keep target-specific bind overrides and rollback evidence. Remove the fork change to official Compose after verifying the one-time copy receipt and retained old volume. |
| Git locale and child environment fixes | `LC_ALL=C` for Git diagnostics and duplicate environment replacement are generally useful | Carry only a minimal named patch when required by a retained contract. Upstream submission is optional side work and does not block the generation. |

## Target ownership boundary

The converged fork should have zero direct fork edits in these upstream-owned
files unless a small generic extension is simultaneously prepared for upstream:

```text
server/internal/handler/issue.go
server/internal/handler/workspace.go
server/internal/handler/github.go
server/internal/handler/vcs_webhook.go
server/cmd/migrate/main.go
Makefile
docker-compose.selfhost.yml
.github/workflows/ci.yml
```

Shared PR API and UI changes are limited to rendering External rows in the
existing Issue PR section. They do not add provenance taxonomy, persistent
conflict state, repair UI, shadow API, or a second provider model.

Expected remaining source delta:

- additive `deploy/fork/**` build, activation, compose override, font overlay,
  and receipt tools;
- the smallest sequential Fork migration runner/ledger;
- a fork-owned External PR authority and durable-recovery module using the
  existing completion path;
- temporary small named patches for Pi or a proven daemon environment value.

## Migration convergence

### Historical boundary

Existing `schema_migrations` rows retain their complete fork basenames. Never
rename, rewrite, or delete those live rows. Immutable prior generation refs and
deployment tags retain the old migration source.

The recommended new layout is:

```text
server/fork-migrations/<timestamp>_<name>.up.sql
server/fork-migrations/<timestamp>_<name>.down.sql
fork_schema_migrations(version text primary key, applied_at timestamptz)
```

The deployment entrypoint applies upstream migrations first, then fork
migrations. Future fork versions no longer compete for upstream numeric
prefixes. A small additive runner is preferable to modifying
`server/cmd/migrate/main.go`.

### External PR authority convergence

1. Keep all 240 live External PR associations as explicit fork-owned facts. Do
   not replace their Issue UUIDs with text-derived links.
2. For the nine non-strict rows, recover missing authority once from the provider
   when possible; otherwise preserve the existing explicit row read-only. Do not
   create archive behavior.
3. Validate the complete canonical callback envelope and include it in the
   immutable idempotency hash. Do not add columns for request-only fields with
   no runtime read, completion, or recovery consumer.
4. Verify callback Workspace slug and Issue key against their UUID records or
   remove the redundant fields from the typed contract.
5. Render External rows through the existing Issue PR API/UI section. Do not add
   provenance taxonomy, conflict persistence, repair UI, or permanent shadow
   reads.
6. Keep terminal admission, reconciliation, and finalization for External PR.
   Make only the narrow correction that native completion without External work
   creates no External finalization row.
7. Compare the accepted-floor fixture and live-row census once in test/operator
   tooling, then remove the comparison path before runtime acceptance.
8. Any destructive cleanup requires backup and readback. Image-only rollback is
   not valid past an accepted schema cleanup boundary.

### Test contraction

Replace the 1,903-line historical continuity matrix and duplicate old migration
fixtures with three boundaries:

1. fresh upstream plus the minimal converged fork migration set;
2. sanitized `ad25aa66b` upgrade-floor schema plus convergence migrations;
3. interrupted convergence retry and rollback fence.

Each test owns a unique schema/database and asserts complete migration versions,
not numeric prefixes. Historical generations remain reproducible from their
immutable refs rather than from active-suite copies of every old SQL file.

## Delivery sequence

### T1: clean upstream generation and additive release path

- Start a new worktree at implementation-time latest accepted upstream `main`.
- Reimplement retained contracts without merging or cherry-picking the prior Fork
  squash.
- Move target operations and the smallest licensed offline-font overlay under
  additive Fork deployment tooling.
- Pass exact-head CI and build each required architecture from the same source
  SHA. A single multi-architecture manifest is optional.

T1 does not change product data or production runtime.

### T2: minimal External PR authority slice

- Reimplement typed UUID binding, immutable natural identity, payload-hash
  idempotency, durable terminal admission, completion policy, and crash
  continuation.
- Validate but do not persist request-only canonical envelope fields.
- Reuse the existing Issue PR section without provenance/conflict product state.
- Make the narrow finalization ownership correction; do not create a generic
  completion rewrite.

### T3: accepted-floor upgrade and two target transactions

- Apply upstream then the small Fork migration stream to fresh and sanitized
  accepted-floor schemas and retry one interrupted migration.
- Give every live External PR row a bounded disposition; normalize or preserve
  the nine non-strict rows without an archive subsystem.
- Back up, migrate, deploy, and read back Mini and imile-win independently using
  artifacts from the same accepted SHA.

### Independent sidecars

- Pi process-group cancellation is a narrow patch and test.
- AGS repository/runtime policy moves out of Multica when its current caller
  boundary is available.
- Upstream submissions are optional follow-up.

None of these sidecars blocks T2 or T3.

## Acceptance targets

- the implementation starts from implementation-time latest upstream `main` in
  a clean worktree and does not replay the prior Fork squash;
- typed callback replay, changed-payload conflict, immutable PR-to-Issue binding,
  retained completion policy, and crash continuation are proven;
- native completion without External work creates no External finalization row;
- all 240 live External PR rows retain explicit facts and the nine non-strict
  rows are normalized or preserved read-only without archive behavior;
- fresh and accepted-floor upgrades plus one interrupted retry pass without
  rewriting applied migration basenames;
- exact-head CI passes before artifacts are built, and every architecture reads
  back the same accepted source SHA;
- Mini and imile-win receive independent backups, migration/runtime readback,
  secret-safe receipts, and rollback instructions.

Stop at these targets. Do not add provenance taxonomy, permanent shadow,
canonical-envelope columns, a generic completion rewrite, a migration product,
a mandatory single OCI manifest, or additional proof matrices without a
reproduced defect.

Success is not a smaller squash commit. Success is a fork whose remaining
changes sit behind stable ownership boundaries and can be re-derived from
behavior contracts on the next upstream refresh.
