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
- 产品方向来自 `/Users/yoyo/.pi/agent/handoffs/mini-570-matt-loop/handoff.md`；实施证据必须来自 exact source/live readback。
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
  ├→ reconciliation control
  ├→ versioned goal-control contract
  └→ Store lifecycle/archive
       → full Reconciler Agent/write calibration
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
2. **Canonical direction**：只写 `downstream blocked_by upstream`；`blocks`仅是派生read view。legacy `issue_dependency`的 `blocks/blocked_by/related` 行不回填、不改写、不删除。
3. **独立 authority**：dependency是当前关系；blocker record是typed evidence。二者resolve永不隐式联动。
4. **CAS**：revision统一为非负`int64`（PostgreSQL `BIGINT`范围）。除ensure外，mutation必须带`expected_revision`；真实变化恰增1，no-op/replay不增。
5. **Receipt**：`(workspace_id,idempotency_key)`唯一；hash覆盖operation、canonical request和server actor/task。同key同hash replay原结果；不同operation/hash/actor/task稳定conflict。Receipt不是授权缓存：任何replay返回前必须重新验证当前membership、task binding、root/scope authority；revoke/expiry/authority loss后不得读取旧结果。
6. **一个active pair、一个owner scope**：workspace-global active endpoint pair只有一条canonical edge并由一个scope拥有。其他scope add/resolve返回`coordination_dependency_scope_conflict`，不得当作no-op；只有owner scope内允许idempotent no-op/resolve。跨scope association及多scope revision延后。
7. **原子DAG**：cycle check与edge commit在同一transaction及workspace advisory lock内；并发反向边最多一个成功。
8. **严格记录**：只接受versioned DTO、枚举和typed internal issue refs；拒绝未知字段、自由文本、URL、任意JSON/KV与超限输入。
9. **服务端身份**：actor/task/workspace只来自可信stamp；task actor只能访问实际issue-root与包含其issue的endpoint。
10. **Soft refs**：禁止新增`FOREIGN KEY`、`REFERENCES`、cascade。UUID refs由service在写时验证workspace/scope/issue存在及一致。
11. **删除fail closed**：第一波不重构现有Issue/Workspace删除链的task cancellation、Autopilot和post-commit event。只要目标仍被scope、endpoint、record evidence/resolution或receipt引用，删除必须在任何现有副作用前返回`coordination_delete_blocked`；lifecycle cleanup/archive由后续窄ticket拥有。
12. **Read service boundary**：每个public API/CLI read经过public typed service method；handler不得直查DB或自行重建authorization/filter/order。
13. **Goal authority不双写**：Store只保存已授权goal/handoff版本的不可变引用（该引用本身也在future ticket实现），不保存第二份目标合同。
14. **Stable errors**：至少覆盖`not_found`、`cross_workspace`、`forbidden`、`invalid_payload`、`revision_conflict`、`idempotency_conflict`、`self_dependency`、`cycle`、`dependency_scope_conflict`、`delete_blocked`。

## Migration 可执行序列

仓库硬规则要求所有新index（包括新表PK/UNIQUE背后的index）使用`CONCURRENTLY`：

1. structure migration：创建table/columns/CHECK；PK/UNIQUE列先`NOT NULL`，禁止inline `PRIMARY KEY/UNIQUE`；
2. 每个index独立单语句migration：`CREATE [UNIQUE] INDEX CONCURRENTLY ...`；
3. attach migration：`ALTER TABLE ... ADD CONSTRAINT ... PRIMARY KEY|UNIQUE USING INDEX ...`。

Reverse down必须按相反顺序：先drop attached constraint；后续index down使用单语句`DROP INDEX CONCURRENTLY IF EXISTS ...`；最后删除structure。实际migration runner若不支持某组合，必须先以runner test证明并调整文件序列，不能把伪SQL写成合同。

## Fork narrative

每片更新`docs/fork-features/work-coordination-store/README.md`与registry，只写已落地事实，覆盖问题、设计、authority/security、non-goals、code/tests、deploy/rollback、upstreamability和retirement。

## 统一 `not_claimed`

当前统一边界：

- Store尚未实现、测试、review、merge或部署；migration prefix未最终分配。
- Store尚未拥有goal-control、lifecycle/archive/cleanup、program scope、lease/fencing、wake claim、preflight、task authority snapshot、Reconciler/Autopilot binding或MINI-570 scheduling authority。
- Agent Kit read-only calibration尚未完成；metadata/comment/现有Issue lifecycle仍未切换。
- 三个fresh graduation roots尚未创建或执行；MINI-570永久保持assisted。
- 未批准本轮mini DB migration、server/CLI apply/restart、破坏性down或数据丢失。
- 不声称通用secret扫描能力；首期安全依赖strict typed allowlist、尺寸上限和拒绝未知字段。
