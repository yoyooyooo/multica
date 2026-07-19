# Work Coordination Source Map

## Scope, receipt, and dependency surface

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
server/migrations/211_coordination_dependency_structure.up.sql
server/migrations/212_coordination_dependency_pk_index.up.sql
server/migrations/213_coordination_dependency_active_pair_index.up.sql
server/migrations/214_coordination_dependency_scope_list_index.up.sql
server/migrations/215_coordination_dependency_downstream_index.up.sql
server/migrations/216_coordination_dependency_upstream_index.up.sql
server/migrations/217_coordination_dependency_attach_constraints.up.sql
server/pkg/db/queries/coordination.sql
server/pkg/db/queries/issue.sql
server/pkg/db/queries/member.sql
server/pkg/db/queries/task_token.sql
server/pkg/db/queries/workspace.sql
server/internal/service/coordination.go
server/internal/service/coordination_dependency.go
server/internal/service/coordination_delete.go
server/internal/handler/coordination.go
server/internal/handler/coordination_dependency.go
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
POST /api/coordination/scopes/{scopeId}/dependencies
GET /api/coordination/scopes/{scopeId}/dependencies
POST /api/coordination/scopes/{scopeId}/dependencies/{dependencyId}/resolve
DELETE /api/issues/{id}
POST /api/issues/batch-delete
DELETE /api/workspaces/{id}
service.CoordinationService.EnsureScope
service.CoordinationService.GetScope
service.CoordinationService.GetScopeByRoot
service.CoordinationService.AddDependency
service.CoordinationService.ListDependencies
service.CoordinationService.ResolveDependency
service.CoordinationService.AcquireIssueDeletion
service.IssueDeletionHandle.TargetIssueIDs
service.IssueDeletionHandle.Delete
service.IssueDeletionHandle.Finish
service.CoordinationService.AcquireWorkspaceDeletion
service.WorkspaceDeletionHandle.Delete
service.WorkspaceDeletionHandle.Finish
handler.Handler.EnsureCoordinationScope
handler.Handler.AddCoordinationDependency
handler.Handler.ListCoordinationDependencies
handler.Handler.ResolveCoordinationDependency
handler.Handler.performIssueDeletion
handler.Handler.performWorkspaceDeletion
middleware.TaskTokenCredentialRefFromContext
cli.CoordinationProductError
main.prepareCoordinationArgs
main.runCoordinationScopeEnsure
main.runCoordinationScopeGet
main.runCoordinationDependencyAdd
main.runCoordinationDependencyList
main.runCoordinationDependencyResolve
```

## Verification source

```text
server/internal/migrations/work_coordination_test.go
server/cmd/migrate/work_coordination_test.go
server/internal/service/coordination_dependency_test.go
server/internal/service/coordination_delete_test.go
server/internal/handler/coordination_dependency_test.go
server/cmd/server/work_coordination_router_test.go
server/cmd/multica/cmd_coordination_test.go
server/internal/cli/work_coordination_errors_test.go
server/internal/service/coordination_skill_test.go
scripts/test-work-coordination-db-required.sh
```

Contracts:

- migration tests cover V1–V2 up/down/up and preserve legacy `issue_dependency`;
- DB-backed service tests cover lifecycle, replay/no-op, owner scope, cycle safety, pagination, capacity, concurrency, and exact Agent authority;
- handler/router tests cover strict wire shape and Issue/Batch/Workspace guards;
- CLI tests cover exact requests, output modes, validation, route/code classification, and stable exits;
- the DB-required harness runs coordination-focused tests with `WORK_COORDINATION_DB_REQUIRED=1`, treats skip as failure, and requires at least one passing `TestWorkCoordination*` event per owning package.
