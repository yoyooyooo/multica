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

`coordination_record`首期持久列合同如下；除表中nullable项和通用server timestamps外，不增加payload/metadata占位列：

| Columns | Exact contract |
| --- | --- |
| `id` | `UUID NOT NULL`；opaque identity，无FK |
| `workspace_id` | `UUID NOT NULL`；tenant key，无FK |
| `coordination_scope_id` | `UUID NOT NULL`；soft scope ref，无FK |
| type/version/state | `kind TEXT NOT NULL CHECK (kind = 'blocker')`；`schema_version INTEGER NOT NULL CHECK (schema_version = 1)`；`status TEXT NOT NULL CHECK (status IN ('open','resolved'))` |
| `root_issue_id` | `UUID NOT NULL`；无FK |
| `downstream_issue_id` | `UUID NOT NULL`；无FK |
| `upstream_issue_id` | `UUID NOT NULL`；无FK |
| `dependency_id` | `UUID NULL`；该组identity/issue/dependency UUID中唯一nullable列，无FK |
| typed codes | `reason_code TEXT NOT NULL CHECK (reason_code = 'waiting_on_issue')`；`resolution_code TEXT NULL CHECK (resolution_code IN ('no_longer_blocking','superseded'))` |
| create provenance | `created_by_type TEXT NOT NULL CHECK (created_by_type IN ('member','agent'))`、`created_by_id UUID NOT NULL`、`created_task_id UUID NULL`、`created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()`；task nullability使用receipt同构CHECK |
| resolution provenance | `resolved_by_type TEXT NULL`、`resolved_by_id UUID NULL`、`resolved_task_id UUID NULL`、`resolved_at TIMESTAMPTZ NULL`；下述table CHECK绑定status/code/type/id/task/time |

Evidence refs不塞进JSONB。新增soft relation table `coordination_record_issue_ref`：

| Column | Contract |
| --- | --- |
| `id UUID NOT NULL` | opaque row identity，concurrent-index+attach PK |
| `workspace_id UUID NOT NULL` | tenant；无FK |
| `coordination_scope_id UUID NOT NULL` | soft scope ref；无FK |
| `record_id UUID NOT NULL` | soft record ref；无FK |
| `phase TEXT NOT NULL` | `CHECK (phase IN ('create','resolution'))` |
| `issue_id UUID NOT NULL` | typed internal issue ref；无FK |
| `position INTEGER NOT NULL` | `CHECK (position BETWEEN 0 AND 31)`；保留canonical response order |
| `created_at TIMESTAMPTZ NOT NULL` | `DEFAULT clock_timestamp()`；客户端不可提供 |

Record state table CHECK精确为两支：`status='open'`时`resolution_code/resolved_by_type/resolved_by_id/resolved_task_id/resolved_at`全NULL；`status='resolved'`时code/type/id/time均NOT NULL、`resolved_by_type IN ('member','agent')`，且member→task NULL、agent→task NOT NULL。Create provenance使用同样的actor/task互斥CHECK。

同record/phase/issue唯一；同record/phase/position唯一。PK/index遵循README concurrent序列，并提供scope/status分页、record refs读取和issue deletion guard index。Service在同一transaction验证所有soft refs的workspace/scope/record/issue一致性并写record+refs。

首期不使用payload/resolution JSONB，因此不存在自由文本、URL、未知KV或“通用secret扫描”承诺。未来增加record schema必须显式migration/DTO/version review，不能偷偷恢复arbitrary JSON。

## Typed payload rules

- create reason首期只允许`waiting_on_issue`；resolution只允许`no_longer_blocking|superseded`；
- evidence refs必须是0..32项的array，kind仅`issue`，duplicate `(kind,id)`拒绝，且每个ID必须是同workspace issue；
- DTO无description/message/note/metadata/URL/arbitrary JSON；输入不得回显到error/log；
- API payload只是transport DTO；持久化拆为typed code columns和`coordination_record_issue_ref` rows，不保存原始JSON；
- 安全性来自allowlist、unknown-field rejection和bounds，不声称通用secret扫描。

## Wire DTO、nullable边界与CLI file mapping

Append HTTP body精确为：

```json
{
  "expected_revision": 2,
  "downstream_issue_id": "<uuid>",
  "upstream_issue_id": "<uuid>",
  "dependency_id": null,
  "schema_version": 1,
  "payload": {
    "reason_code": "waiting_on_issue",
    "evidence_refs": [{"kind":"issue","id":"<uuid>"}]
  }
}
```

