# V3 — Strict typed blocker records

**Blocked by:** V2 accepted head已满足[共享交付门](README.md#每片共享交付门)。
**Blocks:** [04-inspect-and-conformance.md](04-inspect-and-conformance.md)；仅在V3独立review、exact-head CI与主验收完成后解除。

## Objective

在scope/receipt/dependency vertical slices之上，交付strict typed blocker record的DB→service→API→CLI→built-in skill路径。Blocker是发现/解决/evidence authority，不是当前dependency authority；两者resolve始终独立。

## Exact owning modules

- additive migrations：`coordination_record`与soft-ref `coordination_record_issue_ref` structure、concurrent indexes、PK attach及down。
- `coordination.sql`/sqlc generated增量。
- coordination service/types/errors/tests增量。
- coordination handler/routes/tests增量。
- coordination CLI/client/error/tests增量。
- built-in skill/source map与fork narrative增量。
- deletion guard扩展到record字段和typed create/resolution relation refs；不做cleanup。

不得修改Issue/comment/task/Autopilot、dependency resolve语义、Agent Kit、UI或deploy tooling。

## Schema contract

`coordination_record`至少包含：

| Group | Contract |
| --- | --- |
| identity | `id UUID NOT NULL`、`workspace_id`、`coordination_scope_id`；无FK |
| type/version | `kind='blocker'`、`schema_version=1`、`status IN ('open','resolved')` |
| issue refs | `root_issue_id`、`downstream_issue_id`、`upstream_issue_id`、nullable `dependency_id` |
| typed codes | `reason_code='waiting_on_issue'`、nullable `resolution_code IN ('no_longer_blocking','superseded')` |
| provenance | created/resolved actor、nullable task、timestamps，成组CHECK |

Evidence refs不塞进JSONB。新增soft relation table `coordination_record_issue_ref`：

| Column | Contract |
| --- | --- |
| `id UUID NOT NULL` | opaque row identity，concurrent-index+attach PK |
| `workspace_id UUID NOT NULL` | tenant；无FK |
| `coordination_scope_id UUID NOT NULL` | soft scope ref；无FK |
| `record_id UUID NOT NULL` | soft record ref；无FK |
| `phase TEXT NOT NULL` | `create|resolution` |
| `issue_id UUID NOT NULL` | typed internal issue ref；无FK |
| `position INTEGER NOT NULL` | 0-31，保留canonical response order |
| `created_at` | server timestamp |

同record/phase/issue唯一；同record/phase/position唯一。PK/index遵循README concurrent序列，并提供scope/status分页、record refs读取和issue deletion guard index。Service在同一transaction验证所有soft refs的workspace/scope/record/issue一致性并写record+refs。

首期不使用payload/resolution JSONB，因此不存在自由文本、URL、未知KV或“通用secret扫描”承诺。未来增加record schema必须显式migration/DTO/version review，不能偷偷恢复arbitrary JSON。

## Strict DTO

Create payload：

```json
{
  "reason_code":"waiting_on_issue",
  "evidence_refs":[{"kind":"issue","id":"<uuid>"}]
}
```

Resolution：

```json
{
  "resolution_code":"no_longer_blocking",
  "evidence_refs":[{"kind":"issue","id":"<uuid>"}]
}
```

规则：

- create reason首期只允许`waiting_on_issue`；resolution只允许`no_longer_blocking|superseded`；
- evidence refs最多32项，按`(kind,id)`去重；kind仅`issue`，必须是同workspace issue；
- DTO decoder拒绝unknown fields；无description/message/note/metadata/URL/arbitrary JSON；
- canonical request hash覆盖schema/version、endpoints、optional dependency、normalized refs及server actor/task；
- API payload只是transport DTO；持久化拆为typed code columns+`coordination_record_issue_ref` rows，不保存原始JSON；
- 输入不得回显到error/log；安全性来自allowlist+bounded refs，不声称通用secret扫描。

## Service contract

新增public methods：

```text
AppendBlocker(ctx, actor, input) -> MutationResult[Blocker]
ListBlockers(ctx, actor, scopeID, status, cursor) -> BlockerPage
ResolveBlocker(ctx, actor, input) -> MutationResult[Blocker]
```

所有read经过V1 authority seam。Task actor的root必须匹配scope，且其task issue是downstream/upstream之一。

### Append

在receipt replay、authority、workspace lock、scope lock/CAS之后验证：

- root必须等于scope实际root；downstream/upstream同workspace且存在；
- optional `dependency_id`必须同workspace、同owner scope、仍active，且其downstream/upstream与record完全一致；foreign/mismatch/resolved均typed拒绝；
- evidence issue refs全部同workspace存在；record与`phase=create` refs在同一transaction提交；
- 成功append使scope revision恰增1并保存receipt。

同key同hash replay原record。新key重复相同payload不是自动no-op：除非实施前冻结明确business identity，否则它是新的evidence record；不得靠文本相等猜测去重。

### Resolve

- Record必须同workspace/scope且open；task endpoint authority重新校验；
- 只更新record status/resolution code/provenance，并原子写`phase=resolution` refs后使revision增1；不resolve/delete dependency；
- 已resolved record用新key再次resolve为no-op receipt，revision不变；
- dependency已resolved不影响既有record evidence；append时若显式绑定resolved dependency则拒绝。

`ListBlockers`稳定按`(created_at DESC,id DESC)`分页，默认/最大100，opaque cursor；status仅`open|resolved|all`。不得无界list。

## API contract

新增routes：

```text
POST /api/coordination/scopes/{scopeId}/blockers
GET  /api/coordination/scopes/{scopeId}/blockers?status=<open|resolved|all>&cursor=<opaque>&limit=<1..100>
POST /api/coordination/scopes/{scopeId}/blockers/{recordId}/resolve
```

Mutations要求`Idempotency-Key`和非负`int64 expected_revision`。Handler decode为strict typed DTO，拒绝unknown/identity fields和trailing JSON。List只调用service，返回stable order、`next_cursor`。

Stable errors复用V1/V2，并将dependency mismatch/foreign/resolved安全映射为`coordination_invalid_payload|not_found|dependency_scope_conflict`，不得新增message-substring分类或泄露foreign详情。

## CLI 与 skill增量

```text
multica coordination blocker add --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> [--dependency <uuid>] --payload-file <path> --expected-revision <int64> --idempotency-key <key>
multica coordination blocker list --scope <uuid> [--status open|resolved|all] [--cursor <opaque>] [--limit 1..100]
multica coordination blocker resolve --scope <uuid> --blocker <uuid> --resolution-file <path> --expected-revision <int64> --idempotency-key <key>
```

- payload/resolution file bounded read 4096 bytes；本地验证JSON syntax但server是业务authority；
- 不接受inline arbitrary JSON、stdin secret channel、URL fetch或YAML；error不回显完整payload；
- JSON success保留receipt/resource/revision/cursor；JSON error仍为单一stderr value；
- Skill明确dependency=current relation、blocker=evidence、resolve独立、CAS/retry、typed payload、passive/no metadata second truth。

## Deletion guard增量

Issue若出现在record root/downstream/upstream、optional dependency引用，或`coordination_record_issue_ref.issue_id`中，delete在任何既有副作用前返回`coordination_delete_blocked`。Workspace存在任何record/ref也被guard。V3不删除或改写record/ref；这是RESTRICT-style应用层guard，不是cleanup/cascade。

## Acceptance / tests

1. migration fresh/upgrade/down、两个新表PK/concurrent-index序列、record/ref constraints与legacy保持；
2. strict create/resolve DTO：unknown fields/enums、超过32 refs、duplicate refs、foreign/missing evidence；验证数据库不保存原始payload JSON；
3. optional dependency同workspace/scope/endpoints/active一致性，以及mismatch/foreign/resolved拒绝；
4. append/resolve receipt、CAS/authorized replay/different-hash/actor conflict；revoked/expired authority不能借old key replay；
5. blocker resolve后dependency仍active；dependency resolve后blocker状态保持；
6. list stable pagination：100上限、created_at+id tie、cursor无重漏；
7. member/task root+endpoint authority、伪造身份和run-only task拒绝；
8. API/CLI exact DTO/header/file/error/cursor合同；
9. deletion guard覆盖record字段与create/resolution relation refs；blocked时零task/Autopilot/event副作用；
10. Issue status/assignee/comment/metadata/task/Autopilot计数不变；
11. Skill/source map/fork narrative只声明V1-V3。

```bash
make sqlc
(cd server && go test ./internal/migrations ./pkg/db/... ./internal/service ./internal/handler ./internal/middleware ./internal/cli ./cmd/multica)
(cd server && go test -race ./internal/service ./internal/handler ./internal/cli ./cmd/multica)
make build
make test
git diff --check
```

DB tests必须明确实际执行。

## Non-goals

Aggregate inspect、compound dependency+blocker resolve、record kinds扩展、Store cleanup/archive、wake/control、Agent Kit与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧binary并保留additive schema、records和receipts。有效数据存在后不执行普通destructive down。Evidence记录migration、strict fixtures、independent resolve snapshots、pagination、guard matrix、API/CLI/skill、review/CI和fork narrative。

V3最多声明typed blocker vertical slice在source tests成立；不声明inspect aggregate、live deploy或scheduling authority。
