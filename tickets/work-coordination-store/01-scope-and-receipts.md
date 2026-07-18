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
- Issue单删/批删、Workspace删除入口的**lock-held deletion guard seam**及回归测试；`server/internal/service/task.go`与相关task/autopilot queries允许做窄拆分：delete-only的pre-delete DB cancellation/failure走qtx，metrics/agent reconciliation/cache/S3/event通知走post-commit finalizer。不得改变非delete调用方语义，不实现Store cleanup，也不宣称post-commit副作用可rollback。
- `docs/fork-features/work-coordination-store/README.md`与registry，只声明V1事实。

不得修改`001_init`、dependency业务、blocker、Autopilot产品策略、Stage、Issue status/assignee/comment语义、Agent Kit或UI；只允许把现有delete-only task cancellation/Autopilot failure DB动作改为qtx-bound phase并拆出post-commit finalizer。

## Schema contract

### `coordination_scope`

| Column | Contract |
| --- | --- |
| `id UUID NOT NULL` | opaque API identity；物理PK按concurrent-index序列绑定 |
| `workspace_id UUID NOT NULL` | tenant key；无FK |
| `scope_kind TEXT NOT NULL` | `CHECK (scope_kind = 'root')` |
| `state TEXT NOT NULL` | `CHECK (state = 'active')` |
| `root_issue_id UUID NOT NULL` | application-validated实际root；无FK |
| `workflow_profile_key TEXT NOT NULL` | `CHECK (char_length(workflow_profile_key) BETWEEN 1 AND 128 AND workflow_profile_key ~ '^[a-z0-9][a-z0-9._-]{0,127}$')` |
| `revision BIGINT NOT NULL DEFAULT 0` | `CHECK (revision >= 0)`；BIGINT类型本身给出MaxInt64上界 |
| `next_receipt_ordinal BIGINT NOT NULL DEFAULT 0` | `CHECK (next_receipt_ordinal >= 0)`；internal per-scope allocator，不属于coordination revision |
| creation provenance | `created_by_type TEXT NOT NULL CHECK (created_by_type IN ('member','agent'))`、`created_by_id UUID NOT NULL`、`created_task_id UUID NULL`、`created_at/updated_at TIMESTAMPTZ NOT NULL`；table CHECK强制member→task NULL、agent→task NOT NULL |

约束/index：

- active natural key：partial unique index固定为`(workspace_id,root_issue_id,workflow_profile_key) WHERE state='active'`；它不能attach为UNIQUE constraint，保留为named unique index；
- workspace/root lookup；
- `id` PK index；
- 全部index独立`CREATE ... INDEX CONCURRENTLY`，禁止inline PK/UNIQUE；只有非partial PK/适用unique随后`ADD CONSTRAINT ... USING INDEX`。

不增加goal/policy JSON、parent scope、controller、archive字段或future nullable占位。

### `coordination_receipt`

完整列合同如下；除`actor_task_id`外不得nullable：

| Column | Exact contract |
| --- | --- |
| `id` | `UUID NOT NULL`；opaque PK，无FK |
| `workspace_id` | `UUID NOT NULL`；无FK |
| `coordination_scope_id` | `UUID NOT NULL`；scope与receipt同transaction写入，无FK |
| `receipt_ordinal` | `BIGINT NOT NULL CHECK (receipt_ordinal >= 1)`；BIGINT给出MaxInt64上界 |
| `operation` | `TEXT NOT NULL CHECK (char_length(operation) BETWEEN 1 AND 64)`；DB不做跨slice enum |
| `idempotency_key` | `TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 200)` |
| `request_hash` | `BYTEA NOT NULL CHECK (octet_length(request_hash) = 32)`；SHA-256 |
| `resource_type` | `TEXT NOT NULL CHECK (char_length(resource_type) BETWEEN 1 AND 32)`；DB不做跨slice enum |
| `resource_id` | `UUID NOT NULL` |
| `revision_before` / `revision_after` | 均`BIGINT NOT NULL`；table CHECK为`revision_before >= 0 AND revision_after >= revision_before` |
| `result_snapshot` | `JSONB NOT NULL CHECK (jsonb_typeof(result_snapshot) = 'object' AND octet_length(result_snapshot::text) <= 16384)`；只存server-shaped saved result，不存raw request |
| `actor_type` | `TEXT NOT NULL CHECK (actor_type IN ('member','agent'))` |
| `actor_id` | `UUID NOT NULL` |
| `actor_task_id` | `UUID NULL`；table CHECK为`(actor_type='member' AND actor_task_id IS NULL) OR (actor_type='agent' AND actor_task_id IS NOT NULL)` |
| `created_at` | `TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()`；客户端不可提供 |

