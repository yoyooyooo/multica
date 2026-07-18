# Work Coordination Store 实施票据集

## 目标与边界

本目录把 passive Work Coordination Store 拆成五个串行、可独立验证的 **vertical slices**。Store 首期只拥有：

- root-scoped coordination scope；
- canonical `downstream blocked_by upstream` dependency；
- strict typed blocker record；
- revision/CAS、request-hash receipt 与一致 inspect。

它不 wake、不派 task、不修改 Issue status/assignee/comment/metadata，不触发 Autopilot，也不成为 MINI-570 scheduling authority。

```text
root-scoped Autopilot（未来 heartbeat）
  → multica-work-reconciler Agent（未来 controller）
    → versioned goal-control contract（未来目标 authority）
    → Work Coordination Store（当前协调事实）
    → Multica server/CLI safety kernel
    → domain Agents/Squads
```

Passive Store **不拥有** objective、acceptance、claim limit、authority envelope、完整 work graph/handoff/evidence authority。其未来 SSoT 由 [future-goal-control-contract.md](future-goal-control-contract.md) 独立设计；01-05 不预建 nullable 空字段、任意 JSON/KV 或第二份目标 authority。

## Authority 与冻结基线

- Source authority：`github.com:443/yoyooyooo/multica` 的 `refs/heads/main`。
- Initial frozen base：`5e8661b8efb30c0728fb515ea7fa9a9b631a0c02`。
- 该 base 观察到的 migration ceiling 是 `201`；每片开始前必须在上一片 accepted exact head 重新扫描 `server/migrations/*.up.sql`，再分配唯一 prefix。
- 不得把 `source-mini`、`mini-runtime`、陈旧 docs worktree 或本地分叉 `main` 当实现 authority。
- 仓库架构、migration、FK/index 和编码规则以根 `CLAUDE.md` 为准。本目录是 feature delivery contract，不复制通用规则成为第二 SSoT。
- 产品方向来自本轮MINI-570 handoff snapshot；该session-local输入不进入repository authority或portable path claim，实施证据必须来自exact source/live readback。
- 不存在的 `plan.md` / `progress.md` 明确不作为输入。

## Vertical slices

| 顺序 | Ticket | 独立可验收能力 |
| --- | --- | --- |
| V1 | [01-scope-and-receipts.md](01-scope-and-receipts.md) | scope + revision + request-hash receipt；DB→service→API→CLI→initial built-in skill |
| V2 | [02-canonical-dependencies.md](02-canonical-dependencies.md) | canonical dependency add/list/resolve；workspace DAG lock/cycle 与 owner-scope conflict |
| V3 | [03-typed-blocker-records.md](03-typed-blocker-records.md) | strict blocker append/list/resolve；dependency 一致性与独立 resolve |
| V4 | [04-inspect-and-conformance.md](04-inspect-and-conformance.md) | consistent inspect、固定 pagination/order、完整 no-side-effect conformance、source-map 收口 |
| V5 | [05-e2e-passive-deploy.md](05-e2e-passive-deploy.md) | exact-head source acceptance、双客户端 E2E、当次批准后的 passive deploy/live tracer |

```text
initial frozen base + migration-ceiling recheck
  → V1 scope/receipts
  → V2 canonical dependencies
  → V3 typed blocker records
  → V4 inspect/conformance
  → V5 E2E/passive deploy
  → Agent Kit read-only calibration
  ├→ reconciliation control source+live
  ├→ versioned goal-control contract source+live
  └→ Store lifecycle/archive source+live
       （三项全部live）→ full Reconciler Agent/write calibration
                       → three fresh graduation roots
```

后续独立合同：

- [post-deploy-agent-kit-read-only-calibration.md](post-deploy-agent-kit-read-only-calibration.md)
- [future-reconciliation-control.md](future-reconciliation-control.md)
- [future-goal-control-contract.md](future-goal-control-contract.md)
- [future-store-lifecycle.md](future-store-lifecycle.md)
- [future-reconciler-agent.md](future-reconciler-agent.md)
- [graduation-canaries.md](graduation-canaries.md)

## 每片共享交付门

V1-V5 的箭头不是“上一片有提交”即可解锁。每片必须全部满足：

1. 从上一片 accepted exact head 创建 fresh、单 writer worktree；不得复用带未归属改动的 worktree。
2. 运行该片要求的 focused/full checks；DB tests 必须证明实际执行，不能以 skip + exit 0 代替。
3. 未参与实现的 fresh、single-agent/no-subagent reviewer 审查同一 exact head，P0-P2 全部关闭。
4. CI 绑定同一 exact head并终态成功；旧 head、working tree或仅本地结果不能替代。
5. 主交付 Agent复核 reviewer、CI、evidence、claim limit并记录 accepted head，才解除下一片 `Blocked by`。

V5 deployment approval 是额外永久 human gate；source gate不能替代当次 DB migration / server / CLI apply或restart批准。

## V1-V5 共享不变量

