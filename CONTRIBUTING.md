# Contributing Guide

This guide documents the local development workflow for contributors working on the Multica codebase.

It covers:

- first-time setup
- day-to-day development in the main checkout
- isolated worktree development
- the isolated PostgreSQL Compose model
- testing and verification
- full-stack isolated testing (backend + frontend + daemon from source)
- troubleshooting and production-preserving database reset

## Development Model

The main checkout and every Git worktree use distinct PostgreSQL Compose identities.

- the main checkout usually uses `.env`, Compose project `multica`, and `POSTGRES_DB=multica`
- each Git worktree uses a generated `.env.worktree`
- a worktree gets a deterministic `wt_*` Compose project, PostgreSQL container, host port, volume, and database from its canonical physical path
- a worktree must not use the deployment `multica` Compose project or its PostgreSQL port/volume
- PostgreSQL host ports use a 1,000-slot modulo allocation, so distinct worktrees can collide
- backend and frontend ports are also derived from the worktree path hash

Compose project, database, container, and volume identities are isolated. A same-port PostgreSQL contender is serialized by the fixed `/tmp/multica-compose-locks/multica-compose-lock-port-<port>` authority; after the first identity is observed, a different project sharing that port is refused before mutation rather than treated as independently safe.

## Prerequisites

- Node.js `v20+`
- `pnpm` `v10.28+`
- Go `v1.26+`
- Docker

## Important Rules

- The main checkout should use `.env`.
- A worktree should use `.env.worktree`.
- Do not copy `.env` into a worktree directory.

Why:

- the current command flow prefers `.env` over `.env.worktree`
- if a worktree contains `.env`, it can accidentally point back to the main database

## Environment Files

### Main Checkout

Create `.env` once:

```bash
cp .env.example .env
```

By default, `.env` points to:

```bash
COMPOSE_PROJECT_NAME=multica
MULTICA_OWNER=deployment
POSTGRES_DB=multica
POSTGRES_PORT=5432
DATABASE_URL=postgres://multica:multica@localhost:5432/multica?sslmode=disable
PORT=8080
FRONTEND_PORT=3000
```

### Worktree

Generate `.env.worktree` from inside the worktree:

```bash
make worktree-env
```

That generates deterministic values like:

```bash
COMPOSE_PROJECT_NAME=wt_multica_feature_702
MULTICA_OWNER=worktree
WORKTREE_PATH=/physical/path/to/multica-feature
POSTGRES_DB=wt_multica_feature_702
POSTGRES_PORT=16134
PORT=18782
FRONTEND_PORT=13702
DATABASE_URL=postgres://multica:multica@localhost:16134/wt_multica_feature_702?sslmode=disable
```

Notes:

- Compose project, PostgreSQL container, volume, and database are derived from the canonical physical worktree path; a symlink resolves to the same identity
- PostgreSQL host ports use the path hash modulo 1,000 and can collide across distinct worktrees
- guarded Compose commands use `/tmp/multica-compose-locks/multica-compose-lock-port-<port>` to serialize same-port attempts, then reject a foreign project before mutation
- backend and frontend ports are also derived from the worktree path hash
- `make worktree-env` refuses to overwrite an existing `.env.worktree`

To regenerate a worktree env file:

```bash
FORCE=1 make worktree-env
```

## First-Time Setup

### Quick Start (recommended)

From any checkout (main or worktree):

```bash
make dev
```

This single command:

- auto-detects whether you're in a main checkout or a worktree
- creates the appropriate env file (`.env` or `.env.worktree`) if it doesn't exist
- checks that prerequisites (Node.js, pnpm, Go, Docker) are installed
- installs JavaScript dependencies
- ensures the current checkout's isolated PostgreSQL Compose project is running
- creates the current checkout's application database if it does not exist
- runs all migrations
- starts both backend and frontend

### Explicit Setup (advanced)

If you prefer separate control over setup and startup:

#### Main Checkout

```bash
cp .env.example .env
make setup-main
make start-main
```

Stop:

```bash
make stop-main
```

#### Worktree

```bash
make worktree-env
make setup-worktree
make start-worktree
```

Stop:

```bash
make stop-worktree
```

## Recommended Daily Workflow

### Main Checkout

Use the main checkout when you want a stable local environment for `main`.

```bash
make start-main
make stop-main
make check-main
```

### Feature Worktree

Use a worktree when you want isolated Compose resources, data, and app ports.

```bash
git worktree add ../multica-feature -b feat/my-change main
cd ../multica-feature
make dev
```

After that, day-to-day commands are:

