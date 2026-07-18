# V1 — Scope、revision 与 request-hash receipts

**Blocked by:** initial frozen base、migration ceiling重检与[共享交付门](README.md#每片共享交付门)。
**Blocks:** [02-canonical-dependencies.md](02-canonical-dependencies.md)；只有V1 accepted head、独立review和exact-head CI记录完成后才解除。

## Objective

交付第一个可独立使用的passive vertical slice：创建/读取root coordination scope，并以server-stamped actor/task、canonical request hash和持久receipt证明ensure幂等。该片从DB贯通到service、workspace API、`multica coordination scope` CLI和初版built-in skill，但不创建dependency/blocker，不实现inspect或调度副作用。

## Exact owning modules

- migrations：`coordination_scope`、`coordination_receipt`的structure、concurrent indexes、constraint attach及down；ceiling=201时严格使用README冻结的202-210连续序列，ceiling变化则stop并整体顺延。
- `server/pkg/db/queries/coordination.sql`与sqlc generated files。
- `server/internal/service/coordination*.go`：scope/receipt types、errors、service与tests。
- `server/internal/middleware/auth.go`及tests、`server/pkg/db/queries/task_token.sql`/generated helper/tests、`server/internal/handler/coordination*.go`、`handler.go` wiring、`server/cmd/server/router.go`：coordination actor只消费server-only typed credential context。
- `server/cmd/multica/cmd_coordination.go`及tests；`server/internal/cli/client.go`、`errors.go`、顶层JSON error rendering所需的`main.go`/`help.go`最小seam及tests。
- `scripts/verify-work-coordination-db-tests.sh`及脚本tests：V1-V5唯一DB-required机械入口。
- `server/internal/service/builtin_skills/multica-work-coordination/**`与embed/source-map tests。
- Issue单删/批删、Workspace删除入口的**lock-held deletion guard seam**及回归测试；`server/internal/service/task.go`与相关task/autopilot queries允许做窄拆分：delete-only的pre-delete DB cancellation/failure走qtx，metrics/agent reconciliation/cache/S3/event通知成为typed post-commit effects。不得改变非delete调用方语义，不实现Store cleanup，也不宣称post-commit副作用可rollback。
- `docs/fork-features/work-coordination-store/README.md`与registry，只声明V1事实。

不得修改`001_init`、dependency业务、blocker、Autopilot产品策略、Stage、Issue status/assignee/comment语义、Agent Kit或UI；只允许把现有delete-only task cancellation/Autopilot failure DB动作改为qtx-bound phase并拆出typed post-commit effects。

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

按[README migration序列](README.md#migration-可执行序列)精确拆为`202` structure、`203` scope PK、`204` active partial unique、`205` workspace/root lookup、`206` receipt PK、`207` workspace-idempotency unique、`208` scope-ordinal unique、`209` receipt DESC read、`210` attach constraints。若实施起点ceiling不再为201，立即stop并整体顺延这九个连续prefix；不得局部调整。Reverse down先drop attached constraint，index down使用`DROP INDEX CONCURRENTLY IF EXISTS`，最后drop tables。

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
AcquireIssueDeletionHandle(ctx, actor, workspaceID, issueIDs[]) -> IssueDeletionHandle
AcquireWorkspaceDeletionHandle(ctx, actor, workspaceID) -> WorkspaceDeletionHandle
```

Handler不得直查coordination tables。所有read和guard均经过同一workspace/task authority seam。两个concrete handle由service取得并独占pinned `*pgxpool.Conn`，Acquire期间已取得session advisory lock、开始`pgx.Tx`、按UUID锁定entity rows并通过同一qtx执行Store guard；handler不得取得raw qtx、pool fallback或另开transaction。

`IssueDeletionHandle.Delete(ctx, issueID) -> IssueDeletionEffects`在内部`db.New(qtx)`执行`CancelAgentTasksByIssue`、每个cancelled task的`DeleteTaskTokensByTask`、`FailAutopilotRunsByIssue`、attachment read与final Issue delete并返回bounded typed effects。Single调用一次；Batch可逐target调用但只允许同qtx savepoint隔离非guard单target DB失败；失败必须`ROLLBACK TO SAVEPOINT`并成功`RELEASE`后才能继续，rollback/release失败则整批abort，typed effects仅在target savepoint成功release后并入aggregate。`WorkspaceDeletionHandle.Delete(ctx) -> WorkspaceDeletionEffects`内部完成workspace row/chat-session locks、chat pins与既有Workspace DB cleanup及final delete。

两个handle都只另暴露at-most-once `Finish(commit bool) error`：使用独立bounded cleanup context完成commit或rollback、同session `pg_advisory_unlock`验证true并release；unlock false/error、qtx/connection状态不明则`Hijack`并close/discard。`Finish`成功前不得调用effects；成功commit且`Finish`成功后handler才执行typed effects，effects期间绝不持session lock。Effects失败只走既有error/log/operator debt；V1不增加outbox/delete receipt/自动修复。

### `CoordinationActor`

只含server-derived：`WorkspaceID`、`ActorType(member|agent)`、`ActorID`、nullable `TaskID`。业务input不得包含workspace/actor/agent/task字段。

Task actor必须由`server/internal/middleware/auth.go`在验证`mat_`后写入的server-only typed `middleware.TaskTokenCredential{ID,UserID,AgentID,TaskID,WorkspaceID}` context建立，五项均为typed UUID，`ID`精确是`task_token.id` credential ref；只通过`TaskTokenCredentialFromContext(ctx)`读取，coordination handler不得调用`resolveActor`的legacy `X-Agent-ID + X-Task-ID` fallback。`server/pkg/db/queries/task_token.sql`新增workspace-safe `GetTaskTokenByID`及sqlc helper/tests；进入coordination service后在workspace lock内按ID查询current `task_token` row并重新验证未过期/未撤销、task/agent/workspace/user exact binding；随后以workspace-scoped task query加载task且task必须有issue。普通PAT/JWT即使伪造`X-Agent-ID`/`X-Task-ID`也只能是member。Credential ref不进入业务input、canonical hash、wire、receipt或log。沿parent chain解析实际root，missing/cross-workspace/cycle或超过256 hops均fail closed。Member authority来自已验证workspace membership，但service仍逐row校验tenant。

### V1 receipt与Ensure algorithm

V1 typed allowlist只含`operation=ensure_scope`、`resource_type=scope`。按[canonical receipt hash SSoT](README.md#canonical-receipt-hash-ssot)，canonical document中的`request`精确为：

```json
{"root_issue_id":"<lowercase UUID>","workflow_profile_key":"matt-loop"}
```

Member golden完整canonical JSON bytes与SHA-256固定采用README SSoT：workspace zero UUID、root `...0001`、member `...0002`，digest=`d98699aa4465b9a91f590cf80c4f0151856f4f8b3d0eb0db3f82478da603f81e`。测试证明UUID大小写只normalize、不改变digest；profile、actor或task变化会改变digest。Credential ref不参与hash。DB只校验operation/resource_type非空与长度，service写入/读取receipt时都拒绝V1 allowlist外的值。

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

使用strict decoder：`DisallowUnknownFields`、duplicate object key detection并拒绝trailing第二个JSON value；任何客户端身份字段按unknown field拒绝。成功body只允许`{"scope":ScopeDTO,"receipt":ReceiptDTO}`，exact keys/types、UUID/timestamp/integer规范及敏感字段省略均以README V1 POST success wire SSoT为准；不得添加`created/replayed/noop`。Internal outcome `created|noop|replay`只选择HTTP：create 201，noop/replay 200。`result_snapshot`只存exact server-shaped `ScopeDTO`；replay从immutable receipt row重建`ReceiptDTO`，返回原ordinal和saved scope。

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

新增可由`errors.As`识别的strict `ProductError`，只在完整解析allowlisted coordination envelope且code/status/details组合合法时建立；coordination 409→exit 6，legacy/string/unknown 409继续走`HTTPError`且exit 1。顶层用纯函数在Cobra执行前预扫coordination argv，兼容`--output json|table`和`--output=json|table`，让local/Cobra/server errors统一渲染；JSON stderr恰一个value、stdout空，debug不追加prose。非法或冲突output按`coordination_invalid_payload`、exit 5。测试覆盖zero-request、exit 3/4/5/6/1和legacy fallback。

初版`multica-work-coordination` built-in skill只介绍scope ensure/get、idempotency、server identity、passive/未提供dependency等claim limit。Supporting source map引用实施后的真实symbol/route/migration，不能把ticket预期路径当证据。

## Lock-held deletion guard

V1实现并复用[workspace coordination advisory-lock SSoT](README.md#workspace-coordination-advisory-lock-ssot)，不删除Store rows。

- 单Issue、BatchDeleteIssues、Workspace删除各自在任何DB mutation或外部副作用前Acquire concrete handle：pool acquire pinned `*pgxpool.Conn`→session advisory lock→同connection `pgx.Tx`→entity rows按UUID byte order锁定→同qtx Store guard。不得保留瞬时`CheckIssueDeletionAllowed`/`CheckWorkspaceDeletionAllowed` API。
- Single Issue与Workspace atomic。Workspace handle内部完成workspace row/chat-session locks、pins、既有Workspace DB cleanup与final delete，且不得先移除membership或invalidate cache。
- Batch先按现有边界解析：invalid UUID、not-found/inaccessible target skip，resolved UUID去重；actual set为空返回`200 {"deleted":0}`。一个workspace lock/transaction先lock+guard全部actual targets；任一Store guard conflict整批零写入拒绝。Guard全部通过后，非guard单target DB失败以同qtx savepoint回滚该target并继续，effects只含成功target；`deleted`为最终commit实际唯一row数，最终commit失败整体rollback。这是保留current partial-success的唯一允许模式。
- Guard在qtx内检查scope root；receipt reference本身不触发`coordination_delete_blocked`。V1-V5不删除任何Store row。Guard拒绝时`Finish(false)` rollback后verified unlock/release，保证task/token/Autopilot DB mutation及cache/S3/metrics/reconciliation/event均为零。
- Guard通过后，task cancellation、每task token cleanup、Autopilot failure、Workspace DB cleanup与final entity delete共享qtx。固定生命周期：Acquire→typed `Delete`→at-most-once `Finish(commit=true|false)`完成commit/rollback→同session verified unlock/release或Hijack+discard→仅成功commit且Finish成功后调用typed bounded effects。Cleanup使用独立bounded context；effects期间不持session lock。

该合同只声明guard rejection零副作用、qtx rollback回滚pre-delete DB mutation，以及Store refs/Issue/Workspace DB rows不会产生新orphan。Post-commit cache/S3/metrics/reconciliation/event failure只走既有error/log/operator debt；第一波不新增或声称outbox、delete receipt、exactly-once、reliable delivery、automatic repair或DB rollback。

## Acceptance / tests

必须证明：

1. migration fresh up/down/up、PK/concurrent-index序列、lint、sqlc二次生成无drift；逐列证明receipt除`actor_task_id`外均NOT NULL、长度/hash/JSON/actor/revision CHECK、两组unique及ordinal index exact；
2. ensure串行/并发同natural key只产生一个scope；
3. same-key same-hash exact replay；same-key不同profile/actor/task conflict；所有replay/conflict均在workspace lock与二次authority/resource验证后返回；member revoke、task expiry/revoke、root authority loss或并发entity delete后same-key replay被拒绝；
4. actual-root validation：child解析到actual root；cross-workspace parent、missing、cycle及超过256 hops均fail closed；public reads service-only；
5. member与合法issue-bound task；`middleware/auth.go`把`task_token.id`放入server-only typed context，锁内current row revalidation；普通PAT/JWT伪造`X-Agent-ID/X-Task-ID`仍只能是member；coordination禁用legacy `resolveActor` fallback；无issue task拒绝；credential ref不进入input/hash/wire/log；
6. API unknown/identity fields、trailing JSON、tenant边界和safe errors；
7. CLI exact request、zero-request validation、strict `ProductError` `errors.As`、coordination exit 3/4/5/6/1、legacy/string 409 exit1、两种output flag形态、JSON stdout/stderr与top-level debug contract；
8. built-in embed/frontmatter/source-map存在性；
9. before/after Issue status/assignee/comment/task/Autopilot计数不变；
10. `ensure_scope/scope` allowlist、exact member canonical JSON与SHA-256 `d98699aa4465b9a91f590cf80c4f0151856f4f8b3d0eb0db3f82478da603f81e`；unknown operation/resource_type被service拒绝；
11. Ensure分别与单删、BatchDeleteIssues、Workspace删的真实并发race：要么Store写成功且delete被guard，要么delete提交且Store写因entity不存在而失败；不得产生新orphan；
12. guard触发时task/Autopilot DB与cache/S3/metrics/reconciliation/event均未变化；测试证明pinned connection、session lock、entity row locks、guard、pre-delete task/token/Autopilot/Workspace DB operations及final delete共用一个qtx；Single/Workspace atomic；Batch guard conflict全拒绝、savepoint partial-success、dedupe/skip/deleted count/empty actual set均按合同；
13. success顺序严格为qtx commit→verified unlock/release→post-commit metrics/reconciliation/cache/S3/events；`Finish` at-most-once且独立bounded cleanup context；effects失败只验证既有debt路径，不虚构DB rollback；unlock false/error或状态不明Hijack+discard；
14. namespace常量及golden vectors固定为zero UUID→`927402239`、UUID `...0003`→`-1961171921`；session/xact helpers对同一workspace互斥、不同无碰撞fixture可并行；
15. receipt ordinal从1严格递增；exact replay不增、新key no-op增；allocator/receipt rollback不推进counter；较早开始但较晚commit的writer仍不能污染已建立的ordinal pagination window；
16. POST exact `ScopeDTO`/`ReceiptDTO` golden fixtures、created 201/noop+replay 200、无outcome或敏感字段；`result_snapshot`只存ScopeDTO，replay返回原ordinal与saved scope；
17. `scripts/verify-work-coordination-db-tests.sh`脚本及tests按下节机械合同通过。

### Mechanical DB-required gate

V1新增窄脚本`scripts/verify-work-coordination-db-tests.sh`及脚本tests。脚本必须`set -euo pipefail`、设置并导出`WORK_COORDINATION_DB_REQUIRED=1`、从`server`执行`go test -count=1 -json ./internal/migrations ./cmd/migrate ./internal/service ./internal/handler -run 'WorkCoordination'`，先保留`go test`的非零状态，再解析完整JSON event stream。四个exact package各自至少一个matching test必须实际产生`run`与最终`pass`；任一`skip` event、零匹配、package缺失、缺pass、malformed/incomplete JSON或输出含`Skipping tests:`均失败。相关`TestMain`/DB helper在该env下DB缺失、不可达或fixture失败必须Fatal/nonzero，不能`os.Exit(0)`或`Skip`。脚本tests用fake `go` JSON fixtures覆盖四包pass及上述每个negative branch。V1-V5不得复制内联export+go test作为唯一gate。

从repository root执行以下gate；机械脚本及括号内Go命令进入`server` module：

```bash
set -euo pipefail
make sqlc
git diff --exit-code -- server/pkg/db/generated
test -z "$(git status --porcelain --untracked-files=all -- server/pkg/db/generated)"
./scripts/verify-work-coordination-db-tests.sh
(
  cd server
  go test ./internal/migrations ./cmd/migrate ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica
  go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica
)
make build
make test
git diff --check
```

`make sqlc`后的tracked diff与untracked porcelain assertions必须同时为空，任一非空立即使V1 gate失败；`git diff --check`不能替代。机械脚本必须在DB不可用、任一skip或四包任一零匹配时non-zero，并由JSON `run/pass` events证明实际执行的coordination migration/integration test names。

## Non-goals

Dependency、blocker、inspect、Store cleanup/archive、wake/control、Goal authority、Agent Kit、UI与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留additive schema/receipt；仅空Store且明确批准时在disposable DB验证down。Evidence记录exact base/head、migration清单、sqlc drift、service/API/CLI fixtures、review/CI和fork narrative。

V1最多声明scope+receipt vertical slice在source tests中通过；不得声明dependency/blocker/inspect、live deployment、scheduling authority或future control能力。
