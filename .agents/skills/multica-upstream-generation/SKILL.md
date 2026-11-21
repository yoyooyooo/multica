---
name: multica-upstream-generation
description: Refresh this Multica fork from upstream main, rebuild only accepted fork capabilities, prove convergence, and optionally deploy Mini and imile-win.
metadata:
  version: "2.0.0"
  argument-hint: <upstream-ref> [source-only|deploy]
---

# Multica Upstream Generation

Use upstream source as the implementation baseline. Treat old fork code as evidence, not a patch queue. Preserve accepted behavior, external contracts, live data, and rollback artifacts while minimizing changes to paths that already exist upstream.

Read `AGENTS.md`, `CLAUDE.md`, `fork/README.md`, `fork/UPSTREAM_INVASION.md`, and the current manifest under `fork/releases/` before acting.

## Required evidence

Every generation records:

1. remote upstream `main`, all upstream tags, the frozen commit, nearest official tag, and peeled tag commit;
2. capability decisions: `upstream`, `keep`, `reimplement`, `externalize`, `retire`, or `blocked`;
3. changed files split into additive files and modified existing upstream paths;
4. overlap with the selected upstream target and a read-only `merge-tree` result;
5. exact-head tests and CI;
6. for deployments, per-target build IDs, image IDs, OCI revisions, CLI checksums, database backups, restore proofs, immutable tags, and rollback receipts.

Source acceptance and each target deployment are separate state transitions.

## Phase 0: complete refs first

Run before naming a generation or deriving a version:

```bash
git fetch --force upstream main

git fetch --force upstream 'refs/tags/*:refs/tags/*'
git ls-remote upstream refs/heads/main 'refs/tags/v*'
git describe --tags --abbrev=0 <frozen-sha>
git rev-parse '<nearest-tag>^{commit}'
```

Do not derive a generation name from a shallow clone or an incomplete tag set. Pin the selected tag and peeled commit in `fork/UPSTREAM_BASELINE_TAG`; `fork/scripts/verify-source.sh` must reject a missing or different object.

Record worktrees, dirty files, local and remote refs, target runtime revisions, and active task counts. Create a separate worktree. Stop on ambiguous identity, a non-fast-forward ref, dirty source, or active target work.

## Phase 1: contract-first inventory

For every old capability ask, in order:

1. Does current upstream already provide it?
2. Can configuration or an external adapter provide it?
3. Can an additive file use an upstream primitive?
4. Is a small independently upstreamable patch possible?
5. Only then, which existing upstream path must change?

Retain only observable contracts. Drop compatibility layers, transition-only schema, speculative abstractions, and duplicated upstream behavior unless live evidence requires them.

`fork/` is a build and deployment boundary, not proof of product isolation. Maintain `fork/UPSTREAM_INVASION.md` for every existing upstream path changed by the fork. Generated files count as an invasion and must be regenerated from their source.

## Phase 2: clean implementation

Start from the frozen upstream tree and reimplement accepted behavior. Do not merge or mechanically cherry-pick an old generation. Keep the history linear and commits capability-scoped.

Migration rules are strict:

- do not rewrite an applied migration;
- use the fork migration directory and independent ledger;
- add no foreign key or cascading action;
- perform dependent cleanup explicitly in application transactions;
- create every index concurrently in its own single-statement migration;
- use a unique test database for every migration attempt.

Do not refactor a necessary integration hook merely to reduce a path count. Reduce real coupling, not the audit metric.

## Phase 3: convergence proof

After implementation and after every source change:

```bash
bash fork/scripts/verify-source.sh
bash fork/scripts/audit-convergence.sh \
  --previous <prior-fork-ref> \
  --upstream upstream/main \
  --source HEAD
```

Interpret results separately:

- `text_conflict_files=0` proves only textual mergeability;
- every `upstream_overlap_path` still requires semantic review;
- SQL sources and generated sqlc files require regeneration and a clean diff;
- migration ledgers, delete behavior, protocol schemas, and runtime identity require dedicated tests.

Compare old and new against their own merge bases. Never compare absolute diffs across unrelated upstream eras.

Run the narrow affected tests, then the repository gates and exact-head Fork CI. A changed source SHA invalidates prior CI and artifacts.

## Phase 4: immutable build

Build only from a clean exact-head SHA accepted by CI. Derive version after complete tag fetch. Use a generation-specific image prefix so rebuilding an old SHA cannot overwrite a prior release tag.

Verify source SHA, version, platform, image ID, OCI revision, CLI metadata, checksums, and license assets. Receipts must be owner-readable and secret-safe.

## Phase 5: deployment

For Mini and imile-win independently:

1. prove daemon and database task/autopilot counts are zero;
2. freeze a database backup and verify an independent restore;
3. record old images, CLI, storage, network, ports, and rollback source;
4. switch with `fork/scripts/deploy-target.sh`;
5. install the exact-source CLI with `fork/scripts/install-cli-transaction.sh`;
6. verify image IDs and image config labels, not Compose container labels;
7. verify backend readiness, frontend page, migrations, uploads, External PR census, and AGS running-to-terminal 200-to-401 behavior;
8. create a new immutable per-target tag. Never move an old tag.

## Closeout

The formal branch may advance to a documentation-only manifest commit after deployment. The manifest must distinguish formal branch tip, runtime source SHA, and target tags. Record explicit non-claims and the latest observed upstream SHA.

Do not:

- merge upstream into a published generation;
- force-push or move deployment tags;
- build from incomplete tags or a dirty worktree;
- count an automatic merge as semantic compatibility;
- rewrite deployed migrations;
- move code solely to improve a metric;
- store secrets or raw container environments in evidence.