`dependency_id`是唯一nullable字段：允许省略或JSON `null`，两者canonical normalize为`null`；非null必须是UUID。其他字段均required且不得为null。`schema_version`只接受JSON integer `1`。`payload`必须是object；`evidence_refs`必须是非null array，允许0项、最多32项。Root/scope来自path/loaded scope，不接受body自报。

Resolve HTTP body精确为：

```json
{
  "expected_revision": 3,
  "schema_version": 1,
  "resolution": {
    "resolution_code": "no_longer_blocking",
    "evidence_refs": [{"kind":"issue","id":"<uuid>"}]
  }
}
```

Resolve的scope/record/endpoints/dependency均从path和已加载record取得，body不得重复；`resolution`及其字段required/non-null，refs同样允许0..32。Append、resolve及所有nested objects均`DisallowUnknownFields`，拒绝duplicate JSON object keys、trailing第二个value、错误number/string类型；unknown schema version或显式null required field返回`coordination_invalid_payload`。

CLI `--payload-file`文件内容**恰好**映射为wire body的`payload` object；endpoints/dependency/expected revision来自flags，CLI固定插入`schema_version:1`。`--resolution-file`恰好映射`resolution` object；record/expected revision来自flags，CLI同样插入version。文件不得包含outer wrapper、endpoints、identity或schema_version，unknown字段本地拒绝且不发HTTP。文件读取上限4096 bytes，随后仍由server执行完整校验。

## Receipt allowlist与canonical normalization

V3 typed allowlist精确新增：

| Operation | Resource type |
| --- | --- |
| `append_blocker` | `blocker` |
| `resolve_blocker` | `blocker` |

Append canonical `request`的RFC 8785输出精确为：

```json
{"dependency_id":null,"downstream_issue_id":"<lowercase UUID>","expected_revision":"<decimal int64>","payload":{"evidence_refs":[{"id":"<lowercase UUID>","kind":"issue"}],"reason_code":"waiting_on_issue"},"schema_version":1,"scope_id":"<lowercase UUID>","upstream_issue_id":"<lowercase UUID>"}
```

Resolve canonical `request`精确为：

```json
{"expected_revision":"<decimal int64>","record_id":"<lowercase UUID>","resolution":{"evidence_refs":[{"id":"<lowercase UUID>","kind":"issue"}],"resolution_code":"no_longer_blocking"},"schema_version":1,"scope_id":"<lowercase UUID>"}
```

