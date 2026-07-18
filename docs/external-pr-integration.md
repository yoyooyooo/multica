# External PR Integration

> Scope: 当前为本 fork 新增能力，AGS 是第一个接入方。设计目标是通用外部 PR/MR/change 集成，而不是把 Multica 内核语义写死为 AGS 专用。

## 目标

External PR Integration 让外部代码协作系统把一个 PR/MR/change 与 Multica Issue 建立**可信绑定**，并在外部 merge 发生后请求 Multica 执行最终的 Issue 状态转换。

核心原则：

1. **能通用化就通用化**：外部 PR 表和回调保持 `external_pr` / `external-pr` 语义；跨系统任务证明统一使用 purpose-bound Workload Assertion。
2. **能配置就配置**：具体 provider（例如 AGS）、允许列表、service token、签名 secret 通过配置或环境变量注入。
3. **不靠猜测完成 Issue**：PR 标题、分支名、正文里的 issue-like 文本都不是自动完成的权威来源。
4. **Multica 拥有最终状态转换**：外部系统只提交事实和请求，leaf-child-only 与 Issue 级 completion policy 等安全规则由 Multica 原子判断。

## 当前实现

### 表

迁移文件：`server/migrations/135_external_pr_integration.up.sql`

新增表：`external_pull_request_link`

关键字段：

| 字段 | 含义 |
|---|---|
| `workspace_id` / `issue_id` | 被绑定的 Multica Issue |
| `provider` | 外部 provider，例如 `ags`、`gitlab`、`custom` |
| `external_repo` / `external_number` / `external_url` | provider 自己的 PR/MR/change 标识 |
| `merge_provider` / `merge_repo` / `merge_number` / `merge_url` | 实际发生 merge 的外部系统，可为空 |
| `link_confidence` | `authoritative` 或 `inferred`；只有 authoritative 可自动完成 |
| `completion_intent` | 该外部 PR 是否声明“merge 后可尝试完成 Issue” |
| `state` | `open` / `draft` / `closed` / `merged` |
| `idempotency_key` | 外部 merge 事件去重键 |

唯一约束：

```text
(workspace_id, provider, external_repo, external_number)
```

这保证同一个 provider 的同一个外部 PR/MR/change 在一个 workspace 内幂等 upsert。

### Workload Assertion

Canonical endpoint：

```http
POST /api/integrations/workload-assertions
Authorization: Bearer <mat_ task token>
Content-Type: application/json

{
  "purpose": "external_pr_link",
  "target": {
    "provider": "ags",
    "instance": "mini:6666",
    "repository": "jackie/agent-kit"
  }
}
```

该接口在普通 Auth group 内，但要求 `X-Actor-Source: task_token`，因此只能由 task-scoped `mat_` token 调用。客户端不能提交或覆盖 workload 身份；服务端根据 task row、Agent row 和 auth middleware 注入的 workspace/task headers 推导：

- `workspace` / `workspace_id`
- `issue_id` / `issue_key` / `issue_url`
- `task_id`
- `agent_id` / `agent_name`

Canonical issuer 支持两个严格分离的 purpose：

| purpose | audience | target / capabilities |
| --- | --- | --- |
| `external_pr_link` | `urn:multica:external-pr-link:v1` | 外部 provider + repository；`requested_capabilities` 必须为空；Issue 必需 |
| `ags_session_exchange` | `urn:ags:workload-session-exchange:v1` | provider 必须为 `ags`，instance/repository 必需，`requested_capabilities` 非空；Issue 可选 |

Session assertion 请求示例：

```json
{
  "purpose": "ags_session_exchange",
  "target": {
    "provider": "ags",
    "instance": "mini",
    "repository": "jackie/agent-kit"
  },
  "requested_capabilities": ["repo:read"]
}
```

每次请求都签发独立的五分钟 HS256 JWT；即使 task 和 target 相同，External PR 与 session exchange 也使用不同 audience、JTI 和 token instance。JWT 包含 `ver`、`iss`、`sub`、`jti`、`purpose`、`source=task_token`、server-derived `workload`、signed `target` 与 signed capabilities，header 包含 `typ` 与 `kid`。Multica 只证明 workload 和签名请求边界，不决定 AGS principal、repo grant 或 session capability；AGS 必须独立裁决。

Legacy endpoint 继续作为迁移期 compatibility wrapper：

```http
POST /api/integrations/external-pr/link-token
Authorization: Bearer <mat_ task token>
```

它保留原 `link_token` response 和 `external-pr-link` audience。Legacy token 不能用于 AGS session exchange；canonical `external_pr_link` assertion 也不能被 AGS 当作 session proof。

### 注册外部 PR 链接

```http
POST /api/integrations/external-pr/links
Authorization: Bearer <service token>
Content-Type: application/json
```

示例：

