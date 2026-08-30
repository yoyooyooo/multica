# Fork convergence blueprint

## Status

This is an implementation proposal, not current runtime authority. Mini and
imile-win continue to run accepted source `ad25aa66b` until a separately tested
generation completes the transitions below.

The proposal deliberately starts again from current upstream behavior. Prior
fork source is evidence for requirements and live-data compatibility, not an
implementation that must be replayed.

## Baseline

- frozen comparison base: `upstream/main@15280617bc264e367ca9c5e5e5cefdb0988246b7`
- accepted fork runtime source: `ad25aa66bb3ab9b8c55f0cc2825523c0e72c0be7`
- raw delta: 341 files, 21,753 insertions, 928 deletions
- server share: 226 changed files
- upstream movement absorbed by the 2026-08-30 generation: 112 commits

The raw delta overstates product ownership: historical migrations, sqlc output,
vendored fonts, migration fixtures, tests, and generation records account for
most files. The maintenance problem is nevertheless real because the fork also
modifies upstream's Issue, Workspace, GitHub/VCS, migration runner, daemon,
Makefile, Compose, and CI ownership surfaces.

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
5. Upstream generally useful fixes. Keep only a small temporary patch while the
   upstream change is unavailable.
6. Set an explicit upgrade floor at the accepted 2026-08-30 generation. Older
   forks upgrade through that generation rather than keeping every historical
   topology executable forever.

## Capability decisions

| Current fork surface | Current upstream observation | Target decision |
| --- | --- | --- |
| External PR storage, Issue projection, PR sidebar, Forgejo/Gitea/GitLab facts | Upstream already has `vcs_connection`, `vcs_pull_request`, `issue_vcs_pull_request`, normalized provider adapters, shared PR API/UI, monotonic webhook updates, and cross-provider completion | Reimplement on upstream VCS. Remove the parallel `external_pull_request_*` product model after data conversion. |
| AGS `/links` and `/complete-from-merge` callbacks | Upstream accepts provider webhooks, but not the AGS service envelope | Prefer direct Forgejo webhook registration. If AGS still needs its envelope, implement a thin authenticated adapter that emits upstream `vcs.PullRequestEvent`; it must not own a second PR schema or completion kernel. |
| Durable External PR work/finalization scheduler | Upstream provider delivery and monotonic upsert already own redelivery; current live work is drained | Retire by default. If a synchronous AGS acknowledgement requires durable admission, keep one adapter-owned inbox with idempotency only; downstream processing calls the native VCS ingestion service and does not modify Issue/Workspace deletion graphs. |
| Completion policy, parent/stage wake, Issue locking | Upstream VCS/GitHub paths already use the shared close aggregate and `advanceIssueToDone`, but parity for `record_only`, leaf-only completion, parent/stage wake, and crash recovery is not yet proven | Reuse upstream storage and completion entry point. Preserve those four behaviors as contract tests; if a gap remains, add a pure policy/service boundary rather than a parallel PR schema or Issue/Workspace deletion path. Decouple native completion from `external_pr_reconcile_finalization`. |
| Current execution context endpoint and task-bound link token | Upstream claim data already carries task, workspace, agent, Issue, runtime, and trigger facts; exact execution identity is a small missing claim field, while link token is already marked residual | Retire link token. Add exact execution identity to the claim only if AGS requires it, then materialize a claim-time context file or environment projection in the daemon. Avoid a new server query/lock/route; keep an endpoint only for a proven post-claim consumer. |
| Access Grant routing, git/gh shims, platform Git identity | Repository authority belongs to AGS/Agent Kit rather than Multica product state | Move launcher/path/identity policy to AGS bootstrap or runtime configuration. Do not patch Multica task execution for repository-provider policy. |
| `MULTICA_RUN_ID` | Upstream supplies `MULTICA_TASK_ID` but not the fork's exact execution identity field | Prefer task ID if AGS accepts it. Otherwise upstream a small claim plus environment addition; no execution-context subsystem is needed for one variable. |
| Pi process-tree cancellation | Upstream has cross-platform process-group primitives and equivalent handling in several backends, but Pi still lacks complete TERM-to-KILL ownership | Reimplement as a narrow upstream fix using `waitProcessGroupGone`; the current fork's `procDone` race can skip SIGKILL after the leader exits while a descendant remains. Retain only the corrected small patch and Unix test until released upstream. |
| Offline Google Fonts | Upstream web build still uses `next/font/google`; the current vendored bundle does not yet carry distributable font license/copyright material | Prefer an upstream local-font/offline-build fix. Interim fork support moves under `deploy/fork/web/**`, includes the required font licenses, and sets the build mock in an overlay Dockerfile; do not modify app layouts, package scripts, or the official Dockerfile. |
| Local fork CLI/version/deployment scripts | These are owner operations, not product behavior | Move under `deploy/fork/**` and invoke directly from the project skill. Stop adding Makefile targets or editing official self-host files. |
| Uploads bind and old named-volume copy | Both live targets already crossed the boundary and retain receipts | Keep target-specific bind overrides and rollback evidence. Remove the fork change to official Compose after verifying the one-time copy receipt and retained old volume. |
| Git locale and child environment fixes | `LC_ALL=C` for Git diagnostics and duplicate environment replacement are generally useful | Submit independent upstream fixes. Do not carry them as unnamed fork behavior. |