`dependency_id`缺失/显式null都normalize为JSON null。Evidence refs先校验duplicate `(kind,id)`为错误，再按`kind ASC,id ASC`排序，UUID lowercase；客户端数组顺序不影响hash，0项时输出`[]`。Reason/resolution code保持exact allowlisted string。完整document按[canonical receipt hash SSoT](README.md#canonical-receipt-hash-ssot)处理。

Tests冻结append/resolve完整canonical JSON与SHA-256 golden digest，覆盖omitted/null dependency等价、ref顺序等价，以及endpoint/dependency/schema/payload/resolution/actor/task任一变化导致digest变化。DB不增加operation/resource enum CHECK；service读写都拒绝V3 allowlist外值。

## Service contract

新增public methods：

```text
AppendBlocker(ctx, actor, input) -> MutationResult[Blocker]
ListBlockers(ctx, actor, scopeID, status, cursor) -> BlockerPage
ResolveBlocker(ctx, actor, input) -> MutationResult[Blocker]
```

所有read经过V1 authority seam。Task actor的root必须匹配scope，且其task issue是downstream/upstream之一。Receipt allowlist与hash只以上节为准。

### Append

Append/Resolve沿用统一顺序：strict parse后开transaction并先取得README workspace xact lock；持锁重新加载current membership/task authority、Workspace、scope、endpoints及请求record/dependency，才处理receipt。Exact replay还要确认saved blocker/resource仍存在、同workspace/scope且当前actor可读；非replay继续lock scope/CAS并验证。任何replay/conflict都不得绕过lock与二次revalidation：

- root必须等于scope实际root；downstream/upstream同workspace且存在；
- optional `dependency_id`必须引用`coordination_dependency`中同workspace、同owner scope、仍active的row，且其downstream/upstream与record完全一致；foreign/mismatch/resolved均typed拒绝；legacy `issue_dependency`不是合法dependency source；
- evidence issue refs全部同workspace存在；record与`phase=create` refs在同一transaction提交；
- 同lock内count open blockers；已有1000条时返回`coordination_capacity_exceeded`且record/ref/revision/receipt均不变；否则成功append使scope revision恰增1，分配下一个`receipt_ordinal`并保存receipt。

同key同hash replay原record。新key重复相同payload不是自动no-op：除非实施前冻结明确business identity，否则它是新的evidence record；不得靠文本相等猜测去重。

### Resolve

- Record必须同workspace/scope且open；task endpoint authority重新校验；
- 只更新record status/resolution code/provenance，并原子写`phase=resolution` refs后使revision增1、open count减1；分配下一个`receipt_ordinal`，不resolve/delete dependency；
- 已resolved record用新key再次resolve为no-op receipt，revision不变但仍分配新ordinal；exact replay不分配；
- dependency已resolved不影响既有record evidence；append时若显式绑定resolved dependency则拒绝。

`ListBlockers`稳定按`(created_at DESC,id DESC)`分页，默认/最大100；opaque cursor绑定workspace/scope、status filter、读取时`scope_revision`和最后排序键，后续页revision变化返回`coordination_revision_conflict`。Page返回该revision；status仅`open|resolved|all`。每scope open blocker硬上限1000；resolved history仍必须分页，不得无界list。

## API contract

新增routes：

```text
POST /api/coordination/scopes/{scopeId}/blockers
GET  /api/coordination/scopes/{scopeId}/blockers?status=<open|resolved|all>&cursor=<opaque>&limit=<1..100>
POST /api/coordination/scopes/{scopeId}/blockers/{recordId}/resolve
```

Mutation使用上节唯一append/resolve wire body与nullable规则，不在API层定义第二种shape。List只调用service并返回`scope_revision`、stable order和`next_cursor`；foreign/malformed cursor→`coordination_invalid_payload`，revision变化→`coordination_revision_conflict`。

V3增量使用`coordination_capacity_exceeded`；dependency mismatch/resolved→`coordination_invalid_payload`，foreign/missing→不泄露详情的`coordination_not_found`或`coordination_cross_workspace`，owner mismatch→`coordination_dependency_scope_conflict`。HTTP/CLI exit只引用[README Stable wire error SSoT](README.md#stable-wire-error-ssot)，不得使用裸后缀、message substring或泄露foreign详情。

### Response wire SSoT

Append/resolve成功统一返回：

```json
{
  "receipt": {
    "id": "<uuid>",
    "receipt_ordinal": 7,
    "operation": "append_blocker",
    "resource_type": "blocker",
    "resource_id": "<uuid>",
    "revision_before": 2,
    "revision_after": 3,
    "created_at": "2026-07-15T12:34:56.123456Z"
  },
  "resource": {
    "id": "<uuid>",
    "workspace_id": "<uuid>",
    "scope_id": "<uuid>",
    "kind": "blocker",
    "schema_version": 1,
    "status": "open",
    "root_issue_id": "<uuid>",
    "downstream_issue_id": "<uuid>",
    "upstream_issue_id": "<uuid>",
    "dependency_id": null,
    "reason_code": "waiting_on_issue",
    "resolution_code": null,
    "create_evidence_refs": [{"kind":"issue","id":"<uuid>"}],
    "resolution_evidence_refs": [],
    "created_by": {"type":"agent","id":"<uuid>","task_id":"<uuid>"},
    "resolved_by": null,
    "created_at": "2026-07-15T12:34:56.123456Z",
    "resolved_at": null
  },
  "scope_revision": 3,
  "changed": true,
  "replayed": false
}
```

精确规则：

- first append且`changed=true`返回HTTP 201；append no-op/replay与全部resolve success返回200；resolve receipt的`operation=resolve_blocker`；
- `dependency_id`、`resolution_code`、`resolved_by`、`resolved_at`及actor `task_id`是唯一nullable响应字段；member actor的`task_id=null`，agent非null；
- evidence arrays始终存在且按canonical `(kind,id)`排序，空值为`[]`，不返回raw payload/JSONB；
- UUID均lowercase hyphenated；timestamps均UTC RFC3339Nano；int64字段为JSON integer；
- resolve响应把`status=resolved`、resolution fields/refs/provenance/time填满；其余字段保持create事实；
- first mutation/new-key no-op均`replayed=false`，`changed`反映是否改变fact；exact replay复用同receipt/resource/saved `scope_revision`/`changed`，只把`replayed=true`，不伪装成current revision；调用方后续mutation仍须inspect/CAS；
- `result_snapshot`保存`resource + scope_revision + changed`的bounded server shape，不嵌套自身receipt，不保存`replayed`。

List响应精确为：

```json
{
  "scope_id": "<uuid>",
  "scope_revision": 3,
  "status_filter": "open",
  "items": [],
  "next_cursor": null
}
```

`items`使用上述`resource` shape；最多100条。`status_filter`是normalized `open|resolved|all`；`next_cursor`仅为opaque string或null，不得空字符串。

## CLI 与 skill增量

```text
multica coordination blocker add --scope <uuid> --downstream <issue-ref> --upstream <issue-ref> [--dependency <uuid>] --payload-file <path> --expected-revision <int64> --idempotency-key <key>
multica coordination blocker list --scope <uuid> [--status open|resolved|all] [--cursor <opaque>] [--limit 1..100]
multica coordination blocker resolve --scope <uuid> --blocker <uuid> --resolution-file <path> --expected-revision <int64> --idempotency-key <key>
```

- payload/resolution file按上节映射并bounded read 4096 bytes；本地严格验证shape/unknown/trailing但server仍是业务authority；
- 不接受inline arbitrary JSON、stdin secret channel、URL fetch或YAML；error不回显完整payload；
- JSON success逐字段保留上述mutation/list response shape与nullability，不使用`map[string]any`吞掉合同；JSON error仍为单一stderr value；
- Skill明确dependency=current relation、blocker=evidence、resolve独立、CAS/retry、typed payload、passive/no metadata second truth。

## Deletion guard增量

Issue若出现在record root/downstream/upstream或`coordination_record_issue_ref.issue_id`中，session-lock-held guard返回`coordination_delete_blocked`；optional `dependency_id`只允许指向独立`coordination_dependency`，V3不得产生悬空relation。Workspace存在任何record/ref也被guard。Append/resolve使用统一xact lock；单删、BatchDeleteIssues、Workspace删除使用冲突session lock并持有到实际entity DB delete完成/失败。V3不删除或改写record/ref，不实现cleanup，也不以瞬时check替代持锁guard。

## Acceptance / tests

1. migration fresh/upgrade/down、两个新表PK/concurrent-index序列、record/ref constraints；legacy `issue_dependency` schema/rows不变且从不查询；
2. strict append/resolve wire DTO：outer/nested unknown、duplicate keys、nullability、wrong types/version、超过32/duplicate refs、foreign/missing evidence；验证数据库不保存原始payload JSON；
3. optional dependency只解析`coordination_dependency`并满足同workspace/scope/endpoints/active一致性；mismatch/foreign/resolved/legacy ID拒绝；
4. `append_blocker|resolve_blocker`/`blocker` allowlist、canonical JSON/digest golden tests及CLI file-to-wire exact mapping；
5. append/resolve receipt、CAS/authorized replay/different-hash/actor conflict；所有receipt返回在workspace lock后二次authority/resource validation；revoked/expired authority或并发entity delete后不能借old key replay；ordinal对mutation/new-key no-op递增、exact replay不增且rollback不推进；
6. blocker resolve后dependency仍active；dependency resolve后blocker状态保持；
7. list stable pagination：100上限、created_at+id tie、revision/status-bound cursor无重漏；翻页间mutation稳定`coordination_revision_conflict`；open第1000条可写，第1001条返回`coordination_capacity_exceeded`且零写入；
8. member/task root+endpoint authority、伪造身份和run-only task拒绝；
9. API/CLI exact request与mutation/list response golden fixtures：open/resolved/nullability、member/agent provenance、UTC timestamp、empty refs/cursor、changed/no-op/replay及saved-vs-current revision；
10. Append分别与单删、BatchDeleteIssues、Workspace删真实并发race，无新orphan；guard覆盖record字段与create/resolution refs，拒绝时cache/task/Autopilot/event零变化；
11. Issue status/assignee/comment/metadata/task/Autopilot计数不变；
12. Skill/source map/fork narrative只声明V1-V3。

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
git diff --check
```

`make sqlc`后的tracked diff与untracked porcelain assertions必须同时为空，任一非空立即使V3 gate失败；`git diff --check`不能替代。DB-required harness扩充V3 manifest：DB不可用、任何skip或任一required package缺`TestWorkCoordination*` pass evidence都必须non-zero。

## Non-goals

Aggregate inspect、compound dependency+blocker resolve、record kinds扩展、Store cleanup/archive、wake/control、Agent Kit与deployment。

## Rollback / evidence / claim limit

应用rollback恢复旧binary并保留additive schema、records和receipts。有效数据存在后不执行普通destructive down。Evidence记录migration、strict fixtures、independent resolve snapshots、pagination、guard matrix、API/CLI/skill、review/CI和fork narrative。

V3最多声明typed blocker vertical slice在source tests成立；不声明inspect aggregate、live deploy或scheduling authority。
