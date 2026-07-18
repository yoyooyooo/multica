# V4 — Consistent inspect 与 passive conformance

**Blocked by:** V3 accepted head已满足[共享交付门](README.md#每片共享交付门)。
**Blocks:** [05-e2e-passive-deploy.md](05-e2e-passive-deploy.md)；仅在V4独立review、exact-head CI与主验收完成后解除。

## Objective

收口passive Store source contract：提供一致快照`inspect`，固定receipt pagination/order，完成跨scope/error/deletion-guard/no-side-effect conformance，并把CLI、built-in skill、source map和fork narrative校准到V1-V4真实实现。V4不部署、不改变Issue lifecycle，也不引入wake/control。

## Exact owning modules

- `coordination.sql`及必要sqlc generated增量；仅在inspect性能/排序确实需要时新增独立concurrent index migration。
- coordination service/types/tests的`InspectScope`与conformance增量。
- coordination handler/route/e2e tests增量。
- coordination CLI `inspect`、top-level error tests增量。
- built-in skill/source map/fork narrative完整化。
- deletion guard完整矩阵tests；不做Store cleanup。

不得扩record kind、dependency语义、Issue/task/Autopilot、Agent Kit、UI或deploy tooling。

## Inspect service contract

```text
InspectScope(ctx, actor, scopeID, receiptCursor) -> ScopeInspection
```

Handler/CLI不得直查DB。Authority沿用V1：member workspace；task actor实际root必须等于scope root。

`InspectScope`在**一个一致快照、一个HTTP响应**中返回：

- scope identity与`scope_revision`；
- owner scope的**全部**active canonical dependencies，按`(created_at ASC,id ASC)`，由V2在workspace lock内强制的每scope 1000硬上限保证有界；每条可附派生`blocks` view但不制造第二row；
- **全部**open blockers，按`(created_at DESC,id DESC)`，同样由1000硬上限保证有界；
- 最近receipt refs固定100条的page及唯一`next_receipt_cursor`。

一致快照使用单SQL statement或显式read-only repeatable-read transaction；不得用多次普通read拼出混合revision。Inspect不接受dependency/blocker cursor或limit，不能截断当前active/open事实；若DB已违反1000 invariant，返回`coordination_internal`且不返回partial graph。

### Receipt refs pagination

- receipt page size固定为100，不提供`limit`；除最后一页外必须恰100条；排序固定`receipt_ordinal DESC`，不得用`created_at`或UUID推断提交顺序；
- 首页面读取该scope已提交的`MAX(receipt_ordinal)`作为不可变`upper_ordinal`；opaque cursor绑定workspace、scope、`scope_revision`、collection kind=`receipt`、upper ordinal及last ordinal；foreign/malformed cursor返回`coordination_invalid_payload`；
- 后续页只读取`receipt_ordinal <= upper_ordinal AND receipt_ordinal < last_ordinal`。新no-op receipt即使不增加scope revision，也因ordinal更大而不能插入既有window。若scope revision变化，返回`coordination_revision_conflict`并要求从第一页重启；cursor翻页不得重漏；
- receipt ref只含`id,receipt_ordinal,operation,resource_type/resource_id,revision_before/after,created_at`及安全actor type；operation/resource必须通过V1-V3 versioned typed allowlist；不返回request hash bytes、result snapshot、payload或无界history。

Active dependency即使没有open blocker也必须显示。Resolved blocker不使active edge消失；resolved dependency不出现在active list，即使仍有open blocker evidence。Inspect不推断frontier/actionable/wake/terminal，不读metadata/comment作为current truth。

## API / CLI contract

Route：

```text
GET /api/coordination/scopes/{scopeId}/inspect?receipt_cursor=<opaque>
```

CLI：

```text
multica coordination inspect --scope <uuid> [--receipt-cursor <opaque>] [--output json|table]
```

JSON原样保留scope revision、完整active dependencies、完整open blockers、receipt page和`next_receipt_cursor`。Table只做人类显示。API/CLI均调用public service method；无raw DB shortcut或message-substring error分类。V4增量使用`coordination_internal`处理invariant破坏；全部code/HTTP/CLI exit引用[README Stable wire error SSoT](README.md#stable-wire-error-ssot)。

V4增加顶层CLI conformance test：成功JSON stdout只有一个value；失败JSON stderr只有一个error envelope且exit code匹配stable product code，无main追加prose。Legacy server/string body仍安全fallback。

## Passive conformance matrix

### Source full-flow

用真实DB/router和两个独立client/process context执行：

1. Client A ensure scope，revision `r0`；
2. A add `B blocked_by C` → `r1`；
3. A append blocker → `r2`；
4. Client B inspect读到exact `r2`、edge、open blocker、receipt refs；
5. B进程仅以**同一actor + 同一task binding**重放A的add/blocker：agent必须绑定同一`task_id`，member则两次均为`task_id=null`；得到原receipt，revision仍`r2`；
6. 不同actor复用同key必须`coordination_idempotency_conflict`；同actor若membership/task/root authority已revoke或过期，也必须在replay前被拒绝；
7. B resolve blocker → `r3`，inspect显示edge仍active；
8. A resolve dependency → `r4`，inspect显示active edge为空、open blocker为空；
9. `coordination_revision_conflict`、`coordination_cycle`、`coordination_self_dependency`、`coordination_cross_workspace`、`coordination_dependency_scope_conflict`、`coordination_forbidden`均typed失败且零部分写；
10. 并发反向edge最多一个成功。

“两client”表示独立HTTP/CLI execution context，不表示必须不同actor；replay case必须同时匹配actor identity与task binding，因为两者都属于canonical request hash。Agent换`task_id`不能replay；member只允许稳定的null task binding。另设不同actor冲突case。

### No-side-effect snapshot

Flow前后对root/B/C逐项exact比较：

- status；
- assignee type/id；
- `updated_at`；
- comment count；
- active/total task count；
- Autopilot run count（fixture适用时）；
- metadata relevant keys。

全部不变。Store rows/receipts变化单独记录，不得解释为Issue噪声。确认无event被listener解释为wake/task/status/comment动作。

### Cross-scope conformance

- scope A拥有pair后，scope B add同pair返回`coordination_dependency_scope_conflict`；
- B list/inspect不看见A edge；B不能resolve；A/B revisions不变；
- workspace-wide cycle query仍看见A edge并保护后续graph；
- 不创建association或第二edge。

### Deletion guard conformance

对scope root、`coordination_dependency` endpoints、record字段、create/resolution relation refs和workspace逐类注入单删、BatchDeleteIssues、Workspace删除。Guard拒绝发生在任何task/Autopilot DB mutation或cache/S3/metrics/reconciliation/event前，qtx verified rollback后才verified unlock；rollback/unlock/connection任一状态不明则Hijack+close/discard并禁止pool release；receipt history/reference单独存在时不阻塞删除，旧receipt replay因current authority/resource revalidation失败。并发Ensure/Add/Append与三类delete证明无新orphan。无受guard保护引用的成功删除还须验证：同qtx pre-delete task/token/Autopilot DB mutations→entity delete→commit明确成功→verified unlock/release-or-discard→post-release metrics/reconciliation/cache/S3/events调用尝试。对qtx各失败点证明整体rollback和零外部副作用；statement/commit SQLSTATE `40001`/`40P01`均整批失败且不continue/retry/finalize，commit outcome unknown不finalize并discard。Blocking/reentrant listener与S3 fake证明effects开始前session lock已terminal；当前void/no-error adapter只验证调用，不虚构deadline、effect成功、typed debt或可靠恢复。

## Built-in skill / source map 收口

Skill必须准确说明：

- 完整scope/dependency/blocker/inspect命令；
- canonical direction、owner-scope conflict、independent resolve；
- read revision→mutation→conflict后重新inspect；
- 同请求网络重试复用同key；不得改payload复用key；
- strict files、无自由文本/secret/URL；
- Store passive且不写metadata/comment第二authority；
- deletion guard限制与Store lifecycle cleanup尚未提供；
- program scope、goal contract、lease/wake/Reconciler/Autopilot未提供。

Source map引用真实migration/query/service/handler/route/CLI/tests symbols；所有路径存在，symbol可grep。Fork narrative只把V1-V4标为source capability，不提前声称live。

## Acceptance / tests

1. consistent snapshot在并发mutation下不混合revision；
2. 单响应完整返回最多1000 active dependencies与1000 open blockers且顺序固定；receipt refs固定100、ordinal upper-bound cursor无重漏/tenant escape；较早开启但较晚commit的transaction及revision不变的no-op receipt都不污染既有window，revision变化后续页返回`coordination_revision_conflict`；
3. full-flow authorized same-actor replay、different-actor conflict与revoked/expired replay denial；
4. independent blocker/dependency resolve中间态；
5. cross-scope owner conflict/no visibility/no revision change；
6. `coordination_cycle`、`coordination_self_dependency`、`coordination_revision_conflict`、`coordination_cross_workspace`与`coordination_forbidden`零部分写；
7. no-side-effect全部字段exact不变且无event；
8. active dependency/open blocker 1000 hard caps与第1001次mutation的`coordination_capacity_exceeded`零写入；
9. deletion guard覆盖scope/dependency/record/typed-ref矩阵、receipt-only不阻塞回归、Ensure/Add/Append×单删/BatchDeleteIssues/Workspace并发race、`40001`/`40P01`/commit-unknown、release-before-effects及无受guard保护引用路径回归；
10. API/CLI/top-level JSON/error/exit contract逐一覆盖五个coordination 409、legacy/status-mismatch 409、两种`--output`语法/位置及missing value/empty value/invalid value/duplicate/conflicting values；
11. skill embed/frontmatter/source-map/path/symbol/narrative contract；
12. sqlc二次生成无drift，focused/race/full/build/check通过。

Focused Go命令必须从`server` module执行：

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

`make sqlc`后的tracked diff与untracked porcelain assertions必须同时为空，任一非空立即使V4 gate失败；`git diff --check`不能替代。DB-required harness扩充V4 manifest：DB不可用、任何skip或任一required package缺`TestWorkCoordination*` pass evidence都必须non-zero。

## Non-goals

Live deploy、Store cleanup/archive、Agent Kit calibration、goal-control、wake/control/Reconciler/Autopilot、UI、performance/SLO。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留schema/facts/receipts。Evidence记录consistent snapshot、pagination、full-flow、cross-scope、no-side-effect、deletion guard、CLI/skill/source map、review/CI和fork narrative。

V4最多声明完整passive Store source contract通过source conformance；没有V5不得声明mini live可用。
