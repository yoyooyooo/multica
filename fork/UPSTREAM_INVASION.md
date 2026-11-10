# Upstream invasion inventory

This generation is based on upstream `b7f8efcaae6c5f3273c0038252b5d10c81b41068`
(`v0.8.2`). The [generation manifest](releases/v0.8.2-main.20261111.md) records
capability decisions. Source is not deployed. Image prefix for a later build is
`multica-fork-v082-20261111`; this generation does not publish images.

`fork/` is an additive build/deployment boundary, not a product plugin. Existing
upstream paths below remain intentional integration points. Generated sqlc and
lock files count as modified upstream paths and must be regenerated.

## Existing upstream paths

Offline font assets and locale-aware fallback:

- `apps/web/app/globals.css`
- `apps/web/app/layout.tsx`
- `apps/web/package.json`
- `apps/desktop/package.json` (catalog specifier only; desktop already loaded these files)
- `pnpm-workspace.yaml`
- `pnpm-lock.yaml`

Not modified, after review:

- `apps/web/app/(landing)/layout.tsx` — v0.8.2 no longer loads Instrument Serif here.
- `apps/web/app/custom.css` — no remote display face remains to replace.

External PR authority, migration entry point, and readiness:

- `packages/core/types/github.ts`
- `server/cmd/migrate/main.go`
- `server/cmd/server/health.go`
- `server/cmd/server/health_test.go`
- `server/cmd/server/main.go`
- `server/cmd/server/router.go`
- `server/internal/handler/github.go`
- `server/internal/handler/workspace_delete_manifest_test.go`

Not restored:

- `server/internal/handler/issue_child_done.go` — deleted by v0.8.2. Parent
  continuation is `issue_child_event` plus the child_done wakeup. The durable
  comment lives in additive `server/internal/handler/external_pr_parent.go`.

Task-token-bound AGS execution context:

- `server/internal/middleware/auth.go`
- `server/internal/middleware/auth_test.go`
- `server/pkg/db/queries/task_token.sql`
- `server/pkg/db/generated/task_token.sql.go`

These four files still exist on v0.8.2 and the v0.5.0 hooks still apply.

## Semantic review against v0.8.2

The previous generation is evidence, not a patch. Each integration is rebuilt
on the current primitive:

- Keep `getRequestLocale` and `HTML_LANG`. Replace `next/font/google` in the web
  root layout with the Fontsource cuts desktop already ships: Inter optical size
  plus italic, Source Serif 4 plus italic, Geist Mono, and JetBrains Mono.
  `--font-sans` still defers to Appearance `--font-ui` / `--font-cjk`, and the
  `ja` and `zh-TW` CJK overrides stay in static CSS. Landing has no Google
  display face left, so Instrument Serif is not reintroduced. Shared font
  packages move onto the pnpm catalog because desktop already declared them.
- The migration runner already accepts a ledger table and advisory lock. Fork
  options in an additive file call that runner after upstream. Applied
  `001_external_pr_authority` stays byte-for-byte, including its historical
  foreign keys, non-concurrent indexes, and cleanup triggers. Those are applied
  history, not a new schema. New fork schema must not add foreign keys or
  cascading actions.
- Session renewal and anti-forgery stripping stay. Only the verified `mat_`
  branch sets `X-Task-Token-Hash`. The execution-context read locks that hash
  against the running task row.
- `UpdateIssueStatus` fires `record_issue_child_event`. The platform sweeper
  owns the parent assignee run. External PR completion still writes one system
  comment whose id is the reconcile work id, and retries reuse it on unique
  violation. The comment keeps the cancellation warning (`cancelled` / `confirm`)
  and fail-closed custom-status resolution through `issuestatus.Resolver`. It
  does not call the removed comment-triggered dispatcher; that would wake the
  parent twice.
- Issue PR listing still returns the v0.8.2 `auto_complete` decision. External
  rows are appended after GitHub and VCS rows. Open/draft checks use the
  current GitHub, VCS, and external link tables.
- Scheduler registration and route middleware stay. The fork adds the reconcile
  job, the service-token callbacks, and the authenticated context route.
- Readiness counts `fork_schema_migrations` only after the upstream ledger matches.
- Workspace teardown classifies the three external tables as delete and
  `fork_schema_migrations` as keep. `external_pr_reconcile_finalization` is
  optional evidence from older deployments; 001 does not create it.

## Reproduction

```bash
bash fork/scripts/verify-source.sh
bash fork/scripts/audit-convergence.sh \
  --previous origin/fork/v0.5.0-main.20260918 \
  --upstream upstream/main \
  --source HEAD
```

A zero text-conflict count is only textual evidence. It does not replace the
review above or the contract tests.