```bash
make dev              # start (re-runs setup if needed, idempotent)
make stop-worktree    # stop
make check-worktree   # verify
```

## Running Main and Worktree at the Same Time

This is a first-class workflow.

Example:

- main checkout
  - Compose project/container/volume: `multica` / `multica-postgres-1` / `multica_pgdata`
  - database and PostgreSQL host port: `multica` / `5432`
  - backend/frontend: `8080` / `3000`
- worktree checkout
  - Compose project/container/volume: generated `wt_*` identity
  - database and PostgreSQL host port: generated `wt_*` database and a modulo-allocated port such as `16134`
  - backend/frontend: generated worktree ports such as `18782` / `13702`

The two checkouts do not share PostgreSQL containers, volumes, or databases. PostgreSQL host ports can collide; `/tmp/multica-compose-locks/multica-compose-lock-port-<port>` serializes the collision and prevents a different project from reaching mutation. Run `make db-up` and `make db-down` only from the checkout whose resources you intend to target.

## Command Reference

### Current Checkout PostgreSQL

Start the PostgreSQL container for the current env file's Compose project:

```bash
make db-up
```

Stop that same current checkout project:

```bash
make db-down
```

Important:

- guarded commands require the exact generated worktree identity and explicit matching Compose project
- `make db-down` stops the selected project but keeps its Docker volume
- other worktree PostgreSQL containers, volumes, and databases are not targeted

### App Lifecycle

Main checkout:

```bash
make setup-main
make start-main
make stop-main
make check-main
```

Worktree:

```bash
make worktree-env
make setup-worktree
make start-worktree
make stop-worktree
make check-worktree
```

Generic targets for the current checkout:

```bash
make setup
make start
make stop
make check
make dev
make test
make migrate-up
make migrate-down
```

These generic targets require a valid env file in the current directory.

## How Database Creation Works

Database creation is automatic.

The following commands all ensure the target database exists before they continue:

- `make setup`
- `make start`
- `make dev`
- `make test`
- `make migrate-up`
- `make migrate-down`
- `make check`

That logic lives in `scripts/ensure-postgres.sh`.

## Testing

Run all local checks:

```bash
make check-main
```

Or from a worktree:

```bash
make check-worktree
```

This runs:

1. TypeScript typecheck
2. TypeScript unit tests
3. Go tests
4. Playwright E2E tests

Notes:

- Go tests create their own fixture data
- E2E tests create their own workspace and issue fixtures
- the check flow starts backend/frontend only if they are not already running

## Local Codex Daemon

Run the local daemon:

```bash
make daemon
```

The daemon authenticates using the CLI's stored token (`multica login`).
It registers runtimes for all watched workspaces from the CLI config.

## Full-Stack Isolated Testing

This section covers running the complete stack (backend, frontend, daemon) from
source in a fully isolated environment. Useful for testing end-to-end changes
that span multiple components, or for automated CI/AI workflows that need zero
human intervention.

### Why Not Just `make daemon`?

`make daemon` uses the system-installed CLI's stored token and connects to
whatever server is configured in `~/.multica/config.json`. That's fine for
day-to-day development against a shared server, but for fully isolated testing
you need:

- a local backend and frontend (from source)
- a local daemon (from source) with its own profile
- automated authentication (no browser login)
- no interference with your production CLI config

### Dynamic Profile Naming

Each worktree must use a unique daemon profile to avoid collisions when
multiple features run in parallel.

The profile name is derived from the worktree directory using the same
slug + hash pattern as `scripts/init-worktree-env.sh`:

```bash
WORKTREE_DIR="$(basename "$PWD")"
SLUG="$(printf '%s' "$WORKTREE_DIR" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
HASH="$(printf '%s' "$PWD" | cksum | awk '{print $1}')"
OFFSET=$((HASH % 1000))
PROFILE="dev-${SLUG}-${OFFSET}"
```

Example: worktree at `../multica-feat-auth` produces profile
`dev-multica_feat_auth-347`, matching that worktree's port and database
allocation.

### Start the Isolated Environment

Run all steps from the worktree root (where the Makefile is).

#### 1. Start backend, frontend, and database

```bash
make dev
```

Wait for the backend to be healthy:

```bash
PORT=$(grep '^PORT=' .env.worktree 2>/dev/null || grep '^PORT=' .env | head -1 | cut -d= -f2)
PORT=${PORT:-8080}
SERVER="http://localhost:${PORT}"

for i in $(seq 1 30); do
  curl -sf "$SERVER/health" > /dev/null 2>&1 && break
  sleep 2
done
```

#### 2. Create a test user and token (automated auth)

