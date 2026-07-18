# V2 — Canonical dependencies 与原子 workspace DAG

**Blocked by:** V1 accepted head已满足[共享交付门](README.md#每片共享交付门)。
**Blocks:** [03-typed-blocker-records.md](03-typed-blocker-records.md)；仅在V2独立review、exact-head CI与主验收完成后解除。

## Objective

在V1 scope/receipt vertical slice之上，交付canonical `downstream blocked_by upstream` dependency的DB→service→API→CLI→built-in skill完整路径。Cycle check、edge commit、scope CAS与receipt必须同事务；同一workspace active pair只能由一个scope拥有。

## Exact owning modules

- additive migrations：新建soft-ref `coordination_dependency`表、concurrent indexes、constraint attach及down；不修改、不写入legacy `issue_dependency`或`001_init`。
- `coordination.sql`及sqlc generated增量。
- coordination service/types/errors/tests增量。
- coordination handler/routes/tests增量。
- `cmd_coordination.go`、CLI client/error/tests增量。
- `multica-work-coordination` skill/source map与fork narrative增量。
- V1 deletion guard query/service测试扩展到dependency endpoints；不做cleanup。

不得修改Issue lifecycle语义、blocker record、inspect、Autopilot/Stage、Agent Kit或UI。

## Schema contract

新建独立`coordination_dependency` soft-ref表；不得复用带既有`REFERENCES issue ... ON DELETE CASCADE`的legacy `issue_dependency`：

| Column group | Contract |
| --- | --- |
| identity | `id UUID NOT NULL`、`workspace_id UUID NOT NULL`、`coordination_scope_id UUID NOT NULL`；均无FK |
| canonical endpoints | `downstream_issue_id UUID NOT NULL`、`upstream_issue_id UUID NOT NULL`；无FK，self-edge CHECK拒绝 |
| direction | 不存自由`type`；该表唯一语义就是`downstream blocked_by upstream` |
| create provenance | `created_by_type TEXT NOT NULL`、`created_by_id UUID NOT NULL`、`created_task_id UUID NULL`、`created_at TIMESTAMPTZ NOT NULL`；member/agent task规则同receipt |
| resolution provenance | `resolved_by_type TEXT NULL`、`resolved_by_id UUID NULL`、`resolved_task_id UUID NULL`、`resolved_at TIMESTAMPTZ NULL`；active时全NULL，resolved时type/id/time必填且task按actor type成组CHECK |

Rules/indexes：

- partial unique index固定为`(workspace_id,downstream_issue_id,upstream_issue_id) WHERE resolved_at IS NULL`，不把scope放进pair unique key；该partial index不attach为UNIQUE constraint；
- 每条edge由一个`coordination_scope_id`拥有；
- owner-scope list/resolve、workspace reachability和deletion guard indexes；
- PK/index/constraint attach遵循README concurrent序列；禁止FK/REFERENCES/cascade。

Legacy `issue_dependency`保持原schema/rows/既有authority；Store migrations不加列、不迁移、不回填、不写入，Store query/cycle/list完全忽略它。Upgrade tests逐类证明legacy `blocks/blocked_by/related`未变化。

## sqlc contract

新增typed operations：

- 复用[workspace coordination advisory-lock SSoT](README.md#workspace-coordination-advisory-lock-ssot)的transaction-level lock query；
- workspace-scoped endpoint loads；
- `coordination_dependency` get active edge by workspace+pair（必须返回owner scope）；
- create edge；get by workspace+ID；list active by owner scope；resolve with provenance；
- workspace Store graph successors/reachability query；
- deletion guard：Issue是任何`coordination_dependency` endpoint、Workspace仍有`coordination_dependency` row。

所有resource lookup显式带`workspace_id`。

## Service contract

新增public methods：

```text
AddDependency(ctx, actor, input) -> MutationResult[Dependency]
ListDependencies(ctx, actor, scopeID, cursor, limit) -> DependencyPage
ResolveDependency(ctx, actor, input) -> MutationResult[Dependency]
```

`ListDependencies`必须经过V1 authority seam并只返回owner scope的active edge；按`(created_at ASC,id ASC)`稳定分页，默认/最大100。Opaque cursor绑定workspace/scope、读取时`scope_revision`和最后排序键；后续页revision变化返回`coordination_revision_conflict`并要求重启。Page返回该revision；handler不得直查DB。每scope active dependency硬上限为`1000`，不是运行时配置。

### Receipt allowlist与canonical hash

V2 typed allowlist精确新增：

| Operation | Resource type |
| --- | --- |
| `add_dependency` | `dependency` |
| `resolve_dependency` | `dependency` |

V1 receipt表的DB CHECK只限制非空/长度，因此V2不做constraint widening。按[canonical receipt hash SSoT](README.md#canonical-receipt-hash-ssot)，canonical document的`request`精确为：

```json
{"downstream_issue_id":"<lowercase UUID>","expected_revision":"<decimal int64>","scope_id":"<lowercase UUID>","upstream_issue_id":"<lowercase UUID>"}
```

`resolve_dependency`的`request`精确为：

```json
{"dependency_id":"<lowercase UUID>","expected_revision":"<decimal int64>","scope_id":"<lowercase UUID>"}
```

Service/API/CLI tests冻结exact operation/resource strings、完整canonical JSON bytes及SHA-256 golden digest；字段、actor/task或operation变化必须改变digest。

### Mutation顺序

Add/resolve必须：

1. strict parse typed input并构造上述canonical hash；
2. 开transaction并取得统一workspace coordination advisory xact lock；
3. **持锁**重新加载current actor/task authority、Workspace、scope及请求endpoint/dependency resource；revoke/expiry/entity delete先拒绝；
4. 处理receipt：different hash/actor/task→typed conflict；exact match还须确认saved resource仍存在、同workspace/owner scope且当前actor可读，才返回saved result；
5. 非replay再lock owner scope并校验`expected_revision`；
6. 持锁复核endpoint/dependency current state；执行mutation与cycle check；新pair add还在同一lock内count active rows，已达1000则返回`coordination_capacity_exceeded`且零写入；
7. 真实变化使scope revision恰增1；same-scope no-op不增；
8. 持scope lock分配`receipt_ordinal`，保存bounded result+receipt并commit；任一步失败整体rollback。

### Add semantics

- 输入仅`scope_id,expected_revision,downstream_issue_id,upstream_issue_id`；不接受type/direction/actor。
- Task actor的root必须等于scope root，且其task issue必须是downstream或upstream之一。
- self、missing、cross-workspace拒绝。
- Workspace DAG不含legacy rows。若从upstream可达downstream，返回`coordination_cycle`。
- 若active pair不存在且owner scope active dependency少于1000，创建owner scope edge；达到1000稳定返回`coordination_capacity_exceeded`。
- 若active pair已由**同scope**拥有，新key得到no-op receipt并分配新`receipt_ordinal`，revision不变；原key走首次receipt replay且不分配ordinal。
- 若active pair由**其他scope**拥有，返回`coordination_dependency_scope_conflict`；不创建association、不改变任何scope revision、不泄露其他scope详情。
- 并发`A blocked_by B`与`B blocked_by A`在workspace lock内串行，最多一方成功。

### Resolve semantics

- Resource必须由请求scope拥有；其他scope尝试resolve返回`coordination_dependency_scope_conflict`。
- 只写resolved provenance/time并使owner scope active count减1；不处理blocker（V3）。
- 已resolved edge以新key再次resolve为no-op receipt并分配新ordinal，revision不变；exact replay不分配。
- `blocks`只在read DTO中派生，永不写DB。

## API contract

新增workspace-scoped routes：

```text
POST /api/coordination/scopes/{scopeId}/dependencies
GET  /api/coordination/scopes/{scopeId}/dependencies?cursor=<opaque>&limit=<1..100>
POST /api/coordination/scopes/{scopeId}/dependencies/{dependencyId}/resolve
```

Mutation要求`Idempotency-Key`。Add body精确为`{"expected_revision":0,"downstream_issue_id":"<uuid>","upstream_issue_id":"<uuid>"}`；resolve body精确为`{"expected_revision":1}`。`scope_id/dependency_id`由path进入canonical request，不能出现在body。Decoder拒绝unknown/identity字段、duplicate object keys与trailing JSON。List只调用service，返回`scope_revision`、最多100条及`next_cursor`；foreign/malformed cursor返回`coordination_invalid_payload`，合法cursor但current revision变化返回`coordination_revision_conflict`。

Mutation success中的receipt必须包含`receipt_ordinal`；new-key no-op返回新ordinal，exact replay返回原ordinal。V2增量使用`coordination_revision_conflict`、`coordination_dependency_scope_conflict`、`coordination_self_dependency`、`coordination_cycle`、`coordination_capacity_exceeded`；HTTP/CLI exit只引用[README Stable wire error SSoT](README.md#stable-wire-error-ssot)。不得建立第二张表、使用裸后缀或泄露SQL/constraint。

## CLI 与 skill增量

```text
multica coordination dependency add --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> --expected-revision <int64> --idempotency-key <key>
multica coordination dependency list --scope <uuid> [--cursor <opaque>] [--limit 1..100]
multica coordination dependency resolve --scope <uuid> --dependency <uuid> --expected-revision <int64> --idempotency-key <key>
```

- expected revision必须显式提供、非负且不超过`MaxInt64`；零值不能冒充“已设置”。
- `coordination_dependency_scope_conflict`、`coordination_revision_conflict`、`coordination_idempotency_conflict`与`coordination_capacity_exceeded`均原样保留；JSON stderr仍是单一value。
- issue refs先由现有resolver转UUID；CLI不发送身份字段。

Skill新增canonical方向、same-scope no-op、cross-scope conflict、workspace-wide cycle、CAS/retry、`blocks`只读派生和passive边界。Source map更新为真实symbols。

## Deletion guard增量

Issue是任何`coordination_dependency` downstream/upstream、或Workspace仍有该表row时，session-lock-held guard返回`coordination_delete_blocked`。所有V2 add/resolve使用README统一xact lock；单删、BatchDeleteIssues、Workspace删除继续用冲突session lock并持有到实际entity DB delete完成/失败。V2不删除edge、不实现lifecycle cleanup，也不以瞬时check替代持锁guard。

## Acceptance / tests

必须证明：

1. 新`coordination_dependency` migration fresh/down/up、PK/concurrent-index序列；legacy三类rows/schema在upgrade后保持，Store queries忽略且从不写入；
2. soft-ref constraints、workspace active pair unique与owner-scope indexes；
3. add/list/resolve public service与API/CLI均走owner scope；
4. same-scope duplicate no-op；other-scope active pair stable conflict且两个revision均不变；
5. `coordination_self_dependency`、`coordination_not_found`、`coordination_cross_workspace`、`coordination_cycle`；
6. 两个真实并发transaction写反向edge，恰一方成功；
7. stale revision、authorized same-key replay、different-hash/actor conflict零部分写；所有receipt返回在workspace lock后二次authority/resource validation；revoke/expired task或并发entity delete后不能用old key读取旧receipt；
8. member/task endpoint authority与伪造headers/body拒绝；
9. list稳定分页：100上限、created_at+id tie、revision-bound cursor无重漏/tenant escape；翻页间mutation稳定`coordination_revision_conflict`；active第1000条可写，第1001条返回`coordination_capacity_exceeded`且revision/receipt/facts不变；
10. `add_dependency|resolve_dependency`/`dependency` allowlist、wire bodies、canonical JSON与digest golden tests；receipt ordinal对mutation/new-key no-op递增、exact replay不增且rollback不留推进；
11. resolve不隐式改任何blocker/Issue/comment/task/Autopilot；
12. Add分别与单删、BatchDeleteIssues、Workspace删的真实并发race，无新orphan；guard拒绝时cache/task/Autopilot/event零变化；
13. CLI exact request、pagination、int64边界、stable code/exit/JSON；
14. skill/source map/fork narrative只声明V1+V2。

Focused Go命令必须从`server` module执行：

```bash
set -euo pipefail
make sqlc
git diff --exit-code -- server/pkg/db/generated
test -z "$(git status --porcelain --untracked-files=all -- server/pkg/db/generated)"
(
  cd server
  export WORK_COORDINATION_DB_REQUIRED=1
  go test -count=1 -v ./internal/migrations ./cmd/migrate ./internal/service ./internal/handler -run 'WorkCoordination'
  go test ./internal/migrations ./cmd/migrate ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica
  go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica
)
make build
make test
git diff --check
```

`make sqlc`后的tracked diff与untracked porcelain assertions必须同时为空，任一非空立即使V2 gate失败；`git diff --check`不能替代。第一条verbose DB command必须在DB不可用时non-zero fail并输出实际执行的coordination migration/integration test names；任何skip都使gate失败。

## Non-goals

Blocker records、inspect aggregate、cross-scope association、多scope revision、Store lifecycle cleanup、wake/control、Agent Kit与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧binary并保留schema/edges/receipts。有效Store数据存在后不做普通destructive down。Evidence记录migration、legacy fixture、reverse-edge并发、owner-scope matrix、API/CLI fixtures、review/CI和fork narrative。

V2最多声明canonical dependency vertical slice在source tests成立；不声明blocker、inspect、live deploy或scheduling authority。
