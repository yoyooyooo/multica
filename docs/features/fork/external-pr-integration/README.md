# External PR Integration

> Scope: 这是 official-tag-derived `fork/v0.4.12` 的 fork capability，语义来自已接受的 `fork/v0.4.9` generation及保留的`fork/v0.4.8` donor；旧分支与旧迁移账本不会被覆盖。AGS 是第一个接入方。源码与测试通过 generation PR 验收后才属于 accepted source；部署和运行时可用性需要独立证据。

## 目标

External PR Integration 让外部代码协作系统把一个 PR/MR/change 与 Multica Issue 建立**可信绑定**，并在外部 merge 发生后请求 Multica 执行最终的 Issue 状态转换。

核心原则：

1. **能通用化就通用化**：外部 PR 表和回调保持 `external_pr` / `external-pr` 语义；跨系统任务证明统一使用 purpose-bound Workload Assertion。
2. **能配置就配置**：具体 provider（例如 AGS）、允许列表、service token、签名 secret 通过配置或环境变量注入。
3. **不靠猜测完成 Issue**：PR 标题、分支名、正文里的 issue-like 文本都不是自动完成的权威来源。
4. **Multica 拥有最终状态转换**：外部系统只提交事实和请求，leaf-child-only 等安全规则由 Multica 原子判断。

## 当前实现

### 表

迁移文件：`231_external_pr_integration_reconcile`以additive方式兼容clean v0.4.12、historical 135、fork 212及已部署的fork/v0.4.9布局，并移除historical FK；`232`–`235`分别以单语句concurrent migration创建ID、identity、authoritative-open/draft blocker和receipt-idempotency indexes。`fork/v0.4.9`的旧migration文件与ledger保持原样，本generation使用新的`231`–`243`连续编号安全重放idempotent reconciliation。`239`先以单语句`CREATE UNIQUE INDEX CONCURRENTLY`建立`(workspace_id,idempotency_key)` partial unique index，`240`再以单语句concurrent drop移除historical/fork遗留的全局idempotency index；旧全局index与workspace-scoped合同并不等价。升级期间，runner在ledger skip前校验`232`–`237`及`239`的exact key列数、无`INCLUDE`列、predicate、ready/valid状态，并恢复旧runner误记账的invalid/错误定义artifact；`240`删除legacy authority前再次fail closed校验。`241_external_pr_index_reconciliation_fence`完成最终恢复并记账后永久关闭这些历史hook，使未来migration可有意演进index定义而不会被旧定义覆盖。241是forward-only边界：runner和down migration都会拒绝跨越该fence，且不会删除ledger；需要回到pre-241 generation时，必须用accepted pre-241 backup恢复整个数据库，不能通过普通migration down伪造可逆性。`231`/`236`使用有限`lock_timeout`，繁忙部署超时后必须先排空写流量再重试，不能无限等待形成锁队列。`242`/`243`分别为全状态link列表/清理与receipt清理增加`(workspace_id,issue_id,updated_at)`和`(workspace_id,issue_id)` concurrent indexes；普通pre-hook只在尚未记账时恢复失败的concurrent build，不把性能index变成永久schema authority。migration matrix验证旧index消失、新index exact catalog authority、cleanup indexes及fence，并证明两个workspace可合法复用同一key。该matrix在同一个专属disposable database中为各支持起点创建隔离schema；它证明schema-local relation/ledger收敛，不声称独立database或database-global extension状态彼此隔离。

新增表：`external_pull_request_link`和append-only `external_pull_request_receipt`。后者在workspace+idempotency-key advisory lock下保存canonical payload hash；同一key不同payload返回conflict，exact replay不改fact。receipt没有FK，Issue/workspace删除路径在同一应用事务中显式清理。

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
| `idempotency_key` | 最近一次携带的请求键；幂等authority是append-only receipt，不是该可更新projection字段 |

唯一约束：

```text
(workspace_id, provider, external_repo, external_number)
```

identity约束保证同一provider PR在workspace内只有一个fact且不可跨Issue重绑；`external_pull_request_receipt`保证每个idempotency key绑定一个immutable canonical payload。没有可信provider event version时只声明`merged`为absorbing state，不虚构open/draft/closed的全序。

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