Named indexes/constraints：`id` PK；`(workspace_id,idempotency_key)`唯一；`(coordination_scope_id,receipt_ordinal)`唯一；scope receipt read index以`(coordination_scope_id,receipt_ordinal DESC)`开头。Operation不能进入idempotency unique key，否则同key换operation无法fail closed。

在workspace lock与scope row lock内，allocator先确认`next_receipt_ordinal < MaxInt64`，再原子`+1 RETURNING`并把返回值写入receipt；receipt insert/allocator/业务结果同transaction commit或rollback。Exact replay不分配新ordinal；新key no-op会分配。该顺序对transaction start time、`now()`和UUID无依赖。Receipt是replay authority，常规应用rollback不得清空。

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
- get/insert receipt与saved result；持scope row lock原子allocate/increment `next_receipt_ordinal`；按scope+ordinal读取receipt page；
- deletion guard：scope root，以及workspace是否存在任何scope；receipt history本身不构成Issue或Workspace删除阻塞。

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

Handler不得直查coordination tables。所有read和guard均经过同一workspace/task authority seam。`DeletionGuardHandle`由service取得并独占pinned `*pgxpool.Conn`，在Acquire期间已取得session advisory lock、开始`pgx.Tx`、按UUID锁定entity rows并通过同一qtx执行Store guard。Handle只暴露transaction-bound typed pre-delete operations与final entity delete（内部`db.New(qtx)`）、`CommitDB`、`Abort`和`ReleaseAfterPostCommit`；handler不得取得raw pool fallback或另开transaction。

Issue pre-delete qtx operation必须返回bounded post-commit payload（cancelled task rows/agent IDs、预先解析的metrics context、safe event IDs、attachment refs等）：qtx内执行`CancelAgentTasksByIssue`及必须的token cleanup、`FailAutopilotRunsByIssue`、attachment read与final Issue delete；commit后finalizer再做metrics、agent reconciliation、cache/S3 cleanup及task/Issue events。Workspace使用同一phase model。`CommitDB`只commit且保留session lock；`Abort`执行rollback→unlock/connection cleanup；成功commit后的bounded finalizer无论成功或记录失败债务，最终都调用`ReleaseAfterPostCommit`验证unlock并release/discard。

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

