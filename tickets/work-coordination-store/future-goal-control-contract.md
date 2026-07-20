# Future — Versioned goal-control contract authority

**Status:** future capability contract；不属于V1-V5 passive Store。
**Blocked by:** [05-e2e-passive-deploy.md](05-e2e-passive-deploy.md)与[Agent Kit read-only calibration](post-deploy-agent-kit-read-only-calibration.md)完成，随后对当前Goal/Issue/handoff authority做gap audit。
**Blocks:** [future-reconciler-agent.md](future-reconciler-agent.md) write calibration与graduation；仍需reconciliation control和Store lifecycle同时live。

## Objective

为Reconciler提供一个versioned、typed、CAS-protected的目标控制合同authority，明确objective、acceptance、claim limit、authority envelope、permanent human gates、retirement conditions及evidence/handoff引用。它必须消除prompt/Issue description/metadata/handoff多处猜测，但不得把passive Work Coordination Store变成第二份目标authority。

## Authority model

- 当前Goal/Issue/handoff authority在explicit cutover前保持原路径；本文件不因存在就改变authority。
- Future capability建立独立opaque `goal_contract_id + version` authority。每次修改创建新version、执行CAS并保存server-stamped receipt。
- Coordination scope只保存**immutable binding reference**（goal contract ID/version），不复制完整objective/policy JSON；binding mutation属于future control wave，不回填V1-V5表的空字段。
- Issue/UI/Agent Kit可以投影摘要和链接，但projection不是write authority。
- Goal authority自身cutover必须记录old authority snapshot、new version、冲突裁决、effective revision和rollback ceiling，禁止长期双写；该cutover不自动授权Reconciler写入。
- Reconciler write calibration、copilot、controlled及MINI-570 assisted facts bootstrap必须等本合同、reconciliation control与Store lifecycle三项source+live proof同时成立。该bootstrap不构成MINI-570 authority/autonomy cutover；本路线不授权后者。

## Typed contract surface

实施前需拆implementation-ready tickets，至少覆盖：

1. objective与explicit non-goals；
2. machine-checkable acceptance items及claim ceiling；
3. authority envelope：允许的tactical mutations、role boundaries、resource/root scope；
4. permanent human gates与pre-authorized reversible operations；
5. budget/time/attempt ceilings；
6. retirement/supersession conditions；
7. evidence schema、canonical source refs和handoff version refs；
8. version metadata、CAS、idempotency receipt与server actor/task provenance；
9. read projection与scope binding；
10. CLI/built-in skill/Agent Kit read-write contract、source map和live canary。

不得以任意JSON/KV、自由文本memory或prompt blob替代typed versioned contract。必要的人类叙述可以作为bounded description，但不能承载机器权限或gate语义。

## Mutation / human gate

- 创建initial contract与任何objective/acceptance/claim-limit/authority-envelope变更都是显式human gate。尤其claim-limit mutation必须先取得human approval，再以CAS创建新version，并保存绑定approval及exact old/new version的server-stamped receipt；禁止Reconciler直接修改。
- Reconciler只能读取并在当前version envelope内提出战术intent；不能自行创建新有效version。
- CAS conflict、unknown schema、stale binding、superseded contract均fail closed。
- 只有同idempotency key + 同canonical hash + 同actor + 同task binding才replay原receipt（member的task binding为`null`）；同key下hash、actor或task任一不匹配都返回typed idempotency conflict。Replay前仍须重新验证current authority、contract/version及绑定resource，stale/superseded/missing时fail closed。
- Contract version一旦被task authority snapshot引用即不可原地改写；只能新建version。

## Evidence 与 handoff

- Contract保存evidence/handoff的typed references、required classes和completion state，不保存无限session transcript或私有memory。Reconciler只能提交typed evidence/handoff proposal；每个proposal必须绑定exact `goal_contract_id`/current version、workspace/root/scope/task/object identity、expected scope revision及expected previous goal-contract version，proposal本身不改变authority。
- Authority owner仅可用CAS接受proposal；server-stamped acceptance receipt必须确认与proposal完全相同的上述bindings，并记录previous/updated goal-contract version；成功时updated=previous+1。任一binding不一致、expected scope revision或previous version stale、contract/version已superseded或引用resource missing都拒绝且零写入。
- Evidence artifact仍由其source repo/runtime authority拥有；contract只记录immutable ref、digest/type和claim relation。
- Handoff必须有version、predecessor、current frontier、known blockers、claim limit和supersession；不得以一个可覆盖文件丢失历史。
- Passive Store receipt可以引用contract version，但不能反向修改目标状态。

## Acceptance before Reconciler write mode

必须证明：

- concurrent version CAS最多一个成功；stale version不能绑定scope或task；claim-limit success receipt绑定human approval、exact previous/updated versions且updated恰为previous+1；
- idempotency replay只接受同key/hash/actor/task并重新验证current authority/version/resource；任一不匹配、stale或missing均typed拒绝；
- evidence/handoff proposal仅由owner以完全相同bindings、expected scope revision和previous goal-contract version CAS接受，acceptance receipt记录previous/updated version且成功时+1；mismatch/stale/superseded零写入；
- human-gated fields不能由agent/task token自行修改；
- projection drift不会改变authority；
- old/current authority cutover和rollback演练无双写窗口；
- scope binding、task authority snapshot与Reconciler preflight读取同一exact version；
- evidence/handoff refs可验证、bounded且不泄密；
- Agent Kit/MATT contracts准确区分goal authority、Store current facts和control safety kernel。

## Non-goals

Passive dependency/blocker实现、controller lease/wake、Reconciler战术算法、任意memory、自动批准human gates、跨workspace goal、UI产品化或历史MINI-570事实批量回填。

## Claim limit

本文件只分配未来goal-control authority owner和边界。当前不声明schema/API/CLI、scope binding、task snapshot、cutover、Reconciler读取或live proof已实现。
