#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env}"
if [ "$#" -gt 0 ]; then
  shift
fi

RESET_DATABASE=false
if [ "$#" -gt 0 ]; then
  if [ "$#" -ne 1 ] || [ "$1" != "--reset-database" ]; then
    echo "Usage: $0 <env-file> [--reset-database]" >&2
    exit 1
  fi
  RESET_DATABASE=true
fi

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

validate_postgres_identifier() {
  local identifier="$1"
  local label="$2"
  if [[ ! "$identifier" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
    echo "ERROR: $label must be a PostgreSQL identifier containing letters, digits, or underscores" >&2
    exit 1
  fi
}

validate_postgres_identifier "$POSTGRES_DB" POSTGRES_DB
validate_postgres_identifier "$POSTGRES_USER" POSTGRES_USER

ready_timeout="${MULTICA_POSTGRES_READY_TIMEOUT_SECONDS:-60}"
if [[ ! "$ready_timeout" =~ ^[0-9]+$ ]] || [ "$ready_timeout" -lt 1 ] || [ "$ready_timeout" -gt 300 ]; then
  echo "ERROR: MULTICA_POSTGRES_READY_TIMEOUT_SECONDS must be an integer from 1 through 300" >&2
  exit 1
fi

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
  [ -z "$DATABASE_URL" ] || [ "$db_host" = localhost ] || [ "$db_host" = 127.0.0.1 ] || [ "$db_host" = ::1 ]
}

ensure_local_postgres() {
  local reset_database="${1:-false}"
  local project_name="$COMPOSE_PROJECT_NAME"
  local ready_deadline
  local db_exists

  if [ "$reset_database" != true ] && [ "$reset_database" != false ]; then
    echo "ERROR: internal PostgreSQL mutation mode is invalid" >&2
    return 1
  fi

  # compose_prepare_identity ran before this function entered the lock. Check
  # the resulting values again here because every SQL mutation is built from
  # this canonical identity, never from a caller-supplied Compose command.
  validate_postgres_identifier "$POSTGRES_DB" POSTGRES_DB
  validate_postgres_identifier "$POSTGRES_USER" POSTGRES_USER
  ready_deadline=$(( $(date +%s) + ready_timeout ))

  echo "==> Ensuring PostgreSQL container for project '$project_name' on localhost:${POSTGRES_PORT:-5432}..."
  compose_run_canonical up -d postgres

  echo "==> Waiting for PostgreSQL to be ready..."
  until compose_run_canonical exec -T postgres pg_isready -U "$POSTGRES_USER" -d postgres > /dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$ready_deadline" ]; then
      echo "ERROR: PostgreSQL did not become ready within ${ready_timeout} seconds for project '$project_name' on localhost:${POSTGRES_PORT:-5432}." >&2
      return 1
    fi
    sleep 1
  done

  echo "==> Ensuring database '$POSTGRES_DB' exists..."
  db_exists="$(compose_run_canonical exec -T postgres \
    psql -U "$POSTGRES_USER" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '$POSTGRES_DB'")"

  if [ "$db_exists" != 1 ]; then
    compose_run_canonical exec -T postgres \
      psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE \"$POSTGRES_DB\"" \
      > /dev/null
  fi

  # Use TCP inside the container so the query follows PostgreSQL host
  # authentication rather than a local Unix-socket trust rule. The password
  # remains inside the already configured container environment and is never
  # printed or placed in a host command argument.
  echo "==> Verifying credential-backed readiness..."
  if ! compose_run_canonical exec -T postgres \
    sh -ceu 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc "SELECT 1"' \
    > /dev/null 2>&1; then
    echo "ERROR: Credential-backed readiness check failed for database '$POSTGRES_DB'" >&2
    echo "  Configured user '$POSTGRES_USER' could not authenticate." >&2
    return 1
  fi

  echo "✓ PostgreSQL ready (local Docker). Project: $project_name, Database: $POSTGRES_DB"

  if [ "$reset_database" = true ]; then
    echo "==> Dropping and recreating canonical database '$POSTGRES_DB'..."
    compose_run_canonical exec -T postgres \
      psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
      -c "DROP DATABASE IF EXISTS \"$POSTGRES_DB\" WITH (FORCE);" \
      -c "CREATE DATABASE \"$POSTGRES_DB\";"
  fi
}

if is_local; then
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  MULTICA_COMPOSE_ENV_FILE="$ENV_FILE"
  export MULTICA_COMPOSE_ENV_FILE
  # shellcheck disable=SC1091
  . "$SCRIPT_DIR/compose-ownership-guard.sh"

  compose_with_ownership_lock ensure_local_postgres "$RESET_DATABASE"
else
  if [ "$RESET_DATABASE" = true ]; then
    echo "ERROR: Database reset is only valid for a local Docker database." >&2
    exit 1
  fi

  # A remote URL is the complete libpq authority. Start each client with a
  # cleared environment so ambient PG*/POSTGRES_* values cannot retarget it,
  # and keep the URL out of argv and logs by transporting it through
  # PGDATABASE. PGCONNECT_TIMEOUT bounds the authenticated connection attempt;
  # the URI retains its own host, credentials, database, and options.
  remote_pg_isready() {
    env -i PATH="$PATH" PGDATABASE="$DATABASE_URL" pg_isready -t 1
  }

  remote_psql() {
    local client_pid

    if [ "$(date +%s)" -ge "$remote_ready_deadline" ]; then
      return 1
    fi

    env -i PATH="$PATH" PGDATABASE="$DATABASE_URL" PGCONNECT_TIMEOUT=1 \
      psql -v ON_ERROR_STOP=1 -Atqc "SELECT 1" &
    client_pid=$!

    while kill -0 "$client_pid" > /dev/null 2>&1; do
      if [ "$(date +%s)" -ge "$remote_ready_deadline" ]; then
        # This is our own client process, started immediately above. Reap it so
        # the readiness deadline cannot leave a credential-bearing connection
        # attempt running in the background.
        kill -KILL "$client_pid" > /dev/null 2>&1 || true
        wait "$client_pid" > /dev/null 2>&1 || true
        return 1
      fi
      sleep 1
    done

    wait "$client_pid"
  }

  echo "==> Remote database detected (host: $db_host). Skipping Docker."
  if ! command -v pg_isready > /dev/null 2>&1; then
    echo "ERROR: pg_isready is required to verify remote PostgreSQL readiness." >&2
    exit 1
  fi
  if ! command -v psql > /dev/null 2>&1; then
    echo "ERROR: psql is required to verify authenticated remote PostgreSQL readiness." >&2
    exit 1
  fi

  remote_ready_deadline=$(( $(date +%s) + ready_timeout ))
  echo "==> Waiting for PostgreSQL at $db_host:$db_port to be ready..."
  until remote_pg_isready > /dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$remote_ready_deadline" ]; then
      echo "ERROR: Remote PostgreSQL did not become ready within ${ready_timeout} seconds at $db_host:$db_port." >&2
      exit 1
    fi
    sleep 1
  done

  echo "==> Verifying credential-backed readiness (remote)..."
  if ! remote_psql > /dev/null 2>&1; then
    echo "ERROR: Credential-backed readiness check failed for remote database '$db_name'" >&2
    exit 1
  fi

  echo "✓ PostgreSQL ready (remote: $db_host:$db_port). Database: $db_name"
fi