该接口在普通 Auth group 内，但要求 `X-Actor-Source: task_token`，因此只能由 task-scoped `mat_` token 调用。客户端不能提交或覆盖 workload 身份；服务端根据task row、Agent row、workspace authority row和auth middleware注入的workspace/task headers推导：

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
  "requested_resource": {
    "service": "ags",
    "repository": "jackie/agent-kit"
  },
  "requested_operation": {
    "name": "pr.rebase",
    "constraints": {
      "pull_request_number": 41,
      "forgejo_pull_request_number": 52,
      "expected_head_sha": "1111111111111111111111111111111111111111",
      "expected_base_sha": "2222222222222222222222222222222222222222"
    }
  },
  "requested_capabilities": ["repo:read", "repo:write"],
  "requested_ttl": "15m"
}
```

`requested_resource`和`requested_operation`必须同时出现或同时缺席；出现时resource必须精确匹配target，operation必须使用canonical小写名，capabilities必须精确匹配该operation，`constraints`必须是非`null` JSON object。Multica对team-v4九项implemented operation按AgentKit production shape逐项闭合，而不是接受通用scalar：`repo.read`、`git.read`、`git.push`只接受`{}`；`pr.create`要求完整且仅有canonical branch `base_ref`与`head_ref`两键；`pr.read`要求恰好一个variant（正safe-integer `pull_request_number`或canonical `head_ref`）；`pr.rebase`沿用两个正safe-integer PR编号与两个lowercase40 SHA的四键exact intent；`pr.merge`要求两个正safe-integer PR编号、lowercase40 expected head SHA与registered merge method，并在签发时切换到`multica.workspace.maintainer.v1`；`review.read`要求两个正safe-integer PR编号；`ci.read`只接受三种variant：用于repo-wide runs list的`{}`、仅含正safe-integer `run_id`的run/log读取，或两个正safe-integer PR编号加optional lowercase40 `head_sha`的PR runs读取。AgentKit Forgejo真实shape fixture固定上述runs/log请求，并固定state-only PR list、event-only、SHA-only、mixed或unknown CI shape在签名前拒绝。AgentKit production发送short branch name；等价的canonical `refs/heads/...`也可接受，其他ref namespace拒绝。unknown/extra key、mixed `pr.read` variants、旧`exact_head`、missing/wrong/null type、secret-shaped value、非canonical ref/SHA及unsafe number均在签名前fail closed。Default class仍不能签发`pr.merge`；只有请求本身是exact `pr.merge`时producer才选择separate maintainer class。三项deferred `review.submit`、`repo.admin`、`repo.create`由Workload Assertion signer直接拒绝，即使constraints为`{}`也不签发；legacy capability mapping不能合成缺少两项ref的`pr.create`、maintainer merge或任何deferred authority。该source候选在backend部署、AGS authority apply与fresh E2E前不证明live merge可用。

可选`requested_ttl`只允许用于session exchange，必须是trim后的canonical `<positive integer><s|m|h>` string且不超过`15m`；invalid type、`null`、unknown/compound unit、secret-shaped value、零值及超限值均在签名前拒绝。有效值原样进入JWT top-level，缺失时claim也保持缺失。上述内容只证明Multica producer contract已对齐当前AgentKit request与AGS team-v4 registry，不证明任一AGS verifier revision已经部署或验收。

每次请求都签发独立的五分钟 HS256 JWT；即使 task 和 target 相同，两个 purpose 也使用不同 audience、JTI 和 token instance。JWT `iss`来自deployment-unique `MULTICA_WORKLOAD_ASSERTION_ISSUER`；`workload_context.issuer_instance_id`只来自独立、secret-free的`MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID`，后者必须与`iss`不同并精确等于AGS `trusted_issuers[].id`。两者共同的closed shape只有 `ver`、`iss`、`aud`、`sub`、`jti`、`iat`、`nbf`、`exp`、`purpose`、`source=task_token`、signed `target`、`requested_capabilities`和server-derived基础`workload`（workspace、Agent、task/run及purpose允许的Issue/actor字段）。

`external_pr_link`要求Issue lineage与空capabilities，**不包含**`scope`、`workload_context`、`authority`或`requested_ttl`。只有`ags_session_exchange`可额外包含top-level `requested_ttl`及signed `scope{schema,resource,operation,requested_capabilities,compatibility_input?}`，并在workload中包含`workload_context{schema,issuer_instance_id,subject,correlation_id,workspace_id,agent_id,squad_id?,issue_id?,issue_key?,task_id,run_id,trigger_id?,runtime_id?}`及`authority{schema=workload.authority.v1,team_identity_id,membership_epoch>0,policy_class}`。这些只是Multica-owned signed source contract；Multica不决定AGS principal、native repo grant或最终Session capability，也不声明AGS runtime已accepted或deployed。

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

`Authorization`必须是大小写和空白均精确的`Bearer <token>`；raw token、其他scheme、额外前后空白均返回401。Complete endpoint使用同一严格合同。

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

写入前会验证 `issue_id` 确实属于提交的 `workspace_id`，跨 workspace 组合会被拒绝且不会写 link/activity；`external_url` 与 `merge_url` 若非空，必须是绝对 `http(s)` URL。HTTP错误合同为closed分类：请求shape/字段验证失败返回`400`，immutable identity或idempotency payload冲突返回`409`；begin/lock/query/write/activity/completion/commit等基础设施失败返回可重试`503`和固定generic message，响应不得回显数据库、SQLSTATE或内部错误文本。External provider transaction固定使用`provider-workspace → identity/idempotency → sorted Issue advisory → fresh link/receipt row lock`顺序；Issue、batch、workspace和integration删除遵循相容顺序。

### Merge 后请求完成

```http
POST /api/integrations/external-pr/complete-from-merge
Authorization: Bearer <service token>
Content-Type: application/json
```

该接口会先upsert外部PR链接为`merged`，然后与GitHub/native VCS入口共同调用唯一completion kernel。Policy parser不trim、不改大小写：key absent、exact `""`、exact `leaf_child_only`允许leaf-child terminal；exact `record_only`只记事实；null、非字符串、unknown、大小写或空白变体全部fail closed并返回稳定reason `completion_policy_unsupported`。

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

Frontend通过`packages/core` API schema/client和TanStack Query读取同一`/external-prs` projection。Issue详情侧栏提供独立的`External PRs`区块，展示provider PR、merge projection、state、confidence、completion intent和merged SHA；该区块不受GitHub/native PR sidebar开关控制。Issue timeline同时渲染下列system event。Frontend不写或推断External PR事实。

External PR link、merge、auto-complete 记录为 `activity_log` system event：

- `external_pr_linked`
- `external_pr_merged`
- `issue_completed_by_external_pr`

这些event进入issue timeline/activity，不写普通`comment`，也不触发comment/mention唤醒。新Link/状态事务提交后，backend发布`pull_request:updated`；exact idempotency replay不重复广播。frontend同时invalidate native/External PR projection，并按event中的`issue_id`刷新timeline。

## Source and test anchors

- Backend fact/API/completion: `server/internal/handler/external_pr_integration.go`、`pull_request_completion.go`及对应tests。
- Routes: `server/cmd/server/router.go`、`external_pr_routes_integration_test.go`。
- CLI: `server/cmd/multica/cmd_issue.go`及tests。
- Frontend wire contract: `packages/core/types/github.ts`、`api/schemas.ts`、`api/client.ts`、`github/queries.ts`。
- Frontend render: `packages/views/issues/components/pull-request-list.tsx`、`issue-detail.tsx`及对应render tests。
- Live headless proof: `multica issue external-prs <issue> --output json`。
- Browser proof: Issue详情必须显示独立`External PRs`区块和真实provider/merge链接；render test不替代browser reachability。

## Mini live acceptance

`fork-mini-v0.4.12-r2`在规范公开入口`https://mini.tail9146e0.ts.net:37445`完成了独立authenticated browser proof：