1. **Passive**：禁止 Issue status/assignee/comment/metadata、task、wake、Autopilot和会间接触发这些动作的event副作用。
2. **Canonical direction**：Store只写独立soft-ref表`coordination_dependency`中的`downstream blocked_by upstream`；`blocks`仅是派生read view。Legacy `issue_dependency`带既有FK/cascade，Store永不写入、迁移或查询它；其`blocks/blocked_by/related`行保持原authority。
3. **独立 authority**：dependency是当前关系；blocker record是typed evidence。二者resolve永不隐式联动。
4. **CAS**：revision统一为非负`int64`（PostgreSQL `BIGINT`范围）。除ensure外，mutation必须带`expected_revision`；真实变化恰增1，no-op/replay不增。
5. **Receipt**：`(workspace_id,idempotency_key)`唯一；每scope receipt由持workspace lock的transaction分配严格递增`receipt_ordinal`，pagination不依赖timestamp/UUID提交顺序。DB对`operation/resource_type`只做非空与长度约束，service以versioned typed allowlist扩展。Hash按下节唯一算法覆盖operation、canonical request和server actor/task。同key同hash replay原saved result；不同operation/hash/actor/task稳定conflict。Receipt不是授权缓存：任何replay/conflict返回前必须取得workspace lock并持锁重新验证current membership、task binding及Workspace/root/scope/resource存在；revoke/expiry/authority loss或entity delete后不得读取旧结果。
6. **一个active pair、一个owner scope**：workspace-global active endpoint pair只有一条canonical edge并由一个scope拥有。其他scope add/resolve返回`coordination_dependency_scope_conflict`，不得当作no-op；只有owner scope内允许idempotent no-op/resolve。跨scope association及多scope revision延后。
7. **原子DAG**：cycle check与edge commit在同一transaction及workspace advisory lock内；并发反向边最多一个成功。
8. **有界事实**：每scope最多`1000`条active dependencies、`1000`条open blockers；达到上限的新add/append返回`coordination_capacity_exceeded`且零写入。Resolve可降低相应active/open计数。Inspect必须在单响应完整返回这两组事实；receipt history单独固定100条分页。
9. **严格记录**：只接受versioned DTO、枚举和typed internal issue refs；拒绝未知字段、自由文本、URL、任意JSON/KV与超限输入。
10. **服务端身份**：actor/task/workspace只来自可信stamp；task actor只能访问实际issue-root与包含其issue的endpoint。
11. **Soft refs**：禁止新增`FOREIGN KEY`、`REFERENCES`、cascade。UUID refs由service在写时验证workspace/scope/issue存在及一致。
12. **删除fail closed且无TOCTOU**：所有Store mutations在自身transaction内取得统一workspace transaction-level advisory lock。Issue单删、BatchDeleteIssues与Workspace删除则在任何cache/task/Autopilot/event副作用前从pool取得dedicated connection，在该connection上取得冲突的session-level lock、持锁执行全量guard，并持续持有到实际entity DB delete完成或失败；不得把瞬时`Check*DeletionAllowed`当保护。Guard拒绝时保证零副作用；guard通过后仅保证不会新增Store soft-ref orphan，不承诺既有delete流程后续失败所产生的task/Autopilot/event债可回滚。完整协议见下节。
13. **Read service boundary**：每个public API/CLI read经过public typed service method；handler不得直查DB或自行重建authorization/filter/order。
14. **Goal authority不双写**：Store只保存已授权goal/handoff版本的不可变引用（该引用本身也在future ticket实现），不保存第二份目标合同。
15. **Stable errors**：wire/API/CLI只使用下节完整`coordination_*` code，不允许ticket自行省略前缀或按message substring分类。

## Workspace coordination advisory-lock SSoT

- Namespace固定为signed int32 `1464030001`（hex `0x57435331`，ASCII `WCS1`）。Workspace key固定为`SHA-256(canonical UUID 16 raw bytes)`的前4 bytes按big-endian解释后原位转换为signed int32；UUID文本先解析为16 bytes，不对字符串直接hash。所有调用使用PostgreSQL two-int advisory lock `(namespace, workspace_key)`；hash碰撞只会增加串行化，不影响安全。
- Store operation入口（V1 Ensure、V2 add/resolve、V3 append/resolve及后续operation）在初步syntax/identity解析后进入application transaction并调用`pg_advisory_xact_lock(namespace, workspace_key)`；随后持锁重新验证current membership/task authority及Workspace/root/scope/resource存在，才可查receipt并返回replay/conflict，或继续lock scope/resource、CAS、guard、mutation/receipt。Exact replay、typed conflict和no-op都不得绕过lock与二次revalidation。Transaction commit/rollback自动释放。
- Delete path在副作用前取得专用`*sql.Conn`（或等价独占session），在该session调用`pg_advisory_lock(namespace, workspace_key)`；guard与实际entity delete transaction都必须使用该connection。`defer`在commit/rollback或失败后调用同session的`pg_advisory_unlock`并验证返回true，再归还connection；unlock返回false或报错时必须close/discard该connection，禁止带锁归还pool。Process crash或connection close由PostgreSQL释放session lock。
- 单次操作只允许一个workspace。固定顺序为`workspace advisory lock → entity rows按UUID byte order → scope/resource rows按UUID byte order`；BatchDeleteIssues先解析并确认所有实际目标属于同一workspace，再取得一次lock、一次性guard全部目标。不得嵌套或排序多个workspace locks。
- 单Issue、BatchDeleteIssues、Workspace删除的session lock均早于cache invalidation、task cancellation、Autopilot mutation和event publish，并持续到实际Issue/Workspace DB row delete commit/rollback或失败返回。Workspace删除同样先lock/guard，不能先移除membership或cache。
- 所有guard都在持锁期间执行；禁止“check后unlock，再做副作用/delete”。Store mutation持有冲突xact lock并在写前复核entity，故并发结果只能是Store写先成功而delete被guard，或delete先完成而Store写因entity不存在失败。

