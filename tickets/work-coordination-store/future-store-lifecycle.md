# Future — Store lifecycle、archive 与 safe cleanup

**Status:** future capability contract；V1-V5只提供deletion guard。
**Blocked by:** [05-e2e-passive-deploy.md](05-e2e-passive-deploy.md)完成live tracer，且[Agent Kit read-only calibration](post-deploy-agent-kit-read-only-calibration.md) accepted；随后对真实Store数据、Issue/Workspace删除链和retention要求做gap audit。
**Blocks:** Reconciler write calibration/copilot/controlled、MINI-570 bootstrap/cutover与[graduation canaries](graduation-canaries.md)中的retirement/archive证明；仍需reconciliation control与goal-control同时live。

## Objective

让coordination scope/facts在保留evidence与receipt authority的前提下显式retire/archive，并为Issue/Workspace删除提供可审计、可回滚的application-level lifecycle。仓库禁止新增FK/REFERENCES/cascade，因此不能依赖数据库级cascade，也不能把orphan row当正常终态。

## Why separate

现有Issue删除路径可能在parent row delete前取消task、修改Autopilot或发布外部event。V1-V5选择RESTRICT-style guard，避免伪称这些副作用能和Store cleanup整体rollback。Future实现前必须先选择并证明：

- 保持guard，先显式archive/retire facts再允许delete；或
- 重构delete orchestration，使DB cleanup与issue row delete共享qtx，并把不可回滚副作用移到commit后/outbox。

不得实现或插入`CleanupIssueReferences(qtx)`作为本轮补丁，也不得在现有中段声称“整体原子”。Future若选择cleanup，必须另行设计完整orchestration与外部副作用恢复合同。

## Required capabilities

1. versioned scope state machine：active→retire_requested→archived；transition使用CAS、idempotency receipt与server actor/task provenance；
2. retention policy：独立`coordination_dependency`、record、typed issue refs、receipts和goal/control refs各自保留/归档规则；legacy `issue_dependency`继续由原authority拥有，future Store lifecycle不得查询、改写或清理它；
3. Issue/Workspace deletion preflight与explicit lifecycle API；
4. 若允许cleanup，Store rows与Issue/Workspace DB row在同一qtx提交/rollback；
5. task cancellation、Autopilot状态、event/outbox的commit ordering与failure recovery；
6. archive projection与read behavior；active queries不返回archived，但evidence仍可按授权读取；
7. CLI/built-in skill/source map、migration、deploy/rollback与live tests。

所有soft refs由application验证；禁止新增FK/cascade。

## Acceptance

- active scope仍被guard；archived scope按明确policy允许或拒绝delete；
- cleanup success/failure、issue delete failure、DB rollback均无orphan/半清理；
- 若外部副作用存在，证明只在commit后执行或可幂等reconcile，且不把其债务纳入DB atomicity claim；
- receipts/evidence不会因普通delete或application rollback被清空；
- concurrent retire/delete/CAS最多一个合法结果；
- Workspace teardown同样有exact policy与测试；
- fresh review、CI、当次deployment approval和live rollback演练完成。

## Non-goals

Controller lease/wake、Reconciler战术判断、goal contract、任意数据丢失、用down migration作为普通cleanup、恢复数据库cascade。

## Claim limit

当前只分配future lifecycle owner。V1-V5仍明确为delete guard；不声明archive/cleanup/delete orchestration已实现或安全。
