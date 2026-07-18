# Future — Reconciliation control 与 server/CLI safety kernel

**Status:** future contract；不属于01-05 passive Store。
**Blocked by:** [V5 live tracer](05-e2e-passive-deploy.md)与[Agent Kit read-only calibration](post-deploy-agent-kit-read-only-calibration.md)，随后进行live gap audit。
**Blocks:** [future-reconciler-agent.md](future-reconciler-agent.md) write calibration/copilot/controlled；还必须同时有[goal-control contract](future-goal-control-contract.md)与[Store lifecycle](future-store-lifecycle.md) source+live proof。

## Objective

交付确定性control primitives，使Native Stage、Reconciler与manual takeover共享root-scoped并发/权限内核。该层只裁决typed intent能否安全执行，不拥有objective/acceptance/work graph，也不替Agent做战术判断。

## Future owning capabilities

实施前拆成可独立验证vertical slices并列exact modules：program/root topology；controller lease/fencing；wake claim/trigger-bearing dispatch；actionable preflight与active-run/grace/cooldown suppression；immutable task authority snapshot；pause/takeover/retire observability；API/CLI/built-in skill/source-map；migration/source/live tests。

不得把空lease/program/preflight/goal字段预建到passive tables，不复用`sys_cron_executions`、comment或metadata充当control ledger。

## Program/root isolation

program parent只组织、观察和排序child roots。每个child保留独立scope revision、facts/receipts、controller lease/generation、wake ledger与Autopilot。program观察权不能直接写child；shared fix用canonical owner与typed association，不复制dependency/work。每root任一时刻一个有效controller lease。

## Lease、fencing 与 wake claim

- acquire/renew/release/takeover使用DB time；lease绑定scope/controller/generation/expiry。
- stale fencing token对control mutation匹配零row并稳定`lease_lost`；takeover递增generation。
- manual takeover先pause Autopilot、隔离在途lease，再取得同一root authority。
- pre-dispatch expected inputs固定为workspace/root/scope identity、expected scope revision、trigger kind/ref、target intent、controller generation、policy version、exact `goal_contract_id`/`goal_contract_version`及actor/task binding。Wake key绑定这些expected authority inputs本身或其唯一deterministic canonical hash；initial dispatch不得要求尚未创建的task authority snapshot ID/version。
- claim、active-run检查、goal binding复核、immutable task snapshot创建与dispatch receipt在同一transaction；task和receipt原子绑定同一exact `goal_contract_id`/`goal_contract_version`及新建snapshot ID/version。只有同idempotency key + 同canonical hash + 同actor + 同task binding才replay原receipt；同key下hash、actor或task任一不匹配都返回typed conflict。
- initial dispatch以pre-dispatch expected inputs完成CAS并原子创建task、immutable snapshot与dispatch receipt；只有dispatch后的renew/terminal/intent等后续操作才必须提交expected snapshot ID/version，并与receipt及current task binding精确匹配。
- active run/native grace/cooldown返回deterministic no-action；second tick无重复task/comment/status副作用。

## Actionable preflight

server-side deterministic read输入scope/revision、controller generation、trigger、policy version及exact `goal_contract_id`/`goal_contract_version`；在preflight与dispatch时都必须将该pair和scope current binding、effective goal version精确比较。任一stale/mismatch立即fail closed，不返回`actionable`，不得claim、建task或写dispatch receipt。其余输出限定为`skip_no_revision_change|skip_active_run|skip_native_grace|skip_cooldown|needs_human|actionable`。仅`actionable`可创建一次GPT-5.6 task；stale revision/generation不得把旧intent套到新snapshot。

## Task authority snapshot

root Autopilot dispatch时由server保存immutable workspace/root/scope/revision/generation、exact `goal_contract_id`/`goal_contract_version`、trigger与policy version；同一transaction把exact pair写入task authority snapshot和dispatch receipt，二者不得部分提交。task token只解析server snapshot，不接受prompt/body/header自声明。passive issue-bound task policy继续存在；不通过nullable placeholder假装已支持root authority。

## Intent safety与human gates

API只接受typed claim/release、policy内graph mutation、wake/dispatch、pause/resume/retire request、needs-human intents；全部要求适用的expected revision、fencing generation、exact `goal_contract_id`/`goal_contract_version`与idempotency hash。Initial dispatch要求上述pre-dispatch expected authority inputs而不要求snapshot；dispatch后的操作必须另带expected snapshot ID/version。目标/验收/claim-limit/authority、credential/admin、不可逆migration、破坏性rollback、超预算、未预授权production deploy、主观接受永久human gate。

## Tests / acceptance

并发controller只有一个；steal后旧token不能heartbeat/dispatch/terminal；Stage/Reconciler/manual并发wake一个claim；各suppression reason与no-revision skip不启动GPT；actionable仅一次；preflight/dispatch的stale或mismatched `goal_contract_id`/`goal_contract_version`均拒绝且零claim/task/receipt写入；initial dispatch不依赖预存snapshot，并原子创建task snapshot与receipt；后续操作缺少或错配expected snapshot ID/version均拒绝；成功dispatch的task snapshot与receipt原子绑定同一exact pair；replay仅接受同key/hash/actor/task，actor/task mismatch conflict；restart恢复无duplicate；snapshot不可伪造且stale fail closed；program不能混写child；retire不越权archive；stable API/CLI/skill/source-map与exact live evidence齐全。

每个implementation slice须fresh writer、focused/full、fresh independent review、exact-head CI与主验收；source接受后另有deployment plan、当次approval与live canary。Control单独完成不得解锁Reconciler write calibration/copilot/controlled或MINI-570 assisted facts bootstrap；还需goal与lifecycle live。MINI-570 authority/autonomy cutover不在本路线授权面。

## Non-goals

objective/acceptance/claim-limit/work graph/handoff/evidence authority（由[goal-control contract](future-goal-control-contract.md)拥有）、目标理解、战术拆票、第二Supervisor Agent、直接merge/admin/deploy credential、把Autopilot当facts store。

## Rollout / rollback

独立source/apply wave，paused/shadow-first。rollback先pause、停止claim、隔离lease，恢复旧application并保留lease/wake/receipt ledger；不清幂等历史、不恢复Store/metadata双authority。破坏性schema rollback仍是human gate。

## Evidence

exact owning modules、migration/DTO/API/CLI/skill、concurrency/fencing/suppression/restart tests、fresh quality gates、deployment approval、live canary、rollback readback与claim limit。

## Claim limit / `not_claimed`

当前不声明program scope、lease/fencing、wake claim、preflight、task snapshot、dispatch、retirement或write mode已实现/live。read-only calibration不授权任何write。
