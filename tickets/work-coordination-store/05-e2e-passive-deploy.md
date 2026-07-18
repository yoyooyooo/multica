# V5 — Exact-head acceptance、passive deployment 与 live tracer

**Blocked by:** V4 accepted head已满足[共享交付门](README.md#每片共享交付门)。
**Blocks:** [post-deploy-agent-kit-read-only-calibration.md](post-deploy-agent-kit-read-only-calibration.md)。

## Objective

对V1-V4聚合后的同一exact head完成fresh completion review与CI；取得当次明确deployment approval后，把passive Store部署到指定mini环境，用临时workspace和两个独立CLI process证明scope→dependency→blocker→independent resolve→inspect全流程及零调度副作用。

没有当次DB migration/server/CLI apply批准时，只能完成source acceptance与deployment plan，V5保持partial/blocked，不能声称live accepted。

## Exact owning modules

- 仅补V1-V4 exact-head review发现的组合E2E/race/migration-runner tests。
- `docs/fork-features/work-coordination-store/README.md`、registry与`passive-live-evidence.md`。
- 必要的secret-safe tracer harness；优先复用现有CLI/API，不另建生产路径。

V5使用现有build/deploy tooling。若发现packaging必须改动，停止并新开窄ticket；不得隐式修改deploy、Autopilot、Issue lifecycle、Agent Kit、UI或future control。

## Source acceptance

### Aggregate flow

用真实DB/router/CLI执行V4同构流程：

1. A ensure scope→`r0`；
2. A add `B blocked_by C`→`r1`；
3. A append blocker→`r2`；
4. B inspect exact `r2`；
5. 独立B process仅以同一actor + 同一task binding重放A key：agent使用同一`task_id`，member两次均为`task_id=null`；得到原receipt、revision不变；
6. 不同actor复用key得到`coordination_idempotency_conflict`；即使actor相同，membership/task或root authority失效，或receipt引用的saved scope/dependency/blocker等resource已删除、不存在或不再可读，也必须fail closed，不能用old key返回旧receipt；
7. B resolve blocker→`r3`，dependency仍active；
8. A resolve dependency→`r4`，最终active edge/open blocker为空；
9. `coordination_revision_conflict`、`coordination_idempotency_conflict`、`coordination_cycle`、`coordination_self_dependency`、`coordination_cross_workspace`、`coordination_dependency_scope_conflict`、`coordination_forbidden`与`coordination_invalid_payload`均按README SSoT返回且零部分写；
10. 并发反向edge最多一方成功；Ensure/Add/Append分别与单删、BatchDeleteIssues、Workspace删除race均不产生新orphan。三类delete按同一phase执行：Acquire pinned `*pgxpool.Conn`→session lock→begin `pgx.Tx`→UUID Issue locks或Workspace row+chat locks→guard；guard通过后在同qtx执行必须pre-delete的task/token/Autopilot DB mutations、membership teardown及final delete；commit后运行bounded metrics/reconciliation/cache/S3/event finalizer；最后verified unlock/release，异常Hijack+close/discard。Guard或qtx失败零外部副作用并整体rollback；post-commit finalizer失败只记录typed retry debt，不虚构DB rollback；
11. 两类容量独立计数、互不占用配额，并且每组case从第1条到第1001条都在同一个scope内完成：同一scope内active dependencies第1至第1000条均可写，第1001条dependency返回`coordination_capacity_exceeded`且前1000条dependency、revision、receipt保持不变；独立的open-blocker case也在其同一scope内从第1至第1000条均可写，第1001条blocker返回同code且前1000条record/ref、revision、receipt保持不变。分别覆盖两组独立的1000/1001 cases后，inspect单响应完整返回各自上限内的active/open facts，receipt refs固定100条按`receipt_ordinal DESC`与ordinal upper-bound cursor分页。

### No-side-effect snapshot

前后对root/B/C的status、assignee、`updated_at`、comments、tasks、Autopilot runs、relevant metadata exact比较，全部不变。Store facts/receipts单列。

### Required checks

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
make check
git diff --check
```

- `make sqlc`后必须由上述generated目录的`git diff --exit-code`及porcelain clean assertion证明无tracked/untracked drift；`git diff --check`不能替代；
- migration覆盖fresh、legacy upgrade与空Store down/up；新dependency只在`coordination_dependency`，legacy `issue_dependency` schema/rows原样且所有Store query忽略；
- DB-required harness在DB不可用、任一test skip或required package缺`TestWorkCoordination*` pass evidence时non-zero，并输出实际执行的migration/integration test names；
- fresh single-agent/no-subagent completion reviewer审查聚合exact head，P0-P2全关；
- CI绑定同一head并终态成功；
- fork narrative registry状态仍为source-only，尚未部署前不得写live。

## Deployment gate

Apply前逐项形成exact plan并回读：

1. **Target**：唯一mini host、workspace、server、database、CLI/daemon与restart范围；不顺带更新frontend/AGS/Agent Kit。
2. **Quiescence**：`active_task_count=0`，无会被restart终止的Store/API测试任务。
3. **Database**：可恢复backup；记录secret-safe标识/恢复命令、`schema_migrations` before/after、legacy dependency各type计数。
4. **Artifacts**：旧server/CLI/image rollback artifact可用；新source/server/CLI/image exact SHA/digest可回读。
5. **Migration**：additive up先行；concurrent indexes逐个观察；失败停止，不自动down。
6. **Compatibility**：旧CLI对新server无破坏；新CLI遇旧server按legacy unsupported/HTTP 404 fallback安全失败，不误写metadata。
7. **Approval**：用户明确批准**本次**DB migration + server/CLI apply/restart；方向同意或旧批准不能复用。

任一项缺失即停止。不可逆migration、破坏性down、数据恢复继续是permanent human gates。

## Live tracer

在fresh临时workspace创建Root A、Issue B/C；不复用MINI-570或历史canary。

### Before

记录：live source/server/CLI/image exact版本、Store migration、scope不存在、A/B/C no-side-effect字段、active task count。不得记录token/credential/custom env。

### Two-client flow

- A/B是两个独立CLI process/config context；replay case使用同一actor identity，不同actor另测conflict。
- 执行aggregate flow、task-scoped正向和至少一个真实越权负向场景；不得伪造server headers。
- 保存sanitized receipts/revisions/codes/hash-equality结论，不保存bearer、request hash原bytes、环境或payload外内容。

### After

- A/B/C全部before字段exact不变；无comment/status/assignee/task/wake/Autopilot副作用；
- 保存blocker已resolved但dependency仍active的中间inspect；
- inspect在硬上限内始终完整返回active dependency/open blocker；最终两组为空，receipt refs仍按固定100条、`receipt_ordinal DESC`及ordinal-upper-bound+revision cursor分页读取；
- legacy `issue_dependency`各type计数/schema/内容不变，Store从未写入或查询；
- Store tracer rows作为evidence保留。Scope/dependency/record/typed refs仍受首期deletion guard，因此临时workspace不得被强删；receipt history/reference本身不是独立删除阻塞，retention/archive与cleanup由lifecycle ticket裁决。

## Evidence artifact

`docs/fork-features/work-coordination-store/passive-live-evidence.md`至少记录：

- accepted source/head、review/CI、server/CLI/image版本；
- migration before/after、backup/rollback artifact secret-safe引用；
- approval scope；
- sanitized双客户端命令与revision/receipt摘要；
- no-side-effect表、negative codes、deletion-guard限制；
- rollback decision、claim limit、未执行项。

它是evidence，不复制ticket合同。

## Rollback

- 首选恢复旧server/CLI/image，保留additive schema、facts和receipts；
- concurrent index失败只处理该index migration，不删除业务表/receipt；
- 仅Store从未产生有效数据且用户明确批准时考虑reverse down；
- 一旦有有效Store row，普通rollback禁止destructive down；
- 回读旧应用健康和Issue/task正常路径，记录coordination API暂不可用。

## Acceptance

V5完成要求：aggregate source flow、bounds/receipt pagination、lock-held deletion race matrix、focused/race/full/build/check、fresh review、exact-head CI、deployment gate、明确批准、live two-client/task auth/no-side-effect/deletion-guard proof、exact version readback和rollback artifact全部通过。

若只完成source证据，状态必须partial，不能关闭umbrella或解锁Agent Kit calibration。

## Non-goals

MINI-570 assisted facts bootstrap或任何authority/autonomy cutover、Agent Kit calibration、Store lifecycle cleanup、goal-control、lease/wake/Reconciler/Autopilot、program scope、UI、性能/SLO、graduation roots。

## Claim limit

最多声明：passive Work Coordination Store在记录的exact mini环境完成一次获批双客户端tracer，CAS/replay/owner-scope/independent resolve成立，未观察到Issue/comment/assignee/task/Autopilot副作用。不得声明scheduling authority、自动恢复、production scale、Reconciler、MINI-570 autonomy或毕业。