```json
{
  "provider": "ags",
  "workspace_id": "...",
  "issue_id": "...",
  "issue_key": "ABC-12",
  "external_repo": "jackie/ags-multica-demo",
  "external_number": 3,
  "external_url": "http://mini:6666/jackie/ags-multica-demo/pull/3",
  "merge_provider": "forgejo",
  "merge_repo": "jackie/ags-multica-demo",
  "merge_number": 9,
  "merge_url": "http://imile-win:5555/jackie/ags-multica-demo/pulls/9",
  "link_confidence": "authoritative",
  "completion_intent": true,
  "state": "open"
}
```

写入前会验证 `issue_id` 确实属于提交的 `workspace_id`，跨 workspace 组合会被拒绝且不会写 link/activity；`external_url` 与 `merge_url` 若非空，必须是绝对 `http(s)` URL。

### Merge 后请求完成

```http
POST /api/integrations/external-pr/complete-from-merge
Authorization: Bearer <service token>
Content-Type: application/json
```

该接口会先 upsert 外部 PR 链接为 `merged`，然后由 Multica 做 Issue 级 completion policy 与 leaf-child-only 原子判断。需要在 provider merge 后继续等待其他终态证据的 Issue，应在 merge 前写入：

```yaml
external_pr_completion_policy: record_only
```

`record_only` 仍记录 link、merge state、merged SHA 与 system activity，但不会修改 Issue status，也不会触发 parent/Stage wake。证据被独立接受后，由拥有该 workflow 的授权 actor 显式关闭 Issue。

### 查询 Issue 关联的 External PR

```http
GET /api/issues/{issue_id_or_key}/external-prs
Authorization: Bearer <user or PAT token>
```

响应使用 provider-neutral 字段，便于 operator 和 agent smoke 不查 DB 也能判断 linked / merged / completion intent。查询严格按当前 Issue 的 `issue_id` 返回，不向父 Issue 聚合子孙 Issue 的 PR；父级若需要查看交付关系，应使用独立的 related/rollup 视图，而不是改变权威归属：

```json
{
  "external_pull_requests": [
    {
      "provider": "ags",
      "external_repo": "jackie/ags-team-share",
      "external_number": 4,
      "external_url": "http://mini:6666/jackie/ags-team-share/pull/4",
      "state": "merged",
      "link_confidence": "authoritative",
      "completion_intent": true,
      "merge_provider": "forgejo",
      "merge_repo": "jackie/ags-team-share",
      "merge_number": 4,
      "merge_url": "http://forgejo.local/jackie/ags-team-share/pulls/4",
      "merged_sha": "11384b43b138b2a2d79cd7eb3c8c2e533900cfeb"
    }
  ]
}
```

CLI 入口：

```bash
multica issue external-prs MINI-379 --output json
```

Issue detail sidebar 会显示 `External PRs` 区块，展示 provider/repo/number、state、authoritative/inferred、external URL、merge provider/URL、merged SHA 和 completion intent。该区块独立于 GitHub-native `pull-requests` 区块，不把 AGS 语义写入 Multica core。

External PR link、merge、auto-complete 记录为 `activity_log` system event：

- `external_pr_linked`
- `external_pr_merged`
- `issue_completed_by_external_pr`

这些 event 进入 issue timeline/activity，不写普通 `comment`，也不触发 comment/mention 唤醒。

## 自动完成安全规则

只有同时满足以下条件，Multica 才会把 Issue 标记为 `done`：

1. 链接是 `authoritative`。
2. `completion_intent = true`。
3. Issue 当前不是 `done` / `cancelled`。
4. `external_pr_completion_policy` 缺省或规范化后为 `leaf_child_only`；`record_only` 与未知非空值均 fail closed。
5. `parent_issue_id` 非空，也就是它是一个子 Issue。
6. 它没有任何 child Issue，也就是它是 leaf child。
7. 同一 Issue 没有其他仍处于 `open` / `draft` 的 authoritative completion-intent 外部 PR。

因此不会自动完成：

- parent Issue；
- 没有 parent 的孤立 Issue；
- 自己还有 children 的中间节点 Issue；
- 只有 inferred/marker 链接的 Issue；
- 同一 Issue 仍有其他打开 PR 的情况；
- completion policy 为 `record_only` 或未知非空值的 Issue。

SQL status update 会再次检查 completion policy；如果 policy 在预检查后并发变为 `record_only` 或未知值，原子更新仍拒绝完成。成功完成后，Multica 复用 `notifyParentOfChildDone` 路径，让父 Issue 的阶段推进和唤醒逻辑继续由 Multica 内部规则负责。

## 环境变量