- `MINI-1278`显示AGS PR `jackie/agent-kit#279`、Forgejo projection `#266`、`closed`、`authoritative`、completion intent及timeline link activity；
- disposable `MINI-1283`先显示AGS PR `#280` / Forgejo projection `#267`为`open`；
- durable operator通过受支持的`ags-cli pr close`关闭AGS authority后，浏览器在没有reload的情况下收到`pull_request:updated`，重新GET exact Issue `/external-prs`并把侧栏收敛为`closed`；
- browser errors为空；disposable branch已删除并验证absent，三个proof Issue均已取消。

Owning receipt SHA-256：`941ab61790592b1046eeb179436f73c724f186cdaebc163ad06b1f3e5555b977`。该receipt不声明Forgejo CI、merge或独立human approval。

## 自动完成安全规则

只有同时满足以下条件，Multica 才会把 Issue 标记为 `done`：

1. 链接是 `authoritative`。
2. `completion_intent = true`。
3. Issue 当前不是 `done` / `cancelled`。
4. `parent_issue_id` 非空，也就是它是一个子 Issue。
5. 它没有任何 child Issue，也就是它是 leaf child。
6. GitHub、native VCS和provider-neutral external PR三类事实合并后，没有任何仍处于`open` / `draft`的有效链接，并且至少有一个`merged`且带close/completion intent的权威事实。
7. completion policy满足上述exact allowlist；`record_only`或unsupported均不得terminal。

因此不会自动完成：

- parent Issue；
- 没有 parent 的孤立 Issue；
- 自己还有 children 的中间节点 Issue；
- 只有 inferred/marker 链接的 Issue；
- 同一 Issue 仍有其他打开 PR 的情况。

成功完成后，Multica 复用 `notifyParentOfChildDone` 路径，让父 Issue 的阶段推进和唤醒逻辑继续由 Multica 内部规则负责。