## Canonical receipt hash SSoT

`request_hash = SHA-256(UTF-8(RFC 8785 canonical JSON))`。Canonical document固定为：

```json
{"actor":{"id":"<lowercase UUID>","task_id":null,"type":"member|agent"},"hash_version":1,"operation":"<exact allowlisted operation>","request":{},"workspace_id":"<lowercase UUID>"}
```

- `actor.task_id`始终存在；无task为JSON `null`，有task为lowercase UUID。UUID统一lowercase hyphenated；wire `expected_revision`在canonical document中规范为无前导零十进制**字符串**；object key由RFC 8785排序。
- Array的业务顺序若无语义，先按该slice规则normalize再做RFC 8785；禁止hash原始JSON bytes、map迭代顺序、display data、timestamp、idempotency key或HTTP路径。
- V1/V2/V3各自冻结`request`精确字段与operation/resource allowlist，并提供canonical JSON与SHA-256 golden tests；任何未来operation通过新的typed allowlist version加入，不能把service allowlist降级为任意字符串；DB继续只做非空/长度CHECK。

## Stable wire error SSoT

| Code | HTTP | CLI exit class |
| --- | --- | --- |
| `coordination_not_found` | 404 | not-found / 4 |
| `coordination_cross_workspace` | 403 | auth / 3 |
| `coordination_forbidden` | 403 | auth / 3 |
| `coordination_invalid_payload` | 400 | validation / 5 |
| `coordination_capacity_exceeded` | 409 | conflict / 6 |
| `coordination_self_dependency` | 422 | validation / 5 |
| `coordination_cycle` | 422 | validation / 5 |
| `coordination_revision_conflict` | 409 | conflict / 6 |
| `coordination_idempotency_conflict` | 409 | conflict / 6 |
| `coordination_dependency_scope_conflict` | 409 | conflict / 6 |
| `coordination_delete_blocked` | 409 | conflict / 6 |
| `coordination_internal` | 500 | runtime / 1 |

Server envelope固定为`{"error":{"code":"coordination_*","message":"...","details":{...}}}`；`details`可省略，出现时按code严格allowlist且不得含SQL、constraint或输入原文。CLI JSON stderr保持同shape，legacy string/body仅走现有HTTP fallback。

## Migration 可执行序列

仓库硬规则要求所有新index（包括新表PK/UNIQUE背后的index）使用`CONCURRENTLY`：

1. structure migration：创建table/columns/CHECK；PK/UNIQUE列先`NOT NULL`，禁止inline `PRIMARY KEY/UNIQUE`；
2. 每个index独立单语句migration：`CREATE [UNIQUE] INDEX CONCURRENTLY ...`；
3. attach migration：`ALTER TABLE ... ADD CONSTRAINT ... PRIMARY KEY|UNIQUE USING INDEX ...`。

Reverse down必须按相反顺序：先drop attached constraint；后续index down使用单语句`DROP INDEX CONCURRENTLY IF EXISTS ...`；最后删除structure。实际migration runner若不支持某组合，必须先以runner test证明并调整文件序列，不能把伪SQL写成合同。

## Fork narrative

每片更新`docs/fork-features/work-coordination-store/README.md`与registry，只写已落地事实，覆盖问题、设计、authority/security、non-goals、code/tests、deploy/rollback、upstreamability和retirement。

## 统一 Not Claimed

当前统一边界：

- Store尚未实现、测试、review、merge或部署；migration prefix未最终分配。
- Store尚未拥有goal-control、lifecycle/archive/cleanup、program scope、lease/fencing、wake claim、preflight、task authority snapshot、Reconciler/Autopilot binding或MINI-570 scheduling authority。
- Agent Kit read-only calibration尚未完成；metadata/comment/现有Issue lifecycle仍未切换。
- 三个fresh graduation roots尚未创建或执行；MINI-570永久保持assisted。
- 未批准本轮mini DB migration、server/CLI apply/restart、破坏性down或数据丢失。
- 不声称通用secret扫描能力；首期安全依赖strict typed allowlist、尺寸上限和拒绝未知字段。
