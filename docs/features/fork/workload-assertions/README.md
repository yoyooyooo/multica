# Purpose-bound Workload Assertions

## Applicability

This capability is owned by the `fork/v0.4.8` generation. Source and tests are present after generation acceptance; no deployment, configured signing key, AGS verifier state, or runtime availability is implied.

## Capability

A running agent task can exchange its task token for a short-lived assertion whose claims are derived from server-owned workload facts rather than caller-supplied identity. The endpoint is:

```text
POST /api/integrations/workload-assertions
```

Supported purposes are intentionally distinct:

- `external_pr_link` binds an external PR operation to the exact Multica task, Issue, provider, instance, and normalized repository target;
- `ags_session_exchange` binds a delegated AGS session exchange to the exact task, actor, AGS instance, repository, and operation set.

Purpose controls audience and claim shape. An external-PR assertion cannot be reused as AGS session proof, and the legacy external-PR link token remains a compatibility contract rather than a canonical session credential.

## Security boundary

- The task token authenticates the running workload; callers cannot choose workload, Issue, workspace, or actor claims.
- Repository targets are normalized before signing so equivalent host/path spellings do not produce different authority scopes.
- Assertions are short-lived and signed with `MULTICA_WORKLOAD_ASSERTION_SECRET`; raw provider credentials are not returned.
- Signing configuration is deployment-owned. Source presence alone does not prove a usable trust relationship with AGS.

## Source anchors

- `server/internal/handler/workload_assertion.go`
- `server/internal/handler/workload_assertion_test.go`
- `server/cmd/server/router.go`
- `.env.example`
- `docker-compose.selfhost.yml`
- [External PR Integration](../external-pr-integration/README.md)

## Retirement condition

Retire or rework this capability only when an upstream contract provides the same purpose separation, server-derived workload binding, repository normalization, and AGS session-exchange semantics with an explicit migration path.