## 环境变量

| 变量 | 必需 | 说明 |
|---|---:|---|
| `MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET` | 迁移期 | legacy link-token JWT signing secret；canonical secret 为空时也作为 fallback |
| `MULTICA_WORKLOAD_ASSERTION_SECRET` | 推荐 | canonical Workload Assertion signing secret；AGS verifier 配置相同 key material |
| `MULTICA_WORKLOAD_ASSERTION_ISSUER` | 所有self-host启动必需 | stable、deployment-unique JWT `iss`；Compose拒绝缺失/空值，server启动拒绝缺失、空值及placeholder `multica`；Helm render同样要求并传递唯一值 |
| `MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID` | 所有self-host启动必需 | stable、canonical safe、secret-free authority linkage；必须不同于JWT `iss`并精确对应AGS `trusted_issuers[].id`，不得从target名猜测；startup/readiness/Compose/Helm均fail closed |
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
MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:<stable-instance-id>
MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=<ags-trusted-issuer-id>
MULTICA_WORKLOAD_ASSERTION_KEY_ID=multica-workload-assertion-v1
MULTICA_EXTERNAL_PR_SERVICE_TOKEN=<random service token shared with AGS>
MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE=external-pr-link
MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS=ags
```

包含frontend projection的generation部署必须从同一exact head构建backend和frontend。`docker-compose.selfhost.build.yml`把可选`GOPROXY`作为reviewed backend build arg传入，默认`https://proxy.golang.org,direct`；shell值只有在rendered Compose config中读回后才构成生效证据。

从曾使用`multica_backend_uploads`的generation升级时，先执行`make selfhost-migrate-uploads`。该preflight只在旧volume存在时停止其owning backend，以non-overwrite方式复制并逐文件验证到`./data/uploads`，保留旧volume并写secret-safe receipt；冲突文件fail closed。普通`make selfhost`和`make selfhost-build`会在启动应用前执行同一preflight。

```bash
docker compose \
  -f docker-compose.selfhost.yml \
  -f docker-compose.selfhost.build.yml \
  build backend frontend

docker compose \
  -f docker-compose.selfhost.yml \
  -f docker-compose.selfhost.build.yml \
  up -d --no-deps --force-recreate backend frontend
```

关键约束：

- 不要执行 `docker compose down`，避免影响Postgres、network和`multica_pgdata`。
- `--no-deps`确保不重启`postgres`。
- Source Compose以`./data/uploads:/app/data/uploads`保留uploads bind authority；target override可把source绝对化，但不得改为新的named volume。旧named volume仅是迁移source，不是未来runtime authority。
- External PR路由检查应返回非`404`：
  - `POST /api/integrations/workload-assertions`
  - `POST /api/integrations/external-pr/link-token`（legacy wrapper）
  - `POST /api/integrations/external-pr/links`
  - `POST /api/integrations/external-pr/complete-from-merge`
  - `GET /api/issues/{issue_id_or_key}/external-prs`

回滚必须使用已记录的backend/frontend image pair；跨越forward-only migration 241时还必须恢复对应数据库备份，不能只交换镜像。

## Provider profile：AGS

AGS是第一个接入provider。以下内容是Multica-owned integration contract；具体accepted source/runtime和target-local proof由generation manifest及外部系统receipt证明。示例中的issuer必须与本deployment唯一值完全一致：

```yaml
multica:
  enabled: true
  server_url: http://localhost:3000
  external_pr_provider: ags
  link_token_audience: external-pr-link
  link_token_secret: ${same-as-MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET}
  workload_assertion:
    issuer: urn:multica:deployment:<stable-instance-id>
    audience: urn:multica:external-pr-link:v1
    keys:
      multica-workload-assertion-v1:
        secret_file: /run/secrets/multica-workload-assertion
  service_token: ${same-as-MULTICA_EXTERNAL_PR_SERVICE_TOKEN}
  completion_on_merge:
    enabled: true
    mode: leaf_child_only
```

跨仓验收合同：provider client只能经canonical `/api/integrations/workload-assertions`取得purpose-bound assertion；AGS必须验证purpose、audience、key、deployment-unique issuer、workload和target后再保存权威绑定。任何PR body marker都只能作可读projection，不能参与完成授权。本文不以配置示例替代AGS source、provider projection或runtime事实。

## Future / Roadmap

- 把当前 raw SQL handler 路径沉淀为 sqlc 生成方法。
- 评估是否需要把三类provider facts物理收敛到同一张表；当前只共享aggregate和terminal materialization authority。
- 支持 provider-specific policy，例如不同 provider 的 completion mode、allowed repo scope、token audience。
