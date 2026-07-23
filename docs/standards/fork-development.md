# Fork Development Standard

## Scope

This Standard governs branch authority, upstream refresh, fork-delta replay, pull requests, source-built CLI versions, deployment evidence, and generation retirement for this pure fork.

It does not claim that a capability is implemented or deployed. Capability narratives, tracker state, source, CI, artifacts, and runtime evidence retain their own authority.

## Operating model

The fork follows a release-tag-based generation model:

```text
upstream release tag vX.Y.Z
  -> fork/vX.Y.Z
     -> reviewed fork feature/fix branches
     -> exact commit and immutable deployment evidence
```

The fork does not continuously build production from `upstream/main`. Official stable release tags are the normal generation boundary.

## Branch roles

| Ref | Owner | Purpose | Allowed changes |
|---|---|---|---|
| `main` | upstream sync automation | Exact mirror of `upstream/main` for observation and comparison | Upstream synchronization only |
| `fork/vX.Y.Z` | fork maintainers | Canonical source authority for one fork generation based on exact official tag `vX.Y.Z` | Reviewed fork deltas and generation documentation |
| `feature/*`, `fix/*`, `docs/*` | work owner | Short-lived change proposed to the active generation | One bounded capability, fix, or governance change |
| immutable deployment tag | deployment owner | Exact source reference for a built or deployed artifact | Created once; never moved |
| `mini-runtime` | legacy deployment evidence | Existing `c798fa83…` runtime line until a generation replacement is accepted | Emergency repair only; no new general fork capability |

Rules:

1. Fork-only PRs must not target `main`.
2. New work starts from and targets the active `fork/vX.Y.Z` branch.
3. The GitHub default branch identifies the active fork generation. There is no `fork/latest` branch or movable latest tag.
4. Feature branches must be rebased on the active generation before integration. The generation branch advances by fast-forward only; merge commits are forbidden.
5. Required CI workflows must listen to both `main` and `fork/**`; a generation PR without its required checks is not merge-ready.
6. Prior generation branches and deployment tags are retained as immutable history and rollback evidence. Do not rewrite or delete them.

## Fork delta model

Every fork-owned change is a fork delta. A delta may be:

- `feature`: fork-only product or runtime capability;
- `fix`: correction not yet present in the selected upstream release;
- `integration`: provider or control-plane integration;
- `operations`: build, deployment, rollback, or version contract;
- `documentation`: authority, capability narrative, or retained evidence alignment.

Current capability narratives live at:

```text
docs/features/fork/<capability>/README.md
```

Each narrative must state:

- problem and necessity;
- current behavior and authority boundary;
- failure behavior and non-goals;
- source and test anchors;
- generation and deployment applicability;
- rollback boundary;
- retirement condition.

A narrative is current only when its implementation exists in the same generation. Active work status remains in the tracker and PR; do not copy mutable progress into the narrative.

## Creating a generation

For a new official release `vX.Y.Z`:

1. Fetch and verify the official tag and its exact commit.
2. Create `fork/vX.Y.Z` directly from that tag, not from a moving `main` head.
3. Create `docs/releases/fork-generations/vX.Y.Z.md` before activation. Record the upstream tag/SHA, accepted fork deltas, source PRs, verification scope, exact generation head, deployment references, rollback source, and explicit not-claimed items.
4. Inventory the prior generation's fork deltas.
5. Classify each delta as:
   - `keep`: replay without semantic change;
   - `rework`: preserve the capability but adapt it to the new upstream structure;
   - `superseded`: upstream independently provides equivalent behavior;
   - `retire`: the fork no longer needs it;
   - `blocked`: migration cannot yet meet its evidence or safety gate.
6. Before replaying migrations, inspect the new generation's highest migration number and allocate non-overlapping ranges for every accepted delta in dependency order. Old generation numbers are evidence, not reusable authority; renumber colliding migrations and preserve the repository's concurrent-index rules.
7. Replay only accepted deltas through bounded PRs, in dependency order.
8. Run source, migration, frontend, daemon, and capability-specific verification required by the accepted deltas.
9. Switch the GitHub default branch and any deployment source only after generation acceptance. Branch creation or PR merge alone does not authorize deployment.

