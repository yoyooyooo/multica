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
| `POST /api/integrations/external-pr/complete-from-merge` | exact Bearer service token | 依据已登记投影与 completion intent 请求完成 Issue |
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

Claim response把canonical `execution_id`（无独立 execution 时回落Task ID）作为 claim generation / dual-read `run.id` 坐标交给daemon；daemon best-effort 注入 `MULTICA_RUN_ID`，缺失不得阻断普通 Agent 启动。该响应可作为 AGS 绑定 Task/claim generation 的输入，但不是授权证明。AGS 必须独立签发并在使用时复核 Access Grant、executor、operation、repository 与原生仓库授权。

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
请求可带 `target_instance` 作为 request-only fence（精确匹配配置实例，参与 payload hash，
但不落库）。`workspace`/`issue_key` display 字段不再出现在 closed request schema。

历史 workload authority / merge-delegation 表已由 T016 前向退役；回滚需 pre-299 dump。

`POST /external-pr/link-token` 仍为兼容入口（见下方 disposition），普通 Agent PR 路径
不得依赖它。

完成请求必须匹配已登记的 authoritative link。仅 PR/MR 已合并、投影一致且
`completion_intent=true` 时，服务端才推进 Issue；普通 link 更新、评论 marker 或客户端
自报状态不能直接完成 Issue。

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
- `server/internal/handler/external_pr_integration_test.go`
- `server/cmd/server/router.go`
- `server/cmd/server/external_pr_routes_integration_test.go`
- `server/pkg/db/queries/task_token.sql`

Router integration tests own the retired assertion-route `404` contract；AGS 与 Agent Kit
分别负责其自身 legacy exchange/gateway 与 CLI fallback 的退休证明。
