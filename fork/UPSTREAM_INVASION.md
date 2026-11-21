# Upstream integration inventory

The frozen upstream baseline is `e293fca0d3b753f470a6de21c256eae822b4dc34`.
The prior mini runtime generation is `83310cee8d43b14a5187804c93a964dce07546f2`, based on `b7f8efcaae6c5f3273c0038252b5d10c81b41068`.

The current source changes 62 paths: 43 additive files and 19 existing upstream files. The prior generation changed 63 paths, including 21 existing upstream files. Current upstream owns the desktop renderer font imports and CSS; this generation does not replace those two files.

## Existing upstream paths

- `apps/desktop/package.json`
- `apps/web/app/font-stacks.test.ts`
- `apps/web/app/globals.css`
- `apps/web/app/layout.tsx`
- `apps/web/package.json`
- `packages/core/types/github.ts`
- `pnpm-lock.yaml`
- `pnpm-workspace.yaml`
- `server/cmd/migrate/main.go`
- `server/cmd/server/health.go`
- `server/cmd/server/health_test.go`
- `server/cmd/server/main.go`
- `server/cmd/server/router.go`
- `server/internal/handler/github.go`
- `server/internal/handler/workspace_delete_manifest_test.go`
- `server/internal/middleware/auth.go`
- `server/internal/middleware/auth_test.go`
- `server/pkg/db/generated/task_token.sql.go`
- `server/pkg/db/queries/task_token.sql`

## Semantic review of previous overlap

Fifteen paths changed both in the prior fork and in the intervening upstream history. A read-only merge-tree against that old generation reports conflicts in the desktop package manifest, workspace catalog and lockfile. The new generation resolves the contracts from the frozen upstream tree:

| Area | Decision and evidence |
| --- | --- |
| Web and desktop fonts, package manifests, catalog, lockfile | Preserve current upstream appearance/CJK/locale behavior; use four local Fontsource packages. Shared dependency versions remain unchanged. The real font-stacks regression checks all eight appearance stacks. Existing upstream desktop renderer source remains untouched. |
| Migration entry point | Keep current upstream startup retries and migration hooks; wrap only the independent fork ledger. Applied `001_external_pr_authority` up/down files remain byte-identical to both deployed generations. |
| Server scheduling and routes | Register external PR reconciliation beside the upstream jobs. Keep service-token callbacks outside user auth and current-context reads inside the current auth group. |
| PR types and issue detail | Add AGS detail rows to the current GitHub/VCS result and retain `auto_complete`. New board/list/table batch summaries remain upstream-owned. |
| Workspace deletion coverage | Retain fork-table manifest coverage; do not rewrite existing historical rows during deployment. Census records historical orphans separately. |
| Authentication and current context | Clear caller-supplied token-hash headers; populate only after successful task-token authentication. Lock the running task and token at the context read. Exact router regression covers running 200 then terminal 401. |
| Parent completion | Keep upstream child-event wakeup and project-access filtering. Pass current `projectaccess.Hidden` shape for the same system broadcast pattern used upstream. Crash retry emits one closed child event and no inline parent task. |

## Source and build checks

`audit-convergence.sh` compares each generation against its own base. The new generation has no merge commits after the frozen upstream SHA and no textual conflicts against that SHA. These are lineage properties; semantic compatibility is established separately by regeneration, focused contract tests, the exact-source gate, and restored production-database upgrades.

sqlc output is regenerated with the upstream-pinned v1.31.1. The build boundary follows current upstream Go 1.27.4, Node 22.23.4 and OpenSSL floors, copies the newly required ee/plugin-sdk/vitest-setup workspace inputs, and includes local font licenses. Deployment tooling preserves runtime data and records immutable image/CLI identities, database restore evidence and per-target results.