| 变量 | 必需 | 说明 |
|---|---:|---|
| `MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET` | 迁移期 | legacy link-token JWT signing secret；canonical secret 为空时也作为 fallback |
| `MULTICA_WORKLOAD_ASSERTION_SECRET` | 推荐 | canonical Workload Assertion signing secret；AGS verifier 配置相同 key material |
| `MULTICA_WORKLOAD_ASSERTION_ISSUER` | 否 | canonical assertion issuer；默认 `multica` |
| `MULTICA_WORKLOAD_ASSERTION_KEY_ID` | 否 | canonical assertion current `kid`；默认 `multica-workload-assertion-v1` |
| `MULTICA_EXTERNAL_PR_SERVICE_TOKEN` | 是 | service-to-service 写入和 complete 请求 token |
| `MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE` | 否 | legacy link-token JWT audience；默认 `external-pr-link` |
| `MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS` | 否 | 逗号分隔 provider allowlist；为空表示不限制 |
| `MULTICA_APP_URL` | 否 | 用于生成 `issue_url` |

## Self-hosting / 长期二开运行

长期运行 fork 版本时，不需要使用 `/tmp` override。`docker-compose.selfhost.yml` 已经把 External PR Integration 需要的环境变量透传给 `backend` 容器；实际 secret 值放在本地 `.env`、shell env 或部署 secret manager 中，不提交到 git。

推荐本地 fork `.env` 配置：

```env
MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET=<legacy/fallback secret shared with AGS>
MULTICA_WORKLOAD_ASSERTION_SECRET=<current assertion secret shared with AGS>
MULTICA_WORKLOAD_ASSERTION_ISSUER=multica
MULTICA_WORKLOAD_ASSERTION_KEY_ID=multica-workload-assertion-v1
MULTICA_EXTERNAL_PR_SERVICE_TOKEN=<random service token shared with AGS>
MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE=external-pr-link
MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS=ags
```

只替换 backend、保留当前 Postgres 和 frontend 的命令。如果 `proxy.golang.org` 偶发 EOF，可在命令前临时设置 `GOPROXY=https://goproxy.cn,direct`；`docker-compose.selfhost.build.yml` 会把它传入 Docker build。

```bash
docker compose \
  -f docker-compose.selfhost.yml \
  -f docker-compose.selfhost.build.yml \
  build backend

docker compose \
  -f docker-compose.selfhost.yml \
  -f docker-compose.selfhost.build.yml \
  up -d --no-deps --force-recreate backend
```

关键约束：

- 不要执行 `docker compose down`，避免影响 Postgres volume 和 frontend。
- `--no-deps` 确保只重建 `backend`，不重启 `postgres` / `frontend`。
- 切换后 `multica-backend-1` 的 image 应为 `multica-backend:dev`，而 `multica-postgres-1` / `multica-frontend-1` 应保持原样。
- 新路由检查应返回非 `404`：
  - `POST /api/integrations/workload-assertions`
  - `POST /api/integrations/external-pr/link-token`（legacy wrapper）
  - `POST /api/integrations/external-pr/links`
  - `POST /api/integrations/external-pr/complete-from-merge`
  - `GET /api/issues/{issue_id_or_key}/external-prs`

如果需要回滚到官方 backend，同样只操作 backend：

```bash
docker compose \
  -f docker-compose.selfhost.yml \
  up -d --no-deps --force-recreate backend
```

## Provider profile：AGS

AGS 只是第一个 provider。推荐 AGS 配置使用：

```yaml
multica:
  enabled: true
  server_url: http://localhost:3000
  external_pr_provider: ags
  link_token_audience: external-pr-link
  link_token_secret: ${same-as-MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET}
  workload_assertion:
    issuer: multica
    audience: urn:multica:external-pr-link:v1
    keys:
      multica-workload-assertion-v1:
        secret_file: /run/secrets/multica-workload-assertion
  service_token: ${same-as-MULTICA_EXTERNAL_PR_SERVICE_TOKEN}
  completion_on_merge:
    enabled: true
    mode: leaf_child_only
```

AGS 的 gh shim 在 task 环境中检测到 `MULTICA_TOKEN` 且 token 以 `mat_` 开头时，会先确认目标 repo host 与 `AGS_URL` / `AGENT_GIT_SERVICE_URL` / `MULTICA_EXTERNAL_PR_LINK_TOKEN_ALLOWED_HOSTS` 匹配，避免把隐藏 assertion 带到普通 GitHub/Forgejo PR body。匹配后它请求 canonical `/api/integrations/workload-assertions`，签入 provider、AGS instance 和 repository，再把返回 assertion 作为兼容隐藏 HTML marker 带入 PR body。AGS 服务端验证 purpose/audience/key/issuer/workload/target 后保存权威绑定；PR body 中的人类可见 Multica marker 只用于可读性和调试，不参与自动完成授权。

## Future / Roadmap

- 把当前 raw SQL handler 路径沉淀为 sqlc 生成方法。
- 把 GitHub 原有 PR 绑定逻辑逐步收敛到同一张 provider-neutral 表。
- 支持 provider-specific policy，例如不同 provider 的 completion mode、allowed repo scope、token audience。
