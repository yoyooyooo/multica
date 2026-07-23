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
| Worktree `wt_*-postgres-1` and `wt_*_pgdata` | `multica.owner=worktree`, canonical physical worktree path, exact `wt_*` project |

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

Every guarded Compose mutation writes owner PID, process-start evidence,
project, and canonical worktree path beneath:

```text
${TMPDIR:-/tmp}/multica-compose-locks/multica-compose-lock-<project>
```

A competing operation waits only for the configured bounded interval. If the
owner process is gone, its start evidence no longer matches, or evidence never
finishes initialization, the helper atomically renames the directory to a
`.stale.*` quarantine before continuing. A normal release is likewise renamed
to a `.released.*` record only after the releasing process matches the recorded
PID, process-start, project, owner, and canonical worktree path; a mismatch or
rename failure remains fail-closed.

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
