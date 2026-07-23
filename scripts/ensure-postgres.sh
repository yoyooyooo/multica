#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env}"
if [ "$#" -gt 0 ]; then
  shift
fi

POST_MUTATION=()
if [ "$#" -gt 0 ]; then
  if [ "$1" != "--" ]; then
    echo "Usage: $0 <env-file> [-- <post-readiness Docker mutation>]" >&2
    exit 1
  fi
  shift
  if [ "$#" -eq 0 ] || [ "$1" != docker ] || [ "${2:-}" != compose ]; then
    echo "ERROR: post-readiness mutation must be a non-empty docker compose command" >&2
    exit 1
  fi
  POST_MUTATION=("$@")
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
  [ -z "$DATABASE_URL" ] || [ "$db_host" = localhost ] || [ "$db_host" = 127.0.0.1 ] || [ "$db_host" = ::1 ]
}

ensure_local_postgres() {
  # Validate the optional db-reset mutation before the first Compose command.
  # This function runs inside compose_with_ownership_lock, after identity and
  # lock acquisition, so its generated Compose commands and post mutation use
  # the same canonical project target.
  if [ "$#" -gt 0 ]; then
    compose_assert_compose_project_target "$@" || return 1
  fi

  local project_name="$COMPOSE_PROJECT_NAME"
  local compose_args=(-f docker-compose.yml --project-name "$project_name")
  local db_exists

  echo "==> Ensuring PostgreSQL container for project '$project_name' on localhost:${POSTGRES_PORT:-5432}..."
  docker compose "${compose_args[@]}" up -d postgres

  echo "==> Waiting for PostgreSQL to be ready..."
  until docker compose "${compose_args[@]}" exec -T postgres pg_isready -U "$POSTGRES_USER" -d postgres > /dev/null 2>&1; do
    sleep 1
  done

  echo "==> Ensuring database '$POSTGRES_DB' exists..."
  db_exists="$(docker compose "${compose_args[@]}" exec -T postgres \
    psql -U "$POSTGRES_USER" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '$POSTGRES_DB'")"

  if [ "$db_exists" != 1 ]; then
    docker compose "${compose_args[@]}" exec -T postgres \
      psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE \"$POSTGRES_DB\"" \
      > /dev/null
  fi

  # Use TCP inside the container so the query follows PostgreSQL host
  # authentication rather than a local Unix-socket trust rule. The password
  # remains inside the already configured container environment and is never
  # printed or placed in a host command argument.
  echo "==> Verifying credential-backed readiness..."
  if ! docker compose "${compose_args[@]}" exec -T postgres \
    sh -ceu 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -Atqc "SELECT 1"' \
    > /dev/null 2>&1; then
    echo "ERROR: Credential-backed readiness check failed for database '$POSTGRES_DB'" >&2
    echo "  Configured user '$POSTGRES_USER' could not authenticate." >&2
    return 1
  fi

  echo "✓ PostgreSQL ready (local Docker). Project: $project_name, Database: $POSTGRES_DB"

  # db-reset supplies its exact Docker exec mutation here. It remains inside
  # the same ownership lock as the guard and PostgreSQL initialization.
  if [ "$#" -gt 0 ]; then
    "$@"
  fi
}

if is_local; then
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  # shellcheck disable=SC1091
  . "$SCRIPT_DIR/compose-ownership-guard.sh"

  if [ "${#POST_MUTATION[@]}" -gt 0 ]; then
    compose_with_ownership_lock ensure_local_postgres "${POST_MUTATION[@]}"
  else
    compose_with_ownership_lock ensure_local_postgres
  fi
else
  if [ "${#POST_MUTATION[@]}" -gt 0 ]; then
    echo "ERROR: A post-readiness Docker mutation is only valid for a local database." >&2
    exit 1
  fi

  echo "==> Remote database detected (host: $db_host). Skipping Docker."
  if command -v pg_isready > /dev/null 2>&1; then
    echo "==> Waiting for PostgreSQL at $db_host:$db_port to be ready..."
    until pg_isready -d "$DATABASE_URL" > /dev/null 2>&1; do
      sleep 1
    done

    echo "==> Verifying credential-backed readiness (remote)..."
    if ! command -v psql > /dev/null 2>&1; then
      echo "WARNING: psql not found; skipping authenticated readiness check for remote DB"
    elif ! PGPASSWORD="$POSTGRES_PASSWORD" psql -h "$db_host" -p "$db_port" -U "$POSTGRES_USER" -d "$db_name" -v ON_ERROR_STOP=1 -Atqc "SELECT 1" > /dev/null 2>&1; then
      echo "ERROR: Credential-backed readiness check failed for remote database '$db_name'" >&2
      exit 1
    fi

    echo "✓ PostgreSQL ready (remote: $db_host:$db_port). Database: $db_name"
  else
    echo "==> pg_isready not found. Skipping remote connectivity preflight."
    echo "✓ PostgreSQL configured (remote: $db_host:$db_port). Database: $db_name"
  fi
fi
