---
name: multica-upstream-generation
description: Refresh this Multica fork from upstream main or an official tag, re-derive fork capabilities on a clean generation, obtain exact-head source acceptance, and then build or deploy Mini and imile-win. Use for upstream sync, fork replay/convergence, generation refresh, or same-source fork deployment.
metadata:
  version: "1.0.0"
  argument-hint: <upstream-ref> [source-only|deploy]
---

# Multica Upstream Generation

Treat upstream source as the implementation baseline. Treat accepted behavior,
external contracts, live data, and immutable deployment evidence as constraints.
An old fork implementation is donor evidence, not code that must be replayed.

Read before acting:

- `CLAUDE.md`
- `docs/standards/fork-development.md`
- `docs/standards/local-fork-runtime.md` when a CLI or daemon is involved
- `docs/features/fork/README.md` and each active capability document
- the current and prior generation manifests under
  `docs/releases/fork-generations/`

## Required Outputs

Every refresh produces these independently reviewable results:

1. frozen upstream ref and exact commit;
2. commit/file capability inventory with `keep`, `reimplement`, `upstream`,
   `externalize`, `retire`, or `blocked` decisions;
3. a clean generation history: frozen upstream plus fork-owned tip commits;
4. exact-head source acceptance and CI URL;
5. only when deployment was requested, per-target artifacts, backups, runtime
   readback, immutable tags, and secret-safe receipts.

Source acceptance is not deployment acceptance. Mini and imile-win are separate
state transitions even when they use the same source commit.

## Phase 0: Preflight

1. Read repository and fork standards. Do not infer policy from an old session.
2. Record `git status`, worktrees, remotes, local/remote refs, current generation,
   runtime revisions, and active task counts.
3. Preserve unrelated work. Use a new worktree for the generation campaign.
4. Fetch both remotes. Update local `main` by fast-forward only and prove
   `main`, `origin/main`, and `upstream/main` have the intended relationship.
5. Freeze an official tag when possible. A post-tag upstream tip requires an
   exact commit, owner authorization, nearest tag, and explicit claim limit.
6. If deployment may follow, read each target's current images, CLI, daemon,
   Compose files, storage, network, migration ledger, and rollback source.

Stop if a required ref cannot move by fast-forward, the worktree is dirty, a
runtime identity is ambiguous, or the requested deployment target has active
work that cannot be drained safely.

## Phase 1: Contract-First Inventory

Build the inventory before changing source. Compare the frozen upstream with
the accepted fork source at three levels:

- behavior: user-visible outcomes and failure boundaries;
- contracts: HTTP, task env, provider wire, database, migration, storage, CLI,
  and daemon identity;
- implementation: changed files, generated output, tests, and operational
  overlays.

For each capability ask in order:

1. Does current upstream already provide the behavior?
2. Can deployment configuration or an external adapter provide it?
3. Can a small additive module use an upstream-owned primitive?
4. Is an upstream patch appropriate and independently mergeable?
5. Only then, which upstream-owned file must the fork modify?

Do not mechanically merge or cherry-pick an old generation squash. Start from
the frozen upstream tree and reimplement the smallest accepted behavior. Keep a
file-level conflict budget. Changes to these high-churn surfaces require an
explicit "no stable extension point" justification and an upstream disposition:

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

Prefer additive paths such as `deploy/fork/**`, a provider adapter, or a
fork-owned migration directory over edits to those files.

## Phase 2: Clean Generation

1. Create a new integration worktree directly from the frozen upstream commit.
2. Implement accepted capabilities from their contracts, not donor code shape.
3. Keep commits atomic by capability or compatibility boundary.
4. Regenerate sqlc and other generated outputs from merged source definitions;
   never manually reconcile generated files.
5. Keep the generation branch linear. No merge commit, rebase of a published
   generation, force update, or fork-only commit on `main` is allowed.
6. Record every retired behavior and compatibility bridge. Silence is not a
   retirement decision.

