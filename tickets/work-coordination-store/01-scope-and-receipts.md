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
- `server/internal/middleware/auth.go`及typed request-context helper/tests；`server/pkg/db/queries/task_token.sql`增加exact credential row revalidation query。Middleware只把opaque task-token row ID放入server-only context，不暴露raw token/hash。
- `server/cmd/multica/cmd_coordination.go`及tests；`server/internal/cli/client.go`、`errors.go`、顶层JSON error rendering所需的`main.go`/`help.go`最小seam及tests。
- `server/internal/service/builtin_skills/multica-work-coordination/**`与embed/source-map tests。
- Root `scripts/test-work-coordination-db-required.sh`及相关harness tests；所有相关`TestMain`/DB helper在`WORK_COORDINATION_DB_REQUIRED=1`时必须non-zero fail，禁止`Skip`或`os.Exit(0)`。
- Issue单删/批删、Workspace删除入口的**lock-held deletion guard seam**及回归测试；`server/internal/service/task.go`与相关task/autopilot queries允许做窄拆分：delete-only的pre-delete DB cancellation/failure走qtx，metrics/agent reconciliation/cache/S3/event通知在commit确认且session lock terminal释放/丢弃后调用。不得改变非delete调用方语义，不实现Store cleanup，也不宣称post-release副作用bounded、可靠投递、可观测失败、自动重试或可rollback。
- `docs/fork-features/work-coordination-store/README.md`与registry，只声明V1事实。

不得修改`001_init`、dependency业务、blocker、Autopilot产品策略、Stage、Issue status/assignee/comment语义、Agent Kit或UI；只允许把现有delete-only task cancellation/Autopilot failure DB动作改为qtx-bound phase并拆出post-release best-effort effects。

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

