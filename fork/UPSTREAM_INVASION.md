# Upstream invasion inventory

This file records product and build surfaces that remain outside the additive `fork/` deployment boundary. The boundary reduces conflict probability; it does not make fork behavior a plugin.

The inventory is measured from frozen baseline `11bd18a50794eb013061f33783dd20dcc14f8c3c` to accepted runtime source `1ac10890c1bc27814776a877aea2ecbf3ee6baf7`. That delta changed 52 files: 33 additions and 19 modifications to paths that already existed in the baseline. Twenty-one changed files live under `fork/`; the other additive governance files live under `.agents/` and `.github/`.

## Existing upstream paths modified

Offline web build and styling:

- `apps/web/app/(landing)/layout.tsx`
- `apps/web/app/custom.css`
- `apps/web/app/globals.css`
- `apps/web/app/layout.tsx`
- `apps/web/package.json`
- `pnpm-lock.yaml`

External PR authority and integration hooks:

- `packages/core/types/github.ts`
- `server/cmd/migrate/main.go`
- `server/cmd/server/health.go`
- `server/cmd/server/health_test.go`
- `server/cmd/server/main.go`
- `server/cmd/server/router.go`
- `server/internal/handler/github.go`
- `server/internal/handler/issue_child_done.go`
- `server/internal/handler/workspace_delete_manifest_test.go`

AGS current execution context:

- `server/internal/middleware/auth.go`
- `server/internal/middleware/auth_test.go`
- `server/pkg/db/queries/task_token.sql`
- `server/pkg/db/generated/task_token.sql.go`

Against upstream `d4a712abf3880dfbd3daeac5daac1bd4bfb39b6f`, five fork paths also changed upstream: `apps/web/package.json`, `pnpm-lock.yaml`, `server/cmd/migrate/main.go`, `server/cmd/server/main.go`, and `server/cmd/server/router.go`. A read-only `git merge-tree` reported zero textual conflicts, but these five paths still require semantic review.

Reproduce and refresh the comparison with:

```bash
bash fork/scripts/audit-convergence.sh \
  --previous fork/v0.4.22 \
  --upstream upstream/main \
  --source HEAD
```

Do not optimize the count by moving code into artificial wrappers. Remove an invasion only when upstream provides a stable extension point or the fork capability can be retired.