## Target ownership boundary

The converged fork should have zero fork edits in these upstream-owned files:

```text
server/internal/handler/issue.go
server/internal/handler/workspace.go
server/internal/handler/github.go
server/internal/handler/vcs_webhook.go
server/cmd/migrate/main.go
Makefile
docker-compose.selfhost.yml
.github/workflows/ci.yml
packages/core/api/*
packages/views/issues/components/pull-request-list.tsx
```

Expected remaining source delta:

- additive `deploy/fork/**` build, activation, compose override, and receipt tools;
- additive fork migration runner/directory, if live-schema convergence still
  needs it;
- zero or one small AGS-to-native-VCS adapter;
- temporary small upstream-bound patches for Pi and any accepted daemon env
  value.

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

### External PR conversion

1. Establish a real upstream VCS connection for each live workspace/Forgejo
   instance. There is one live workspace per observed target.
2. Normalize the nine live links missing strict merge fields by authoritative
   provider lookup or mark them read-only archived facts.
3. Backfill live rows into `vcs_pull_request` and
   `issue_vcs_pull_request`, preserving state, URL, repository, number,
   close intent, timestamps, and Issue identity.
4. Compare old and native PR projections for every live Issue.
5. Enable provider webhook ingestion and prove new open/closed/merged updates
   use only native VCS tables.
6. After a drain window, remove orphan rows and retire custom callback,
   reconcile, finalization, link-token, and projection code.
7. A final compatibility migration drops legacy tables only after backup and
   readback. Image-only rollback is not valid past that point.

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

### Wave 1: operational extraction

- Add the project skill and move deployment-only behavior toward
  `deploy/fork/**`.
- Stop changing Makefile, official Compose, official Dockerfiles, and workflow
  branch lists for target-specific operation.
- Submit the corrected Pi cancellation, deterministic Git locale, child-environment,
  CI path-filter/checkout, and offline-font licensing fixes upstream.
- Add an exact-SHA artifact promotion job that builds arm64/amd64 once and
  publishes one immutable OCI manifest after source acceptance.

This wave should not change product data or runtime behavior.

### Wave 2: native VCS shadow path

- Configure native Forgejo connections and webhook delivery.
- Implement only the adapter or importer still required by AGS.
- Backfill and compare native projections without switching reads.
- Resolve the nine non-strict live links explicitly.

### Wave 3: authority switch

- Switch PR reads and completion to upstream native VCS behavior.
- Stop issuing link tokens and stop accepting the old callback after a declared
  compatibility window.
- Prove no pending reconcile work, projection parity, merged-PR completion,
  Issue/workspace deletion, and provider redelivery.

### Wave 4: historical retirement

- Apply the compatibility cleanup migration with per-host backups.
- Remove the parallel External PR runtime, generated queries, frontend union,
  scheduler registration, historical active migration chain, and giant
  continuity fixtures from the new generation.
- Record the 2026-08-30 generation as the minimum direct upgrade source.

## Acceptance targets

- current fork behavior is represented by a decision row, including retired
  behavior;
- no fork migration is added to upstream's numeric sequence;
- all 240 currently live External PR links are converted or explicitly archived;
- no custom External PR work is pending at cutover;
- native provider redelivery is idempotent and merged close-intent,
  `record_only`, leaf-only completion, and parent/stage wake behavior are proven;
- one accepted-SHA arm64/amd64 OCI manifest is promoted before target rollout;
- exact-head CI passes before any final arm64/amd64 image build;
- Mini and imile-win receive independent backups, migration readback, runtime
  receipts, and rollback instructions;
- secret-safe evidence contains no raw container environment.

Success is not a smaller squash commit. Success is a fork whose remaining
changes sit behind stable ownership boundaries and can be re-derived from
behavior contracts on the next upstream refresh.