For deterministic local automation, set `MULTICA_DEV_VERIFICATION_CODE=888888`
in your env file before starting the backend:

```bash
curl -s -X POST "$SERVER/auth/send-code" \
  -H "Content-Type: application/json" \
  -d '{"email": "dev@localhost"}'

JWT=$(curl -s -X POST "$SERVER/auth/verify-code" \
  -H "Content-Type: application/json" \
  -d '{"email": "dev@localhost", "code": "888888"}' | jq -r '.token')

PAT=$(curl -s -X POST "$SERVER/api/tokens" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{"name": "auto-dev", "expires_in_days": 365}' | jq -r '.token')
```

#### 3. Create a workspace

```bash
WS=$(curl -s -X POST "$SERVER/api/workspaces" \
  -H "Authorization: Bearer $PAT" \
  -H "Content-Type: application/json" \
  -d '{"name": "Dev", "slug": "dev"}' | jq -r '.id')
```

#### 4. Compute profile name and write CLI config

```bash
# Compute profile (see Dynamic Profile Naming above)
WORKTREE_DIR="$(basename "$PWD")"
SLUG="$(printf '%s' "$WORKTREE_DIR" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
HASH="$(printf '%s' "$PWD" | cksum | awk '{print $1}')"
OFFSET=$((HASH % 1000))
PROFILE="dev-${SLUG}-${OFFSET}"

FRONTEND_PORT=$(grep '^FRONTEND_PORT=' .env.worktree 2>/dev/null || grep '^FRONTEND_PORT=' .env | head -1 | cut -d= -f2)
FRONTEND_PORT=${FRONTEND_PORT:-3000}

CONFIG_DIR="$HOME/.multica/profiles/$PROFILE"
mkdir -p "$CONFIG_DIR"

cat > "$CONFIG_DIR/config.json" << EOF
{
  "server_url": "$SERVER",
  "app_url": "http://localhost:${FRONTEND_PORT}",
  "token": "$PAT",
  "workspace_id": "$WS",
  "watched_workspaces": [{"id": "$WS", "name": "Dev"}]
}
EOF
```

#### 5. Start the daemon from source

```bash
make cli ARGS="daemon start --profile $PROFILE"
```

The daemon runs from the current worktree's Go source, connecting to the
local backend. Agent-executed `multica` commands automatically use the same
binary (the daemon prepends its own directory to `PATH`).

### Stop the Isolated Environment

```bash
# Compute profile (same formula)
PROFILE="dev-$(printf '%s' "$(basename "$PWD")" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')-$(( $(printf '%s' "$PWD" | cksum | awk '{print $1}') % 1000 ))"

# 1. Stop daemon
make cli ARGS="daemon stop --profile $PROFILE"

# 2. Stop backend + frontend
make stop            # main checkout
make stop-worktree   # worktree checkout

# 3. (Optional) Stop this checkout's PostgreSQL Compose project
make db-down

# 4. (Optional) Clean build artifacts
make clean

# 5. (Optional) Retain profile config for inspection
# Do not permanently delete it. If disposal is approved, move this one reviewed
# absolute path through your OS's verified recycle/trash mechanism.
```

### Desktop App Local Testing

To test the Electron desktop app against a local backend:

```bash
# After backend is running (make dev)
pnpm dev:desktop
```

This automatically:

1. Compiles the `multica` CLI from `server/cmd/multica` into
   `apps/desktop/resources/bin/multica`
2. Creates an isolated profile named `desktop-localhost-<PORT>`
3. Starts and manages its own daemon instance
4. Connects to the local backend

Login in the Desktop UI with `dev@localhost` and the generated code from the
backend logs. If you set `MULTICA_DEV_VERIFICATION_CODE=888888` before starting
the backend, you can use `888888` instead.

If the backend runs on a non-default port (worktree), create
`apps/desktop/.env.development.local`:

```bash
VITE_API_URL=http://localhost:<backend-port>
VITE_WS_URL=ws://localhost:<backend-port>/ws
```

#### Running multiple worktrees side-by-side

`pnpm dev:desktop` auto-isolates a worktree so several worktrees can run their
own desktop dev instance at once — no extra setup. From a linked worktree it
derives, from the worktree path (same `cksum % 1000` offset as the backend /
frontend ports in `.env.worktree`):

- `DESKTOP_RENDERER_PORT` = `5174 + offset` — its own Vite dev server (`5174`
  base leaves `5173` for the primary checkout, even when `offset` is `0`)
- `DESKTOP_APP_SUFFIX` = `<folder>-<offset>` — its own single-instance lock /
  `userData`, and an app named `Multica Canary <folder>-<offset>` so it is
  distinguishable in Cmd+Tab. The offset keeps it unique across worktrees that
  share a folder name at different paths.

