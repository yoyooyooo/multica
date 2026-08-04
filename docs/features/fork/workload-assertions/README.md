# Purpose-bound Workload Assertions

## Applicability

This capability is semantically replayed from accepted `fork/v0.4.9` into the official-tag-derived `fork/v0.4.12` generation. The retained `fork/v0.4.9` is the previous-generation rollback source, and `fork/v0.4.8` remains older donor evidence; neither branch may be overwritten. Source and tests do not imply deployment, configured signing key, AGS verifier state, or runtime availability.

## Capability

A running agent task can exchange its task token for a short-lived assertion whose claims are derived from server-owned workload facts rather than caller-supplied identity. The endpoint is:

```text
POST /api/integrations/workload-assertions
```

Supported purposes are intentionally distinct:

- `external_pr_link` binds an external PR operation to the exact Multica task, Issue, provider, instance, and normalized repository target;
- `ags_session_exchange` binds a delegated AGS session exchange to the exact task, actor, AGS instance, repository, and operation set.

Purpose controls audience and claim shape. An external-PR assertion cannot be reused as AGS session proof, and the legacy external-PR link token remains a compatibility contract rather than a canonical session credential.

For `ags_session_exchange`, callers may submit either the bounded compatibility capability list or the exact operation shape:

```json
{
  "purpose": "ags_session_exchange",
  "target": {"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
  "requested_resource": {"service": "ags", "repository": "jackie/agent-kit"},
  "requested_operation": {
    "name": "pr.rebase",
    "constraints": {
      "pull_request_number": 41,
      "forgejo_pull_request_number": 52,
      "expected_head_sha": "1111111111111111111111111111111111111111",
      "expected_base_sha": "2222222222222222222222222222222222222222"
    }
  },
  "requested_capabilities": ["repo:read", "repo:write"],
  "requested_ttl": "15m"
}
```

Resource and operation must appear together. The resource must equal the normalized target, the operation name must use exact canonical casing, capabilities must exactly match that operation, and `constraints` must be a JSON object (never missing or `null`). The fixed `multica.workspace.default.v1` team-v4 ceiling follows AgentKit's production operation shapes exactly:

| operation | exact signed constraints |
|---|---|
| `repo.read` | `{}` |
| `git.read` | `{}` |
| `git.push` | `{}` |
| `pr.create` | exactly required `base_ref` + `head_ref`, both canonical branch refs |
| `pr.read` | exactly one variant: positive safe-integer `pull_request_number`, or canonical `head_ref` |
| `pr.rebase` | exactly positive safe-integer `pull_request_number` + `forgejo_pull_request_number` and lowercase 40-hex `expected_head_sha` + `expected_base_sha` |
| `review.read` | exactly positive safe-integer `pull_request_number` + `forgejo_pull_request_number` |
| `ci.read` | exactly one variant: `{}` for a repository-wide CI list; positive safe-integer `run_id`; or positive safe-integer `pull_request_number` + `forgejo_pull_request_number` with optional lowercase 40-hex `head_sha` |

AgentKit production sends short branch names such as `main` or `agent/delegated-pr`; the equivalent canonical `refs/heads/...` spelling is also accepted, while other `refs/...` namespaces, malformed Git ref syntax, and secret-shaped values fail closed. The Forgejo command fixtures pin repository-wide `runs` to `{}`, `log` to `{run_id}`, and PR-scoped runs to the exact PR projection; state-only PR lists, event-only or SHA-only CI requests, mixed CI variants, and unknown keys fail before signing. Unknown/extra keys, mixed `pr.read` variants, the old `exact_head` vocabulary, missing keys, string/boolean/null/fractional/unsafe numbers, and malformed SHA/ref values are rejected before signing. The default class still rejects `pr.merge`. An exact merge request is only normalizable input; it selects `multica.workspace.maintainer.v1` and requests `repo:read + repo:write` only when a separate active server-owned delegation matches the exact workspace, Task/Run, repository, AGS/Forgejo PR numbers, lowercase full expected head SHA, and registered merge method. The remaining deferred operations—`review.submit`, `repo.admin`, and `repo.create`—are rejected directly, including when constraints are `{}`. Legacy capability mapping remains only where a supported exact-empty operation is expressible and cannot synthesize unconstrained `pr.create`, maintainer merge, or deferred authority. This is source-only until the Multica backend, AGS maintainer binding, gateway, and CLI consumer are deployed and proven together.

Optional `requested_ttl` is accepted only for session exchange as a trimmed canonical `<positive integer><s|m|h>` string no greater than `15m`; invalid types, `null`, unknown/compound units, secret-shaped values, zero, and larger durations fail before signing. An accepted value is copied unchanged as the top-level JWT claim, while absence remains absence. These are Multica producer guarantees aligned to the inspected AgentKit production requests and AGS team-v4 registry; they do not by themselves prove an AGS verifier revision is deployed or accepts the assertion.

## Owner/operator PR merge delegation

A human workspace owner or admin creates, reads, and revokes the exact grant through supported routes:

