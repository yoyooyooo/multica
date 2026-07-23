#!/usr/bin/env bash
# Regression tests for worktree Compose isolation.
#
# Public seams under test:
#   1. compose-guard-mustpass.sh <env> -- <canonical-compose-intent>
#   2. ensure-postgres.sh <env> [--reset-database]
#
# The suite uses a deterministic fake Docker binary only. It never contacts
# the host daemon and intentionally retains its artifacts for inspection.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
. "$REPO_ROOT/scripts/worktree-identity.sh"
ONLY_CASE="${1:-}"
TEST_ROOT="${TEST_ROOT:-$(mktemp -d "${TMPDIR:-/tmp}/multica-compose-isolation.XXXXXX")}"
FAKE_BIN="$TEST_ROOT/bin"

PASS=0
FAIL=0
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass() {
  PASS=$((PASS + 1))
  echo -e "${GREEN}PASS${NC} $1"
}

fail() {
  FAIL=$((FAIL + 1))
  echo -e "${RED}FAIL${NC} $1"
}

run_case() {
  local name="$1"
  shift
  if [ -n "$ONLY_CASE" ] && [ "$ONLY_CASE" != "$name" ]; then
    return
  fi
  echo "--- $name ---"
  "$@"
}

mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -euo pipefail

state_root="${FAKE_DOCKER_STATE:?FAKE_DOCKER_STATE is required}"

label_value() {
  local resource_dir="$1"
  local key="$2"
  [ -f "$resource_dir/labels" ] || return 0
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$resource_dir/labels"
}

