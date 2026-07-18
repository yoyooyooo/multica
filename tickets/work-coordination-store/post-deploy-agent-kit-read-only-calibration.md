# Post-deploy — Agent Kit read-only contract 与 MATT enrollment 校准

**Status:** 独立跨仓交付ticket；不属于Multica V1-V5 source。
**Blocked by:** [05-e2e-passive-deploy.md](05-e2e-passive-deploy.md)完成accepted live tracer。
**Blocks:** [future-reconciliation-control.md](future-reconciliation-control.md)、[future-goal-control-contract.md](future-goal-control-contract.md)与[future-store-lifecycle.md](future-store-lifecycle.md)开始实施；它不单独解锁任何write能力。

## Objective

在Agent Kit canonical assets中加入**只读**Work Coordination Store认知，让MATT coordinator/critic/observer能够通过已部署的`multica coordination inspect`读取scope revision、dependency、blocker和receipt refs，并在shadow模式与现有Issue lifecycle/metadata口径对账。

该ticket不让Store成为scheduling authority，不调用Store mutation，不创建Reconciler/Autopilot，不做MINI-570 assisted facts bootstrap或authority/autonomy cutover，也不改变当前Stage/coordinator职责。

## Authority 与目标仓库

- Agent Kit reusable assets以Agent Kit仓库自身authority为准；当前项目实例的source/PR/merge/publish必须遵循mini AGS→Forgejo→backup流程，不能把Multica GitHub仓库或本ticket目录当Agent Kit source authority。
- 实施前在Agent Kit exact accepted head创建fresh single-writer worktree，并读取其`AGENTS.md`、docs governance和当前MATT contract。
- Multica live read contract固定为V5 accepted server/CLI版本；若V5之后API drift，先回到Multica修复，不在skill里猜兼容。

## In-scope assets

实施worker按Agent Kit当时结构定位并最小修改：

- `skills/contracts/multica-issue-collaboration-contract/SKILL.md`及其references；
- MATT Loop coordinator/critic/observer canonical templates；
- `docs/multica/issue-collaboration.md`或其当时current authority；
- target/topology projection中仅为read-only enrollment所需的skill绑定；
- asset/self-containment/source-map/projection tests。

若实际owner路径已变化，先修正ticket source map，不创建平行兼容文件。

## Read-only behavior contract

1. 只调用`scope get`/`inspect`/dependency list/blocker list等read commands；不得调用ensure/add/resolve。
2. Store输出按V5 exact contract解释；comment/metadata仍是历史/current Issue路径，尚未切换为Store projection或双写。
3. 当Store scope不存在、版本不匹配、读取失败或revision变化时，返回Agent Kit本地classification `not_available|stale_read`并保持现有协调路径；不得自行创建scope或修复事实。
4. Shadow对账只记录分类：match、legacy-only、store-only、conflict、unavailable；不改Issue/status/assignee/dependency/blocker/task。
5. MINI-570永久保持`assisted`；不得把read-only校准描述成autonomy证据。
6. 不引入第二Supervisor Agent；未来`multica-work-reconciler`仍由独立ticket拥有。
7. 不保存raw token、credential、custom env、request hash或完整payload；只保留secret-safe IDs/revisions/result classifications。

## Current authority声明

校准完成后仍必须明确：

- Native Stage/现有coordinator继续拥有当前liveness/frontier行为；
- passive Store尚未拥有wake/dispatch/status/terminal authority；
- metadata/comment不得与Store双写为两份canonical dependency；
- 任何Reconciler write calibration、copilot、controlled或MINI-570 assisted facts bootstrap都必须同时等future reconciliation control、versioned goal-control与Store lifecycle三项source+live proof完成，并另获独立批准；该bootstrap不切换MINI-570 scheduling/write/terminal authority，本路线不授权其authority/autonomy cutover。

## Acceptance / tests

- skill/template/docs对同一read-only边界一致，无“Store已接管”或提前write claim；
- command allowlist/fixtures证明只发read请求；
- scope missing、API unsupported、revision drift、tenant/forbidden均fail closed且零mutation；
- shadow classification fixture覆盖match/legacy-only/store-only/conflict/unavailable；
- MATT coordinator仍保持原Stage/frontier职责，observer只读；
- self-containment、source-map、topology/placement和target projection tests通过；
- fresh independent review、exact-head CI、authorized linear merge、origin/backup及目标projection readback完成。

Live shadow canary至少覆盖一个非MINI-570 fresh root或disposable fixture；它只证明读取与分类，不证明write control。此ticket完成后，control、goal与lifecycle任一项缺失都继续fail closed，不能进入write calibration/copilot/controlled或MINI-570 assisted facts bootstrap。

## Non-goals

Store mutation、MINI-570 assisted facts bootstrap或authority/autonomy cutover、controller lease/wake claim、Reconciler Agent、Autopilot、Issue cleanup、deployment/restart、graduation roots。

## Rollback / claim limit

Rollback移除read-only enrollment/skill projection，恢复旧Agent Kit assets；Multica Store和数据不变。保留shadow evidence，不将失败重写为成功。

最多声明Agent Kit/MATT assets能够只读消费V5 Store并完成shadow分类；不得声明Store成为scheduling authority、Reconciler可写、MINI-570恢复autonomous或Agent Kit/Multica双写已安全。
