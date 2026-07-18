# Future — `multica-work-reconciler` Agent 与 root-scoped Autopilot

**Status:** future contract；不属于passive Store。
**Read-only prerequisite:** [post-deploy calibration](post-deploy-agent-kit-read-only-calibration.md)。
**Write-mode blocked by:** [reconciliation control](future-reconciliation-control.md)、[versioned goal-control contract](future-goal-control-contract.md)与[Store lifecycle/archive](future-store-lifecycle.md)均完成source+live proof。
**Blocks:** [graduation canaries](graduation-canaries.md)。

## Objective 与 topology

建立workspace utility Agent `multica-work-reconciler`。root-scoped Autopilot只提供event/periodic heartbeat；Reconciler读取versioned goal contract、Store facts与control preflight，在authority envelope内协调domain Agents/Squads。

```text
root-scoped Autopilot heartbeat
  → multica-work-reconciler
    → versioned goal-control authority
    → passive Store current coordination facts
    → control safety kernel
    → MATT Loop / other domain roles
```

不新增Work Supervisor Agent。Reconciler位于MATT squad外，不兼任fresh critic、durable maintainer或deployment operator，不直接持有merge/admin/deploy credential。

## Exact future owning modules

实施前按Agent Kit exact head锁定：Reconciler Agent declaration/prompt；root Autopilot enrollment/binding；goal/control/Store contracts与source maps；MATT/other squad enrollment rules；behavior/negative/canary tests。Multica server能力只能由各自source tickets拥有，不能在Agent配置中用prompt补洞。

## Tactical authority

在accepted、版本化 objective/acceptance/claim-limit/authority envelope内可创建、拆分、重排、暂停、替换child work，维护typed work graph/frontier，选择domain roles，处理常规blocker/stale work，编排review/CI/merge/backup/预授权可回滚deploy，并维护typed evidence/handoff。

改变四项合同、冲突事实主观裁决、扩大root/program边界必须`needs_human`。Issue文本/comment/metadata只能生成candidate，不能覆盖[goal-control SSoT](future-goal-control-contract.md)。

## Root/program isolation

program parent可观察排序child roots；每child保留独立facts/revision/lease/wake/snapshot/Autopilot。写child前必须取得其lease并通过preflight。shared fix选择一个canonical owner，其他root typed association/等待，禁止重复PR/merge/deploy。

## Enrollment economics

复杂、跨天、多Stage、多角色或动态blocker root默认候选；简单ticket不启用。Goal/Scope建立后paused-first。event优先、periodic fallback；`skip_no_revision_change`、active-run、grace、cooldown均不启动模型；仅`actionable`启动GPT-5.6。cadence/SLO由fresh canary校准。满足versioned retirement conditions只写`retire_requested`，无archive authority时等待coordinator/janitor。

## Gradual handover

1. human controlled：无Reconciler write；
2. shadow：只读评估对账；
3. copilot：批准的可逆typed intents；
4. controlled：唯一root lease内自主协调；
5. manual takeover：先pause、隔离lease再移交。

Agent Kit **read-only** calibration可在control前完成；任何write calibration、copilot或controlled必须同时等reconciliation control、versioned goal-control与Store lifecycle三项source+live proof。三者缺一即fail closed。

## MINI-570 boundary

MINI-570永久`assisted transition dogfood`。只有passive Store、reconciliation control、versioned goal-control与Store lifecycle均完成source+live proof，并通过fresh canary与独立批准后，才可一次性snapshot bootstrap/cutover；历史自然语言仅candidate，冲突由human裁决，不把assignment当liveness、不回放lost wake、不批量改status/assignee/dependency。它不能替代fresh graduation roots。

## Run contract

每个actionable run验证immutable authority snapshot与current revision/generation；读取goal versions、Store/Issue/task facts；区分facts/evidence/projection/candidate；通过typed intents做最小收敛动作；保留独立roles/human gates；same intent复用same key；stale/lease-lost立即停止并重新preflight；更新typed evidence/handoff与claim limit；非actionable no-op；禁止raw provider/comment prompt/metadata双写/credential直连绕过kernel。

## Permanent human gates

objective/acceptance/claim-limit/authority、credential/admin、不可逆migration、破坏性rollback、超预算、未预授权production deployment、主观产品接受。Reconciler可准备材料、等待并验证approval receipt，不能代替批准。

## Tests / acceptance

shadow对账；single wake/active suppression/grace/cooldown/second tick；envelope内graph mutation与越界needs-human；fresh critic single-agent/no-subagent、fresh durable maintainer、独立operator未被绕过；restart/stale receipt/dynamic blocker恢复；event/fallback；no revision无GPT；retire不越权；program隔离；typed evidence/handoff；manual takeover。

实现必须fresh writer、focused/full、fresh review、exact-head CI、主验收；Agent Kit/runtime apply另需明确approval与live canary。

## Non-goals

第二Supervisor/service、AGS/Forgejo work authority、Reconciler持credential或替代independent roles、Autopilot保存facts、简单ticket昂贵heartbeat、用MINI-570/历史fail声明clean。

## Rollout / rollback

paused/manual/shadow-first；每次扩大权限需receipt/批准。rollback先pause、停止dispatch、隔离lease并退回上一阶段，保留Store/control/goal ledgers；不洗白失败或恢复双authority。

## Evidence

exact Agent Kit/runtime versions、contract/topology diff、shadow/canary矩阵、role separation、goal/control/Store readbacks、fresh gates、approval/rollback、handoff与claim limit。

## Claim limit / `not_claimed`

当前不声明Reconciler、root binding、actionable routing、tactical autonomy、MINI-570 handover、retirement或weak observer已实现/live。
