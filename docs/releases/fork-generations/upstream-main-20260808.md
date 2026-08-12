# One-time upstream/main fork generation — 2026-08-08

## Status and authority

**Previous live / historical generation** (no longer the formal home). Superseded by formal generation `fork/v0.4.22` documented in [`v0.4.22.md`](v0.4.22.md).

This file remains immutable rollback/source inventory for the post-`v0.4.21` freeze line. It began as an owner-authorized freeze of a post-`v0.4.21` upstream tip rather than an exact official tag tip. It does **not** claim current formal authority and does **not** invent current deployment acceptance.

- generation branch: `fork/upstream-main-20260808`
- integration/work branch (not deploy authority by itself): `feature/upstream-main-20260808-fork-replay`
- frozen upstream base: `upstream/main@2b35f8017ab3b773e0356e562ecb04e55a7a9bd7`
- nearest official release: `v0.4.21@0dfaac266eed3b7ac710de33d8207e4f71cfb20b`
- unreleased upstream delta at freeze: 10 commits after `v0.4.21`
- prior accepted fork (rollback): `fork/v0.4.12@d5ec9569ede6e48e3caced031254259f48b83f41`
- prior fork base: `v0.4.12@e52b50658a66d09bffed126e34116ad826c03623`

History shape (origin head `b3118a340`):

```text
...upstream through 2b35f8017...
4ca4d619f jackie  ci(fork): bootstrap upstream-main generation checks
9955cc07f jackie  feat(fork): replay generation on upstream main
28dc443eb jackie  ci(mobile): verify upstream-main generation
b3118a340 jackie  fix(fork): remediate access grant review findings   <-- tip is fork-owned
```

Local deploy tip may advance by fast-forward only (example: `5354e11c0` best-effort `MULTICA_RUN_ID` daemon fix). The previous generation and its deployment artifacts remain immutable rollback authority; that branch is never force-updated or repurposed.

**Superseded formal base:** official tag **`v0.4.22`** is now the formal freeze home (`fork/v0.4.22`). Keep this generation branch immutable for live rollback until mini (and any other hosts still on `b3118a340`) complete a separately evidenced redeploy. Do not extend this freeze as formal authority.

## Replay method

A direct 23-commit linear rebase was attempted first and stopped at the first commit because upstream had independently replaced many of the same migrations, handler paths, docs, locales and CI contracts. The failed attempt is retained as negative evidence. The accepted method replays the **net final fork delta** from `v0.4.12..d5ec9569e` with a three-way base, then resolves each overlap against current upstream structures. This avoids reintroducing intermediate Workload Assertion and delegated-merge HTTP authority that later hard-cut commits retired.

| Prior commit(s) | Classification | Replay decision |
|---|---|---|
| `e9ec09a2c`–`54751bb35` | keep / rework | Retain external-PR facts, leaf completion policy, process-tree supervision, self-host storage/build contracts and current capability docs; rework them into current upstream handlers, CI and self-host scripts. Historical deployment-acceptance prose remains historical rather than becoming current runtime proof. |
| `55f8c9643`–`7dd9148b5` | retire live authority / keep audit schema | Do not restore Workload Assertion, public delegation, delegated merge or legacy gateway routes. Retain historical delegation tables only for audit and explicit workspace cleanup. |
| `f93d310a2`–`d60f3ee03` | keep where not superseded | Preserve CLI rerun/error-safety semantics where the final delta still differs from upstream; otherwise accept current upstream implementation. |
| `c1d0e75db` | keep / rework | Retain server-selected current execution context on current upstream task, run and handler structures. |
| `0aa42b0fd` | keep / rework | Retain Access Grant-only hard cut, external-PR link token and exact retired-route `404` contract; never restore Session/assertion/gateway fallback. |
| `d5ec9569e` | keep | Retain minimum 32-byte link-token secret, readiness documentation correction and formal-authority safeguards. |

## Conflict decisions

- CI/Makefile/self-host scripts: current upstream structure wins; fork branch coverage, CLI-version admission, bind-owned uploads, retired-key rejection, Helm secret boundaries and exact build inputs are re-applied.
- GitHub integration translations: current upstream generic GitHub guide wins. Fork-only provider completion details remain authoritative in `docs/features/fork/external-pr-integration/README.md` and the built-in source map rather than replacing newer generic documentation.
- Issue updates: current upstream channel-media description locking and fork completion/topology serialization are composed in one transaction for combined status/parent/description writes.
- Issue and workspace deletion: current upstream application-owned deletion graph remains; fork provider/completion locks and historical external-PR/delegation cleanup are inserted into the same transaction.
- Pi cancellation: current upstream concurrent stdin handling remains; fork process-group TERM→KILL cancellation is composed with it.
- sqlc output: query sources are merged first, then all generated output is regenerated from the resulting schema and queries.
- Locale JSON: fork-owned key deltas are recursively overlaid onto current upstream dictionaries; unrelated upstream keys remain.

## Migration boundary

Frozen upstream owns migrations through `264`. The old fork used `231`–`250`, which now collide numerically with upstream. This generation allocates the replay migrations to the new non-overlapping range `265`–`284`:

- external-PR reconciliation and indexes: `265`–`277`;
- retired delegated-merge audit schema and indexes: `278`–`284`.

Migration `278` is idempotent for databases that already applied the old fork filenames: columns and audit tables use `IF NOT EXISTS`. A production dump upgrade must pass before runtime switching. Migration `275` is the forward-only index-reconciliation fence; rollback across this generation requires the recorded database restore, not image replacement alone.

## Safety contracts

The final source must prove:

- `MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET` is trimmed and at least 32 bytes;
- current execution context and link-token routes remain task-bound and grant no repository authority;
- retired Workload Assertion, delegated Session/merge and legacy gateway routes are absent and return `404`;
- runtime configuration exposes no retired assertion/delegation keys;
- backend and frontend are built from one exact clean commit;
- each host retains its prior database, env, CLI, image and compose rollback evidence.

## Deployment and rollback

Deployment is a separate transition after exact-head CI and source acceptance. Both mini and imile-win must run the same exact source in backend, frontend, CLI and daemon. Preserve Postgres, uploads bind mounts, network identity and `imile-win` logical device name.

Rollback authority is `fork/v0.4.12@d5ec9569ede6e48e3caced031254259f48b83f41` plus the pre-change image digests, CLI bytes, owner-only runtime configuration and database dumps recorded by the deployment receipt. Because migrations `265`–`284` cross a forward-only boundary, a full rollback restores the corresponding database dump.

## Claim limit

This manifest records **historical** generation identity and prior live tip intent only. Current formal home is [`v0.4.22.md`](v0.4.22.md). This file does not prove CI, source acceptance, publication, deployment, health, route behavior, or that live hosts have moved off `b3118a340`; those remain separate receipts.
