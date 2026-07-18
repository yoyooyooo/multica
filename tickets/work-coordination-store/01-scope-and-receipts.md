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
- Issue单删/批删、Workspace删除入口的**lock-held deletion guard seam**及回归测试：允许引入dedicated connection/session lock并让最终entity delete transaction使用该connection，但不实现Store cleanup，也不宣称既有外部副作用可rollback。
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

- active natural key：partial unique index固定为`(workspace_id,root_issue_id,workflow_profile_key) WHERE state='active'`；它不能attach为UNIQUE constraint，保留为named unique index；
- workspace/root lookup；
- `id` PK index；
- 全部index独立`CREATE ... INDEX CONCURRENTLY`，禁止inline PK/UNIQUE；只有非partial PK/适用unique随后`ADD CONSTRAINT ... USING INDEX`。

不增加goal/policy JSON、parent scope、controller、archive字段或future nullable占位。

### `coordination_receipt`

至少包含：

- `id UUID NOT NULL`、`workspace_id UUID NOT NULL`、`coordination_scope_id UUID NOT NULL`（scope与receipt在同一transaction中先后写入；无FK）；
- `operation TEXT`（DB只做1-64 chars非空/长度CHECK，不做跨slice enum CHECK；V1 service allowlist只允许`ensure_scope`）；
- `idempotency_key` 1-200 chars；
- `request_hash BYTEA`，固定32 bytes SHA-256；
- `resource_type TEXT`（DB只做1-32 chars非空/长度CHECK；V1 service allowlist只允许`scope`）、`resource_id`；
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
- 统一workspace coordination advisory xact lock，namespace/key算法在V1冻结，V2-V5及删除路径必须复用；
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
AcquireIssueDeletionGuard(ctx, actor, workspaceID, issueIDs[]) -> DeletionGuardHandle
AcquireWorkspaceDeletionGuard(ctx, actor, workspaceID) -> DeletionGuardHandle
```

Handler不得直查coordination tables。所有read和guard均经过同一workspace/task authority seam。`DeletionGuardHandle`由service取得并独占`*sql.Conn`，只暴露在该connection上开始实际delete transaction及`Finish`；handler不得换回pool执行entity delete，也不得在delete完成/失败前调用`Finish`。

### `CoordinationActor`

只含server-derived：`WorkspaceID`、`ActorType(member|agent)`、`ActorID`、nullable `TaskID`。业务input不得包含workspace/actor/agent/task字段。

Task actor必须由`X-Actor-Source=task_token`可信stamp建立，通过workspace-scoped task query加载；task必须有issue。沿parent chain解析实际root，missing/cross-workspace/cycle均fail closed。Member authority来自已验证workspace membership，但service仍逐row校验tenant。

### V1 receipt与Ensure algorithm

V1 typed allowlist只含`operation=ensure_scope`、`resource_type=scope`。按[canonical receipt hash SSoT](README.md#canonical-receipt-hash-ssot)，canonical document中的`request`精确为：

```json
{"root_issue_id":"<lowercase UUID>","workflow_profile_key":"matt-loop"}
```

测试冻结完整canonical JSON bytes与SHA-256 digest，并证明UUID大小写只normalize、不改变digest；profile、actor或task变化会改变digest。DB只校验operation/resource_type非空与长度，service写入/读取receipt时都拒绝V1 allowlist外的值。

Ensure顺序：

1. strict validate key、root、profile与typed input；root必须是workspace内实际parent-chain root。
2. 构造上述version 1 canonical hash；不含timestamp/display data/idempotency key。
3. transaction内重新执行当前membership/task/root authority；receipt不是授权缓存，revoke/expiry/authority loss必须先拒绝。
4. 按[workspace lock SSoT](README.md#workspace-coordination-advisory-lock-ssot)取得统一workspace coordination advisory xact lock；该lock先于scope/resource row lock。
5. 再查`(workspace,key)`：同operation/hash/actor/task replay原receipt/result；不同则`coordination_idempotency_conflict`。
6. 持锁复核Workspace/root Issue仍存在；并发natural-key ensure依靠unique index收敛，loser reload现有scope。
7. 新scope revision=0；已有scope以新key创建no-op receipt，revision不变。
8. 保存bounded result snapshot与receipt后commit。

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

使用strict decoder：`DisallowUnknownFields`、duplicate object key detection并拒绝trailing第二个JSON value；任何客户端身份字段按unknown field拒绝。首次创建201；existing/no-op/replay 200；body均含saved receipt+scope。

Stable envelope：

```json
{"error":{"code":"coordination_invalid_payload","message":"...","details":{}}}
```

V1实现README SSoT中的`coordination_not_found`、`coordination_cross_workspace`、`coordination_forbidden`、`coordination_invalid_payload`、`coordination_idempotency_conflict`、`coordination_delete_blocked`。HTTP/CLI映射只引用README表；message不得含SQL、constraint、payload原文或路径。

## CLI 与初版 built-in skill

Commands：

```text
multica coordination scope ensure --root <issue-ref> --workflow-profile <key> --idempotency-key <key> [--output json|table]
multica coordination scope get (--scope <uuid> | --root <issue-ref> --workflow-profile <key>) [--output json|table]
```

默认JSON。CLI复用现有issue-ref resolver；缺key/非法flag在零HTTP请求前失败。Revision类型统一为非负`int64`。

Structured product error必须保留stable code。`--output json`失败时stderr只有一个JSON value且无额外prose；为此新增可unwrap/already-rendered product error或让顶层main统一按output mode渲染，并用顶层执行helper/子进程测试验证stderr与exit code。旧server/string body继续按HTTP status安全fallback。

初版`multica-work-coordination` built-in skill只介绍scope ensure/get、idempotency、server identity、passive/未提供dependency等claim limit。Supporting source map引用实施后的真实symbol/route/migration，不能把ticket预期路径当证据。

## Lock-held deletion guard

V1实现并复用[workspace coordination advisory-lock SSoT](README.md#workspace-coordination-advisory-lock-ssot)，不删除Store rows。

- 单Issue删除、BatchDeleteIssues、Workspace删除各自在任何cache/task/Autopilot/event副作用前取得dedicated connection及冲突的session-level workspace lock；guard与最终entity delete transaction使用该connection，release只发生在实际DB delete commit/rollback或失败之后。不得保留瞬时`CheckIssueDeletionAllowed`/`CheckWorkspaceDeletionAllowed` API。
- Batch先解析全部实际目标并确认单workspace，再按UUID byte order锁row并一次性guard；不得逐项check后释放lock。Workspace删除不得先移除membership或invalidate cache。
- Guard在session lock持有期间检查scope root/receipt引用；拒绝返回`coordination_delete_blocked`并保证cache/task/Autopilot/event零变化。Ensure在冲突xact lock内复核Workspace/root仍存在。
- `defer`必须在同一session显式unlock并验证成功；panic/crash/connection close依赖PostgreSQL自动释放。测试包含unlock失败/connection close的pool污染保护。

该合同只声明guard rejection零副作用，以及Store refs/Issue/Workspace DB rows不会因并发TOCTOU形成**新**orphan。Guard通过后既有delete流程若在实际entity delete前后失败，可能留下task/Autopilot/event债；第一波不声称这些外部副作用随DB rollback恢复。

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
10. `ensure_scope/scope` allowlist、canonical JSON与SHA-256 golden vectors；unknown operation/resource_type被service拒绝；
11. Ensure分别与单删、BatchDeleteIssues、Workspace删的真实并发race：要么Store写成功且delete被guard，要么delete提交且Store写因entity不存在而失败；不得产生新orphan；
12. guard触发时cache/task/Autopilot/event均未变化；session lock持有到实际DB delete结束，connection close释放；guard通过后的delete failure不虚构task/Autopilot/event rollback；
13. namespace常量与至少两个workspace UUID→signed int32 key golden vectors；session/xact helpers对同一workspace互斥、不同无碰撞fixture可并行。

Focused Go命令必须从`server` module执行：

```bash
make sqlc
(
  cd server
  WORK_COORDINATION_DB_REQUIRED=1 go test -count=1 -v ./internal/migrations ./cmd/migrate ./internal/service ./internal/handler -run 'WorkCoordination'
  go test ./internal/migrations ./cmd/migrate ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica
  go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica
)
make build
make test
git diff --check
```

第一条verbose DB command必须在DB不可用时non-zero fail，输出实际执行的coordination migration/integration test names；任何skip都使该gate失败。

## Non-goals

Dependency、blocker、inspect、Store cleanup/archive、wake/control、Goal authority、Agent Kit、UI与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留additive schema/receipt；仅空Store且明确批准时在disposable DB验证down。Evidence记录exact base/head、migration清单、sqlc drift、service/API/CLI fixtures、review/CI和fork narrative。

V1最多声明scope+receipt vertical slice在source tests中通过；不得声明dependency/blocker/inspect、live deployment、scheduling authority或future control能力。
