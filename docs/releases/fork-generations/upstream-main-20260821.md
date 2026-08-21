# One-time upstream/main fork generation — 2026-08-21

## Status and authority

**Candidate generation** under the rule “formal = latest synced upstream version,” owner-authorized to freeze `upstream/main` rather than exact tag `v0.4.32`.

This file does **not** claim Mini or imile runtime switch. Live Mini remains `fork-mini-v0.4.22-r4@409bdc0ee` until a separate deployment receipt exists.

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

## Claim limit

This manifest records **candidate** generation identity only. It does not prove CI, source acceptance, publication, Mini/imile deployment, or that live hosts have moved off `409bdc0ee`.
