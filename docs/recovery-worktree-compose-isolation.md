# Recovery and Rollback: Worktree Compose Isolation

This document describes how to detect and recover from a Compose project
ownership split (a worktree container replacing or relabeling live production
resources), and how to roll back the isolation change.

## Detecting split ownership

### Quick check

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Labels "multica.owner"}}'
```

Expected output for correct state:

| Container | multica.owner |
|-----------|---------------|
| `multica-postgres-1` | `deployment` (or unlabeled) |
| `multica-backend-1`  | `deployment` (or unlabeled) |
| `multica-frontend-1` | `deployment` (or unlabeled) |
| `wt_*_*-postgres-1`  | `worktree` |

Worktree containers start with `wt_` prefix. If any `multica-*` container
shows `multica.owner=worktree`, ownership is split.

### Detailed inspection

```bash
# List all running containers with ownership labels
docker ps --format 'table {{.Names}}\t{{.ID}}\t{{.RunningFor}}\t{{.Labels "multica.owner"}}\t{{.Labels "multica.worktree.path"}}\t{{.Labels "com.docker.compose.project"}}'

# List all stopped containers for a project
docker ps -a --filter "label=com.docker.compose.project=multica" --format 'table {{.Names}}\t{{.Status}}\t{{.Labels "multica.owner"}}'

# Check production volumes
docker volume ls --filter "label=com.docker.compose.project=multica" --format 'table {{.Name}}\t{{.Labels "multica.owner"}}\t{{.Labels "multica.worktree.path"}}'

# Verify production PostgreSQL credentials work
docker compose exec -T postgres psql -U multica -d multica -c "SELECT 1"
```

## Stopping isolated resources only

To stop worktree containers without touching production:

```bash
# Stop a specific worktree project
docker compose --project-name wt_<slug>_<offset> down

# Or by container name
docker rm -f wt_<slug>_<offset>-postgres-1

# Verify only worktree containers stopped
docker ps --format '{{.Names}}' | grep -E '^multica-'
# Only multica-* containers should still be running
```

## Preserving production containers and volumes

If the ownership guard is absent or bypassed, production resources may have
been recreated or relabeled. Recovery steps:

1. **Stop any worktree project that may have recreated production containers:**

   ```bash
   docker compose --project-name multica down
   ```

   ⚠️ This stops the `multica-*` project. If the production backend is
   still the original deployment, this will cause an outage. Only do this
   if you are certain the current `multica-*` containers are worktree-owned.

2. **Verify production volume integrity:**

   ```bash
   docker volume inspect multica_pgdata --format '{{.Driver}}'
   # Should be "local"
   docker volume ls | grep multica
   ```

3. **Restart production from the original deployment stack:**

   ```bash
   # From the original deployment directory
   docker compose -f docker-compose.selfhost.yml up -d postgres backend frontend
   ```

4. **Verify production functionality:**

   ```bash
   curl -sf http://localhost:8080/health
   docker compose exec -T postgres psql -U multica -d multica -c "SELECT 1"
   ```

## Reverting the source change

To revert the isolation source change (e.g. to recover from an incorrect
deployment or to stop using the Compose isolation pattern):

```bash
# From the multica repo
git diff origin/main -- docker-compose.yml scripts/init-worktree-env.sh \
  scripts/ensure-postgres.sh scripts/compose-ownership-guard.sh \
  scripts/compose-guard-mustpass.sh Makefile

# Revert all changes
git checkout origin/main -- docker-compose.yml scripts/init-worktree-env.sh \
  scripts/ensure-postgres.sh scripts/compose-ownership-guard.sh \
  scripts/compose-guard-mustpass.sh Makefile
```

Then run `make setup` with `.env` (using the previous shared Compose project).
Re-run the isolation tests if needed to verify the revert.

## Rollback vs. new fix

If the isolation change itself is incorrect or insufficient, the preferred
path is:

1. Revert the change locally.
2. Create a new bounded branch from the current `fork/v0.4.8`.
3. Apply a corrected fix on the new branch.
4. Push and request a fresh review.

Do not force-push or rebase the existing PR branch after review has started,
unless explicitly requested.
