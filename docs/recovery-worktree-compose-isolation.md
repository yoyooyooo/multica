# Recovery and Rollback: Worktree Compose Isolation

This runbook covers a worktree Compose ownership collision without changing
production credentials, volumes, or unrelated containers. It is a recovery
reference, not permission to operate the live deployment.

## Detect split ownership

List running and stopped resources with their ownership evidence:

```bash
docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Label "multica.owner"}}\t{{.Label "multica.worktree.path"}}\t{{.Label "multica.worktree.project"}}\t{{.Label "com.docker.compose.project"}}'
docker volume ls --format 'table {{.Name}}\t{{.Label "multica.owner"}}\t{{.Label "multica.worktree.path"}}\t{{.Label "multica.worktree.project"}}\t{{.Label "com.docker.compose.project"}}'
```

Expected ownership:

| Resource | Required identity |
| --- | --- |
| Production `multica-*` resources | `multica.owner=deployment`; project `multica` |
| Worktree `wt_*-postgres-1` and `wt_*_pgdata` | `multica.owner=worktree`, canonical physical worktree path, exact `wt_<basename>_<80-bit-path-digest>` project |

A missing, malformed, path-aliased, or foreign custom label is a collision.
Do not relabel it in place. Preserve it as evidence and stop the attempted
worktree operation.

## Safe worktree stop

Use the worktree env file and the canonical lock boundary. This stops only the
already verified worktree project and retains its data volume for inspection:

```bash
test -f .env.worktree || { echo "Missing .env.worktree" >&2; exit 1; }
make db-down ENV_FILE=.env.worktree
```

Never substitute `multica` for a `wt_*` project. Never use a broad wildcard,
force deletion, or a production Compose command to clean up a worktree.

## Lock recovery

Every guarded Compose mutation fully writes one immutable regular-file owner
record (PID, process-start evidence, project, canonical worktree path,
PostgreSQL host port, lock key, and start time) before atomically publishing
that record beneath one fixed host-local namespace:

```text
/tmp/multica-compose-locks/multica-compose-lock-port-<port>
```

The physical path for that root is canonicalized. `MULTICA_COMPOSE_LOCK_ROOT`,
`TMPDIR`, env-file values, current-directory aliases, and Compose variables do
not select another lock namespace. Compose project and database names use an
80-bit SHA-256 prefix of the canonical physical worktree path (with a readable,
length-bounded basename prefix); they do not reuse the bounded port slot. The
PostgreSQL port remains a 1000-slot allocation, and the lock key is
`postgres-port-<port>`, so distinct projects that resolve to one host port
serialize the whole preflight-and-mutation boundary. After the first operation
creates the binding, a different project fails its ownership/port preflight
instead of reaching a second mutation.

A competing operation reads one complete immutable record and waits only for
the configured bounded interval. There is no caller-controlled initialization
grace and no visible partial-owner state. A malformed record remains
fail-closed; a complete record is atomically renamed to a `.stale.*` quarantine
only after its owner is gone or its PID has been reused with different start
evidence. A normal release is likewise renamed to a `.released.*` record only
after the releasing process matches the recorded PID, process-start, project,
owner, canonical worktree path, port, and key; a mismatch or rename failure
remains fail-closed.

Do not manually delete lock or test directories. Keep quarantined evidence in
place; if host administration later requires disposal, use the platform's
verified recycle/trash mechanism on one reviewed absolute path at a time.

## Production preservation

If a production resource appears worktree-owned, stop and escalate to the
production owner. Before any production recovery action, capture the exact
container IDs, start times, labels, and production volume identity. A worktree
repair must not rotate PostgreSQL roles, mutate credentials, recreate
production containers, or change the production volume.

The production owner may use the original deployment stack only after verifying
that its target is the intended deployment directory and that no worktree
resource is selected. Validate application and database readiness separately:

```bash
curl -sf http://localhost:8080/health
docker compose -f docker-compose.selfhost.yml exec -T postgres \
  psql -U multica -d multica -c 'SELECT 1'
```

## Source rollback

Do not rewrite the active PR branch. If this isolation implementation needs to
be backed out, create a bounded corrective change from the current
`fork/v0.4.8` generation and use a reviewed `git revert <accepted-commit>` or
a replacement commit. Keep the original commit, PR, lock quarantine, and
resource readback as incident evidence.

Before requesting review, run the deterministic fake-Docker regression suite:

```bash
bash scripts/test-compose-isolation.sh
```

The suite proves guard-before-mutation behavior, stopped/foreign resource
refusal, strict volume labels, canonical paths, bounded stale-lock quarantine,
real two-contender serialization, configured first initialization, authenticated
readiness, and production-identity preservation without contacting live Docker.
