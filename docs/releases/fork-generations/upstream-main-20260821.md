# One-time upstream/main fork generation — 2026-08-21

## Status and authority

**Current formal generation** under the rule “formal = latest synced upstream version,” owner-authorized to freeze `upstream/main` rather than exact tag `v0.4.32`.

Mini was switched on 2026-08-21 to exact source `de66369a4908298ff1e7467b42d28f4b1c63bcf3` (`v0.4.32-3-gde66369a4`) under immutable tag `fork-mini-upstream-main-20260821-r1`. imile-win was switched the same day to the same source under `fork-imile-upstream-main-20260821-r1` (native linux/amd64 build, not Mini arm64 image import).

- generation branch: `fork/upstream-main-20260821`
- integration/work branch: `feature/upstream-main-20260821-fork-replay`
- frozen upstream base: `upstream/main@8f9c766e40d950565efdbc978c512b424e2a8d0d`
- nearest official release: `v0.4.32@d60775aa9394b911b18701a326f655465604e7d1`
- unreleased upstream delta at freeze: 2 commits after `v0.4.32` (`520359a2c` daemon GC, `8f9c766e4` brief status catalog)
- prior accepted fork (rollback): `fork/v0.4.22@409bdc0eed4a93ba86da5a08dab30e93c652ac05` / immutable tag `fork-mini-v0.4.22-r4`
- retirement: open `fork/vX.Y.Z` from the next official tag this fork tracks

History shape:

```text
[upstream/main @ 8f9c766e4]
  feat(fork): replay generation on upstream main   <-- fork-owned tip
```

## Replay method

A 47-commit linear rebase onto `v0.4.32`/`upstream/main` was not used. Upstream independently advanced ~298 commits and reused migration numbers `272`–`316` for unrelated schema.

Accepted method: three-way merge of the **net final fork delta** `v0.4.22..fork/v0.4.22` onto `8f9c766e4` (merge-base `8bd49bba8`), then resolve overlaps against current upstream structures. No merge commit is retained; the result is a linear fork-only tip.

| Prior commit range / surface | Classification | Replay decision |
|---|---|---|
| External PR integration, silent continuation, current-execution-context, completion policy | keep / rework | Keep routes, reconcile worker, link-token, and leaf-child completion. Compose with upstream GitHub/VCS close-intent policy, combined PR aggregates, and `evaluatePullRequestCompletionLocked`. |
| T016–T018 schema `272`–`316` | keep / dual-ledger | Keep current filenames. They do not collide with upstream filenames; Mini already-ledgered fork versions skip, missing upstream `272_*`–`397_*` apply. |
| Workload assertion / delegated merge | retire live authority | Do not restore routes. Historical dual-ledger recovery hooks remain. Retired routes stay exact `404`. |
| Pi process-group cancellation | keep / rework | Compose `configureProcessGroup` + TERM→KILL with upstream `logAgentCommand` / `signalProcessGroup(*exec.Cmd)`. |
| Offline Google Fonts | keep | Copy `apps/web/offline-fonts` and image-build mock. |
| Local CLI installer + version admission | keep / rework | Keep installer and `validate-cli-build-version`; compose with upstream Windows `$(EXE)` suffix on `make build`. |
| AGS task git/gh shims, daemon identity, `MULTICA_RUN_ID` | keep | Replay daemon/task env and execution-context `ExecutionID`. |
| Access-Grant-only cutover | keep | No Session/assertion/gateway fallback. |
| sqlc | rework | Merge query sources, regenerate generated Go. |
| Locales / GitHub generic docs | keep overlay | Upstream dictionaries and generic GitHub guide win; fork keys overlay. |
| CI | keep / rework | Upstream path filters, backend/sqlc/image gates, and Helm/entrypoint tests win; listen to `fork/upstream-main-20260821` and keep fork CLI installer tests. |

## Conflict decisions

- Issue HTTP writes: status/parent use fork `updateIssueSerialized`; description/title/attachments use upstream `updateIssueAtomically`.
- GitHub/VCS terminal completion: keep fork identifier-clear + `evaluatePullRequestCompletionLocked`; keep upstream `closePolicy` narrowing.
- Workspace delete: upstream table manifest plus fork `external_pr_*` tables.
- `cmd/migrate`: keep upstream `concurrentIndexCleanups` / retry / conditions; add fork reconcile fence, dual-ledger hooks, and fork concurrent-index registrations.