container_names() {
  local all="$1"
  local project="$2"
  local wanted_state="$3"
  local resource_dir name state actual_project
  for resource_dir in "$state_root"/containers/*; do
    [ -d "$resource_dir" ] || continue
    name="$(basename "$resource_dir")"
    state="$(cat "$resource_dir/state" 2>/dev/null || printf 'running')"
    [ "$all" = true ] || [ "$state" = running ] || continue
    if [ -n "$wanted_state" ] && [ "$state" != "$wanted_state" ]; then
      continue
    fi
    actual_project="$(label_value "$resource_dir" 'com.docker.compose.project')"
    [ -z "$project" ] || [ "$actual_project" = "$project" ] || continue
    printf '%s\n' "$name"
  done
}

volume_names() {
  local project="$1"
  local resource_dir name actual_project
  for resource_dir in "$state_root"/volumes/*; do
    [ -d "$resource_dir" ] || continue
    name="$(basename "$resource_dir")"
    actual_project="$(label_value "$resource_dir" 'com.docker.compose.project')"
    [ -z "$project" ] || [ "$actual_project" = "$project" ] || continue
    printf '%s\n' "$name"
  done
}

render_label() {
  local resource_dir="$1"
  local format="$2"
  local key=""
  if [ -z "$format" ]; then
    printf '{}\n'
    return 0
  fi
  case "$format" in
    *'multica.owner'*) key='multica.owner' ;;
    *'multica.worktree.path'*) key='multica.worktree.path' ;;
    *'multica.worktree.project'*) key='multica.worktree.project' ;;
    *'com.docker.compose.project'*) key='com.docker.compose.project' ;;
    *'.Driver'*) printf 'local\n'; return 0 ;;
  esac
  [ -n "$key" ] && label_value "$resource_dir" "$key"
}

command_name="${1:-}"
[ "$#" -gt 0 ] && shift

case "$command_name" in
  ps)
    all=false
    project=""
    wanted_state=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -a) all=true ;;
        --filter)
          shift
          filter="${1:-}"
          case "$filter" in
            label=com.docker.compose.project=*) project="${filter#label=com.docker.compose.project=}" ;;
            status=*) wanted_state="${filter#status=}" ;;
          esac
          ;;
        --format) shift ;;
      esac
      shift || true
    done
    container_names "$all" "$project" "$wanted_state"
    ;;
  inspect)
    format=""
    while [ "$#" -gt 1 ]; do
      case "$1" in
        --format) shift; format="${1:-}" ;;
      esac
      shift || true
    done
    name="${1:-}"
    resource_dir="$state_root/containers/$name"
    [ -d "$resource_dir" ] || exit 1
    render_label "$resource_dir" "$format"
    ;;
  port)
    name="${1:-}"
    resource_dir="$state_root/containers/$name"
    [ -d "$resource_dir" ] || exit 1
    if [ -f "$resource_dir/port" ]; then
      printf '127.0.0.1:%s\n' "$(cat "$resource_dir/port")"
    fi
    ;;
  volume)
    subcommand="${1:-}"
    [ "$#" -gt 0 ] && shift
    case "$subcommand" in
      ls)
        project=""
        while [ "$#" -gt 0 ]; do
          case "$1" in
            --filter)
              shift
              filter="${1:-}"
              case "$filter" in
                label=com.docker.compose.project=*) project="${filter#label=com.docker.compose.project=}" ;;
              esac
              ;;
            --format) shift ;;
          esac
          shift || true
        done
        volume_names "$project"
        ;;
      inspect)
        format=""
        while [ "$#" -gt 1 ]; do
          case "$1" in
            --format) shift; format="${1:-}" ;;
          esac
          shift || true
        done
        name="${1:-}"
        resource_dir="$state_root/volumes/$name"
        [ -d "$resource_dir" ] || exit 1
        render_label "$resource_dir" "$format"
        ;;
      *) exit 1 ;;
    esac
    ;;
  compose)
    arguments=("$@")
    project=""
    project_directory=""
    compose_file=""
    env_file=""
    action=""
    for ((index = 0; index < ${#arguments[@]}; index++)); do
      case "${arguments[$index]}" in
        --project-name|-p)
          index=$((index + 1))
          project="${arguments[$index]:-}"
          ;;
        --project-name=*)
          project="${arguments[$index]#--project-name=}"
          ;;
        -p?*)
          project="${arguments[$index]#-p}"
          ;;
        -f|--file)
          index=$((index + 1))
          compose_file="${arguments[$index]:-}"
          ;;
        --file=*)
          compose_file="${arguments[$index]#--file=}"
          ;;
        --env-file)
          index=$((index + 1))
          env_file="${arguments[$index]:-}"
          ;;
        --env-file=*)
          env_file="${arguments[$index]#--env-file=}"
          ;;
        --project-directory)
          index=$((index + 1))
          project_directory="${arguments[$index]:-}"
          ;;
        --project-directory=*)
          project_directory="${arguments[$index]#--project-directory=}"
          ;;
        up|down|exec)
          action="${arguments[$index]}"
          break
          ;;
      esac
    done
    project="${project:-${COMPOSE_PROJECT_NAME:-multica}}"
    printf '%s|%s|%s|%s|%s\n' "$project" "$project_directory" "$compose_file" "$env_file" "$action" >> "$state_root/compose-targets.log"
    printf '%s\n' "${COMPOSE_FILE:-}" > "$state_root/last-compose-file-env"

    case "$action" in
      up)
        printf '%s\n' "${FAKE_DOCKER_ACTOR:-single}" >> "$state_root/mutation.log"
        if [ "${FAKE_DOCKER_BLOCK_UP:-0}" = 1 ]; then
          : > "$state_root/mutation.started"
          while [ ! -f "$state_root/release-up" ]; do
            sleep 0.05
          done
        fi
        if [ "${FAKE_DOCKER_SKIP_RESOURCE_CREATE:-0}" != 1 ]; then
          container_dir="$state_root/containers/${project}-postgres-1"
          volume_dir="$state_root/volumes/${project}_pgdata"
          mkdir -p "$container_dir" "$volume_dir"
          printf 'running\n' > "$container_dir/state"
          printf '%s\n' "${POSTGRES_PORT:-5432}" > "$container_dir/port"
          for resource_dir in "$container_dir" "$volume_dir"; do
            cat > "$resource_dir/labels" <<LABELS
com.docker.compose.project=$project
multica.owner=${MULTICA_OWNER:-deployment}
multica.worktree.path=${WORKTREE_PATH:-}
multica.worktree.project=$project
LABELS
          done
        fi
        printf '%s\n' "${POSTGRES_DB:-multica}" > "$state_root/initialized-db"
        ;;
      down)
        printf 'down:%s\n' "$project" >> "$state_root/mutation.log"
        ;;
      exec)
        argument_text="${arguments[*]}"
        if [[ "$argument_text" == *'pg_isready'* ]] && [ "${FAKE_DOCKER_PG_ISREADY_FAIL:-0}" = 1 ]; then
          exit 1
        elif [[ "$argument_text" == *'SELECT 1 FROM pg_database'* ]]; then
          printf '1\n'
        elif [[ "$argument_text" == *'SELECT 1'* ]]; then
          database=""
          user=""
          for ((index = 0; index < ${#arguments[@]}; index++)); do
            case "${arguments[$index]}" in
              -d) index=$((index + 1)); database="${arguments[$index]:-}" ;;
              -U) index=$((index + 1)); user="${arguments[$index]:-}" ;;
            esac
          done
          database="${database:-${POSTGRES_DB:-}}"
          user="${user:-${POSTGRES_USER:-}}"
          printf '%s\n' "$database" > "$state_root/authenticated-db"
          printf '%s\n' "$user" > "$state_root/authenticated-user"
          printf '1\n'
        elif [[ "$argument_text" == *'DROP DATABASE'* ]]; then
          service=""
          database=""
          user=""
          for ((index = 0; index < ${#arguments[@]}; index++)); do
            case "${arguments[$index]}" in
              exec)
                service_index=$((index + 1))
                while [ "$service_index" -lt "${#arguments[@]}" ]; do
                  case "${arguments[$service_index]}" in
                    -T) service_index=$((service_index + 1)) ;;
                    *) service="${arguments[$service_index]}"; break ;;
                  esac
                done
                ;;
              -d) index=$((index + 1)); database="${arguments[$index]:-}" ;;
              -U) index=$((index + 1)); user="${arguments[$index]:-}" ;;
            esac
          done
          printf '%s\n' "$service" > "$state_root/post-mutation-service"
          printf '%s\n' "$user" > "$state_root/post-mutation-user"
          printf '%s\n' "$database" > "$state_root/post-mutation-database"
          printf '%s\n' "$argument_text" > "$state_root/post-mutation-argv"
          : > "$state_root/post-mutation.started"
          if [ "${FAKE_DOCKER_BLOCK_POST:-0}" = 1 ]; then
            while [ ! -f "$state_root/release-post-mutation" ]; do
              sleep 0.05
            done
          fi
          printf '%s\n' "$project" > "$state_root/post-mutation"
        fi
        ;;
      *) exit 1 ;;
    esac
    ;;
  *) exit 1 ;;
esac
FAKE_DOCKER
chmod +x "$FAKE_BIN/docker"

for helper in lsof ss netstat; do
  cat > "$FAKE_BIN/$helper" <<'FAKE_HELPER'
#!/usr/bin/env bash
set -euo pipefail
if [ "${FAKE_LSOF_PORT:-}" != "" ] && [ "$(basename "$0")" = lsof ]; then
  printf 'fixture 4242 user 3u IPv4 0x0 TCP 127.0.0.1:%s (LISTEN)\n' "$FAKE_LSOF_PORT"
  exit 0
fi
exit 1
FAKE_HELPER
  chmod +x "$FAKE_BIN/$helper"
done

cat > "$FAKE_BIN/pg_isready" <<'FAKE_PG_ISREADY'
#!/usr/bin/env bash
set -euo pipefail
# The production command clears its environment. Derive this fake-only state
# location from the fixture worktree when FAKE_DOCKER_STATE is intentionally
# absent, so the recording seam never requires a propagated libpq variable.
state_root="${FAKE_DOCKER_STATE:-${PWD%/worktree}/state}"
printf '%s\n' "$@" >> "$state_root/remote-pg-isready-argv"
{
  [ -n "${PGDATABASE+x}" ] && echo 'PGDATABASE=set' || echo 'PGDATABASE=unset'
  [ -n "${PGPASSWORD+x}" ] && echo 'PGPASSWORD=set' || echo 'PGPASSWORD=unset'
  [ -n "${PGHOST+x}" ] && echo 'PGHOST=set' || echo 'PGHOST=unset'
  [ -n "${PGPORT+x}" ] && echo 'PGPORT=set' || echo 'PGPORT=unset'
  [ -n "${PGUSER+x}" ] && echo 'PGUSER=set' || echo 'PGUSER=unset'
  [ -n "${PGSSLMODE+x}" ] && echo 'PGSSLMODE=set' || echo 'PGSSLMODE=unset'
  [ -n "${POSTGRES_DB+x}" ] && echo 'POSTGRES_DB=set' || echo 'POSTGRES_DB=unset'
  [ -n "${POSTGRES_USER+x}" ] && echo 'POSTGRES_USER=set' || echo 'POSTGRES_USER=unset'
  [ -n "${POSTGRES_PASSWORD+x}" ] && echo 'POSTGRES_PASSWORD=set' || echo 'POSTGRES_PASSWORD=unset'
} > "$state_root/remote-pg-isready-libpq-env"
printf 'call\n' >> "$state_root/remote-pg-isready-calls"
if [ "${FAKE_REMOTE_PG_ISREADY_FAIL:-0}" = 1 ] || [ -f "$state_root/remote-pg-isready-fail" ]; then
  exit 1
fi
FAKE_PG_ISREADY
chmod +x "$FAKE_BIN/pg_isready"

cat > "$FAKE_BIN/psql" <<'FAKE_PSQL'
#!/usr/bin/env bash
set -euo pipefail
state_root="${FAKE_DOCKER_STATE:-${PWD%/worktree}/state}"
printf '%s\n' "$@" > "$state_root/remote-psql-argv"
{
  [ -n "${PGDATABASE+x}" ] && echo 'PGDATABASE=set' || echo 'PGDATABASE=unset'
  [ -n "${PGPASSWORD+x}" ] && echo 'PGPASSWORD=set' || echo 'PGPASSWORD=unset'
  [ -n "${PGHOST+x}" ] && echo 'PGHOST=set' || echo 'PGHOST=unset'
  [ -n "${PGPORT+x}" ] && echo 'PGPORT=set' || echo 'PGPORT=unset'
  [ -n "${PGUSER+x}" ] && echo 'PGUSER=set' || echo 'PGUSER=unset'
  [ -n "${PGSSLMODE+x}" ] && echo 'PGSSLMODE=set' || echo 'PGSSLMODE=unset'
  [ -n "${PGCONNECT_TIMEOUT+x}" ] && echo 'PGCONNECT_TIMEOUT=set' || echo 'PGCONNECT_TIMEOUT=unset'
  [ -n "${POSTGRES_DB+x}" ] && echo 'POSTGRES_DB=set' || echo 'POSTGRES_DB=unset'
  [ -n "${POSTGRES_USER+x}" ] && echo 'POSTGRES_USER=set' || echo 'POSTGRES_USER=unset'
  [ -n "${POSTGRES_PASSWORD+x}" ] && echo 'POSTGRES_PASSWORD=set' || echo 'POSTGRES_PASSWORD=unset'
} > "$state_root/remote-psql-libpq-env"
FAKE_PSQL
chmod +x "$FAKE_BIN/psql"

FAKE_PG_ONLY="$TEST_ROOT/fake-pg-only"
mkdir -p "$FAKE_PG_ONLY"
ln -s "$FAKE_BIN/pg_isready" "$FAKE_PG_ONLY/pg_isready"
cat > "$FAKE_PG_ONLY/date" <<'FAKE_DATE'
#!/usr/bin/env bash
exec /bin/date "$@"
FAKE_DATE
cat > "$FAKE_PG_ONLY/env" <<'FAKE_ENV'
#!/usr/bin/env bash
exec /usr/bin/env "$@"
FAKE_ENV
chmod +x "$FAKE_PG_ONLY/date" "$FAKE_PG_ONLY/env"

cat > "$FAKE_BIN/mv" <<'FAKE_MV'
#!/usr/bin/env bash
set -euo pipefail
if [ "${FAKE_MV_FAIL_RELEASE:-0}" = 1 ] && [[ "${2:-}" == *.released.* ]]; then
  exit 1
fi
exec /bin/mv "$@"
FAKE_MV
chmod +x "$FAKE_BIN/mv"

cat > "$FAKE_BIN/ln" <<'FAKE_LN'
#!/usr/bin/env bash
set -euo pipefail
if [ "${FAKE_LN_PAUSE_BEFORE_PUBLICATION:-0}" = 1 ] && [[ "${2:-}" == *'/multica-compose-lock-port-'* ]]; then
  : > "${FAKE_DOCKER_STATE:?FAKE_DOCKER_STATE is required}/lock-publication.started"
  while [ ! -f "$FAKE_DOCKER_STATE/release-lock-publication" ]; do
    sleep 0.05
  done
fi
exec /bin/ln "$@"
FAKE_LN
chmod +x "$FAKE_BIN/ln"

cat > "$FAKE_BIN/git" <<'FAKE_GIT'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = rev-parse ]; then
  case "${2:-}" in
    --show-toplevel)
      printf '%s\n' "${FAKE_GIT_TOPLEVEL:-$PWD}"
      exit 0
      ;;
    --git-common-dir)
      printf '%s\n' "${FAKE_DOCKER_STATE:?}/git-common"
      exit 0
      ;;
    --git-dir)
      if [ "${FAKE_GIT_LINKED_WORKTREE:-0}" = 1 ]; then
        printf '%s\n' "${FAKE_DOCKER_STATE:?}/git-linked"
      else
        printf '%s\n' "${FAKE_DOCKER_STATE:?}/git-common"
      fi
      exit 0
      ;;
  esac
fi
exit 1
FAKE_GIT
chmod +x "$FAKE_BIN/git"

export PATH="$FAKE_BIN:$PATH"

new_fixture() {
  local name="$1"
  CASE_ROOT="$TEST_ROOT/$name"
  FAKE_DOCKER_STATE="$CASE_ROOT/state"
  # The production lock root is intentionally not configurable. Each fixture
  # keeps fake-Docker state task-local, while lock behavior uses the same fixed
  # namespace as a production entrypoint.
  unset MULTICA_COMPOSE_LOCK_ROOT
  export CASE_ROOT FAKE_DOCKER_STATE
  mkdir -p "$FAKE_DOCKER_STATE/containers" "$FAKE_DOCKER_STATE/volumes"
}

derive_worktree_identity() {
  worktree_identity_derive "$1"
  FIXTURE_WORKTREE_PATH="$WORKTREE_IDENTITY_PATH"
  FIXTURE_PROJECT="$WORKTREE_IDENTITY_PROJECT"
  FIXTURE_PORT="$WORKTREE_IDENTITY_PORT"
  FIXTURE_DATABASE="$WORKTREE_IDENTITY_DATABASE"
}

write_env() {
  local env_file="$1"
  local path="$3"

  derive_worktree_identity "$path"
  : > "$FIXTURE_WORKTREE_PATH/docker-compose.yml"
  cat > "$env_file" <<ENV
COMPOSE_PROJECT_NAME=$FIXTURE_PROJECT
MULTICA_OWNER=worktree
WORKTREE_PATH=$FIXTURE_WORKTREE_PATH
POSTGRES_DB=$FIXTURE_DATABASE
POSTGRES_USER=fixture_user
POSTGRES_PASSWORD=fixture-password-not-for-output
POSTGRES_PORT=$FIXTURE_PORT
DATABASE_URL=postgres://fixture_user:fixture-password-not-for-output@localhost:$FIXTURE_PORT/$FIXTURE_DATABASE?sslmode=disable
ENV
}

env_value() {
  local env_file="$1"
  local key="$2"
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$env_file"
}

run_guard() {
  local worktree="$1"
  local env_file="$2"
  shift 2
  (
    cd "$worktree"
    bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- "$@"
  )
}

run_ensure() {
  local worktree="$1"
  local env_file="$2"
  shift 2
  (
    cd "$worktree"
    bash "$REPO_ROOT/scripts/ensure-postgres.sh" "$env_file" "$@"
  )
}

run_ensure_without_psql() {
  local worktree="$1"
  local env_file="$2"
  shift 2
  (
    cd "$worktree"
    PATH="$FAKE_PG_ONLY"
    export PATH
    /bin/bash "$REPO_ROOT/scripts/ensure-postgres.sh" "$env_file" "$@"
  )
}

lock_path_for_env() {
  local worktree="$1"
  local env_file="$2"
  (
    cd "$worktree"
    set -a
    . "$env_file"
    set +a
    MULTICA_COMPOSE_ENV_FILE="$env_file"
    export MULTICA_COMPOSE_ENV_FILE
    . "$REPO_ROOT/scripts/compose-ownership-guard.sh"
    compose_prepare_identity
    compose_lock_expected_path
  )
}

fixture_container() {
  local name="$1"
  local state="$2"
  local compose_project="$3"
  local owner="$4"
  local worktree_path="$5"
  local worktree_project="$6"
  local port="${7:-}"
  local resource_dir="$FAKE_DOCKER_STATE/containers/$name"
  mkdir -p "$resource_dir"
  printf '%s\n' "$state" > "$resource_dir/state"
  [ -z "$port" ] || printf '%s\n' "$port" > "$resource_dir/port"
  cat > "$resource_dir/labels" <<LABELS
com.docker.compose.project=$compose_project
LABELS
  [ "$owner" = '-' ] || printf 'multica.owner=%s\n' "$owner" >> "$resource_dir/labels"
  [ "$worktree_path" = '-' ] || printf 'multica.worktree.path=%s\n' "$worktree_path" >> "$resource_dir/labels"
  [ "$worktree_project" = '-' ] || printf 'multica.worktree.project=%s\n' "$worktree_project" >> "$resource_dir/labels"
}

fixture_volume() {
  local name="$1"
  local compose_project="$2"
  local owner="$3"
  local worktree_path="$4"
  local worktree_project="$5"
  local resource_dir="$FAKE_DOCKER_STATE/volumes/$name"
  mkdir -p "$resource_dir"
  cat > "$resource_dir/labels" <<LABELS
com.docker.compose.project=$compose_project
LABELS
  [ "$owner" = '-' ] || printf 'multica.owner=%s\n' "$owner" >> "$resource_dir/labels"
  [ "$worktree_path" = '-' ] || printf 'multica.worktree.path=%s\n' "$worktree_path" >> "$resource_dir/labels"
  [ "$worktree_project" = '-' ] || printf 'multica.worktree.project=%s\n' "$worktree_project" >> "$resource_dir/labels"
}

mutation_count() {
  if [ -f "$FAKE_DOCKER_STATE/mutation.log" ]; then
    wc -l < "$FAKE_DOCKER_STATE/mutation.log" | tr -d ' '
  else
    printf '0\n'
  fi
}

wait_for_file() {
  local target="$1"
  local attempts="${2:-10}"
  local attempt=0

  while [ "$attempt" -lt "$attempts" ]; do
    [ -f "$target" ] && return 0
    sleep 1
    attempt=$((attempt + 1))
  done
  return 1
}

test_canonical_identity() {
  new_fixture canonical-identity
  local physical="$CASE_ROOT/physical-worktree"
  local alias="$CASE_ROOT/path-alias"
  local alias_env="$CASE_ROOT/alias.env"
  local physical_env="$CASE_ROOT/physical.env"
  mkdir -p "$physical"
  ln -s "$physical" "$alias"

  (
    cd "$alias"
    FORCE=1 bash "$REPO_ROOT/scripts/init-worktree-env.sh" "$alias_env" > "$CASE_ROOT/alias.out"
  )
  (
    cd "$physical"
    FORCE=1 bash "$REPO_ROOT/scripts/init-worktree-env.sh" "$physical_env" > "$CASE_ROOT/physical.out"
  )

  local canonical_physical alias_path physical_path alias_project physical_project alias_port physical_port
  canonical_physical="$(cd "$physical" && pwd -P)"
  alias_path="$(bash -c '. "$1"; printf "%s" "$WORKTREE_PATH"' _ "$alias_env")"
  physical_path="$(bash -c '. "$1"; printf "%s" "$WORKTREE_PATH"' _ "$physical_env")"
  alias_project="$(bash -c '. "$1"; printf "%s" "$COMPOSE_PROJECT_NAME"' _ "$alias_env")"
  physical_project="$(bash -c '. "$1"; printf "%s" "$COMPOSE_PROJECT_NAME"' _ "$physical_env")"
  alias_port="$(bash -c '. "$1"; printf "%s" "$POSTGRES_PORT"' _ "$alias_env")"
  physical_port="$(bash -c '. "$1"; printf "%s" "$POSTGRES_PORT"' _ "$physical_env")"

  if [ "$alias_path" = "$canonical_physical" ] && [ "$physical_path" = "$canonical_physical" ] && \
    [ "$alias_project" = "$physical_project" ] && [ "$alias_port" = "$physical_port" ]; then
    pass "canonical path produces one stable worktree identity"
  else
    fail "path aliases produced different worktree identities"
  fi
}

test_linked_worktree_requires_owner() {
  new_fixture linked-worktree-requires-owner
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project
  mkdir -p "$worktree"
  : > "$worktree/docker-compose.yml"
  derive_worktree_identity "$worktree"
  project="$FIXTURE_PROJECT"
  cat > "$env_file" <<ENV
COMPOSE_PROJECT_NAME=$project
WORKTREE_PATH=$FIXTURE_WORKTREE_PATH
POSTGRES_DB=$FIXTURE_DATABASE
POSTGRES_USER=fixture_user
POSTGRES_PASSWORD=fixture-password-not-for-output
POSTGRES_PORT=$FIXTURE_PORT
DATABASE_URL=postgres://fixture_user:fixture-password-not-for-output@localhost:$FIXTURE_PORT/$FIXTURE_DATABASE?sslmode=disable
ENV

  local missing_refused=false
  if ! FAKE_GIT_LINKED_WORKTREE=1 run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/missing-owner.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    missing_refused=true
  fi

  printf 'MULTICA_OWNER=deployment\n' >> "$env_file"
  local deployment_refused=false
  if ! FAKE_GIT_LINKED_WORKTREE=1 run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/deployment-owner.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    deployment_refused=true
  fi

  if [ "$missing_refused" = true ] && [ "$deployment_refused" = true ]; then
    pass "linked worktree cannot inherit or request deployment Compose defaults"
  else
    fail "linked worktree accepted a deployment Compose ownership path"
  fi
}

test_wrapper_mutation() {
  new_fixture wrapper-mutation
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"

  if run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/wrapper.out" 2>&1 && \
    [ "$(mutation_count)" = 1 ]; then
    pass "canonical wrapper keeps guard and requested mutation in one boundary"
  else
    fail "canonical wrapper did not execute exactly one guarded mutation"
  fi
}

test_canonical_compose_target() {
  new_fixture canonical-compose-target
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local canonical_env_file
  local expected_target
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  canonical_env_file="$(cd "$(dirname "$env_file")" && pwd -P)/$(basename "$env_file")"
  expected_target="$(env_value "$env_file" COMPOSE_PROJECT_NAME)|$(env_value "$env_file" WORKTREE_PATH)|$(env_value "$env_file" WORKTREE_PATH)/docker-compose.yml|$canonical_env_file|up"

  if run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/canonical-target.out" 2>&1 && \
    grep -Fqx "$expected_target" "$FAKE_DOCKER_STATE/compose-targets.log" && \
    [ -z "$(cat "$FAKE_DOCKER_STATE/last-compose-file-env")" ]; then
    pass "canonical wrapper binds Compose project, directory, env, and config before mutation"
  else
    fail "canonical wrapper did not bind the exact Compose target"
  fi
}

test_compose_selector_refusal() {
  new_fixture compose-selector-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project alternate alternate_env selector_env all_refused=true
  mkdir -p "$worktree" "$CASE_ROOT/alternate"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  alternate="$CASE_ROOT/alternate/evil-compose.yml"
  alternate_env="$CASE_ROOT/alternate/evil.env"
  : > "$alternate"
  : > "$alternate_env"

  if run_guard "$worktree" "$env_file" \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/project-name.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose -p "$project" up -d postgres > "$CASE_ROOT/short-project.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose --project-name "$project" -p "$project" up -d postgres > "$CASE_ROOT/duplicate-project.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose --project-directory "$CASE_ROOT/alternate" up -d postgres > "$CASE_ROOT/project-directory.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose --project-name "$project" --project-directory "$CASE_ROOT/alternate" -f "$alternate" up -d postgres > "$CASE_ROOT/combined-target-divergence.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose -f "$alternate" up -d postgres > "$CASE_ROOT/file.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose --file "$alternate" up -d postgres > "$CASE_ROOT/long-file.out" 2>&1; then
    all_refused=false
  fi
  if run_guard "$worktree" "$env_file" \
    docker compose --env-file "$alternate_env" up -d postgres > "$CASE_ROOT/env-file.out" 2>&1; then
    all_refused=false
  fi
  if COMPOSE_FILE="$alternate" run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/compose-file-env.out" 2>&1; then
    all_refused=false
  fi
  selector_env="$CASE_ROOT/selector.env"
  cp "$env_file" "$selector_env"
  printf 'COMPOSE_FILE=%s\n' "$alternate" >> "$selector_env"
  if run_guard "$worktree" "$selector_env" \
    docker compose up -d postgres > "$CASE_ROOT/compose-file-in-env.out" 2>&1; then
    all_refused=false
  fi

  if [ "$all_refused" = true ] && [ "$(mutation_count)" = 0 ]; then
    pass "caller-controlled Compose selectors and COMPOSE_FILE are refused before mutation"
  else
    fail "a caller-controlled Compose selector reached a mutation"
  fi
}

test_make_override_refusal() {
  new_fixture make-override-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local alternate output
  mkdir -p "$worktree" "$CASE_ROOT/alternate"
  write_env "$env_file" ignored "$worktree" ignored
  alternate="$CASE_ROOT/alternate/evil-compose.yml"
  : > "$alternate"

  output="$(make -C "$REPO_ROOT" -n ENV_FILE="$env_file" \
    COMPOSE_ARGS="--project-directory $CASE_ROOT/alternate -f $alternate" \
    COMPOSE="docker compose --project-directory $CASE_ROOT/alternate" \
    POSTGRES_DB=foreign_db POSTGRES_USER=foreign_user db-up db-down db-reset 2>&1)"

  if printf '%s\n' "$output" | grep -Fq "$alternate" || \
    printf '%s\n' "$output" | grep -Fq foreign_db || \
    printf '%s\n' "$output" | grep -Fq foreign_user || \
    ! printf '%s\n' "$output" | grep -Fq -- '--reset-database'; then
    fail "make command-line overrides still influence Compose or reset mutation inputs"
  else
    pass "make command-line overrides cannot retarget Compose or database reset mutations"
  fi
}

test_deployment_identity_release() {
  new_fixture deployment-identity-release
  local checkout="$CASE_ROOT/main-checkout"
  local env_file="$CASE_ROOT/main.env"
  local lock_path
  mkdir -p "$checkout"
  : > "$checkout/docker-compose.yml"
  cat > "$env_file" <<ENV
COMPOSE_PROJECT_NAME=multica
MULTICA_OWNER=deployment
WORKTREE_PATH=
POSTGRES_DB=multica
POSTGRES_USER=fixture_user
POSTGRES_PASSWORD=fixture-password-not-for-output
POSTGRES_PORT=5432
DATABASE_URL=postgres://fixture_user:fixture-password-not-for-output@localhost:5432/multica?sslmode=disable
ENV

  lock_path="$(lock_path_for_env "$checkout" "$env_file")"
  if run_guard "$checkout" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/deployment.out" 2>&1 && \
    [ "$(mutation_count)" = 1 ] && \
    find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print -quit | grep -q .; then
    pass "deployment identity retains its verified lock release path"
  else
    fail "deployment identity did not complete a verified lock release"
  fi
}

test_stopped_foreign_refusal() {
  new_fixture stopped-foreign-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  fixture_container "foreign-stopped-postgres" exited "$project" deployment /deploy/live multica 5432

  if docker ps --filter "label=com.docker.compose.project=$project" --format '{{.Names}}' | grep -q foreign-stopped-postgres; then
    fail "stopped fixture leaked into running-container listing"
  elif docker ps -a --filter "label=com.docker.compose.project=$project" --format '{{.Names}}' | grep -q foreign-stopped-postgres; then
    if run_guard "$worktree" "$env_file" \
      docker compose down > "$CASE_ROOT/stopped.out" 2>&1; then
      fail "stopped foreign resource reached cleanup mutation"
    elif [ "$(mutation_count)" = 0 ]; then
      pass "actual stopped foreign Compose fixture is refused before cleanup mutation"
    else
      fail "stopped foreign resource produced a mutation"
    fi
  else
    fail "stopped fixture was not visible to docker ps -a"
  fi
}

test_strict_volume_labels() {
  new_fixture strict-volume-labels
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project canonical_worktree
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  fixture_volume "${project}_pgdata" "$project" - - -

  local labeled_refused=false
  if ! run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/volume.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    labeled_refused=true
  fi

  new_fixture strict-unlabeled-volume
  worktree="$CASE_ROOT/worktree"
  env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  mkdir -p "$FAKE_DOCKER_STATE/volumes/${project}_pgdata"
  : > "$FAKE_DOCKER_STATE/volumes/${project}_pgdata/labels"

  local unlabeled_refused=false
  if ! run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/unlabeled-volume.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    unlabeled_refused=true
  fi

  new_fixture strict-foreign-volume
  worktree="$CASE_ROOT/worktree"
  env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  mkdir -p "$FAKE_DOCKER_STATE/volumes/${project}_pgdata"
  canonical_worktree="$(env_value "$env_file" WORKTREE_PATH)"
  cat > "$FAKE_DOCKER_STATE/volumes/${project}_pgdata/labels" <<LABELS
com.docker.compose.project=foreign_project
multica.owner=worktree
multica.worktree.path=$canonical_worktree
multica.worktree.project=$project
LABELS

  local foreign_refused=false
  if ! run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/foreign-volume.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    foreign_refused=true
  fi

  if [ "$labeled_refused" = true ] && [ "$unlabeled_refused" = true ] && [ "$foreign_refused" = true ]; then
    pass "volumes with missing, unlabeled, or foreign project evidence are refused"
  else
    fail "a malformed volume ownership fixture was accepted"
  fi
}

test_path_alias_refusal() {
  new_fixture path-alias-refusal
  local physical="$CASE_ROOT/physical-worktree"
  local alias="$CASE_ROOT/path-alias"
  local env_file="$CASE_ROOT/worktree.env"
  local project port
  mkdir -p "$physical"
  ln -s "$physical" "$alias"
  write_env "$env_file" ignored "$alias" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  port="$(env_value "$env_file" POSTGRES_PORT)"
  fixture_container "${project}-postgres-1" running "$project" worktree "$alias" "$project" "$port"

  if run_guard "$alias" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/alias.out" 2>&1; then
    fail "noncanonical resource label was accepted through a path alias"
  elif [ "$(mutation_count)" = 0 ]; then
    pass "noncanonical path label is rejected before mutation"
  else
    fail "path-alias fixture produced a mutation"
  fi
}

test_port_refusal() {
  new_fixture port-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project port
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  port="$(env_value "$env_file" POSTGRES_PORT)"
  fixture_container foreign-port-owner running foreign_project deployment /deploy/live foreign_project "$port"

  if run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/docker-port.out" 2>&1; then
    fail "foreign Docker port binder was accepted"
  else
    pass "foreign Docker port binder is refused"
  fi

  new_fixture nondocker-port-refusal
  worktree="$CASE_ROOT/worktree"
  env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  port="$(env_value "$env_file" POSTGRES_PORT)"
  if FAKE_LSOF_PORT="$port" run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/nondocker-port.out" 2>&1; then
    fail "non-Docker port binder was accepted"
  else
    pass "non-Docker port binder is refused through portable lsof detection"
  fi
}

test_concurrent_collision() {
  new_fixture concurrent-collision
  local project
  local worktree_a="$CASE_ROOT/worktree"
  local worktree_b="$CASE_ROOT/worktree-alias"
  local env_a="$CASE_ROOT/a.env"
  local env_b="$CASE_ROOT/b.env"
  mkdir -p "$worktree_a"
  ln -s "$worktree_a" "$worktree_b"
  write_env "$env_a" ignored "$worktree_a" ignored
  write_env "$env_b" ignored "$worktree_b" ignored
  project="$(env_value "$env_a" COMPOSE_PROJECT_NAME)"

  if [ "$project" != "$(env_value "$env_b" COMPOSE_PROJECT_NAME)" ]; then
    fail "canonical aliases did not derive one collision key"
    return
  fi

  FAKE_DOCKER_ACTOR=first FAKE_DOCKER_BLOCK_UP=1 MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5 \
    run_guard "$worktree_a" "$env_a" \
    docker compose up -d postgres > "$CASE_ROOT/first.out" 2>&1 &
  local first_pid=$!

  local started=false
  for _ in $(seq 1 100); do
    if [ -f "$FAKE_DOCKER_STATE/mutation.started" ]; then
      started=true
      break
    fi
    sleep 0.05
  done

  if [ "$started" != true ]; then
    fail "first contender did not reach the guarded mutation seam"
    : > "$FAKE_DOCKER_STATE/release-up"
    wait "$first_pid" || true
    return
  fi

  local second_status=0
  if FAKE_DOCKER_ACTOR=second MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=1 \
    run_guard "$worktree_b" "$env_b" \
    docker compose up -d postgres > "$CASE_ROOT/second.out" 2>&1; then
    second_status=0
  else
    second_status=$?
  fi

  : > "$FAKE_DOCKER_STATE/release-up"
  local first_status=0
  if wait "$first_pid"; then
    first_status=0
  else
    first_status=$?
  fi

  if [ "$first_status" = 0 ] && [ "$second_status" -ne 0 ] && \
    [ "$(mutation_count)" = 1 ] && grep -qx first "$FAKE_DOCKER_STATE/mutation.log"; then
    pass "two colliding canonical contenders allow at most one guard-plus-mutation pass"
  else
    fail "concurrent collision did not preserve one guarded mutation"
  fi
}

test_atomic_lock_publication() {
  local initialization_grace

  for initialization_grace in 0 -1 999999999; do
    new_fixture "atomic-lock-publication-${initialization_grace#-}"
    local worktree="$CASE_ROOT/worktree"
    local env_file="$CASE_ROOT/worktree.env"
    local first_pid
    local second_pid
    local first_status=0
    local second_status=0
    mkdir -p "$worktree"
    write_env "$env_file" ignored "$worktree" ignored

    (
      export FAKE_DOCKER_ACTOR=first
      export FAKE_LN_PAUSE_BEFORE_PUBLICATION=1
      export MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=1
      run_guard "$worktree" "$env_file" docker compose up -d postgres > "$CASE_ROOT/first.out" 2>&1
    ) &
    first_pid=$!

    if ! wait_for_file "$FAKE_DOCKER_STATE/lock-publication.started"; then
      fail "first contender did not pause before atomic lock publication (grace $initialization_grace)"
      : > "$FAKE_DOCKER_STATE/release-lock-publication"
      wait "$first_pid" || true
      continue
    fi

    (
      export FAKE_DOCKER_ACTOR=second
      export FAKE_DOCKER_BLOCK_UP=1
      export MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5
      export MULTICA_COMPOSE_LOCK_INITIALIZATION_GRACE_SECONDS="$initialization_grace"
      run_guard "$worktree" "$env_file" docker compose up -d postgres > "$CASE_ROOT/second.out" 2>&1
    ) &
    second_pid=$!

    if ! wait_for_file "$FAKE_DOCKER_STATE/mutation.started"; then
      fail "second contender did not claim the complete atomic record (grace $initialization_grace)"
      : > "$FAKE_DOCKER_STATE/release-lock-publication"
      : > "$FAKE_DOCKER_STATE/release-up"
      wait "$first_pid" || true
      wait "$second_pid" || true
      continue
    fi

    : > "$FAKE_DOCKER_STATE/release-lock-publication"
    if wait "$first_pid"; then
      first_status=0
    else
      first_status=$?
    fi
    : > "$FAKE_DOCKER_STATE/release-up"
    if wait "$second_pid"; then
      second_status=0
    else
      second_status=$?
    fi

    if [ "$first_status" -ne 0 ] && [ "$second_status" = 0 ] && \
      [ "$(mutation_count)" = 1 ] && grep -qx second "$FAKE_DOCKER_STATE/mutation.log"; then
      pass "complete atomic publication ignores initialization-grace $initialization_grace"
    else
      fail "atomic publication allowed an initialization-grace race ($initialization_grace)"
    fi
  done
}

test_lock_root_override_serialization() {
  new_fixture lock-root-override-serialization
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local env_a="$CASE_ROOT/caller-a.env"
  local env_b="$CASE_ROOT/caller-b.env"
  local root_a="$CASE_ROOT/caller-root-a"
  local root_b="$CASE_ROOT/caller-root-b"
  local tmp_a="$CASE_ROOT/caller-tmp-a"
  local tmp_b="$CASE_ROOT/caller-tmp-b"
  local first_pid
  local first_status=0
  local second_status=0
  local caller_roots_unused=true
  mkdir -p "$worktree" "$root_a" "$root_b" "$tmp_a" "$tmp_b"
  write_env "$env_file" ignored "$worktree" ignored
  cp "$env_file" "$env_a"
  cp "$env_file" "$env_b"
  cat >> "$env_a" <<ENV
MULTICA_COMPOSE_LOCK_ROOT=$root_a
TMPDIR=$tmp_a
ENV
  cat >> "$env_b" <<ENV
MULTICA_COMPOSE_LOCK_ROOT=$root_b
TMPDIR=$tmp_b
ENV

  (
    export MULTICA_COMPOSE_LOCK_ROOT="$root_b"
    export TMPDIR="$tmp_b"
    export FAKE_DOCKER_ACTOR=first
    export FAKE_DOCKER_BLOCK_UP=1
    export MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5
    run_guard "$worktree" "$env_a" docker compose up -d postgres > "$CASE_ROOT/first.out" 2>&1
  ) &
  first_pid=$!

  if ! wait_for_file "$FAKE_DOCKER_STATE/mutation.started"; then
    fail "first caller-root contender did not reach the fake-Docker mutation seam"
    : > "$FAKE_DOCKER_STATE/release-up"
    wait "$first_pid" || true
    return
  fi

  if (
    export MULTICA_COMPOSE_LOCK_ROOT="$root_a"
    export TMPDIR="$tmp_a"
    export FAKE_DOCKER_ACTOR=second
    export MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=1
    run_guard "$worktree" "$env_b" docker compose up -d postgres > "$CASE_ROOT/second.out" 2>&1
  ); then
    second_status=0
  else
    second_status=$?
  fi

  : > "$FAKE_DOCKER_STATE/release-up"
  if wait "$first_pid"; then
    first_status=0
  else
    first_status=$?
  fi

  if find "$root_a" -maxdepth 1 -name 'multica-compose-lock-*' -print -quit | grep -q . || \
    find "$root_b" -maxdepth 1 -name 'multica-compose-lock-*' -print -quit | grep -q .; then
    caller_roots_unused=false
  fi

  if [ "$first_status" = 0 ] && [ "$second_status" -ne 0 ] && \
    [ "$(mutation_count)" = 1 ] && grep -qx first "$FAKE_DOCKER_STATE/mutation.log" && \
    [ "$caller_roots_unused" = true ] && [ "$(command -v docker)" = "$FAKE_BIN/docker" ]; then
    pass "caller lock-root and TMPDIR overrides cannot split the canonical fake-Docker lock"
  else
    fail "caller lock-root or TMPDIR override split the canonical lock namespace"
  fi
}

test_same_port_distinct_projects_serialize() {
  new_fixture same-port-distinct-projects-serialize
  local candidates="$CASE_ROOT/candidates"
  local seen_ports="$CASE_ROOT/seen-ports"
  local attempt=0
  local candidate
  local port_record
  local worktree_a=""
  local worktree_b=""
  local env_a="$CASE_ROOT/a.env"
  local env_b="$CASE_ROOT/b.env"
  local project_a
  local project_b
  local port_a
  local port_b
  local first_pid
  local second_pid
  local first_status=0
  local second_status=0
  mkdir -p "$candidates" "$seen_ports"

  # A file keyed by the modulo-1000 port is portable on Bash 3 and guarantees
  # a collision after at most 1001 distinct physical worktree paths.
  while [ "$attempt" -le 1000 ]; do
    candidate="$candidates/worktree-$attempt"
    mkdir -p "$candidate"
    derive_worktree_identity "$candidate"
    port_record="$seen_ports/$FIXTURE_PORT"
    if [ -f "$port_record" ]; then
      worktree_a="$(awk 'NR == 1 { print; exit }' "$port_record")"
      worktree_b="$candidate"
      break
    fi
    printf '%s\n' "$candidate" > "$port_record"
    attempt=$((attempt + 1))
  done

  if [ -z "$worktree_a" ] || [ -z "$worktree_b" ]; then
    fail "could not find two physical worktrees with one modulo-1000 PostgreSQL port"
    return
  fi

  write_env "$env_a" ignored "$worktree_a" ignored
  write_env "$env_b" ignored "$worktree_b" ignored
  project_a="$(env_value "$env_a" COMPOSE_PROJECT_NAME)"
  project_b="$(env_value "$env_b" COMPOSE_PROJECT_NAME)"
  port_a="$(env_value "$env_a" POSTGRES_PORT)"
  port_b="$(env_value "$env_b" POSTGRES_PORT)"

  if [ "$project_a" = "$project_b" ] || [ "$port_a" != "$port_b" ]; then
    fail "same-port fixture did not produce distinct projects on one canonical PostgreSQL port"
    return
  fi

  (
    export FAKE_DOCKER_ACTOR=first
    export FAKE_DOCKER_BLOCK_UP=1
    export MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5
    run_guard "$worktree_a" "$env_a" docker compose up -d postgres > "$CASE_ROOT/first.out" 2>&1
  ) &
  first_pid=$!

  if ! wait_for_file "$FAKE_DOCKER_STATE/mutation.started"; then
    fail "first same-port contender did not reach the fake-Docker mutation seam"
    : > "$FAKE_DOCKER_STATE/release-up"
    wait "$first_pid" || true
    return
  fi

  (
    export FAKE_DOCKER_ACTOR=second
    export MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5
    run_guard "$worktree_b" "$env_b" docker compose up -d postgres > "$CASE_ROOT/second.out" 2>&1
  ) &
  second_pid=$!

  # The first command creates the port binding before it releases the lock.
  # A port-keyed contender must then fail the foreign ownership preflight.
  sleep 1
  : > "$FAKE_DOCKER_STATE/release-up"

  if wait "$first_pid"; then
    first_status=0
  else
    first_status=$?
  fi
  if wait "$second_pid"; then
    second_status=0
  else
    second_status=$?
  fi

  if [ "$first_status" = 0 ] && [ "$second_status" -ne 0 ] && \
    [ "$(mutation_count)" = 1 ] && grep -qx first "$FAKE_DOCKER_STATE/mutation.log" && \
    grep -Fq 'belongs to a different owner, worktree, or project' "$CASE_ROOT/second.out"; then
    pass "distinct projects sharing one canonical PostgreSQL port serialize and reject the second preflight"
  else
    fail "same-port distinct projects allowed more than one Compose mutation"
  fi
}

test_same_basename_port_collision_has_distinct_resources() {
  new_fixture same-basename-port-collision-has-distinct-resources
  local candidates="$CASE_ROOT/candidates"
  local seen_ports="$CASE_ROOT/seen-ports"
  local attempt=0
  local candidate
  local port_record
  local worktree_a=""
  local worktree_b=""
  local env_a="$CASE_ROOT/a.env"
  local env_b="$CASE_ROOT/b.env"
  local project_a
  local project_b
  local database_a
  local database_b
  local port_a
  local port_b
  mkdir -p "$candidates" "$seen_ports"

  # Keep the basename fixed so this fixture distinguishes resource identity
  # from the deliberately bounded port allocation. A modulo-1000 collision is
  # guaranteed after at most 1001 physical paths.
  while [ "$attempt" -le 1000 ]; do
    candidate="$candidates/parent-$attempt/worktree"
    mkdir -p "$candidate"
    derive_worktree_identity "$candidate"
    port_record="$seen_ports/$FIXTURE_PORT"
    if [ -f "$port_record" ]; then
      worktree_a="$(awk 'NR == 1 { print; exit }' "$port_record")"
      worktree_b="$candidate"
      break
    fi
    printf '%s\n' "$candidate" > "$port_record"
    attempt=$((attempt + 1))
  done

  if [ -z "$worktree_a" ] || [ -z "$worktree_b" ]; then
    fail "could not find two same-basename worktrees with one bounded PostgreSQL port"
    return
  fi

  : > "$worktree_a/docker-compose.yml"
  : > "$worktree_b/docker-compose.yml"
  (
    cd "$worktree_a"
    FORCE=1 bash "$REPO_ROOT/scripts/init-worktree-env.sh" "$env_a" > "$CASE_ROOT/init-a.out"
  )
  (
    cd "$worktree_b"
    FORCE=1 bash "$REPO_ROOT/scripts/init-worktree-env.sh" "$env_b" > "$CASE_ROOT/init-b.out"
  )

  project_a="$(env_value "$env_a" COMPOSE_PROJECT_NAME)"
  project_b="$(env_value "$env_b" COMPOSE_PROJECT_NAME)"
  database_a="$(env_value "$env_a" POSTGRES_DB)"
  database_b="$(env_value "$env_b" POSTGRES_DB)"
  port_a="$(env_value "$env_a" POSTGRES_PORT)"
  port_b="$(env_value "$env_b" POSTGRES_PORT)"

  if [ "$(basename "$worktree_a")" = "$(basename "$worktree_b")" ] && \
    [ "$port_a" = "$port_b" ] && [ "$project_a" != "$project_b" ] && \
    [ "$database_a" != "$database_b" ] && [ "$project_a" = "$database_a" ] && \
    [ "$project_b" = "$database_b" ] && \
    run_guard "$worktree_a" "$env_a" docker compose up -d postgres > "$CASE_ROOT/guard-a.out" 2>&1 && \
    [ "$(mutation_count)" = 1 ] && \
    [ -d "$FAKE_DOCKER_STATE/containers/${project_a}-postgres-1" ] && \
    [ -d "$FAKE_DOCKER_STATE/volumes/${project_a}_pgdata" ]; then
    pass "same-basename worktrees sharing a port use distinct Compose and database identities"
  else
    fail "same-basename port collision shared a Compose project or database identity"
  fi
}

test_post_mutation_boundary() {
  new_fixture post-mutation-boundary
  local project database
  local worktree_a="$CASE_ROOT/worktree"
  local worktree_b="$CASE_ROOT/worktree-alias"
  local env_a="$CASE_ROOT/a.env"
  local env_b="$CASE_ROOT/b.env"
  mkdir -p "$worktree_a"
  ln -s "$worktree_a" "$worktree_b"
  write_env "$env_a" ignored "$worktree_a" ignored
  write_env "$env_b" ignored "$worktree_b" ignored
  project="$(env_value "$env_a" COMPOSE_PROJECT_NAME)"
  database="$(env_value "$env_a" POSTGRES_DB)"

  if [ "$project" != "$(env_value "$env_b" COMPOSE_PROJECT_NAME)" ]; then
    fail "canonical aliases did not derive one post-mutation lock key"
    return
  fi

  FAKE_DOCKER_ACTOR=first FAKE_DOCKER_BLOCK_POST=1 FAKE_DOCKER_SKIP_RESOURCE_CREATE=1 \
    MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5 run_ensure "$worktree_a" "$env_a" --reset-database \
    > "$CASE_ROOT/first.out" 2>&1 &
  local first_pid=$!

  local started=false
  for _ in $(seq 1 100); do
    if [ -f "$FAKE_DOCKER_STATE/post-mutation.started" ]; then
      started=true
      break
    fi
    sleep 0.05
  done

  if [ "$started" != true ]; then
    fail "ensure-postgres did not reach its post-readiness mutation seam"
    : > "$FAKE_DOCKER_STATE/release-post-mutation"
    wait "$first_pid" || true
    return
  fi

  local second_status=0
  if FAKE_DOCKER_ACTOR=second MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=1 \
    run_guard "$worktree_b" "$env_b" \
    docker compose up -d postgres > "$CASE_ROOT/second.out" 2>&1; then
    second_status=0
  else
    second_status=$?
  fi

  : > "$FAKE_DOCKER_STATE/release-post-mutation"
  local first_status=0
  if wait "$first_pid"; then
    first_status=0
  else
    first_status=$?
  fi

  if [ "$first_status" = 0 ] && [ "$second_status" -ne 0 ] && \
    [ "$(mutation_count)" = 1 ] && [ "$(cat "$FAKE_DOCKER_STATE/post-mutation")" = "$project" ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/post-mutation-service")" = postgres ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/post-mutation-user")" = fixture_user ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/post-mutation-database")" = postgres ] && \
    grep -Fq "DROP DATABASE IF EXISTS \"$database\" WITH (FORCE);" "$FAKE_DOCKER_STATE/post-mutation-argv" && \
    grep -Fq "CREATE DATABASE \"$database\";" "$FAKE_DOCKER_STATE/post-mutation-argv"; then
    pass "db-reset builds the exact canonical service, user, and database mutation inside the lock"
  else
    fail "post-readiness mutation released the ownership lock too early"
  fi
}

test_post_mutation_scope_refusal() {
  new_fixture post-mutation-scope-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"

  if run_ensure "$worktree" "$env_file" -- \
    docker compose --project-name "$project" exec -T foreign_service psql -U foreign_user -d postgres \
    -c 'DROP DATABASE IF EXISTS "foreign_db" WITH (FORCE);' > "$CASE_ROOT/foreign-post-mutation.out" 2>&1; then
    fail "arbitrary post-readiness command was accepted"
  elif [ ! -e "$FAKE_DOCKER_STATE/post-mutation.started" ] && \
    [ ! -e "$FAKE_DOCKER_STATE/post-mutation" ]; then
    pass "post-readiness mutation rejects arbitrary service, user, and database targets"
  else
    fail "arbitrary post-readiness command reached the Docker mutation seam"
  fi
}

test_stale_lock_quarantine() {
  new_fixture stale-lock-quarantine
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project
  local port
  local lock_path
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  port="$(env_value "$env_file" POSTGRES_PORT)"
  lock_path="$(lock_path_for_env "$worktree" "$env_file")"
  cat > "$lock_path" <<META
pid=999999
project=$project
owner=worktree
worktree_path=$(env_value "$env_file" WORKTREE_PATH)
pid_start=unavailable
port=$port
key=postgres-port-$port
started_epoch=1
META

  if run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/stale.out" 2>&1 && \
    find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").stale.*" -print -quit | grep -q .; then
    pass "stale lock is quarantined by rename before a bounded recovery"
  else
    fail "stale lock was not safely quarantined and recovered"
  fi
}

test_first_initialization_and_readiness() {
  new_fixture first-initialization-and-readiness
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local database
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  database="$(env_value "$env_file" POSTGRES_DB)"

  if ! grep -Fq 'POSTGRES_DB: "${POSTGRES_DB:-multica}"' "$REPO_ROOT/docker-compose.yml"; then
    fail "docker-compose.yml does not parameterize POSTGRES_DB for first initialization"
    return
  fi
  if ! grep -Fq 'multica.owner: "${MULTICA_OWNER:-deployment}"' "$REPO_ROOT/docker-compose.yml" || \
    ! grep -Fq 'multica.worktree.path: "${WORKTREE_PATH:-}"' "$REPO_ROOT/docker-compose.yml" || \
    ! grep -Fq 'multica.worktree.project: "${COMPOSE_PROJECT_NAME:-multica}"' "$REPO_ROOT/docker-compose.yml"; then
    fail "docker-compose.yml does not declare exact worktree volume ownership labels"
    return
  fi

  if run_ensure "$worktree" "$env_file" > "$CASE_ROOT/ensure.out" 2>&1 && \
    [ "$(cat "$FAKE_DOCKER_STATE/initialized-db")" = "$database" ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/authenticated-db")" = "$database" ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/authenticated-user")" = fixture_user ] && \
    ! grep -Fq fixture-password-not-for-output "$CASE_ROOT/ensure.out"; then
    pass "configured database is first-initialized and used by secret-safe authenticated SELECT 1"
  else
    fail "configured database initialization or authenticated readiness proof failed"
  fi
}

write_remote_env() {
  local env_file="$1"
  cat > "$env_file" <<'ENV'
POSTGRES_DB=env_db
POSTGRES_USER=env_user
POSTGRES_PASSWORD=env_password
POSTGRES_PORT=5432
DATABASE_URL=postgres://url_user:url_password@db.example.test:6543/url_db?sslmode=require
ENV
}

assert_secret_free_remote_client_evidence() {
  local file
  for file in \
    "$FAKE_DOCKER_STATE/remote-pg-isready-argv" \
    "$FAKE_DOCKER_STATE/remote-psql-argv" \
    "$CASE_ROOT/ensure.out"; do
    if grep -Fq 'url_password' "$file" || grep -Fq 'postgres://url_user:' "$file"; then
      return 1
    fi
  done
  return 0
}

test_remote_database_url_authority() {
  new_fixture remote-database-url-authority
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/remote.env"
  mkdir -p "$worktree"
  write_remote_env "$env_file"

  if PGHOST=foreign.example.test PGPORT=4444 PGUSER=foreign_user \
    PGSSLMODE=disable PGPASSWORD=foreign_password \
    run_ensure "$worktree" "$env_file" > "$CASE_ROOT/ensure.out" 2>&1 && \
    grep -Fx -- '-t' "$FAKE_DOCKER_STATE/remote-pg-isready-argv" > /dev/null && \
    grep -Fx -- '1' "$FAKE_DOCKER_STATE/remote-pg-isready-argv" > /dev/null && \
    grep -Fx -- 'PGDATABASE=set' "$FAKE_DOCKER_STATE/remote-pg-isready-libpq-env" > /dev/null && \
    grep -Fx -- 'PGPASSWORD=unset' "$FAKE_DOCKER_STATE/remote-pg-isready-libpq-env" > /dev/null && \
    grep -Fx -- 'PGHOST=unset' "$FAKE_DOCKER_STATE/remote-pg-isready-libpq-env" > /dev/null && \
    grep -Fx -- 'PGPORT=unset' "$FAKE_DOCKER_STATE/remote-pg-isready-libpq-env" > /dev/null && \
    grep -Fx -- 'PGUSER=unset' "$FAKE_DOCKER_STATE/remote-pg-isready-libpq-env" > /dev/null && \
    grep -Fx -- 'PGSSLMODE=unset' "$FAKE_DOCKER_STATE/remote-pg-isready-libpq-env" > /dev/null && \
    grep -Fx -- 'POSTGRES_PASSWORD=unset' "$FAKE_DOCKER_STATE/remote-psql-libpq-env" > /dev/null && \
    grep -Fx -- 'PGCONNECT_TIMEOUT=set' "$FAKE_DOCKER_STATE/remote-psql-libpq-env" > /dev/null && \
    grep -Fx -- 'PGDATABASE=set' "$FAKE_DOCKER_STATE/remote-psql-libpq-env" > /dev/null && \
    grep -Fx -- 'PGPASSWORD=unset' "$FAKE_DOCKER_STATE/remote-psql-libpq-env" > /dev/null && \
    ! grep -Ex -- '-d|-h|-p|-U|env_user|env_db|foreign_user' "$FAKE_DOCKER_STATE/remote-psql-argv" > /dev/null && \
    assert_secret_free_remote_client_evidence; then
    pass "remote readiness keeps DATABASE_URL out of argv and ignores ambient libpq authority"
  else
    fail "remote readiness leaked DATABASE_URL or mixed it with ambient PostgreSQL authority"
  fi
}

test_remote_readiness_timeout() {
  new_fixture remote-readiness-timeout
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/remote.env"
  mkdir -p "$worktree"
  write_remote_env "$env_file"

  : > "$FAKE_DOCKER_STATE/remote-pg-isready-fail"
  if MULTICA_POSTGRES_READY_TIMEOUT_SECONDS=1 \
    run_ensure "$worktree" "$env_file" > "$CASE_ROOT/timeout.out" 2>&1; then
    fail "remote PostgreSQL readiness loop accepted repeated failures"
  elif [ "$(wc -l < "$FAKE_DOCKER_STATE/remote-pg-isready-calls" | tr -d ' ')" -ge 2 ] && \
    grep -Fq 'Remote PostgreSQL did not become ready within 1 seconds' "$CASE_ROOT/timeout.out" && \
    [ ! -e "$FAKE_DOCKER_STATE/mutation.log" ]; then
    pass "remote PostgreSQL readiness fails at the shared bounded deadline"
  else
    fail "remote PostgreSQL readiness failure was not bounded or attempted a Docker mutation"
  fi
}

test_remote_missing_psql_fails_closed() {
  new_fixture remote-missing-psql
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/remote.env"
  mkdir -p "$worktree"
  write_remote_env "$env_file"

  if run_ensure_without_psql "$worktree" "$env_file" > "$CASE_ROOT/missing-psql.out" 2>&1; then
    fail "remote PostgreSQL readiness accepted a missing authenticated client"
  elif grep -Fq 'psql is required to verify authenticated remote PostgreSQL readiness' "$CASE_ROOT/missing-psql.out" && \
    ! grep -Fq '✓ PostgreSQL ready' "$CASE_ROOT/missing-psql.out" && \
    [ ! -e "$FAKE_DOCKER_STATE/remote-pg-isready-calls" ]; then
    pass "missing psql fails closed without claiming remote readiness"
  else
    fail "missing psql did not produce a non-ready authenticated result"
  fi
}

test_invalid_shared_readiness_timeout() {
  new_fixture invalid-shared-readiness-timeout
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/remote.env"
  mkdir -p "$worktree"
  write_remote_env "$env_file"

  if MULTICA_POSTGRES_READY_TIMEOUT_SECONDS=0 \
    run_ensure "$worktree" "$env_file" > "$CASE_ROOT/invalid-timeout.out" 2>&1; then
    fail "invalid shared readiness timeout was accepted for a remote database"
  elif grep -Fq 'MULTICA_POSTGRES_READY_TIMEOUT_SECONDS must be an integer from 1 through 300' "$CASE_ROOT/invalid-timeout.out" && \
    [ ! -e "$FAKE_DOCKER_STATE/remote-pg-isready-calls" ]; then
    pass "shared readiness timeout validation rejects out-of-range remote values"
  else
    fail "shared readiness timeout validation ran remote checks before refusing invalid input"
  fi
}

test_readiness_timeout() {
  new_fixture readiness-timeout
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local lock_path
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  lock_path="$(lock_path_for_env "$worktree" "$env_file")"

  if FAKE_DOCKER_PG_ISREADY_FAIL=1 MULTICA_POSTGRES_READY_TIMEOUT_SECONDS=1 \
    run_ensure "$worktree" "$env_file" > "$CASE_ROOT/timeout.out" 2>&1; then
    fail "unready PostgreSQL was accepted indefinitely"
  elif [ "$(mutation_count)" = 1 ] && \
    grep -Fq 'did not become ready within 1 seconds' "$CASE_ROOT/timeout.out" && \
    find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print -quit | grep -q .; then
    pass "PostgreSQL readiness has a bounded failure path that releases the lock"
  else
    fail "PostgreSQL readiness timeout did not preserve bounded lock cleanup"
  fi
}

test_idempotency_and_production_identity() {
  new_fixture idempotency-and-production-identity
  local project
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  fixture_container multica-postgres-1 running multica deployment /deploy/live multica 5432
  printf 'production-fixed-id\n' > "$FAKE_DOCKER_STATE/containers/multica-postgres-1/id"
  printf 'production-start-time\n' > "$FAKE_DOCKER_STATE/containers/multica-postgres-1/started"
  cp -R "$FAKE_DOCKER_STATE/containers/multica-postgres-1" "$CASE_ROOT/production-before"

  if run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/first.out" 2>&1 && \
    run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/second.out" 2>&1 && \
    cmp -s "$CASE_ROOT/production-before/id" "$FAKE_DOCKER_STATE/containers/multica-postgres-1/id" && \
    cmp -s "$CASE_ROOT/production-before/started" "$FAKE_DOCKER_STATE/containers/multica-postgres-1/started" && \
    cmp -s "$CASE_ROOT/production-before/labels" "$FAKE_DOCKER_STATE/containers/multica-postgres-1/labels"; then
    pass "idempotent worktree mutation preserves production identity fixture"
  else
    fail "idempotency or production-identity preservation failed"
  fi
}

test_current_checkout_refusal() {
  new_fixture current-checkout-refusal
  local current_worktree="$CASE_ROOT/current-worktree"
  local foreign_worktree="$CASE_ROOT/foreign-worktree"
  local foreign_env="$CASE_ROOT/foreign.env"
  local foreign_project
  mkdir -p "$current_worktree" "$foreign_worktree"
  write_env "$foreign_env" ignored "$foreign_worktree" ignored
  foreign_project="$(env_value "$foreign_env" COMPOSE_PROJECT_NAME)"

  if run_guard "$current_worktree" "$foreign_env" \
    docker compose up -d postgres > "$CASE_ROOT/foreign-path.out" 2>&1; then
    fail "foreign WORKTREE_PATH was accepted from a different current checkout"
  elif [ "$(mutation_count)" = 0 ]; then
    pass "current physical checkout refuses a foreign worktree environment before mutation"
  else
    fail "foreign WORKTREE_PATH reached a Compose mutation"
  fi
}

test_identity_field_refusal() {
  new_fixture identity-field-refusal
  local worktree="$CASE_ROOT/worktree"
  local canonical_env="$CASE_ROOT/canonical.env"
  local tampered_env command_project
  local project
  local all_refused=true
  mkdir -p "$worktree"
  write_env "$canonical_env" ignored "$worktree" ignored
  project="$(env_value "$canonical_env" COMPOSE_PROJECT_NAME)"

  for field in project port database; do
    tampered_env="$CASE_ROOT/$field.env"
    cp "$canonical_env" "$tampered_env"
    command_project="$project"
    case "$field" in
      project)
        command_project=wt_tampered_target
        printf 'COMPOSE_PROJECT_NAME=%s\n' "$command_project" >> "$tampered_env"
        ;;
      port)
        printf 'POSTGRES_PORT=59999\n' >> "$tampered_env"
        ;;
      database)
        printf 'POSTGRES_DB=wt_tampered_database\n' >> "$tampered_env"
        ;;
    esac

    if run_guard "$worktree" "$tampered_env" \
      docker compose up -d postgres > "$CASE_ROOT/$field.out" 2>&1; then
      all_refused=false
    fi
  done

  if [ "$all_refused" = true ] && [ "$(mutation_count)" = 0 ]; then
    pass "project, port, and database must match the current canonical worktree identity"
  else
    fail "a tampered canonical identity field reached a Compose mutation"
  fi
}

test_mutation_target_refusal() {
  new_fixture mutation-target-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local guard_refused=false
  local post_mutation_refused=false
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored

  if ! run_guard "$worktree" "$env_file" \
    docker compose --project-name multica down > "$CASE_ROOT/mismatched-target.out" 2>&1; then
    guard_refused=true
  fi
  if ! run_ensure "$worktree" "$env_file" -- \
    docker compose --project-name multica exec -T postgres psql -U fixture_user -d postgres \
    -c 'DROP DATABASE IF EXISTS "fixture";' > "$CASE_ROOT/mismatched-post-target.out" 2>&1; then
    post_mutation_refused=true
  fi

  if [ "$guard_refused" = true ] && [ "$post_mutation_refused" = true ] && [ "$(mutation_count)" = 0 ]; then
    pass "guarded and post-readiness Compose mutations must use the canonical project target"
  else
    fail "a mismatched Compose target reached a mutation"
  fi
}

test_nonowner_lock_release_refusal() {
  new_fixture nonowner-lock-release-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project
  local port
  local lock_path
  local key_mismatch_lock="$CASE_ROOT/key-mismatch-lock"
  local release_records_before
  local release_records_after_nonowner
  local release_records_after
  local nonowner_refused=false
  local key_mismatch_refused=false
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  project="$(env_value "$env_file" COMPOSE_PROJECT_NAME)"
  port="$(env_value "$env_file" POSTGRES_PORT)"
  lock_path="$(lock_path_for_env "$worktree" "$env_file")"
  release_records_before="$(find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print | wc -l | tr -d ' ')"
  cat > "$lock_path" <<META
pid=999999
pid_start=foreign-process-start
project=foreign_project
owner=deployment
worktree_path=/foreign/worktree
port=$port
key=postgres-port-$port
started_epoch=1
META

  if (
    cd "$worktree"
    set -a
    . "$env_file"
    set +a
    export COMPOSE_OWNERSHIP_LOCK_PATH="$lock_path"
    . "$REPO_ROOT/scripts/compose-ownership-guard.sh"
    compose_lock_release
  ) > "$CASE_ROOT/nonowner-release.out" 2>&1; then
    nonowner_refused=false
  else
    release_records_after_nonowner="$(find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print | wc -l | tr -d ' ')"
    if [ -f "$lock_path" ] && [ "$release_records_before" = "$release_records_after_nonowner" ]; then
      nonowner_refused=true
    fi
  fi

  # Keep every owner fact exact except the port-derived key. The release matcher
  # must refuse this record even though PID/process-start and identity match.
  if (
    cd "$worktree"
    set -a
    . "$env_file"
    set +a
    . "$REPO_ROOT/scripts/compose-ownership-guard.sh"
    compose_prepare_identity
    owner_pid="${BASHPID:-$$}"
    owner_start="$(ps -o lstart= -p "$owner_pid" 2>/dev/null | tr -s ' ' | sed 's/^ //; s/ $//')"
    cat > "$key_mismatch_lock" <<META
pid=$owner_pid
pid_start=$owner_start
project=$COMPOSE_PROJECT_NAME
owner=$MULTICA_OWNER
worktree_path=$WORKTREE_PATH
port=$POSTGRES_PORT
key=foreign-port-key
started_epoch=1
META
    compose_lock_owner_matches_current_process "$key_mismatch_lock"
  ) > "$CASE_ROOT/key-mismatch-release.out" 2>&1; then
    key_mismatch_refused=false
  else
    key_mismatch_refused=true
  fi

  release_records_after="$(find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print | wc -l | tr -d ' ')"
  if [ "$nonowner_refused" = true ] && [ "$key_mismatch_refused" = true ] && [ "$release_records_before" = "$release_records_after" ]; then
    pass "only the recorded owner and its port-derived lock key can release a Compose lock"
  else
    fail "non-owner or mismatched lock-key release did not remain fail-closed"
  fi
}

test_release_failure_propagation() {
  new_fixture release-failure-propagation
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local lock_path
  local release_records_before
  local release_records_after
  mkdir -p "$worktree"
  write_env "$env_file" ignored "$worktree" ignored
  lock_path="$(lock_path_for_env "$worktree" "$env_file")"
  release_records_before="$(find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print | wc -l | tr -d ' ')"

  if FAKE_MV_FAIL_RELEASE=1 run_guard "$worktree" "$env_file" \
    docker compose up -d postgres > "$CASE_ROOT/release-failure.out" 2>&1; then
    fail "lock release failure was hidden after a Compose mutation"
  else
    release_records_after="$(find "$(dirname "$lock_path")" -maxdepth 1 -type f -name "$(basename "$lock_path").released.*" -print | wc -l | tr -d ' ')"
    if [ "$(mutation_count)" = 1 ] && [ -f "$lock_path" ] && \
      [ "$release_records_before" = "$release_records_after" ]; then
      pass "lock release failure is returned and leaves the lock fail-closed"
    else
      fail "lock release failure did not preserve fail-closed evidence"
    fi
  fi
}

STATIC_SCAN_MESSAGE=""

static_scan_no_match() {
  local output="$1"
  shift

  local status
  if rg -n "$@" > "$output" 2>&1; then
    STATIC_SCAN_MESSAGE="static scan found a forbidden match: $*"
    return 1
  else
    status=$?
  fi

  if [ "$status" -eq 1 ]; then
    STATIC_SCAN_MESSAGE=""
    return 0
  fi

  STATIC_SCAN_MESSAGE="static scan error (rg exit $status): $*"
  return 1
}

test_static_safety_contract() {
  if ! static_scan_no_match "$TEST_ROOT/caller-lock-root.out" 'MULTICA_COMPOSE_LOCK_ROOT|TMPDIR|MULTICA_COMPOSE_LOCK_INITIALIZATION_GRACE_SECONDS|owner\.meta' "$REPO_ROOT/scripts/compose-ownership-guard.sh"; then
    fail "production Compose locking still exposes a caller-selected root, grace, or directory-owner model: $STATIC_SCAN_MESSAGE"
    return
  fi
  if ! static_scan_no_match "$TEST_ROOT/stale-lock-root-doc.out" '\$\{TMPDIR' "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md" || \
    ! grep -Fq '/tmp/multica-compose-locks/multica-compose-lock-port-<port>' "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md"; then
    fail "recovery runbook does not document the fixed port-keyed lock namespace: ${STATIC_SCAN_MESSAGE:-required authority is absent}"
    return
  fi
  if ! static_scan_no_match "$TEST_ROOT/invalid-runbook-formatter.out" '\.Labels[[:space:]]+"' "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md" || \
    ! grep -Fq '{{.Label "multica.owner"}}' "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md"; then
    fail "recovery runbook does not use Docker's runnable single-label formatter: ${STATIC_SCAN_MESSAGE:-required formatter is absent}"
    return
  fi
  if ! static_scan_no_match "$TEST_ROOT/delete-command.out" '(^|[[:space:]])rm([[:space:]]|$)' \
    "$REPO_ROOT/scripts/compose-ownership-guard.sh" \
    "$REPO_ROOT/scripts/compose-guard-mustpass.sh" \
    "$REPO_ROOT/scripts/ensure-postgres.sh" \
    "$REPO_ROOT/scripts/test-compose-isolation.sh"; then
    fail "Compose isolation scripts contain a permanent deletion command or could not be scanned: $STATIC_SCAN_MESSAGE"
    return
  fi
  if ! static_scan_no_match "$TEST_ROOT/unsafe-runbook.out" 'docker rm|git checkout' "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md"; then
    fail "recovery runbook contains an unsafe direct deletion or checkout instruction: $STATIC_SCAN_MESSAGE"
    return
  fi
  if ! static_scan_no_match "$TEST_ROOT/stale-identity.out" 'multica_<slug>_<hash>|rm[[:space:]]+-rf' "$REPO_ROOT/CONTRIBUTING.md" || \
    ! static_scan_no_match "$TEST_ROOT/unsafe-contributor-reset.out" 'docker compose.*down[[:space:]]+-v' \
      "$REPO_ROOT/CONTRIBUTING.md" \
      "$REPO_ROOT/apps/docs/content/docs/developers/contributing.zh.mdx"; then
    fail "contributor docs retain stale identity, permanent deletion, or an unguarded volume-deletion path: $STATIC_SCAN_MESSAGE"
    return
  fi
  if ! static_scan_no_match "$TEST_ROOT/credential-mutation.out" 'ALTER[[:space:]]+USER' \
    "$REPO_ROOT/docker-compose.yml" \
    "$REPO_ROOT/scripts/compose-ownership-guard.sh" \
    "$REPO_ROOT/scripts/compose-guard-mustpass.sh" \
    "$REPO_ROOT/scripts/ensure-postgres.sh"; then
    fail "isolation implementation contains a credential mutation or could not be scanned: $STATIC_SCAN_MESSAGE"
    return
  fi
  local document
  for document in \
    "$REPO_ROOT/CLAUDE.md" \
    "$REPO_ROOT/CONTRIBUTING.md" \
    "$REPO_ROOT/apps/docs/content/docs/developers/contributing.zh.mdx" \
    "$REPO_ROOT/.env.example" \
    "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md"; do
    if ! grep -Fq '/tmp/multica-compose-locks/multica-compose-lock-port-<port>' "$document"; then
      fail "isolation documentation lacks the fixed port-keyed lock authority: $document"
      return
    fi
  done
  if ! static_scan_no_match "$TEST_ROOT/stale-port-contract.out" 'unique port|without conflict|rm[[:space:]]+-rf' \
    "$REPO_ROOT/CLAUDE.md" \
    "$REPO_ROOT/CONTRIBUTING.md" \
    "$REPO_ROOT/apps/docs/content/docs/developers/contributing.zh.mdx" \
    "$REPO_ROOT/.env.example"; then
    fail "isolation documentation retains an invalid port or permanent-deletion contract: $STATIC_SCAN_MESSAGE"
    return
  fi
  if static_scan_no_match "$TEST_ROOT/static-scan-error.out" 'never-matches' "$TEST_ROOT/does-not-exist"; then
    fail "static scan accepted a missing target"
    return
  elif [[ "$STATIC_SCAN_MESSAGE" != static\ scan\ error* ]]; then
    fail "static scan did not distinguish a scan error from a clean absence"
    return
  fi
  pass "isolation static scans distinguish matches, clean absence, and scan errors"
}

echo "=== Deterministic fake-Docker worktree Compose isolation tests ==="
echo "Artifacts retained at: $TEST_ROOT"
run_case canonical-identity test_canonical_identity
run_case linked-worktree-requires-owner test_linked_worktree_requires_owner
run_case wrapper-mutation test_wrapper_mutation
run_case canonical-compose-target test_canonical_compose_target
run_case compose-selector-refusal test_compose_selector_refusal
run_case make-override-refusal test_make_override_refusal
run_case current-checkout-refusal test_current_checkout_refusal
run_case identity-field-refusal test_identity_field_refusal
run_case mutation-target-refusal test_mutation_target_refusal
run_case deployment-identity-release test_deployment_identity_release
run_case stopped-foreign-refusal test_stopped_foreign_refusal
run_case strict-volume-labels test_strict_volume_labels
run_case path-alias-refusal test_path_alias_refusal
run_case port-refusal test_port_refusal
run_case concurrent-collision test_concurrent_collision
run_case atomic-lock-publication test_atomic_lock_publication
run_case lock-root-override-serialization test_lock_root_override_serialization
run_case same-port-distinct-projects-serialize test_same_port_distinct_projects_serialize
run_case same-basename-port-collision-has-distinct-resources test_same_basename_port_collision_has_distinct_resources
run_case post-mutation-boundary test_post_mutation_boundary
run_case post-mutation-scope-refusal test_post_mutation_scope_refusal
run_case stale-lock-quarantine test_stale_lock_quarantine
run_case nonowner-lock-release-refusal test_nonowner_lock_release_refusal
run_case release-failure-propagation test_release_failure_propagation
run_case first-initialization-and-readiness test_first_initialization_and_readiness
run_case remote-database-url-authority test_remote_database_url_authority
run_case remote-readiness-timeout test_remote_readiness_timeout
run_case remote-missing-psql test_remote_missing_psql_fails_closed
run_case invalid-shared-readiness-timeout test_invalid_shared_readiness_timeout
run_case readiness-timeout test_readiness_timeout
run_case idempotency-and-production-identity test_idempotency_and_production_identity
run_case static-safety-contract test_static_safety_contract

echo "=== Results: $PASS passed, $FAIL failed ==="
exit "$FAIL"
