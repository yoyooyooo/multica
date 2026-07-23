#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE"
  echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

POSTGRES_DB="${POSTGRES_DB:-multica}"
POSTGRES_USER="${POSTGRES_USER:-multica}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-multica}"
DATABASE_URL="${DATABASE_URL:-}"

export PGPASSWORD="$POSTGRES_PASSWORD"

db_host=""
db_port="${POSTGRES_PORT:-5432}"
db_name="$POSTGRES_DB"

parse_database_url() {
  local rest authority hostport path port_part

  rest="${DATABASE_URL#*://}"
  rest="${rest%%\?*}"
  authority="${rest%%/*}"
  path="${rest#*/}"

  if [ "$authority" = "$rest" ]; then
    path=""
  fi

  hostport="${authority##*@}"

  if [[ "$hostport" == \[* ]]; then
    db_host="${hostport#\[}"
    db_host="${db_host%%]*}"
    port_part="${hostport#*\]}"
    if [[ "$port_part" == :* ]] && [ -n "${port_part#:}" ]; then
      db_port="${port_part#:}"
    fi
  else
    db_host="${hostport%%:*}"
    if [[ "$hostport" == *:* ]] && [ -n "${hostport##*:}" ]; then
      db_port="${hostport##*:}"
    fi
  fi

  if [ -n "$path" ]; then
    db_name="${path%%/*}"
  fi
}

if [ -n "$DATABASE_URL" ]; then
  parse_database_url
fi

is_local() {
  [ -z "$DATABASE_URL" ] || [ "$db_host" = "localhost" ] || [ "$db_host" = "127.0.0.1" ] || [ "$db_host" = "::1" ]
}

if is_local; then
  # ---------- Compose ownership guard ----------
  # Before any Docker mutation, verify that our requested Compose project
  # name and host port do not collide with foreign ownership. This prevents
  # a worktree from accidentally recreating or relabeling live "multica-*"
  # deployment containers.
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

  # shellcheck disable=SC1091
  . "$SCRIPT_DIR/compose-ownership-guard.sh"

  # Invoke the guard function explicitly (sourcing alone is not enough)
  if ! guard_compose_ownership; then
    echo "ERROR: Compose ownership guard rejected the operation." >&2
    echo "  Did you mean to use a different env file or worktree name?" >&2
    exit 1
  fi

  # ---------- TOCTOU lock ----------
  # Cross-process atomic reservation: two worktrees choosing the same hash
  # /port/project must not both pass the guard. We acquire a mkdir-based
  # lock and hold it through the first compose up. The lock file name
  # includes the project name so unrelated projects do not block each other.
  lock_target="/tmp/multica-compose-lock-${COMPOSE_PROJECT_NAME:-multica}"
  if ! mkdir "$lock_target" 2>/dev/null; then
    echo "ERROR: Could not acquire compose lock for project '${COMPOSE_PROJECT_NAME:-multica}'" >&2
    echo "  Another process may be setting up this project concurrently." >&2
    echo "  If no other process is running, remove: $lock_target" >&2
    exit 1
  fi
  _release_lock() { rm -rf "$lock_target"; }
  trap _release_lock EXIT

  # ---------- Local: use Docker ----------
  project_name="${COMPOSE_PROJECT_NAME:-multica}"
  compose_args=(-f docker-compose.yml --project-name "$project_name")

  echo "==> Ensuring PostgreSQL container for project '$project_name' on localhost:${POSTGRES_PORT:-5432}..."
  docker compose "${compose_args[@]}" up -d postgres

  echo "==> Waiting for PostgreSQL to be ready..."
  until docker compose "${compose_args[@]}" exec -T postgres pg_isready -U "$POSTGRES_USER" -d postgres > /dev/null 2>&1; do
    sleep 1
  done

  echo "==> Ensuring database '$POSTGRES_DB' exists..."
  db_exists="$(docker compose "${compose_args[@]}" exec -T postgres \
    psql -U "$POSTGRES_USER" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '$POSTGRES_DB'")"

  if [ "$db_exists" != "1" ]; then
    docker compose "${compose_args[@]}" exec -T postgres \
      psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE \"$POSTGRES_DB\"" \
      > /dev/null
  fi

  # ---------- Credential-backed readiness (SELECT 1) ----------
  # pg_isready only verifies the postmaster is accepting TCP connections.
  # An authenticated SELECT 1 against the configured database/credential
  # proves that our credentials work and the intended database is usable.
  echo "==> Verifying credential-backed readiness..."
  if ! docker compose "${compose_args[@]}" exec -T postgres \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 \
    -Atqc "SELECT 1" > /dev/null 2>&1; then
    echo "ERROR: Credential-backed readiness check failed for $POSTGRES_DB" >&2
    echo "  User $POSTGRES_USER could not authenticate to database $POSTGRES_DB" >&2
    exit 1
  fi

  echo "✓ PostgreSQL ready (local Docker). Project: $project_name, Database: $POSTGRES_DB"
else
  # ---------- Remote: skip Docker, verify connectivity ----------
  echo "==> Remote database detected (host: $db_host). Skipping Docker."
  if command -v pg_isready > /dev/null 2>&1; then
    echo "==> Waiting for PostgreSQL at $db_host:$db_port to be ready..."
    until pg_isready -d "$DATABASE_URL" > /dev/null 2>&1; do
      sleep 1
    done

    # Remote readiness also needs credential-backed check
    echo "==> Verifying credential-backed readiness (remote)..."
    if ! command -v psql > /dev/null 2>&1; then
      echo "  WARNING: psql not found; skipping authenticated readiness check for remote DB"
    else
      if ! PGPASSWORD="$POSTGRES_PASSWORD" psql -h "$db_host" -p "$db_port" -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc "SELECT 1" > /dev/null 2>&1; then
        echo "ERROR: Credential-backed readiness check failed for remote $POSTGRES_DB at $db_host:$db_port" >&2
        exit 1
      fi
    fi

    echo "✓ PostgreSQL ready (remote: $db_host:$db_port). Database: $db_name"
  else
    echo "==> pg_isready not found. Skipping remote connectivity preflight."
    echo "✓ PostgreSQL configured (remote: $db_host:$db_port). Database: $db_name"
  fi
fi
