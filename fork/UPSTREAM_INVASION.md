# Upstream invasion inventory

This generation is based on upstream `2df765a3c8f39789c9fb76316378bcffc20d22d9`
(`v0.5.0`). The [generation manifest](releases/v0.5.0-main.20260918.md) records
capability decisions and separate source/deployment acceptance states.

`fork/` is an additive build/deployment boundary, not a product plugin. Existing
upstream paths below remain intentional integration points. Generated sqlc and
lock files count as modified upstream paths and must be regenerated.

## Existing upstream paths

Offline font assets and locale-aware fallback:

- `apps/web/app/(landing)/layout.tsx`
- `apps/web/app/custom.css`
- `apps/web/app/globals.css`
- `apps/web/app/layout.tsx`
- `apps/web/package.json`
- `pnpm-lock.yaml`

External PR authority, migration entry point, and readiness:

- `packages/core/types/github.ts`
- `server/cmd/migrate/main.go`
- `server/cmd/server/health.go`
- `server/cmd/server/health_test.go`
- `server/cmd/server/main.go`
- `server/cmd/server/router.go`
- `server/internal/handler/github.go`
- `server/internal/handler/issue_child_done.go`
- `server/internal/handler/workspace_delete_manifest_test.go`

Task-token-bound AGS execution context:

- `server/internal/middleware/auth.go`
- `server/internal/middleware/auth_test.go`
- `server/pkg/db/queries/task_token.sql`
- `server/pkg/db/generated/task_token.sql.go`

## Semantic review against v0.5.0

The previous generation's overlap with the new baseline is not resolved by
accepting an automatic merge. Each integration is rebuilt against its current
primitive:

- Keep upstream's locale hydration and `HTML_LANG` extraction; replace only
  remote font loading. Regenerate the lockfile with the repository pnpm version.
- Keep migration notices, conditional index retirement, ordering and retry
  behavior. Separate fork options live in an additive file and call the upstream
  runner; applied fork 001 is byte-for-byte unchanged.
- Preserve upstream session renewal and anti-forgery header stripping. Only a
  verified task token sets the additional internal token hash.
- Preserve upstream cancellation-aware parent messages, staged barriers,
  fail-closed custom-status resolution, and derived-origin trigger attribution.
  Add only stable comment identity for durable External PR retries.
- Upstream migrations 462/468 remove reference-only PR associations and their
  columns. External reconciliation checks remaining GitHub, VCS and AGS
  open/draft associations without querying the retired column.
- Keep upstream's scheduler and route middleware; register only the external
  callbacks and authenticated context endpoint.
- Include upstream's maintenance executable in the additive backend image.

The old fork 001 uses legacy foreign keys, non-concurrent indexes and cleanup
triggers. These are preserved applied history, not newly introduced schema.
Deletion tests must continue to cover the retained cleanup contract. Future
schema changes must use application-owned cleanup and concurrent index files.

## Reproduction

```bash
bash fork/scripts/verify-source.sh
bash fork/scripts/audit-convergence.sh \
  --previous fork/v0.4.37-main.20260901 \
  --upstream upstream/main \
  --source HEAD
```

Refresh exact-head metrics in the release manifest after validation. A zero
text-conflict count is only textual evidence; it does not replace the semantic
review above or the DB-backed regression and upgrade-restore tests.
