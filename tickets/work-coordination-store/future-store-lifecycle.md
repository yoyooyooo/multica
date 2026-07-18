# Future — Store lifecycle、archive 与 safe cleanup

**Status:** future capability contract；V1-V5只提供deletion guard。
**Blocked by:** [05-e2e-passive-deploy.md](05-e2e-passive-deploy.md)完成live tracer，且[Agent Kit read-only calibration](post-deploy-agent-kit-read-only-calibration.md) accepted；随后对真实Store数据、Issue/Workspace删除链和retention要求做gap audit。
**Blocks:** Reconciler write calibration/copilot/controlled与MINI-570 assisted facts bootstrap；仍需reconciliation control与goal-control同时live。Lifecycle/archive必须先独立完成source acceptance、获批apply与live proof，[graduation canaries](graduation-canaries.md)随后依赖并验证其已上线行为；本ticket的source/live acceptance绝不反向依赖graduation。它不解锁MINI-570 authority/autonomy cutover。

## Objective

让coordination scope/facts在保留evidence与receipt authority的前提下显式retire/archive，并为Issue/Workspace删除提供可审计、可回滚的application-level lifecycle。仓库禁止新增FK/REFERENCES/cascade，因此不能依赖数据库级cascade，也不能把orphan row当正常终态。

## Why separate

V1-V5只为并发安全guard做窄orchestration seam：guard后的必需pre-delete task/token/Autopilot/Workspace DB mutation与entity delete共享qtx；commit/rollback后`Finish`先verified unlock/release，只有成功Finish后才运行不可回滚cache/S3/metrics/reconciliation/event effects并沿既有路径记录operator debt。V1-V5仍不删除Store facts、不提供archive policy、outbox或可靠投递/自动修复。Future实现前必须选择并证明：

- 保持guard，先显式archive/retire facts再允许delete；或
- 在V1 seam上加入Store cleanup，使cleanup与Issue/Workspace row delete共享qtx，并把post-commit debt升级为durable outbox/reconciler recovery。

不得把`CleanupIssueReferences(qtx)`插入现有中段后声称“整体原子”。Future若选择cleanup，必须另行设计完整retention、orchestration与外部副作用恢复合同。

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

本ticket须在任何graduation canary创建前独立完成以下source+live gates；graduation结果不是本ticket进入live或被accept的前置条件。

- active scope仍被guard；archived scope按明确policy允许或拒绝delete；
- cleanup success/failure、issue delete failure、DB rollback均无orphan/半清理；
- 若外部副作用存在，证明只在commit后执行或可幂等reconcile，且不把其债务纳入DB atomicity claim；
- receipts/evidence不会因普通delete或application rollback被清空；receipt history/reference本身不作为独立删除阻塞，retention/archive preflight按显式policy保存或迁移它，删除后的replay因current authority/resource mismatch而fail closed；
- concurrent retire/delete/CAS最多一个合法结果；
- Workspace teardown同样有exact policy与测试；
- fresh review、CI、当次deployment approval和live rollback演练完成。

## Non-goals

Controller lease/wake、Reconciler战术判断、goal contract、任意数据丢失、用down migration作为普通cleanup、恢复数据库cascade。

## Claim limit

当前只分配future lifecycle owner。V1-V5仍明确为delete guard；不声明archive/cleanup/delete orchestration已实现或安全。
