# Work Coordination Source Map

## Scope and receipt surface

Source:

```text
server/migrations/202_coordination_scope_receipt_structure.up.sql
server/migrations/203_coordination_scope_pk_index.up.sql
server/migrations/204_coordination_scope_active_natural_index.up.sql
server/migrations/205_coordination_scope_workspace_root_index.up.sql
server/migrations/206_coordination_receipt_pk_index.up.sql
server/migrations/207_coordination_receipt_idempotency_index.up.sql
server/migrations/208_coordination_receipt_scope_ordinal_unique_index.up.sql
server/migrations/209_coordination_receipt_scope_ordinal_read_index.up.sql
server/migrations/210_coordination_scope_receipt_attach_constraints.up.sql
server/pkg/db/queries/coordination.sql
server/pkg/db/queries/task_token.sql
server/internal/service/coordination.go
server/internal/service/coordination_delete.go
server/internal/handler/coordination.go
server/internal/handler/coordination_delete.go
server/internal/handler/issue.go
server/internal/handler/workspace.go
server/internal/middleware/auth.go
server/internal/cli/client.go
server/internal/cli/errors.go
server/cmd/server/router.go
server/cmd/multica/cmd_coordination.go
server/cmd/multica/main.go
```

Routes and symbols:

```text
POST /api/coordination/scopes
GET /api/coordination/scopes/{scopeId}
GET /api/coordination/scopes/by-root
DELETE /api/issues/{id}
POST /api/issues/batch-delete
DELETE /api/workspaces/{id}
service.CoordinationService.EnsureScope
service.CoordinationService.GetScope
service.CoordinationService.GetScopeByRoot
service.CoordinationService.AcquireIssueDeletion
service.IssueDeletionHandle.TargetIssueIDs
service.IssueDeletionHandle.Delete
service.IssueDeletionHandle.Finish
service.CoordinationService.AcquireWorkspaceDeletion
service.WorkspaceDeletionHandle.Delete
service.WorkspaceDeletionHandle.Finish
handler.Handler.EnsureCoordinationScope
handler.Handler.performIssueDeletion
handler.Handler.performWorkspaceDeletion
middleware.TaskTokenCredentialRefFromContext
cli.CoordinationProductError
main.prepareCoordinationArgs
main.runCoordinationScopeEnsure
main.runCoordinationScopeGet
```

## DB-required harness

Source:

```text
scripts/test-work-coordination-db-required.sh
```

Contracts:

- the harness runs coordination-focused tests with `WORK_COORDINATION_DB_REQUIRED=1`;
- the harness treats skip as failure;
- the harness requires at least one passing `TestWorkCoordination*` event per package.
