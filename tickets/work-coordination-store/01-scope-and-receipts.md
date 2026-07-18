# V1 — Scope、revision 与 request-hash receipts

**Blocked by:** initial frozen base、migration ceiling重检与[共享交付门](README.md#每片共享交付门)。
**Blocks:** [02-canonical-dependencies.md](02-canonical-dependencies.md)；只有V1 accepted head、独立review和exact-head CI记录完成后才解除。

## Objective

交付第一个可独立使用的passive vertical slice：创建/读取root coordination scope，并以server-stamped actor/task、canonical request hash和持久receipt证明ensure幂等。该片从DB贯通到service、workspace API、`multica coordination scope` CLI和初版built-in skill，但不创建dependency/blocker，不实现inspect或调度副作用。

## Exact owning modules

- migrations：`coordination_scope`、`coordination_receipt`的structure、concurrent indexes、constraint attach及down；prefix按实施时ceiling重算。
- `server/pkg/db/queries/coordination.sql`与sqlc generated files。
- `server/internal/service/coordination*.go`：scope/receipt types、errors、service与tests。
- `server/internal/handler/coordination*.go`、`handler.go` wiring、`server/cmd/server/router.go`。
- `server/cmd/multica/cmd_coordination.go`及tests；`server/internal/cli/client.go`、`errors.go`、顶层JSON error rendering所需的`main.go`/`help.go`最小seam及tests。
- `server/internal/service/builtin_skills/multica-work-coordination/**`与embed/source-map tests。
- Issue/Workspace删除入口的**只读deletion guard接线**及回归测试；不重构删除transaction，不做cleanup。
- `docs/fork-features/work-coordination-store/README.md`与registry，只声明V1事实。

不得修改`001_init`、dependency业务、blocker、Autopilot、Stage、Issue status/assignee/comment、Agent Kit或UI。

## Schema contract

### `coordination_scope`

| Column | Contract |
| --- | --- |
| `id UUID NOT NULL` | opaque API identity；物理PK按concurrent-index序列绑定 |
| `workspace_id UUID NOT NULL` | tenant key；无FK |
| `scope_kind TEXT NOT NULL` | V1 CHECK仅允许`root` |
| `state TEXT NOT NULL` | V1 CHECK仅允许`active` |
| `root_issue_id UUID NOT NULL` | application-validated实际root；无FK |
| `workflow_profile_key TEXT NOT NULL` | 1-128 chars，`^[a-z0-9][a-z0-9._-]{0,127}$` |
| `revision BIGINT NOT NULL DEFAULT 0` | CHECK `0 <= revision <= MaxInt64` |
| creation provenance | `created_by_type/id`、nullable `created_task_id`、`created_at`、`updated_at`，成组CHECK |

约束/index：

- active natural key：`(workspace_id,root_issue_id,workflow_profile_key)`唯一；
- workspace/root lookup；
- `id` PK index；
- 全部index独立`CREATE ... INDEX CONCURRENTLY`，禁止inline PK/UNIQUE；随后`ADD CONSTRAINT ... USING INDEX`。

不增加goal/policy JSON、parent scope、controller、archive字段或future nullable占位。

### `coordination_receipt`

至少包含：

- `id UUID NOT NULL`、`workspace_id UUID NOT NULL`、nullable `coordination_scope_id`（ensure创建前可空）；
- `operation`（V1只允许`ensure_scope`）；
- `idempotency_key` 1-200 chars；
- `request_hash BYTEA`，固定32 bytes SHA-256；
- `resource_type='scope'`、`resource_id`；
- `revision_before/after`非负`BIGINT`且after不小于before；
- bounded `result_snapshot JSONB`，object且最多16KiB；
- server-stamped actor/task provenance与`created_at`。

`(workspace_id,idempotency_key)`全局唯一；operation不能进入unique key，否则同key换operation无法fail closed。receipt是replay authority，常规应用rollback不得清空。

## Migration contract

按[README migration序列](README.md#migration-可执行序列)拆文件：

1. structure：无inline PK/UNIQUE；
2. 每个PK/natural/lookup index独立concurrent migration；
3. PK/适用unique constraint attach；
4. reverse down先drop constraint，index down使用`DROP INDEX CONCURRENTLY IF EXISTS`，最后drop tables。

测试必须覆盖空库up/down/up、constraint/index形态、migration lint和runner真实执行。V1不触碰legacy `issue_dependency`。

## sqlc contract

`coordination.sql`至少提供：

- create scope；get by workspace+ID；get active by natural key；lock scope；CAS increment primitive（V1暂不暴露mutation使用，但V2必须复用）；
- workspace-scoped actual-root parent-chain validation query；
- get/insert receipt与saved result；
- deletion guard：scope root、receipt scope/resource对Issue的引用，以及workspace是否存在任何scope/receipt。

所有lookup显式带`workspace_id`，不能只按UUID。

## Service contract

Public typed methods：

```text
EnsureScope(ctx, actor, input) -> MutationResult[Scope]
GetScope(ctx, actor, scopeID) -> Scope
GetScopeByRoot(ctx, actor, rootIssueID, workflowProfileKey) -> Scope
CheckIssueDeletionAllowed(ctx, actor, workspaceID, issueID) -> error
CheckWorkspaceDeletionAllowed(ctx, actor, workspaceID) -> error
```

Handler不得直查coordination tables。所有read和guard均经过同一workspace/task authority seam。

### `CoordinationActor`

只含server-derived：`WorkspaceID`、`ActorType(member|agent)`、`ActorID`、nullable `TaskID`。业务input不得包含workspace/actor/agent/task字段。

Task actor必须由`X-Actor-Source=task_token`可信stamp建立，通过workspace-scoped task query加载；task必须有issue。沿parent chain解析实际root，missing/cross-workspace/cycle均fail closed。Member authority来自已验证workspace membership，但service仍逐row校验tenant。

### Ensure algorithm

1. strict validate key、root、profile与typed input；root必须是workspace内实际parent-chain root。
2. canonical hash覆盖operation、workspace/root/profile和server actor/task；不含timestamp/display data。
3. transaction内重新执行当前membership/task/root authority；receipt不是授权缓存，revoke/expiry/authority loss必须先拒绝。
4. 再查`(workspace,key)`：同operation/hash/actor/task replay原receipt/result；不同则`coordination_idempotency_conflict`。
5. 并发natural-key ensure依靠unique index收敛，loser reload现有scope。
6. 新scope revision=0；已有scope以新key创建no-op receipt，revision不变。
7. 保存bounded result snapshot与receipt后commit。

已授权replay发生在current-state/CAS检查前，使成功响应丢失后仍可取得首次结果；但它始终发生在当前authorization之后。V1不允许任何revision mutation；V2开始使用CAS primitive。

## API contract

Routes位于现有authenticated + `RequireWorkspaceMember` group；静态`/by-root`先于`/{scopeId}`注册：

```text
POST /api/coordination/scopes
GET  /api/coordination/scopes/by-root?root_issue_id=<uuid>&workflow_profile_key=<key>
GET  /api/coordination/scopes/{scopeId}
```

POST要求`Idempotency-Key` header，body仅：

```json
{"root_issue_id":"<uuid>","workflow_profile_key":"matt-loop"}
```

使用`DisallowUnknownFields`并拒绝trailing第二个JSON value；任何客户端身份字段按unknown field拒绝。首次创建201；existing/no-op/replay 200；body均含saved receipt+scope。

Stable envelope：

```json
{"error":{"code":"coordination_invalid_payload","message":"..."}}
```

V1至少实现`coordination_not_found`、`cross_workspace`、`forbidden`、`invalid_payload`、`idempotency_conflict`、`delete_blocked`。message不得含SQL、constraint、payload原文或路径。

## CLI 与初版 built-in skill

Commands：

```text
multica coordination scope ensure --root <issue-ref> --workflow-profile <key> --idempotency-key <key> [--output json|table]
multica coordination scope get (--scope <uuid> | --root <issue-ref> --workflow-profile <key>) [--output json|table]
```

默认JSON。CLI复用现有issue-ref resolver；缺key/非法flag在零HTTP请求前失败。Revision类型统一为非负`int64`。

Structured product error必须保留stable code。`--output json`失败时stderr只有一个JSON value且无额外prose；为此新增可unwrap/already-rendered product error或让顶层main统一按output mode渲染，并用顶层执行helper/子进程测试验证stderr与exit code。旧server/string body继续按HTTP status安全fallback。

初版`multica-work-coordination` built-in skill只介绍scope ensure/get、idempotency、server identity、passive/未提供dependency等claim limit。Supporting source map引用实施后的真实symbol/route/migration，不能把ticket预期路径当证据。

## Deletion guard

V1不删除Store rows。Issue是scope root或被receipt引用、Workspace仍有scope/receipt时，existing delete入口必须在task cancellation、Autopilot变化或event发布前返回`coordination_delete_blocked`。Guard是read-only；lifecycle cleanup/archive延后。

## Acceptance / tests

必须证明：

1. migration fresh up/down/up、PK/concurrent-index序列、lint、sqlc二次生成无drift；
2. ensure串行/并发同natural key只产生一个scope；
3. same-key same-hash exact replay；same-key不同profile/actor/task conflict；member revoke、task expiry/revoke或root authority loss后same-key replay仍被拒绝；
4. actual-root validation：child、cross-workspace parent、missing/cycle拒绝；
5. member与合法issue-bound task；普通PAT/JWT伪造agent/task headers不能提升authority；无issue task拒绝；
6. API unknown/identity fields、trailing JSON、tenant边界和safe errors；
7. CLI exact request、zero-request validation、JSON stdout/stderr与top-level exit；
8. built-in embed/frontmatter/source-map存在性；
9. before/after Issue status/assignee/comment/task/Autopilot计数不变；
10. deletion guard触发时delete链任何既有副作用均未发生。

Commands从仓库根运行：

```bash
make sqlc
(cd server && go test ./internal/migrations ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica)
(cd server && go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica)
make build
make test
git diff --check
```

DB tests必须明确显示实际执行，不能把skip当通过。

## Non-goals

Dependency、blocker、inspect、Store cleanup/archive、wake/control、Goal authority、Agent Kit、UI与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留additive schema/receipt；仅空Store且明确批准时在disposable DB验证down。Evidence记录exact base/head、migration清单、sqlc drift、service/API/CLI fixtures、review/CI和fork narrative。

V1最多声明scope+receipt vertical slice在source tests中通过；不得声明dependency/blocker/inspect、live deployment、scheduling authority或future control能力。