按[README migration序列](README.md#migration-可执行序列)拆文件。V1开始时重新扫描并令`N=ceiling+1`，在任何文件落盘前原子预留`N..N+8`：

1. `N` structure：两表，无inline PK/UNIQUE；
2. `N+1` scope PK；`N+2` scope active partial unique；`N+3` `(workspace_id,root_issue_id)` lookup；
3. `N+4` receipt PK；`N+5` `(workspace_id,idempotency_key)` unique；`N+6` `(scope_id,receipt_ordinal)` unique；`N+7` `(scope_id,receipt_ordinal DESC)` read index；
4. `N+8` PK/适用unique constraint attach；partial unique仍只作为index；
5. reverse down先drop constraint，index down使用`DROP INDEX CONCURRENTLY IF EXISTS`，最后drop tables。

若实施起点ceiling仍为`201`，预期range为`202..210`；若任一prefix已占用则整段重新计算，禁止部分改号或沿用陈旧range。测试必须覆盖空库up/down/up、constraint/index形态、migration lint和runner真实执行。V1不触碰legacy `issue_dependency`。

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
EnsureScope(ctx, actor, input) -> EnsureScopeResult{Scope, Receipt, Outcome(created|noop|replay)}
GetScope(ctx, actor, scopeID) -> Scope
GetScopeByRoot(ctx, actor, rootIssueID, workflowProfileKey) -> Scope
AcquireIssueDeletion(ctx, actor, workspaceID, parsedIssueIDs[]) -> IssueDeletionHandle
AcquireWorkspaceDeletion(ctx, actor, workspaceID) -> WorkspaceDeletionHandle
```

Handler不得直查coordination tables。所有public reads和guard均经过同一workspace/task authority seam；handler不得复刻task-root授权。

Service提供两个concrete handle：`IssueDeletionHandle`（单删或同workspace batch）与`WorkspaceDeletionHandle`。Acquire内部独占pinned `*pgxpool.Conn`，取得session advisory lock、开始`pgx.Tx`、按UUID byte order锁Issue rows，或锁Workspace row后再按UUID锁chat-session rows，然后同qtx执行Store guard。Handle只暴露at-most-once `Delete(ctx)`、`CommitDB`、`Abort`、`ReleaseAfterCommit`；`Delete`内部使用`db.New(qtx)`完成typed dependent DB cleanup与final entity delete，并把immutable `PostCommitEffects`保留在handle内部。Handler永远拿不到raw qtx/pool fallback，不能另开transaction，也不能在release前取得或执行effects。

`IssueDeletionHandle.Delete`在qtx内执行`CancelAgentTasksByIssue`、对应task-token cleanup、`FailAutopilotRunsByIssue`、attachment/effect census与final Issue delete；payload含cancelled task rows/agent IDs、metrics context、safe event IDs、attachment refs。Workspace handle复用同一phase model并封装既有membership/workspace teardown。`CommitDB`只确认commit；成功后必须立即调用`ReleaseAfterCommit`，用独立5秒cleanup context verified unlock并release，或Hijack+close/discard异常connection。只有connection达到terminal `released|discarded`后，`ReleaseAfterCommit`才交出payload，handler随后调用现有metrics、agent reconciliation、cache/S3及task/Issue/Workspace event seams。

当前event bus同步、无context/error结果，storage delete也无error结果；因此V1 post-release phase只承诺best-effort调用尝试与safe日志，不承诺30秒总deadline、逐effect幂等、typed retry debt、可靠投递、自动修复或真实失败可观测。Blocking/reentrant listener或S3实现不得再持有session advisory lock；durable effect ID/outbox/recovery属于future lifecycle。

Acquire内部状态固定为`acquiring → ready`；guard拒绝或Acquire panic/error不返回handle，而是在内部rollback→verified unlock后直接terminal `released|discarded`。已返回handle只允许`ready → deleted → committed → released|discarded`，或`ready|deleted → Abort → released|discarded`；不存在悬空`aborted` state。非法/重复调用返回typed internal error且不得重复DB/effect。Advisory-lock acquisition调用返回error即视为session lock状态不明并Hijack+close；unlock false/error、commit/rollback状态不明或connection异常同样discard，绝不`Release`。只有`CommitDB`明确成功才能进入post-release effects；panic defer按当前state执行Acquire内部cleanup、Abort或commit后的`ReleaseAfterCommit`，但不在defer中执行effects。

任一statement或commit返回SQLSTATE `40001`/`40P01`时整次操作失败：禁止per-target skip/savepoint continuation、禁止在failed tx/同一handle继续、禁止post-commit effects且V1不自动retry。可确认未commit时使用独立cleanup context rollback→verified unlock；commit outcome unknown或rollback/unlock/transport状态不明时Hijack+close/discard。两类路径都返回safe `coordination_internal`，不暴露SQLSTATE/raw DB error。Commit outcome unknown不得自动retry，因为可能重复已提交delete；V1明确不补偿这种不确定结果。

### `CoordinationActor` 与 exact credential

只含server-derived：`WorkspaceID`、`ActorType(member|agent)`、`ActorID`、nullable `TaskID`、nullable internal `TaskCredentialRef`。业务input/hash/wire DTO不得包含workspace/actor/agent/task/credential字段；member `ActorID`固定为已认证user UUID且credential/task均null。

`mat_` auth成功后，middleware把查询到的`task_token.id`写入不可伪造的typed request context；不写header，不保留raw token/hash，不记录日志。Coordination handler只从该context构造agent actor，严禁调用或复制legacy `resolveActor` header-pair fallback。普通PAT/JWT即使携带`X-Agent-ID/X-Task-ID`也一律按member user UUID处理；`X-Actor-Source`继续由middleware先删除，客户端不能建立task actor。

取得workspace lock后、任何receipt replay/conflict或mutation前，service按exact `task_token.id`重查row仍存在且`expires_at > DB now()`，并精确匹配actor user/agent/task/workspace；随后重查task仍属于agent/workspace、有issue且未失去所需authority。同task的另一个token row不能替代presented credential；expired/deleted credential fail closed。`TaskCredentialRef`只用于current authority revalidation，不进入canonical request hash，因此同actor/task通过新有效credential重试仍按既有key/hash规则处理。

Task实际root使用workspace-scoped bounded recursive query：visited UUID array显式检测cycle，最大256 hops；missing/foreign parent返回不泄露foreign详情的`coordination_not_found`，cycle/depth overflow返回`coordination_invalid_payload`。Member authority来自current workspace membership，service仍逐row校验tenant。

### V1 receipt与Ensure algorithm

V1 typed allowlist只含`operation=ensure_scope`、`resource_type=scope`。按[canonical receipt hash SSoT](README.md#canonical-receipt-hash-ssot)，canonical document中的`request`精确为：

```json
{"root_issue_id":"<lowercase UUID>","workflow_profile_key":"matt-loop"}
```

V1使用typed deterministic document builder，不得依赖Go map iteration、raw request JSON或通用`map[string]any`。Member golden固定canonical bytes为：

```json
{"actor":{"id":"00000000-0000-0000-0000-000000000002","task_id":null,"type":"member"},"hash_version":1,"operation":"ensure_scope","request":{"root_issue_id":"00000000-0000-0000-0000-000000000001","workflow_profile_key":"matt-loop"},"workspace_id":"00000000-0000-0000-0000-000000000000"}
```

其SHA-256必须为`d98699aa4465b9a91f590cf80c4f0151856f4f8b3d0eb0db3f82478da603f81e`。测试另证明UUID大小写只normalize、不改变digest；profile、actor或task变化会改变digest。DB只校验operation/resource_type非空与长度，service写入/读取receipt时都拒绝V1 allowlist外的值。

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

使用strict decoder：`DisallowUnknownFields`、duplicate object key detection并拒绝trailing第二个JSON value；任何客户端身份字段按unknown field拒绝。首次创建201；existing/new-key no-op/exact replay 200。Service内部outcome固定为`created|noop|replay`，只决定201/200，不进入wire。

POST response固定为`{"scope":<ScopeDTO>,"receipt":<ReceiptDTO>}`；两个GET固定为`{"scope":<ScopeDTO>}`。不得临时追加`created/replayed/noop`：

```json
{
  "scope": {
    "id": "<uuid>", "workspace_id": "<uuid>", "scope_kind": "root", "state": "active",
    "root_issue_id": "<uuid>", "workflow_profile_key": "matt-loop", "revision": 0,
    "created_by": {"actor_type": "member", "actor_id": "<uuid>", "task_id": null},
    "created_at": "<UTC RFC3339Nano>", "updated_at": "<UTC RFC3339Nano>"
  },
  "receipt": {
    "id": "<uuid>", "receipt_ordinal": 1, "operation": "ensure_scope",
    "resource_type": "scope", "resource_id": "<uuid>",
    "revision_before": 0, "revision_after": 0, "created_at": "<UTC RFC3339Nano>"
  }
}
```

`ScopeDTO.scope_kind`与storage enum逐字相同，V1只允许`root`，不存在`issue_tree` alias/转换。`result_snapshot`只保存server-shaped `ScopeDTO`，不保存含receipt的wrapper；因此scope timestamps/creator先确定，receipt insert的ID/ordinal/created_at不会形成snapshot循环。ReceiptDTO总是从immutable receipt row重建且不暴露request hash、idempotency key、credential ref或raw request。Exact replay使用saved ScopeDTO与原receipt row，返回原ordinal及原scope representation；新key no-op保存当次current ScopeDTO并分配新receipt。

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

新增可`errors.As`/unwrap的`cli.ProductError`，仅在coordination endpoint响应同时满足JSON content type、strict known envelope和README status/code mapping时建立；它保存safe envelope与HTTP status，不保存raw body。README列出的全部五个coordination 409 code均映射exit 6；legacy/string/unknown-envelope/status-mismatch 409继续现有safe fallback与exit 1，禁止全局改写409语义。

Coordination默认JSON。顶层在Cobra parse前扫描`coordination`后的完整argv，支持`--output json|table`和`--output=json|table`，覆盖nested subcommand前后Cobra接受的位置并尊重`--`。只允许一个output flag；missing value/empty value/invalid value/duplicate/conflicting values均零HTTP请求返回`coordination_invalid_payload`/exit 5并用默认JSON renderer。恰有一个有效`table`才输出safe prose；其余失败stdout为空、stderr恰一个stable envelope JSON value（允许末尾换行），`--debug`也不得追加prose/stack/raw body。顶层execute helper与子进程测试逐一覆盖五个409 code、legacy fallback、两种flag语法/位置、重复/冲突/非法值、parse error、零请求validation以及exit 1/3/4/5/6。

初版`multica-work-coordination` built-in skill只介绍scope ensure/get、idempotency、server identity、passive/未提供dependency等claim limit。Supporting source map引用实施后的真实symbol/route/migration，不能把ticket预期路径当证据。

## Lock-held deletion guard

V1实现并复用[workspace coordination advisory-lock SSoT](README.md#workspace-coordination-advisory-lock-ssot)，不删除Store rows。

- 单Issue、BatchDeleteIssues、Workspace删除各自在任何cache/task/Autopilot/event副作用前Acquire handle：pool acquire pinned `*pgxpool.Conn`→session advisory lock→同connection `pgx.Tx`→entity rows按UUID byte order锁定→同qtx Store guard。不得保留瞬时`CheckIssueDeletionAllowed`/`CheckWorkspaceDeletionAllowed` API。
- Batch body维持现有shape；其ID列表解析冻结为：非法UUID、missing或foreign-workspace ID维持既有skip/no-leak语义；合法UUID先去重再按raw UUID bytes排序。取得session lock/qtx后才解析并锁定仍存在的实际target set，一次性guard并all-or-nothing删除；任一actual target的dependent DB cleanup/final delete失败则整批rollback，不保留旧逐项partial commit，也不使用per-target savepoint。`deleted`固定为实际成功提交删除的unique actual target数量；duplicate不重复计数，零actual target返回200/`deleted:0`。Workspace现有workspace row与chat-session locks必须并入handle Acquire qtx，且不得先移除membership或invalidate cache。
- Guard在该qtx内检查scope root；receipt reference本身不触发`coordination_delete_blocked`。拒绝时`Abort` rollback后再unlock，保证task/Autopilot DB mutation及cache/S3/metrics/reconciliation/event均为零。
- Guard通过后，必须pre-delete的task/Autopilot DB mutation与final Issue/Workspace delete在同一qtx；任一步失败整体rollback。`40001`/`40P01`在statement或commit阶段均整批失败，不continue、不自动retry、不运行effects；commit outcome unknown同样不自动retry/finalize并discard connection。
- Lifecycle顺序固定：Acquire/guard→qtx pre-delete DB mutations→qtx entity delete→`CommitDB`明确成功→`ReleaseAfterCommit` verified unlock/release-or-discard→post-release best-effort metrics/reconciliation/cache/S3/events。Abort为rollback→unlock/release-or-discard。Unlock false/error、qtx/commit状态不明或connection异常时调用pgxpool `Hijack`并close/discard；panic defer走Abort或commit后的release，process crash/connection close依赖PostgreSQL自动释放。

该合同只声明guard rejection零副作用、qtx rollback回滚pre-delete DB mutation、session lock在effects前terminal释放/丢弃，以及Store refs/Issue/Workspace DB rows不会产生新orphan。第一波不声称cache/S3/metrics/reconciliation/event调用成功、有统一deadline/error结果、可重试、可靠投递、与DB原子或可rollback。

## Acceptance / tests

必须证明：

1. 从实施head重算并原子预留V1九文件range；migration fresh up/down/up、PK/concurrent-index/attach序列、lint、sqlc二次生成无drift；逐列证明receipt除`actor_task_id`外均NOT NULL、长度/hash/JSON/actor/revision CHECK、两组unique及ordinal index exact；
2. ensure串行/并发同natural key只产生一个scope；
3. same-key same-hash exact replay；same-key不同profile/actor/task conflict；所有replay/conflict均在workspace lock与二次authority/resource验证后返回；member revoke、task expiry/revoke、root authority loss或并发entity delete后same-key replay被拒绝；
4. actual-root validation：child正常；missing/cross-workspace parent不泄露foreign详情；cycle与第257 hop fail closed；
5. member与合法issue-bound task；普通PAT/JWT伪造agent/task headers不能提升authority；无issue task拒绝；middleware auth后、workspace lock前删除/过期presented token row必须失败，同task另一有效token不能替代；credential ref不进wire/hash/log；
6. API unknown/identity fields、trailing JSON、tenant边界和safe errors；POST/GET exact DTO golden、UTC time、int64、201/200；result snapshot只含ScopeDTO，replay从原row重建receipt且无key/hash/raw request；
7. CLI exact request、zero-request validation；五个coordination 409逐code strict ProductError/exit 6与legacy/status-mismatch 409 exit 1；`--output json`/`--output=json`、table、nested位置、`--`及missing value/empty value/invalid value/duplicate/conflicting values矩阵；JSON failure stdout空/stderr单value，debug无prose，top-level exit 1/3/4/5/6；
8. built-in embed/frontmatter/source-map存在性；
9. before/after Issue status/assignee/comment/task/Autopilot计数不变；
10. `ensure_scope/scope` allowlist、canonical JSON与SHA-256 golden vectors；unknown operation/resource_type被service拒绝；
11. Ensure分别与单删、BatchDeleteIssues、Workspace删的真实并发race：要么Store写成功且delete被guard，要么delete提交且Store写因entity不存在而失败；不得产生新orphan；Batch覆盖invalid/missing/foreign skip、duplicate只计一次、零actual、actual targets all-or-nothing rollback与`deleted`计数；
12. guard触发时task/Autopilot DB与cache/S3/metrics/reconciliation/event均未变化；测试证明pinned connection、session lock、entity row locks、guard、pre-delete task/token/Autopilot DB operations、Workspace既有row/chat locks及final delete共用一个qtx；任一步失败全部rollback；handler无法取得raw qtx；
13. success顺序严格为qtx commit明确成功→独立5秒context verified unlock/release-or-discard→post-release best-effort metrics/reconciliation/cache/S3/events；blocking/reentrant listener和S3 fake证明effects开始前session lock已terminal；只断言现有void adapters被调用，不声称成功/debt/deadline；statement-time及commit-time `40001`/`40P01`注入均整批rollback/失败、零effects、无同handle continue/auto-retry；commit outcome unknown零effects且discard；handle非法重复调用无重复DB/effect；
14. namespace常量及README两个exact正/负signed golden vectors；session/xact helpers对同一workspace互斥、不同无碰撞fixture可并行；
15. receipt ordinal从1严格递增；exact replay不增、新key no-op增；allocator/receipt rollback不推进counter；较早开始但较晚commit的writer仍不能污染已建立的ordinal pagination window。

新增root script `scripts/test-work-coordination-db-required.sh`作为唯一DB-required harness。它必须`set -euo pipefail`、export `WORK_COORDINATION_DB_REQUIRED=1`、用`go test -count=1 -json -run '^TestWorkCoordination'`真实运行`internal/migrations`、`cmd/migrate`、`internal/service`、`internal/handler`，保存临时NDJSON且trap清理；任何go test non-zero、`Action=skip`、`--- SKIP:`或`Skipping tests:`立即non-zero。脚本还必须逐package断言至少一个`TestWorkCoordination*` pass event，防止`TestMain os.Exit(0)`或regex无匹配假绿；缺DB的负向harness测试必须证明该script non-zero。后续slice扩充同一manifest，不另造弱gate。

Required commands：

```bash
set -euo pipefail
make sqlc
git diff --exit-code -- server/pkg/db/generated
test -z "$(git status --porcelain --untracked-files=all -- server/pkg/db/generated)"
./scripts/test-work-coordination-db-required.sh
(
  cd server
  export WORK_COORDINATION_DB_REQUIRED=1
  go test ./internal/migrations ./cmd/migrate ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica
  go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica
)
make build
make test
git diff --check
```

`make sqlc`后的tracked diff与untracked porcelain assertions必须同时为空，任一非空立即使V1 gate失败；`git diff --check`不能替代。Harness必须输出实际pass的coordination migration/integration test names；skip、缺package pass evidence或DB不可用均使gate失败。

## Non-goals

Dependency、blocker、inspect、Store cleanup/archive、wake/control、Goal authority、Agent Kit、UI与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留additive schema/receipt；仅空Store且明确批准时在disposable DB验证down。Evidence记录exact base/head、migration清单、sqlc drift、service/API/CLI fixtures、review/CI和fork narrative。

V1最多声明scope+receipt vertical slice在source tests中通过；不得声明dependency/blocker/inspect、live deployment、scheduling authority或future control能力。