```text
POST /api/workspaces/{workspace_id}/workload-delegations/pr-merge
GET  /api/workspaces/{workspace_id}/workload-delegations/pr-merge/{delegation_id}
POST /api/workspaces/{workspace_id}/workload-delegations/pr-merge/{delegation_id}/revoke
```

Creation accepts only `task_id`, equal `run_id`, canonical `repository`, both PR numbers, `expected_head_sha`, `merge_method`, and a positive `ttl_seconds` no greater than 900. The server records a generated authority revision, granting human, grant/expiry timestamps, and optional revocation actor/reason. A newer owner-authorized grant for the same Task/Run atomically revokes the previous revision. Task tokens and cloud credentials are rejected by the HTTP middleware and handler; workload metadata, Agent/Squad/name/role, Prompt, Skill, Context, profile, environment, and requested operation cannot create or widen a grant.

Assertion issuance locks the running Task authority and the exact active delegation in one transaction. Missing, expired, revoked, wrong-workspace, wrong-Task/Run, wrong-repository, wrong-PR, wrong-head, or wrong-method rows all return `403` before JWT signing. The assertion expiry is capped by the delegation expiry. Revocation linearizes with issuance through the delegation row lock; it prevents future assertions but does not pretend to invalidate a JWT already signed before the revocation transaction.

## Closed claim shapes by purpose

Both purposes include only the common signed claims `ver`, `iss`, `aud`, `sub`, `jti`, `iat`, `nbf`, `exp`, `purpose`, `source=task_token`, `target`, `requested_capabilities`, and the server-derived basic `workload` fields: `workspace`, `workspace_id`, `agent_id`, `agent_name`, `task_id`, optional `run_id`, and purpose-appropriate optional Issue/actor fields.

`external_pr_link` uses audience `urn:multica:external-pr-link:v1`. It requires Issue lineage, requires `requested_capabilities=[]`, and does **not** contain signed `scope`, `workload_context`, or `authority` fields.

`ags_session_exchange` uses audience `urn:ags:workload-session-exchange:v1`. In addition to the common claims, it contains optional top-level `requested_ttl`, signed `scope{schema,resource,operation,requested_capabilities,compatibility_input?}` and embeds server-derived `workload_context{schema,issuer_instance_id,subject,correlation_id,workspace_id,agent_id,squad_id?,issue_id?,issue_key?,task_id,run_id,trigger_id?,runtime_id?}` plus `authority{schema,team_identity_id,membership_epoch,policy_class}` in `workload`. `membership_epoch` is positive.

## Security boundary

- The task token authenticates the running workload; callers cannot choose workload, Issue, workspace, or actor claims. Authentication joins the token to an exact `running` task, and issuance locks/freshly rechecks both rows in one transaction. Task completion/failure/cancellation therefore linearizes against issuance: terminalization first rejects issuance; issuance first proves only that the task was running at that signing transaction, not that it remains running for the assertion's full TTL.
- Repository targets are normalized before signing so equivalent host/path spellings do not produce different authority scopes.
- Assertions are short-lived and signed with `MULTICA_WORKLOAD_ASSERTION_SECRET`; raw provider credentials are not returned.
- Signing configuration is deployment-owned. `MULTICA_WORKLOAD_ASSERTION_ISSUER` is the deployment-unique JWT `iss`. The separate required secret-free `MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID` must be a canonical safe ID, differ from `iss`, and exactly match AGS `trusted_issuers[].id`; it is the only value written to `workload_context.issuer_instance_id`. Install helpers generate each value once and preserve it, but operators must configure the generated ID explicitly in AGS rather than infer it from a target name. Source presence alone does not prove a usable trust relationship with AGS.
- The session-only signed `scope` cannot be replaced by caller metadata after issuance.
- `pr.merge` maintainer authority additionally requires a durable, revisioned, bounded, human-created server row whose exact facts are locked and checked before signing. Requested operation alone never selects privileged authority.
- Session-only `workload_context` and `authority` are Multica-owned issuer facts. AGS must still verify the accepted issuer/key/schema and re-evaluate current principal, team binding, native grant and requested operation; this source does not claim an accepted or deployed AGS runtime.

## Source anchors

- `server/internal/handler/workload_assertion.go`
- `server/internal/handler/workload_assertion_test.go`
- `server/internal/handler/workload_pr_merge_delegation.go`
- `server/internal/handler/workload_pr_merge_delegation_test.go`
- `server/pkg/db/queries/workload_pr_merge_delegation.sql`
- `server/migrations/244_workload_pr_merge_delegation.up.sql` through `246_workload_pr_merge_delegation_active_index.up.sql`
- `server/cmd/server/router.go`
- `server/cmd/server/workload_pr_merge_delegation_routes_integration_test.go`
- `.env.example`
- `docker-compose.selfhost.yml`
- [External PR Integration](../external-pr-integration/README.md)

## Retirement condition

Retire or rework this capability only when an upstream contract provides the same purpose separation, server-derived workload binding, repository normalization, and AGS session-exchange semantics with an explicit migration path.