### Migration Rules

- An applied migration's full basename and ledger row are immutable.
- Select, compare, and assert migrations by complete version, never only by
  numeric prefix.
- New fork-only migrations must not consume upstream's open numeric sequence.
  Use the fork-owned directory and ledger selected by the active convergence
  plan.
- Historical migrations may leave the active tree only after an explicit
  upgrade floor, fresh-install proof, live-ledger proof, and immutable prior
  generation retention are recorded.
- A migration test uses a unique database/schema/Compose project. Never reuse a
  failed run's database as the success environment.

## Phase 3: Source Acceptance

Iterate with the narrowest useful tests, then run the complete gates justified
by the affected surfaces. At minimum consider:

```bash
pnpm typecheck
pnpm test
pnpm build
pnpm lint
bash scripts/validate-cli-build-version.test.sh
bash scripts/install-local-fork-cli.test.sh
bash scripts/test-go.test.sh
```

Backend integration and migration changes require the repository's dedicated
database harness and exact-head CI. Tests that share a database must create
unique rows, query only their own identities, set pagination explicitly, and
clean up row-level side effects. Passing in isolation is not enough when the CI
suite shares fixtures.

Push the candidate source and wait for CI on that exact SHA. If source changes:

- prior CI is no longer acceptance evidence;
- candidate ID, build directory, image tag, and receipt root are obsolete;
- rerun affected local gates and exact-head CI;
- do not relabel or reuse an old candidate artifact.

## Phase 4: Artifact Build

Build deployable artifacts only after exact-head source acceptance. This avoids
rebuilding arm64 and amd64 images for candidates that later fail CI.

1. Derive version with `git describe --tags --match 'v[0-9]*' --always`.
2. Build backend, frontend, and CLI from one clean exact commit.
3. Use native or declared multi-architecture builders with dependency caches;
   prefer one accepted-SHA OCI manifest over per-target source rebuilds.
4. Label every image with exact revision, version, and immutable deployment ID.
5. Verify labels, platform, image ID, CLI version, source cleanliness, and all
   third-party font/binary license material.

Do not store raw `docker inspect` output in evidence: `Config.Env` can contain
database, JWT, provider, and service credentials. Persist an allowlisted,
redacted projection; keep owner-only receipt directories mode `0700` and run a
secret scan before publication.

## Phase 5: Per-Target Deployment

For each target independently:

1. prove daemon active/running/resource-wait task counts are zero;
2. prove database task queue and autopilot work are quiescent;
3. freeze a database dump and SHA-256;
4. record old image IDs, CLI target, Compose inputs, volume/bind, and network;
5. stop the daemon and switch backend first;
6. require `/readyz` DB and migrations `ok`, then switch frontend;
7. activate the exact-source CLI and restart the same binary as daemon;
8. verify backend, frontend, CLI, daemon, device identity, migrations, storage,
   network, and login/readiness probes;
9. write rollback instructions that include database restore when the boundary
   is forward-only.

Never claim same-source convergence from image tags alone. Read revision and
version from every running component.

## Closeout

Fast-forward the generation branch only after source acceptance. Create a new
immutable deployment tag per target, write the generation manifest and
capability decisions, and retain prior generations as rollback evidence.

The final report names exact source, CI, runtime versions, image IDs, migration
delta, receipt hashes, rollback source, and explicit non-claims. A docs-only
tip after the deployment tag must say that it does not change runtime source.

## Forbidden Shortcuts

- merging upstream into a published generation;
- force-pushing or moving an immutable deployment tag;
- deploying an unaccepted feature tip;
- building final images before exact-head CI succeeds;
- selecting migrations by numeric prefix alone;
- reusing a failed integration database as clean evidence;
- manually editing generated sqlc output;
- storing secrets in receipts, logs, Issues, or repository files;
- treating a successful health probe as proof of browser, provider, or rollback
  behavior it did not exercise.