Do not rebase or force-update the prior generation in place. Do not mechanically replay every historical commit: fixups, obsolete generated files, conflict-only changes, and superseded behavior must not be preserved without a current reason.

## Pull request rules

Every fork PR must identify:

- active generation base;
- fork delta category and owning tracker item;
- dependency on other fork deltas;
- current capability docs affected;
- verification commands and result scope;
- deployment impact and whether a restart, migration, or rollback plan is required;
- upstream source SHA when carrying an exceptional upstream-main hotfix.

A PR may be source-complete without being deployed. Merge evidence and deployment evidence are separate records.

When an open PR targets `main`, do not merge it as a fork change. Recreate or rebase its bounded commits onto the active generation, change the base only after the diff is reviewed against that generation, and preserve a cross-link explaining the supersession.

## Upstream refresh policy

Normal upgrades use official stable tags only. `main` may continue mirroring `upstream/main` for visibility, but it is not a production source.

An unreleased upstream commit may enter an active generation only for a documented security or blocking fix. The PR must record:

- exact upstream commit;
- why waiting for the next release is unacceptable;
- focused regression evidence;
- the release or condition that retires the exceptional projection.

## Source-built CLI and daemon version contract

The Makefile's default version command is the source of truth:

```bash
git describe --tags --match 'v[0-9]*' --always --dirty
```

A deployable source build must report a clean, parser-compatible value:

```text
vX.Y.Z-N-g<hex-sha>
```

A bare official `vX.Y.Z` is valid only for an artifact built from that exact official tag. Dirty builds and arbitrary labels such as `mini-runtime-<sha>` are not deployable.

This constraint is enforced before the Makefile artifact build by:

- Make target `validate-cli-build-version`;
- `scripts/validate-cli-build-version.sh`;
- `scripts/validate-cli-build-version.test.sh`.

The resulting value must also remain compatible with the product capability gates in:

- `packages/core/runtimes/cli-version.ts`;
- `server/pkg/agent/version.go`.

`make multica` remains a local source-execution path and may run from a dirty tree, but its output is not a deployable artifact or deployment evidence.

Before activation, record and compare:

1. source commit and expected clean `git describe` value;
2. candidate `multica -v` output;
3. candidate artifact SHA-256;
4. daemon task drain state;
5. executable path and process identity after restart;
6. runtime `metadata.cli_version` after reconnect;
7. the user-visible capability gate that required the build.

If any identity or version value differs, stop before switching the binary. Never repair the symptom by editing runtime/database metadata or broadening a parser to accept a label that does not prove a version floor.

## Deployment and rollback

Deployment authority is the tuple:

```text
accepted generation head
+ immutable deployment tag
+ artifact digest
+ target-specific approval
+ runtime readback
```

Use deployment tags that do not match the upstream `v[0-9]*` namespace, for example:

```text
fork-mini-runtime-v0.4.8-r1
fork-mini-backend-v0.4.8-r1
```

Do not move an existing deployment tag. A rollback restores a previously evidenced artifact and follows the target's task-drain and restart boundary; it must not be described as preserving capabilities absent from that artifact.

## Documentation and evidence placement

| Information | Owner |
|---|---|
| Binding branch/build/deployment process | `CLAUDE.md` and `docs/standards/**` |
| Current fork capability behavior | `docs/features/fork/**` |
| Immutable generation inventory | `docs/releases/fork-generations/**` |
| Active work, dependencies, approvals, proof runs | Tracker Issues and PRs |
| Implemented behavior | Code, migrations, and tests |
| Live deployment state | Runtime metadata, artifact records, and target-specific receipts |

Documentation changes can prove authority alignment only. They cannot prove source acceptance, CI success, deployment, or runtime health without the corresponding evidence.