## Migration boundary

Frozen upstream owns migrations through `397`. Fork-only files remain `272`–`316` under their current names because the runner keys by full version string, not the numeric prefix.

- Mini already has fork `272_external_pr_*` … `316_*` in `schema_migrations`. Those skip.
- Mini does not have upstream `272_rollup_*` … `397_*`. Those apply forward.
- Fresh installs apply both sets in lexical filename order. Fork tables are independent of plugin/dingtalk/MCP objects.
- Forward-only fences stay: `282`/`275` index reconciliation, `299` dead-authority retirement, `301` external-pr-core. Rollback across them requires the recorded dump.

## Safety contracts

The final source must prove:

- `MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET` is trimmed and at least 32 bytes;
- current execution context and link-token routes remain task-bound;
- retired Workload Assertion / delegated merge / gateway routes return `404`;
- backend and frontend are built from one exact clean commit;
- Mini preserves `multica_pgdata`, network identity, and uploads bind mount.

## Accepted Mini deployment

Mini was switched on 2026-08-21 to exact source `de66369a4908298ff1e7467b42d28f4b1c63bcf3` using immutable tag `fork-mini-upstream-main-20260821-r1`:

- backend local image ID: `sha256:c3a92be6c0aa91475e87ee345295a5e9d95c26ec65f04c68cd6b0aefa9ed30af`;
- frontend local image ID: `sha256:c1a7002cb238fa59c26d0e30ccf8982dfc4dcacb43778b175eae91a6119ab193`;
- both images carry revision `de66369a4`, version `v0.4.32-3-gde66369a4`, and deployment label `fork-mini-upstream-main-20260821-r1`;
- CLI/daemon report the same version; backend `/readyz` reports DB and migrations `ok`; `/` and `/login` return 200;
- retired workload-assertion / delegated-merge routes return exact `404`;
- `multica_pgdata` and `multica_default` identities were preserved;
- migration ledger grew 415 → 532 by applying 117 upstream versions through `397_plugin_installation_package_version_index` while retaining fork ceiling `316_external_pr_reconcile_finalization_primary_key`;
- backend image was built from deploy-local `golang:1.26.6-alpine` because `golang:1.26-alpine` resolved to 1.26.5 against `go.mod` 1.26.6.

Owner-only deployment receipt:

```text
/Users/yoyo/.local/state/multica/deployments/20260821T112728Z-fork-upstream-main-20260821-de66369a4-mini/deployment-receipt.json
SHA-256: fc4f9669581c631067e6b85b58dd55f24cbfd67d69e079433c6676a054d276be
```

Rollback images are the previous live r4 pair (`409bdc0ee`) plus the frozen dump in that receipt directory.

## Accepted imile-win deployment

imile-win was switched on 2026-08-21 to the same exact source `de66369a4` using immutable tag `fork-imile-upstream-main-20260821-r1`:

- backend local image ID: `sha256:a218b4178c29296162dd960a286eaa400c9c9c7fe8e552fdbce2f3d6f04da72e`;
- frontend local image ID: `sha256:8e8bf51dc994b09e9e72c5434fa68c0a9d5cba348e43aef8ca2f822cae043c12`;
- images were built on imile-win as `linux/amd64` from `$DEPLOY/source` at `de66369a4`;
- CLI/daemon report `v0.4.32-3-gde66369a4`; daemon `device_name=imile-win`;
- `/readyz` db+migrations `ok`; `/` and `/login` 200; retired routes 404;
- `multica_pgdata` and `multica_default` preserved;
- ledger 398 → 515 (+117) through `397_plugin_installation_package_version_index`, fork ceiling 316 retained.

Owner-only deployment receipt:

```text
/root/.local/state/multica/deployments/20260821T112728Z-fork-upstream-main-20260821-de66369a4-imile/deployment-receipt.json
SHA-256: 411e5160def8cafe6dd2a1930cd6ded58c3d52287df6972b71003925a96e7510
```

## Claim limit

This document is the **single current formal generation home**. Mini and imile image IDs differ because they are native builds for arm64 vs amd64; both carry revision `de66369a4`. It does **not** claim:

- registry-published image digests exist;
- GitHub default branch has switched;
- browser click-through beyond HTTP 200.
