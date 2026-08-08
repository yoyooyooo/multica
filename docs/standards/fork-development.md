# Fork Development Standard

## Scope

This Standard governs branch authority, official upstream refresh, fork-delta replay, pull requests, source-built versions, deployment evidence, and generation retirement for this fork.

Capability docs, source, CI, artifacts, provider state, and runtime receipts retain separate authority.

## Operating model

```text
official release tag vX.Y.Z
  -> fork/vX.Y.Z
     -> bounded feature/fix branch
     -> fast-forward source acceptance
     -> immutable deployment tag and artifact receipts
```

Production generations start from official stable tags, not moving `upstream/main`.

## Branch roles

| Ref | Purpose |
| --- | --- |
| `main` | Exact upstream mirror; no fork-only changes. |
| `fork/vX.Y.Z` | Canonical source authority for one official-tag-derived generation. |
| `feature/*`, `fix/*`, `docs/*` | Short-lived bounded changes targeting the active generation. |
| immutable deployment tag | Exact source used to build one accepted target deployment. |

Rules:

1. Fork-only PRs never target `main`.
2. The GitHub default branch identifies the active generation; no movable `fork/latest` exists.
3. Generation branches advance by fast-forward only; merge commits and force updates are forbidden.
4. Previous generation branches, images, tags, backups, and receipts remain immutable rollback evidence.
5. Required workflows must listen to `main` and the active generation, including path-specific mobile checks.

## Fork delta model

Every fork-owned change is a delta: `feature`, `fix`, `integration`, `operations`, or `documentation`.

Current capability narratives live at `docs/features/fork/<capability>/README.md`. Each states behavior, failure boundaries, source/test anchors, deployment applicability, rollback boundary, and retirement condition.

## Creating or repairing a generation

1. Freeze the official tag and exact commit.
2. Create `fork/vX.Y.Z` directly from that tag.
3. Inventory **every prior generation and donor delta at commit/file granularity**. A prior squash or accepted generation is not sufficient evidence that an older donor capability was absorbed.
4. Classify every delta as `keep`, `rework`, `superseded`, `retire`, or `blocked`, with a source/test observation supporting the classification.
5. Compare old user-visible surfaces, process/runtime fixes, build/deploy contracts, and docs/CI gates separately; backend replay does not imply frontend replay.
6. Allocate non-overlapping forward-only migration numbers. Never rename or rewrite historical ledger entries.
7. Replay accepted semantics through bounded PRs; do not mechanically cherry-pick an old squash.
8. Run capability-specific tests, full source gates, exact-head CI, and review.
9. Build backend and frontend from the same exact deployment source.
10. Switch source/default branch or runtime only after the corresponding acceptance evidence exists.

A newly discovered omitted `keep` delta reopens generation convergence. Preserve the deployed generation as rollback evidence, repair through a new revision, and lower prior completion claims instead of rewriting history.

## Pull request rules

Every fork PR identifies:

- active generation base;
- delta category and donor/source references;
- affected capability docs;
- verification commands and claim limits;
- deployment, migration, restart, and rollback impact.

Source acceptance, deployment acceptance, and browser/provider proof are separate transitions.

## Upstream refresh policy

Normal upgrades use official stable tags. An unreleased upstream commit enters an active generation only for a documented blocking or security fix with exact source, focused evidence, and a retirement condition.

## Source-built version contract

The Makefile version authority is:

```bash
git describe --tags --match 'v[0-9]*' --always --dirty
```

Deployable source builds report either `vX.Y.Z` at that exact tag or `vX.Y.Z-N-g<hex-sha>`. Dirty builds and arbitrary labels are rejected by:

- `make validate-cli-build-version`;
- `scripts/validate-cli-build-version.sh`;
- `scripts/validate-cli-build-version.test.sh`.

`make multica` remains a local source-execution path and is not deployment evidence.

## Deployment and rollback

Deployment authority is:

```text
accepted generation head
+ immutable deployment tag
+ backend/frontend image digests
+ database backup/migration evidence when applicable
+ target runtime readback
```

Do not move deployment tags. When a migration boundary is forward-only, rollback requires the recorded database restore as well as prior images.

Self-host source must preserve the uploads bind mount contract, deployment URL/issuer identity, and reviewed build arguments. Target overrides may pin images or absolute bind paths but must not silently change storage ownership. When an accepted older generation used a named uploads volume, the forward generation must provide an idempotent, fail-closed copy-and-verify preflight before switching runtime authority to the bind path; it must retain the old volume until separate operator disposition.

## Documentation placement

| Information | Current home |
| --- | --- |
| Binding branch/build/deploy procedure | `CLAUDE.md`, `docs/standards/**` |
| Current fork capability | `docs/features/fork/**` |
| Generation inventory and claim limit | `docs/releases/fork-generations/**` |
| Active work and CI | PR/tracker |
| Implemented behavior | Source/tests/migrations |
| Live state | Images, provider readback, runtime receipts |
