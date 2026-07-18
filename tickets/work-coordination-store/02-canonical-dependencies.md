# V2 — Canonical dependencies 与原子 workspace DAG

**Blocked by:** V1 accepted head已满足[共享交付门](README.md#每片共享交付门)。
**Blocks:** [03-typed-blocker-records.md](03-typed-blocker-records.md)；仅在V2独立review、exact-head CI与主验收完成后解除。

## Objective

在V1 scope/receipt vertical slice之上，交付canonical `downstream blocked_by upstream` dependency的DB→service→API→CLI→built-in skill完整路径。Cycle check、edge commit、scope CAS与receipt必须同事务；同一workspace active pair只能由一个scope拥有。

## Exact owning modules

- additive migrations：扩展legacy `issue_dependency`的nullable Store列、Store-only CHECK、concurrent indexes与必要constraint attach；不修改`001_init`。
- `coordination.sql`及sqlc generated增量。
- coordination service/types/errors/tests增量。
- coordination handler/routes/tests增量。
- `cmd_coordination.go`、CLI client/error/tests增量。
- `multica-work-coordination` skill/source map与fork narrative增量。
- V1 deletion guard query/service测试扩展到dependency endpoints；不做cleanup。

不得修改Issue lifecycle语义、blocker record、inspect、Autopilot/Stage、Agent Kit或UI。

## Schema contract

对现有`issue_dependency(id,issue_id,depends_on_issue_id,type)`做additive nullable扩展：

- `workspace_id UUID NULL`
- `coordination_scope_id UUID NULL`
- created actor/task/timestamp provenance
- resolved actor/task/timestamp provenance

Store row定义为`coordination_scope_id IS NOT NULL`。Store-only规则：

- `workspace_id`和created provenance完整；
- `type='blocked_by'`；`issue_id=downstream`，`depends_on_issue_id=upstream`；
- self-edge拒绝；active=`resolved_at IS NULL`；resolved provenance成组；
- workspace-global active endpoint pair唯一，不把scope放进pair unique key；
- 每条active pair由row中的一个`coordination_scope_id`拥有；
- legacy unscoped `blocks/blocked_by/related` rows保持原值，不进入Store query/cycle/list。

不新增FK/REFERENCES/cascade。Structure/index/constraint attach遵循README concurrent-index序列。

## sqlc contract

新增typed operations：

- workspace advisory transaction lock，key使用独立`work-coordination` namespace + workspace UUID；
- workspace-scoped endpoint loads；
- get active edge by workspace+pair（必须返回owner scope）；
- create edge；get by workspace+ID；list active by owner scope；resolve with provenance；
- workspace Store graph successors/reachability query；
- deletion guard：Issue是任何Store dependency endpoint、Workspace仍有Store dependency。

所有resource lookup显式带`workspace_id`。

## Service contract

新增public methods：

```text
AddDependency(ctx, actor, input) -> MutationResult[Dependency]
ListDependencies(ctx, actor, scopeID, cursor, limit) -> DependencyPage
ResolveDependency(ctx, actor, input) -> MutationResult[Dependency]
```

`ListDependencies`必须经过V1 authority seam并只返回owner scope的active edge；按`(created_at ASC,id ASC)`稳定分页，默认/最大100，opaque cursor绑定workspace/scope与排序键；handler不得直查DB。

### Mutation顺序

除V1 receipt replay规则外，add/resolve必须：

1. strict typed input + canonical hash；
2. transaction内重做当前actor/task authority；revoke/expiry/authority loss先于receipt replay返回；
3. 再处理receipt replay/conflict；
4. 取得workspace coordination advisory xact lock；
5. lock owner scope并校验`expected_revision`；
6. 校验endpoint workspace/existence/current state；
7. mutation与cycle check；
8. 真实变化使scope revision恰增1；same-scope no-op不增；
9. 保存bounded result+receipt并commit。

### Add semantics

- 输入仅`scope_id,expected_revision,downstream_issue_id,upstream_issue_id`；不接受type/direction/actor。
- Task actor的root必须等于scope root，且其task issue必须是downstream或upstream之一。
- self、missing、cross-workspace拒绝。
- Workspace DAG不含legacy rows。若从upstream可达downstream，返回`coordination_cycle`。
- 若active pair不存在，创建owner scope edge。
- 若active pair已由**同scope**拥有，新key得到no-op receipt，revision不变；原key走首次receipt replay。
- 若active pair由**其他scope**拥有，返回`coordination_dependency_scope_conflict`；不创建association、不改变任何scope revision、不泄露其他scope详情。
- 并发`A blocked_by B`与`B blocked_by A`在workspace lock内串行，最多一方成功。

### Resolve semantics

- Resource必须由请求scope拥有；其他scope尝试resolve返回`dependency_scope_conflict`。
- 只写resolved provenance/time；不处理blocker（V3）。
- 已resolved edge以新key再次resolve为no-op receipt，revision不变。
- `blocks`只在read DTO中派生，永不写DB。

## API contract

新增workspace-scoped routes：

```text
POST /api/coordination/scopes/{scopeId}/dependencies
GET  /api/coordination/scopes/{scopeId}/dependencies?cursor=<opaque>&limit=<1..100>
POST /api/coordination/scopes/{scopeId}/dependencies/{dependencyId}/resolve
```

Mutation要求`Idempotency-Key`和body中的非负`int64 expected_revision`；add还含两个UUID endpoint。Decoder拒绝unknown/identity字段与trailing JSON。List只调用service，返回最多100条及`next_cursor`；foreign/invalid cursor返回`coordination_invalid_payload`。

新增error mapping：

| Code | HTTP |
| --- | --- |
| `coordination_revision_conflict` | 409 |
| `coordination_dependency_scope_conflict` | 409 |
| `coordination_self_dependency` | 422 |
| `coordination_cycle` | 422 |

其他V1 codes保持。Raw SQL/constraint不得外泄。

## CLI 与 skill增量

```text
multica coordination dependency add --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> --expected-revision <int64> --idempotency-key <key>
multica coordination dependency list --scope <uuid> [--cursor <opaque>] [--limit 1..100]
multica coordination dependency resolve --scope <uuid> --dependency <uuid> --expected-revision <int64> --idempotency-key <key>
```

- expected revision必须显式提供、非负且不超过`MaxInt64`；零值不能冒充“已设置”。
- `dependency_scope_conflict`与revision/idempotency conflict均保留stable product code；JSON stderr仍是单一value。
- issue refs先由现有resolver转UUID；CLI不发送身份字段。

Skill新增canonical方向、same-scope no-op、cross-scope conflict、workspace-wide cycle、CAS/retry、`blocks`只读派生和passive边界。Source map更新为真实symbols。

## Deletion guard增量

Issue是任何Store dependency downstream/upstream、或Workspace仍有Store dependency时，existing delete入口在既有副作用前返回`coordination_delete_blocked`。V2不删除/resolve edge，不实现lifecycle cleanup。

## Acceptance / tests

必须证明：

1. legacy三类rows upgrade后字节/语义保持，Store queries忽略它们；
2. Store-only constraints、workspace active pair unique与concurrent-index序列；
3. add/list/resolve public service与API/CLI均走owner scope；
4. same-scope duplicate no-op；other-scope active pair stable conflict且两个revision均不变；
5. self/missing/cross-workspace/cycle；
6. 两个真实并发transaction写反向edge，恰一方成功；
7. stale revision、authorized same-key replay、different-hash/actor conflict零部分写；revoke/expired task不能用old key读取旧receipt；
8. member/task endpoint authority与伪造headers/body拒绝；
9. list稳定分页：100上限、created_at+id tie、cursor无重漏/tenant escape；
10. resolve不隐式改任何blocker/Issue/comment/task/Autopilot；
11. deletion guard在endpoint引用存在时零现有副作用；
12. CLI exact request、pagination、int64边界、stable code/exit/JSON；
13. skill/source map/fork narrative只声明V1+V2。

```bash
make sqlc
(cd server && go test ./internal/migrations ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica)
(cd server && go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica)
make build
make test
git diff --check
```

DB concurrency tests必须证明实际执行。

## Non-goals

Blocker records、inspect aggregate、cross-scope association、多scope revision、Store lifecycle cleanup、wake/control、Agent Kit与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧binary并保留schema/edges/receipts。有效Store数据存在后不做普通destructive down。Evidence记录migration、legacy fixture、reverse-edge并发、owner-scope matrix、API/CLI fixtures、review/CI和fork narrative。

V2最多声明canonical dependency vertical slice在source tests成立；不声明blocker、inspect、live deploy或scheduling authority。
