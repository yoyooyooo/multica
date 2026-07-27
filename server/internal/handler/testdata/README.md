# Workload assertion producer bridge fixtures

`TestExportCanonicalSessionExchangeProducerFixture` invokes the production
`CreateWorkloadAssertion` handler against a migrated PostgreSQL database and
writes the handler's exact response bytes for downstream AGS verification.

The AgentKit cross-repository harness provides this exact contract:

- `DATABASE_URL`: a migrated, dedicated PostgreSQL database;
- `PRINCIPAL_SESSION_BRIDGE_SECRET`: the HS256 test key shared with the AGS
  verifier step;
- `PRINCIPAL_SESSION_BRIDGE_MULTICA_OUTPUT`: the Multica signed-response path;
- `PRINCIPAL_SESSION_BRIDGE_AGS_OUTPUT`: the distinct downstream AGS Session
  response path.

The explicit selector fails when any bridge variable is absent or whitespace;
it never skips or silently writes to a temporary fallback. Example invocation:

```bash
DATABASE_URL="$DEDICATED_DATABASE_URL" \
PRINCIPAL_SESSION_BRIDGE_SECRET="$TEST_SIGNING_KEY" \
PRINCIPAL_SESSION_BRIDGE_MULTICA_OUTPUT="$TMPDIR/multica-signed-response.json" \
PRINCIPAL_SESSION_BRIDGE_AGS_OUTPUT="$TMPDIR/ags-session.json" \
go test ./internal/handler \
  -run '^TestExportCanonicalSessionExchangeProducerFixture$' -count=1 -v
```

The Multica output is the real HTTP response, not reconstructed JSON. Its top
level is the production `workloadAssertionResponse` shape: `assertion`,
`assertion_type`, `purpose`, `expires_at`, and `workload`. The three golden
files byte-check the complete deterministic response and canonical workload
mapping for full and omitted optional provenance. Exported assertions use the
production clock so AGS receives a live token.

A normal full-package test run, where no explicit `-run` selector names the
bridge test, still exercises the handler with isolated temporary paths and a
fixed non-production key. This preserves ordinary package validation without
weakening the explicit cross-repository gate.
