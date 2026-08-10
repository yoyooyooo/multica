# Fork Development Standard

## Scope

This Standard governs branch authority, official upstream refresh, fork-delta replay, pull requests, source-built versions, deployment evidence, and generation retirement for this fork.

Capability docs, source, CI, artifacts, provider state, and runtime receipts retain separate authority.

## Operating model

```text
latest synced upstream version (prefer official tag vX.Y.Z; otherwise frozen upstream tip)
  -> new clean fork/* generation branch created from that base
     -> replay net fork deltas so fork-only commits sit at the tip
     -> bounded feature/fix PR into that generation (fast-forward only)
     -> source acceptance
     -> immutable deployment tag + artifact receipts
```

**Formal generation authority is always the fork generation built on the latest upstream version this fork has already synchronized.** Deploy only that generation. Do not deploy from long-lived `feature/*` as if it were the generation branch.

Shape of a clean generation history:

```text
[upstream base at frozen version/commit]
  ... upstream commits only ...
  [first fork-only commit]
  ... more fork-only commits ...
  [generation head]   <-- last commits are always fork-owned
```

When upstream advances, open a **new** generation by replaying onto the new base rather than merging or force-rewriting the previous generation branch.

## Branch roles

| Ref | Purpose |
| --- | --- |
| `main` | Exact upstream mirror; no fork-only changes. |
| `fork/vX.Y.Z` or `fork/<frozen-upstream-label>` | Canonical source authority for one generation frozen on a synced upstream base. |
| `feature/*`, `fix/*`, `docs/*` | Short-lived bounded changes targeting the active generation only. |
| immutable deployment tag | Exact source used to build one accepted target deployment. |

Rules:

1. Fork-only PRs never target `main`.
2. The active generation branch (and GitHub default when switched) identifies formal authority; no movable `fork/latest` exists.
3. Generation branches advance by fast-forward only; merge commits and force updates are forbidden.
4. Previous generation branches, images, tags, backups, and receipts remain immutable rollback evidence.
5. Required workflows must listen to `main` and the active generation, including path-specific mobile checks.
6. Production deploy uses the active generation head (or its immutable deploy tag), never an unintegrated feature tip unless that tip has already been fast-forwarded into the generation.

## Fork delta model

Every fork-owned change is a delta: `feature`, `fix`, `integration`, `operations`, or `documentation`.

Current capability narratives live at `docs/features/fork/<capability>/README.md`. Each states behavior, failure boundaries, source/test anchors, deployment applicability, rollback boundary, and retirement condition.

## Creating or repairing a generation

1. Freeze the latest **already-synced** upstream version: prefer official tag `vX.Y.Z`; if the generation intentionally includes a small post-tag upstream tip, freeze that exact commit and record the nearest tag.
2. Create a new clean `fork/*` generation branch **from that frozen base** (not by rewriting the previous generation).
3. Inventory **every prior generation and donor delta at commit/file granularity**. A prior squash or accepted generation is not sufficient evidence that an older donor capability was absorbed.
4. Classify every delta as `keep`, `rework`, `superseded`, `retire`, or `blocked`, with a source/test observation supporting the classification.
5. Compare old user-visible surfaces, process/runtime fixes, build/deploy contracts, and docs/CI gates separately; backend replay does not imply frontend replay.
6. Allocate non-overlapping forward-only migration numbers. Never rename or rewrite historical ledger entries.
7. Replay accepted fork semantics so the resulting history is upstream base + fork-only tip; do not mechanically cherry-pick an old squash when structure has diverged.
8. Run capability-specific tests, full source gates, exact-head CI, and review.
9. Build backend and frontend from the same exact deployment source.
10. Switch source/default branch or runtime only after the corresponding acceptance evidence exists.
11. After upstream releases a newer version the fork intends to track, start a **new** generation from that base rather than accumulating unbounded mid-generation upstream merges.

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

Normal upgrades freeze the latest official stable tag the fork chooses to track, then open a new generation and replay. A post-tag unreleased upstream tip may enter a generation only when owner-authorized, with exact frozen commit, nearest tag, focused evidence, and a retirement condition when the next official tag is adopted.

Do not treat “whatever is currently running in production” as generation authority when it diverges from the recorded active `fork/*` head; either promote the generation docs/receipts to match live, or redeploy from the formal generation.

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
