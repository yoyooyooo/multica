# External PR Integration

## 目标

Multica 只维护两类事实：

1. 当前运行 Task 的 provider-neutral execution context；
2. Issue 与外部 PR/MR 的链接、状态和完成意图。

Multica **不再**为 AGS 签发 workload assertion，也不创建、批准、消费或记录
`pr.merge` delegation。仓库操作权限只来自 AGS canonical Access Grant 与 AGS
使用时复核的原生仓库授权。

## 当前公开面

| 路由 | 调用方与认证 | 作用 |
|---|---|---|
| `GET /api/integrations/current-execution-context` | still-running Task token | 返回 `multica.current-execution-context.v2` 最小事实：Workspace/Agent/Task ids、`claim.generation`（`run.id` dual-read 别名）和可选 Issue/Squad/Runtime/Trigger ids；无 display enrichment |
| `POST /api/integrations/external-pr/link-token` | still-running Task token | **兼容入口（T017 residual）**：签发 task-bound correlation token；不授予仓库操作权限；owner=Multica fork maintainer；目标退役：完整 404 + AGS verify/assertion 字段同代清理（登记于 evidence `external-pr-link-token-census.md`） |
| `POST /api/integrations/external-pr/links` | exact Bearer service token | 幂等登记或更新外部 PR 投影 |
| `POST /api/integrations/external-pr/complete-from-merge` | exact Bearer service token | 幂等登记 merged 事实并返回 durable reconcile acknowledgement；Issue 仅由 worker 完成 |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/external-prs` | Workspace member | 读取外部 PR 链接 |
| `GET /api/workspaces/{workspace_id}/issues/{issue_id}/pull-requests` | Workspace member | 读取统一 PR 投影 |

请求头中的伪造 Workspace、Agent 或 actor 字段不能覆盖 Task token 的服务端绑定。
Task 终态、token 失效或跨 Task/Workspace 不匹配时，current-context 与 link-token
均 fail closed。

## Current execution context

响应只包含运行定位和归因事实，不包含：

- assertion、JWT authority 或 Policy Class；
- operation、capability 或 merge method；
- AGS Session、Access Grant、provider token 或其他凭据。

Claim response把canonical `execution_id`（无独立 execution 时回落Task ID）作为 claim generation / dual-read `run.id` 坐标交给daemon；daemon best-effort 注入 `MULTICA_RUN_ID`，缺失不得阻断普通 Agent 启动。该响应可作为 AGS 内部绑定 Task/claim generation 的输入，但不是授权证明，也不是 Agent 命令面。

**普通 Agent 协作面（与 Program A / ags-cli 0.2.0 对齐）：** 只使用 `git` 与 `gh`（Runtime shim → `ags-cli gh`）。Access Grant 由 launcher/服务自动 issue/reuse，**禁止**要求 Agent 先跑 grant/session/access。Multica External PR association / link-token 为 best-effort 关联；关联失败可 warn，**不得**单独阻断合法 PR create。真拒绝来自 AGS 仓库权限、protected 与 exact effect（如 merge）。

External-PR link token 与授权链独立：audience 必须精确匹配配置，`source=task_token`，
有效期最长五分钟，并且不携带 assertion `kid` 或 `purpose`。签名 secret 必须是至少
32 字节的密码学随机值；空值或过短值均 fail closed。它只能关联回调，不能选择 Policy
Class、operation、capability、Session、Grant 或 provider credential。

## External PR 数据与完成规则

`external_pull_request_link` 只保存 Multica 产品字段：Workspace、Issue、provider、
repository、PR number、URL、state、`link_confidence`、completion intent，以及可选
merge-provider 镜像（merge_repo/number/url/sha）。AGS binding/head/base/method/revision
等 projection 不进入 Multica link（T017）。

`external_pull_request_receipt` 是幂等回执的唯一所有者（workspace + idempotency_key）。
当前 association boundary 为兼容现行 AGS wire，closed request schema 接受 `workspace` / `issue_key`
display 字段，以及完整的 canonical repository / provider binding / expected head+base / base ref /
delegated merge method / projection revision envelope。该 envelope 必须全有或全无；存在时会执行
canonical identity、repo、SHA、branch、method 与 `target_instance` 精确校验，并参与 payload hash。
当前 generation 只接受与校验这些字段，不持久化为 Multica authority，也不因此开放 merge
delegation。`target_instance` 仍是 request-only fence（精确匹配配置实例，不落库）。

历史 workload authority / merge-delegation 表已由 T016 前向退役；回滚需 pre-299 dump。

`POST /external-pr/link-token` 仍为兼容入口（见下方 disposition），普通 Agent PR 路径
不得依赖它。

**Basic link 与 canonical merge projection 是两种不同合同：** basic link 是 provider-neutral
事实，只需要 provider、外部 repository/number、URL、state 和 link confidence；空的
`MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS` 不把它强制限定为 Forgejo。只有带完整
`target_instance`、canonical/provider repository、binding identity/revision、expected
head/base、base ref、merge method 与 projection facts revision 的 canonical merge
projection 才是 Forgejo 且 instance-bound；它必须匹配
`MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID`，不能用部分字段伪造。两者都不携带
provider token、raw provider effect 或 `pr.revalidate` selector。

AGS typed terminal admission 只接受 `provider=ags` 的 closed `/links` 或 merged
`/complete-from-merge` 矩阵：body 必须带 canonical、非空的 `workspace`、`issue_key`、
`external_url`，以及显式 `state`、`completion_intent`、`link_confidence=authoritative`、
`idempotency_key` 和完整 Forgejo merge facts；这些 identity 字段也纳入同一
idempotency-key canonical hash。closed
不得带 `merged_sha`，merged 必须带小写 40-hex `merged_sha` 且 completion intent 为 true。
该窄 validator 不改变 generic open-link 或非 AGS provider-neutral 行为。终态事实提交事务会
同时写入窄的 `external_pr_terminal` durable reconcile work；Bus、HTTP 响应与 finalizer 仅作
nudge。worker 重新读取当前 Issue、link、completion policy 和 provider-neutral facts，并且
只能调用现有 completion kernel。work 的 `succeeded`/
`recorded`/`dead` 不等价于 Issue `done`；`record_only`、unsupported、inferred 或缺失
completion intent 永不关闭 Issue、Stage barrier 或 parent wake。Issue/workspace 删除在应用事务中显式清理该 work，不使用 FK/cascade；删除、source sweep 与
finalizer 共用 provider-workspace fence、按 UUID 排序的 Issue advisory/row locks，并在锁后
重读事实，禁止删除后产生 orphan work 或 stale parent side effect。finalization 的
`dead` 结果会以 typed outcome 暴露给 scheduler，linked work 不会被误标为 `succeeded`。
Finalization 保留 lease-only claimed 语义：`pending`/`retry_wait` 持有未过期
`lease_token` 时表示已 claimed；不额外引入公开 `claimed` 状态。lease 在 retry ceiling
过期后会转为 `dead`，即使 `work_id` 为空也会让 scheduler 告警。

parent child-done comment 使用 parent + stage/nonstage + sorted relevant child IDs 的稳定
barrier generation key；同一 generation 只创建一个 comment/task，新增相关 child 才产生新
generation。实时 EventBus hint 仍是 at-least-once；typed finalization issue event 携带
`intent_id + step` delivery key，inbox listener 只对该 key 做窄范围幂等去重，不建立通用
outbox 或 ledger。

typed AGS merged 请求必须匹配当前 authoritative link、完整 Forgejo 投影与
`completion_intent=true`；HTTP receiver 只确认事实并排队 durable reconcile work，锁后 worker
才推进 Issue。普通 link 更新、评论 marker 或客户端自报状态不能直接完成 Issue；provider-neutral
legacy 路径若仍保留，其同步行为与 typed AGS admission 明确隔离。

## 已退休的公开面

以下路由必须不存在并返回 `404`：

```text
POST /api/integrations/workload-assertions
GET  /api/workspaces/{workspace_id}/workload-delegations/pr-merge
GET  /api/workspaces/{workspace_id}/workload-delegations/pr-merge/{delegation_id}
POST /api/workspaces/{workspace_id}/workload-delegations/pr-merge/{delegation_id}/approve
POST /api/workspaces/{workspace_id}/workload-delegations/pr-merge/{delegation_id}/revoke
POST /api/integrations/ags/workload-delegations/pr-merge/{delegation_id}/introspect
POST /api/integrations/ags/workload-delegations/pr-merge/{delegation_id}/consume
POST /api/integrations/ags/workload-delegations/pr-merge/{delegation_id}/effects
```

CLI 的 `multica issue merge status|approve|revoke` 同时退休。以下配置也不再属于
Multica runtime contract：

```text
MULTICA_WORKLOAD_ASSERTION_SECRET
MULTICA_WORKLOAD_ASSERTION_ISSUER
MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID
MULTICA_WORKLOAD_ASSERTION_KEY_ID
MULTICA_DELEGATED_PR_MERGE_ENABLED
```

`/readyz` 只报告数据库与 migration 检查，不再暴露 assertion 或 delegated-merge
配置状态。

## 仍在使用的配置

| 变量 | 用途 |
|---|---|
| `MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET` | Task-bound external PR link token 签名；至少 32 字节的密码学随机值 |
| `MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE` | link token audience；默认 `external-pr-link` |
| `MULTICA_EXTERNAL_PR_SERVICE_TOKEN` | service callback 的 exact Bearer token |
| `MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID` | service peer 的 secret-free instance identity |
| `MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS` | 可选 provider allowlist |
| `MULTICA_APP_URL` | 生成 Issue URL；不参与授权 |

所有 secret 必须由部署 secret 管理注入，不能写入源码、日志、Issue 或证据包。

## Source and test anchors

- `server/internal/handler/current_execution_context.go`
- `server/internal/handler/current_execution_context_test.go`
- `server/internal/handler/external_pr_link_token.go`
- `server/internal/handler/external_pr_integration.go`
- `server/internal/handler/external_pr_reconcile.go`
- `server/internal/handler/external_pr_integration_test.go`
- `server/migrations/302_external_pr_reconcile_work.up.sql`
- `server/migrations/303-305_external_pr_reconcile_work_*_index.up.sql`
- `server/migrations/306-310_external_pr_reconcile_finalization_*.sql`
- `server/migrations/311-312_inbox_item_delivery_key*.sql`
- `server/migrations/313-316_external_pr_reconcile_*_id_index|*_primary_key.*.sql`（315/316 对 DDL/ledger 间崩溃做 exact authority 校验与幂等重放）
- `server/cmd/server/notification_listeners.go`（typed finalization inbox delivery-key dedup）
- `server/cmd/server/router.go`
- `server/cmd/server/external_pr_routes_integration_test.go`
- `server/pkg/db/queries/task_token.sql`

Router integration tests own the retired assertion-route `404` contract；AGS 与 Agent Kit
分别负责其自身 legacy exchange/gateway 与 CLI fallback 的退休证明。

## Mini deployment applicability

Mini has run this capability from exact generation tip `f219f4513` since the accepted
`fork-mini-v0.4.22-r1` deployment. Runtime evidence includes successful
`external_pr_reconcile` scheduler executions, durable work/finalization state readback,
401 responses for unauthenticated service POSTs, and 404 for the retired workload-assertion
POST. Source tests remain the authority for crash windows and completion semantics; idle
scheduler ticks prove registration and execution, not an end-to-end provider merge during the
deployment window. See the [generation manifest](../../../releases/fork-generations/v0.4.22.md)
for image IDs, DB/uploads boundaries, rollback, receipt location, and non-Mini claim limits.
