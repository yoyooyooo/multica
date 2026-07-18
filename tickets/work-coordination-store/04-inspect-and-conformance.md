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
InspectScope(ctx, actor, scopeID, dependencyCursor, blockerCursor, receiptCursor) -> ScopeInspectionPage
```

Handler/CLI不得直查DB。Authority沿用V1：member workspace；task actor实际root必须等于scope root。

`InspectScope`在**一个一致快照**中返回：

- scope identity与`scope_revision`；
- owner scope的active canonical dependency page，每条可附派生`blocks` view但不制造第二row；
- open blocker page；
- 最近receipt refs page；
- 三个独立`next_*_cursor`。

一致快照可使用单SQL statement或显式read-only repeatable-read transaction；不得用多次普通read拼出混合revision。

### Bounded snapshot pages

- 三类page每页固定最大100条，默认100，不允许客户端提高上限；
- dependencies按`(created_at ASC,id ASC)`；blockers/receipts按`(created_at DESC,id DESC)`；
- 每个opaque cursor绑定workspace、scope、**scope revision**、collection kind和最后排序键；foreign/invalid cursor返回`coordination_invalid_payload`；
- 下一页读取前若scope revision变化，返回`coordination_revision_conflict`，客户端必须从第一页重启，禁止把不同revision页面拼成一个snapshot；
- cursor翻页不得重漏；三个collection可独立分页；
- receipt ref只含`id,operation,resource_type/resource_id,revision_before/after,created_at`及安全actor type；不返回request hash bytes、result snapshot、payload或无界history。

Active dependency即使没有open blocker也必须显示。Resolved blocker不使active edge消失；resolved dependency不出现在active list，即使仍有open blocker evidence。Inspect不推断frontier/actionable/wake/terminal，不读metadata/comment作为current truth。

## API / CLI contract

Route：

```text
GET /api/coordination/scopes/{scopeId}/inspect?dependency_cursor=<opaque>&blocker_cursor=<opaque>&receipt_cursor=<opaque>
```

CLI：

```text
multica coordination inspect --scope <uuid> [--dependency-cursor <opaque>] [--blocker-cursor <opaque>] [--receipt-cursor <opaque>] [--output json|table]
```

JSON原样保留scope revision、三个pages和三个next cursors。Table只做人类显示。API/CLI均调用public service method；无raw DB shortcut或message-substring error分类。

V4增加顶层CLI conformance test：成功JSON stdout只有一个value；失败JSON stderr只有一个error envelope且exit code匹配stable product code，无main追加prose。Legacy server/string body仍安全fallback。

## Passive conformance matrix

### Source full-flow

用真实DB/router和两个独立client/process context执行：

1. Client A ensure scope，revision `r0`；
2. A add `B blocked_by C` → `r1`；
3. A append blocker → `r2`；
4. Client B inspect读到exact `r2`、edge、open blocker、receipt refs；
5. **同一actor identity**的B进程用原key重放A的add/blocker，得到原receipt，revision仍`r2`；
6. 不同actor复用同key必须`idempotency_conflict`；同actor若membership/task/root authority已revoke或过期，也必须在replay前被拒绝；
7. B resolve blocker → `r3`，inspect显示edge仍active；
8. A resolve dependency → `r4`，inspect显示active edge为空、open blocker为空；
9. stale revision、cycle、self、cross-workspace、other-scope pair、task越权均typed失败且零部分写；
10. 并发反向edge最多一个成功。

“两client”表示独立HTTP/CLI execution context，不表示必须不同actor；replay case必须同actor，因为actor/task属于canonical request hash。另设不同actor冲突case。

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

- scope A拥有pair后，scope B add同pair返回`dependency_scope_conflict`；
- B list/inspect不看见A edge；B不能resolve；A/B revisions不变；
- workspace-wide cycle query仍看见A edge并保护后续graph；
- 不创建association或第二edge。

### Deletion guard conformance

对scope root、dependency endpoints、record字段、create/resolution relation refs、receipt引用和workspace逐类注入delete请求；均在任何task cancellation、Autopilot变化或event前返回`coordination_delete_blocked`。无Store引用的普通delete路径保持既有行为，V4不得顺带重构。

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
2. 三类page固定100、stable order、revision-bound cursor无重漏/tenant escape；revision变化后续页稳定conflict；
3. full-flow authorized same-actor replay、different-actor conflict与revoked/expired replay denial；
4. independent blocker/dependency resolve中间态；
5. cross-scope owner conflict/no visibility/no revision change；
6. cycle/self/stale/cross-workspace/task越权零部分写；
7. no-side-effect全部字段exact不变且无event；
8. deletion guard全引用矩阵与无引用路径回归；
9. API/CLI/top-level JSON/error/exit contract；
10. skill embed/frontmatter/source-map/path/symbol/narrative contract；
11. sqlc二次生成无drift，focused/race/full/build/check通过。

```bash
make sqlc
(cd server && go test ./internal/migrations ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica)
(cd server && go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica)
make build
make test
make check
git diff --check
```

DB-backed tests必须证明实际执行。

## Non-goals

Live deploy、Store cleanup/archive、Agent Kit calibration、goal-control、wake/control/Reconciler/Autopilot、UI、performance/SLO。

## Rollback / evidence / claim limit

应用rollback恢复旧server/CLI并保留schema/facts/receipts。Evidence记录consistent snapshot、pagination、full-flow、cross-scope、no-side-effect、deletion guard、CLI/skill/source map、review/CI和fork narrative。

V4最多声明完整passive Store source contract通过source conformance；没有V5不得声明mini live可用。