1. strict parse key、root、profile与typed input；构造version 1 canonical hash，不含timestamp/display data/idempotency key。
2. 开transaction并按[workspace lock SSoT](README.md#workspace-coordination-advisory-lock-ssot)取得workspace advisory xact lock；该lock先于scope/resource row lock。
3. **持锁**重新加载并验证current membership/task binding、Workspace、actual root与profile authority；receipt不是授权缓存，revoke/expiry/authority loss先拒绝。
4. 查`(workspace,key)`：不同operation/hash/actor/task→`coordination_idempotency_conflict`；exact match还须持锁加载saved scope/resource并确认仍存在、同workspace且当前actor仍可读，才返回saved result。
5. 非replay路径持锁复核Workspace/root仍存在；并发natural-key ensure依靠unique index收敛，loser reload现有scope。
6. 新scope revision=0；已有scope以新key创建no-op receipt，revision不变。
7. 持scope row lock分配下一个`receipt_ordinal`；保存bounded result snapshot与receipt后commit。Allocator/receipt/业务结果任一失败均rollback。

Replay发生在current-state/CAS判断前以恢复丢失响应，但只能在workspace lock、current authorization及saved resource revalidation之后返回；exact replay不分配ordinal。V1不允许任何coordination revision mutation；V2开始使用CAS primitive。

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

使用strict decoder：`DisallowUnknownFields`、duplicate object key detection并拒绝trailing第二个JSON value；任何客户端身份字段按unknown field拒绝。首次创建201；existing/new-key no-op/exact replay 200；body均含saved receipt+scope，receipt必须含`receipt_ordinal`；exact replay返回原ordinal。

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

- 单Issue、BatchDeleteIssues、Workspace删除各自在任何cache/task/Autopilot/event副作用前Acquire handle：pool acquire pinned `*pgxpool.Conn`→session advisory lock→同connection `pgx.Tx`→entity rows按UUID byte order锁定→同qtx Store guard。不得保留瞬时`CheckIssueDeletionAllowed`/`CheckWorkspaceDeletionAllowed` API。
- Batch先解析全部实际目标并确认单workspace，再在一个qtx内锁定全部实际rows并一次性guard；不得逐项check后释放transaction/session lock。Workspace现有workspace row与chat-session locks必须并入handle qtx，且不得先移除membership或invalidate cache。
- Guard在该qtx内检查scope root；receipt reference本身不触发`coordination_delete_blocked`。拒绝时`Abort` rollback后再unlock，保证task/Autopilot DB mutation及cache/S3/metrics/reconciliation/event均为零。
- Guard通过后，必须pre-delete的task/Autopilot DB mutation与final Issue/Workspace delete在同一qtx；任一步失败整体rollback。Commit成功后才执行bounded post-commit finalizer；其失败不能回滚entity delete，必须记录typed retry/debt evidence并继续安全释放session lock。
- Lifecycle顺序固定：Acquire/guard→qtx pre-delete DB mutations→qtx entity delete→`CommitDB`→post-commit finalizer→`ReleaseAfterPostCommit` unlock/release。Abort为rollback→unlock/release。Unlock返回false/error、qtx状态不明或connection异常时调用pgxpool `Hijack`并close/discard；panic defer走Abort或commit后的release，process crash/connection close依赖PostgreSQL自动释放。

该合同只声明guard rejection零副作用、qtx rollback回滚pre-delete DB mutation，以及Store refs/Issue/Workspace DB rows不会产生新orphan。Commit后的cache/S3/metrics/reconciliation/event失败属于显式可重试债务；第一波不声称它们与DB原子或可rollback。

## Acceptance / tests

必须证明：

1. migration fresh up/down/up、PK/concurrent-index序列、lint、sqlc二次生成无drift；逐列证明receipt除`actor_task_id`外均NOT NULL、长度/hash/JSON/actor/revision CHECK、两组unique及ordinal index exact；
2. ensure串行/并发同natural key只产生一个scope；
3. same-key same-hash exact replay；same-key不同profile/actor/task conflict；所有replay/conflict均在workspace lock与二次authority/resource验证后返回；member revoke、task expiry/revoke、root authority loss或并发entity delete后same-key replay被拒绝；
4. actual-root validation：child、cross-workspace parent、missing/cycle拒绝；
5. member与合法issue-bound task；普通PAT/JWT伪造agent/task headers不能提升authority；无issue task拒绝；
6. API unknown/identity fields、trailing JSON、tenant边界和safe errors；
7. CLI exact request、zero-request validation、JSON stdout/stderr与top-level exit；
8. built-in embed/frontmatter/source-map存在性；
9. before/after Issue status/assignee/comment/task/Autopilot计数不变；
10. `ensure_scope/scope` allowlist、canonical JSON与SHA-256 golden vectors；unknown operation/resource_type被service拒绝；
11. Ensure分别与单删、BatchDeleteIssues、Workspace删的真实并发race：要么Store写成功且delete被guard，要么delete提交且Store写因entity不存在而失败；不得产生新orphan；
12. guard触发时task/Autopilot DB与cache/S3/metrics/reconciliation/event均未变化；测试证明pinned connection、session lock、entity row locks、guard、pre-delete task/token/Autopilot DB operations、Workspace既有row/chat locks及final delete共用一个qtx；任一步失败全部rollback；
13. success顺序严格为qtx commit→post-commit metrics/reconciliation/cache/S3/events→verified unlock/release；finalizer失败记录typed debt且仍安全release，不虚构DB rollback；unlock/connection失败Hijack+discard；
14. namespace常量与至少两个workspace UUID→signed int32 key golden vectors；session/xact helpers对同一workspace互斥、不同无碰撞fixture可并行；
15. receipt ordinal从1严格递增；exact replay不增、新key no-op增；allocator/receipt rollback不推进counter；较早开始但较晚commit的writer仍不能污染已建立的ordinal pagination window。

Focused Go命令必须从`server` module执行：

```bash
set -euo pipefail
make sqlc
git diff --exit-code -- server/pkg/db/generated
test -z "$(git status --porcelain --untracked-files=all -- server/pkg/db/generated)"
(
  cd server
  export WORK_COORDINATION_DB_REQUIRED=1
  go test -count=1 -v ./internal/migrations ./cmd/migrate ./internal/service ./internal/handler -run 'WorkCoordination'
  go test ./internal/migrations ./cmd/migrate ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica
  go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica
)
make build
make test
git diff --check
```

`make sqlc`后的tracked diff与untracked porcelain assertions必须同时为空，任一非空立即使V1 gate失败；`git diff --check`不能替代。第一条verbose DB command必须在DB不可用时non-zero fail，输出实际执行的coordination migration/integration test names；任何skip都使该gate失败。

## Non-goals

Dependency、blocker、inspect、Store cleanup/archive、wake/control、Goal authority、Agent Kit、UI与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留additive schema/receipt；仅空Store且明确批准时在disposable DB验证down。Evidence记录exact base/head、migration清单、sqlc drift、service/API/CLI fixtures、review/CI和fork narrative。

V1最多声明scope+receipt vertical slice在source tests中通过；不得声明dependency/blocker/inspect、live deployment、scheduling authority或future control能力。