The primary checkout is left untouched (`5173`, `Multica Canary`). Set either
env var explicitly to override the derived value. Which backend each instance
talks to is still controlled only by `apps/desktop/.env*` above — point each
worktree's desktop at its own backend to also isolate the daemon profile.

### Isolation Guarantee

Nothing in this flow touches the system-installed `multica` or the default
`~/.multica/config.json`:

| Resource | System / Production | Local Dev (per-worktree) |
|---|---|---|
| Config | `~/.multica/config.json` | `~/.multica/profiles/dev-<slug>-<hash>/config.json` |
| Daemon PID | `~/.multica/daemon.pid` | `~/.multica/profiles/dev-<slug>-<hash>/daemon.pid` |
| Health port | `19514` | `19514 + 1 + (name_hash % 1000)` |
| Workspaces dir | `~/multica_workspaces/` | `~/multica_workspaces_dev-<slug>-<hash>/` |
| Database | remote / production | local Docker: generated `wt_<slug>_<offset>` |
| Desktop profile | `desktop-api.multica.ai` | `desktop-localhost-<port>` |

Multiple worktrees can run simultaneously when their derived resources do not collide. A PostgreSQL host-port collision is serialized by `/tmp/multica-compose-locks/multica-compose-lock-port-<port>` and the foreign project is refused before mutation; regenerate or choose a different worktree path before retrying.

## Troubleshooting

### Missing Env File

If you see:

```text
Missing env file: .env
```

or:

```text
Missing env file: .env.worktree
```

then create the expected env file first.

Main checkout:

```bash
cp .env.example .env
```

Worktree:

```bash
make worktree-env
```

### Check Which Database a Checkout Uses

Inspect the env file:

```bash
cat .env
cat .env.worktree
```

Look for:

- `POSTGRES_DB`
- `DATABASE_URL`
- `PORT`
- `FRONTEND_PORT`

### List Databases in the Current Checkout's PostgreSQL Container

Choose the env file deliberately. This read-only command refuses a missing
project or user and never falls back to the deployment `multica` project:

```bash
ENV_FILE=.env.worktree  # use .env only in the main checkout
test -f "$ENV_FILE" || { echo "Missing env file: $ENV_FILE" >&2; exit 1; }
set -a
. "$ENV_FILE"
set +a
test -n "${COMPOSE_PROJECT_NAME:-}" && test -n "${POSTGRES_USER:-}" || {
  echo "Env file must set COMPOSE_PROJECT_NAME and POSTGRES_USER" >&2
  exit 1
}
docker compose --project-name "$COMPOSE_PROJECT_NAME" exec -T postgres \
  psql -U "$POSTGRES_USER" -d postgres -At -c "select datname from pg_database order by datname;"
```

This lists only that project's PostgreSQL instance; it does not enumerate other worktree containers.

### Worktree Is Accidentally Using the Main Database

Check whether the worktree contains `.env`.

It should not.

The safe worktree setup is:

```bash
make worktree-env
make setup-worktree
make start-worktree
```

### App Stops but PostgreSQL Keeps Running

That is expected.

- `make stop`
- `make stop-main`
- `make stop-worktree`

only stop backend/frontend processes.

To stop the PostgreSQL container for the current checkout only:

```bash
make db-down
```

## Database Reset

To stop the current checkout's PostgreSQL container while retaining its local
volume and database, use the guarded command:

```bash
make db-down
```

For a fresh database in the current checkout only, stop app processes, reset
the canonical database, then start again:

```bash
make stop
make db-reset
make start
```

- `make db-reset` builds its service, user, and database target from the selected env file after canonical identity validation
- it refuses a remote `DATABASE_URL`, copied/retargeted worktree identity, and caller-controlled Compose selectors
- other worktree containers, volumes, and databases are not targeted
- `ENV_FILE=.env.worktree` is valid only when that generated file belongs to the current physical checkout

This guide intentionally provides no volume-deletion command. `make db-down`
retains the volume as evidence and preserves data; if local volume disposal is
required, stop and use an owner-approved maintenance procedure rather than a
direct Compose command.

## Typical Flows

### Stable Main Environment

```bash
make dev
```

### Feature Worktree

```bash
git worktree add ../multica-feature -b feat/my-change main
cd ../multica-feature
make dev
```

### Return to a Previously Configured Worktree

```bash
cd ../multica-feature
make start-worktree
```

### Validate Before Pushing

Main checkout:

```bash
make check-main
```

Worktree:

```bash
make check-worktree
```
