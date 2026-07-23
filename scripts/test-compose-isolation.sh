#!/usr/bin/env bash
# Regression tests for worktree Compose isolation.
#
# Public seams under test:
#   1. compose-guard-mustpass.sh <env> -- <docker-compose-mutation>
#   2. ensure-postgres.sh <env> [-- <post-readiness-mutation>]
#
# The suite uses a deterministic fake Docker binary only. It never contacts
# the host daemon and intentionally retains its artifacts for inspection.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
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
    project="${COMPOSE_PROJECT_NAME:-multica}"
    action=""
    for ((index = 0; index < ${#arguments[@]}; index++)); do
      case "${arguments[$index]}" in
        --project-name)
          index=$((index + 1))
          project="${arguments[$index]:-}"
          ;;
        -f)
          index=$((index + 1))
          ;;
        up|down|exec)
          action="${arguments[$index]}"
          break
          ;;
      esac
    done

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
        if [[ "$argument_text" == *'SELECT 1 FROM pg_database'* ]]; then
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

cat > "$FAKE_BIN/git" <<'FAKE_GIT'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = rev-parse ]; then
  case "${2:-}" in
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
  MULTICA_COMPOSE_LOCK_ROOT="$CASE_ROOT/locks"
  export CASE_ROOT FAKE_DOCKER_STATE MULTICA_COMPOSE_LOCK_ROOT
  mkdir -p "$FAKE_DOCKER_STATE/containers" "$FAKE_DOCKER_STATE/volumes" "$MULTICA_COMPOSE_LOCK_ROOT"
}

write_env() {
  local env_file="$1"
  local project="$2"
  local path="$3"
  local port="$4"
  cat > "$env_file" <<ENV
COMPOSE_PROJECT_NAME=$project
MULTICA_OWNER=worktree
WORKTREE_PATH=$path
POSTGRES_DB=${project}_db
POSTGRES_USER=fixture_user
POSTGRES_PASSWORD=fixture-password-not-for-output
POSTGRES_PORT=$port
DATABASE_URL=postgres://fixture_user:fixture-password-not-for-output@localhost:$port/${project}_db?sslmode=disable
ENV
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
  local project="wt_missing_owner"
  mkdir -p "$worktree"
  cat > "$env_file" <<ENV
COMPOSE_PROJECT_NAME=$project
WORKTREE_PATH=$worktree
POSTGRES_DB=${project}_db
POSTGRES_USER=fixture_user
POSTGRES_PASSWORD=fixture-password-not-for-output
POSTGRES_PORT=56000
DATABASE_URL=postgres://fixture_user:fixture-password-not-for-output@localhost:56000/${project}_db?sslmode=disable
ENV

  local missing_refused=false
  if ! FAKE_GIT_LINKED_WORKTREE=1 bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/missing-owner.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    missing_refused=true
  fi

  printf 'MULTICA_OWNER=deployment\n' >> "$env_file"
  local deployment_refused=false
  if ! FAKE_GIT_LINKED_WORKTREE=1 bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/deployment-owner.out" 2>&1 && \
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
  local project="wt_wrapper_boundary"
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56001

  if bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/wrapper.out" 2>&1 && \
    [ "$(mutation_count)" = 1 ]; then
    pass "canonical wrapper keeps guard and requested mutation in one boundary"
  else
    fail "canonical wrapper did not execute exactly one guarded mutation"
  fi
}

test_stopped_foreign_refusal() {
  new_fixture stopped-foreign-refusal
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project="wt_stopped_foreign"
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56002
  fixture_container "foreign-stopped-postgres" exited "$project" deployment /deploy/live multica 5432

  if docker ps --filter "label=com.docker.compose.project=$project" --format '{{.Names}}' | grep -q foreign-stopped-postgres; then
    fail "stopped fixture leaked into running-container listing"
  elif docker ps -a --filter "label=com.docker.compose.project=$project" --format '{{.Names}}' | grep -q foreign-stopped-postgres; then
    if bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
      docker compose --project-name "$project" down > "$CASE_ROOT/stopped.out" 2>&1; then
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
  local project="wt_strict_volume"
  local canonical_worktree
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56003
  fixture_volume "${project}_pgdata" "$project" - - -

  local labeled_refused=false
  if ! bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/volume.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    labeled_refused=true
  fi

  new_fixture strict-unlabeled-volume
  worktree="$CASE_ROOT/worktree"
  env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree" "$FAKE_DOCKER_STATE/volumes/${project}_pgdata"
  write_env "$env_file" "$project" "$worktree" 56003
  : > "$FAKE_DOCKER_STATE/volumes/${project}_pgdata/labels"

  local unlabeled_refused=false
  if ! bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/unlabeled-volume.out" 2>&1 && \
    [ "$(mutation_count)" = 0 ]; then
    unlabeled_refused=true
  fi

  new_fixture strict-foreign-volume
  worktree="$CASE_ROOT/worktree"
  env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree" "$FAKE_DOCKER_STATE/volumes/${project}_pgdata"
  write_env "$env_file" "$project" "$worktree" 56003
  canonical_worktree="$(cd "$worktree" && pwd -P)"
  cat > "$FAKE_DOCKER_STATE/volumes/${project}_pgdata/labels" <<LABELS
com.docker.compose.project=foreign_project
multica.owner=worktree
multica.worktree.path=$canonical_worktree
multica.worktree.project=$project
LABELS

  local foreign_refused=false
  if ! bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/foreign-volume.out" 2>&1 && \
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
  local project="wt_path_alias"
  mkdir -p "$physical"
  ln -s "$physical" "$alias"
  write_env "$env_file" "$project" "$alias" 56004
  fixture_container "${project}-postgres-1" running "$project" worktree "$alias" "$project" 56004

  if bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/alias.out" 2>&1; then
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
  local project="wt_port_refusal"
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56005
  fixture_container foreign-port-owner running foreign_project deployment /deploy/live foreign_project 56005

  if bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/docker-port.out" 2>&1; then
    fail "foreign Docker port binder was accepted"
  else
    pass "foreign Docker port binder is refused"
  fi

  new_fixture nondocker-port-refusal
  worktree="$CASE_ROOT/worktree"
  env_file="$CASE_ROOT/worktree.env"
  project="wt_nondocker_port"
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56006
  if FAKE_LSOF_PORT=56006 bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/nondocker-port.out" 2>&1; then
    fail "non-Docker port binder was accepted"
  else
    pass "non-Docker port binder is refused through portable lsof detection"
  fi
}

test_concurrent_collision() {
  new_fixture concurrent-collision
  local project="wt_concurrent_collision"
  local worktree_a="$CASE_ROOT/worktree-a"
  local worktree_b="$CASE_ROOT/worktree-b"
  local env_a="$CASE_ROOT/a.env"
  local env_b="$CASE_ROOT/b.env"
  mkdir -p "$worktree_a" "$worktree_b"
  write_env "$env_a" "$project" "$worktree_a" 56007
  write_env "$env_b" "$project" "$worktree_b" 56007

  FAKE_DOCKER_ACTOR=first FAKE_DOCKER_BLOCK_UP=1 MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5 \
    bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_a" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/first.out" 2>&1 &
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
    bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_b" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/second.out" 2>&1; then
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
    pass "two colliding contenders allow at most one guard-plus-mutation pass"
  else
    fail "concurrent collision did not preserve one guarded mutation"
  fi
}

test_post_mutation_boundary() {
  new_fixture post-mutation-boundary
  local project="wt_post_mutation"
  local worktree_a="$CASE_ROOT/worktree-a"
  local worktree_b="$CASE_ROOT/worktree-b"
  local env_a="$CASE_ROOT/a.env"
  local env_b="$CASE_ROOT/b.env"
  mkdir -p "$worktree_a" "$worktree_b"
  write_env "$env_a" "$project" "$worktree_a" 56011
  write_env "$env_b" "$project" "$worktree_b" 56011

  FAKE_DOCKER_ACTOR=first FAKE_DOCKER_BLOCK_POST=1 FAKE_DOCKER_SKIP_RESOURCE_CREATE=1 \
    MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS=5 bash "$REPO_ROOT/scripts/ensure-postgres.sh" "$env_a" -- \
    docker compose --project-name "$project" exec -T postgres psql -U fixture_user -d postgres \
    -c 'DROP DATABASE IF EXISTS "fixture";' > "$CASE_ROOT/first.out" 2>&1 &
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
    bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_b" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/second.out" 2>&1; then
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
    [ "$(mutation_count)" = 1 ] && [ "$(cat "$FAKE_DOCKER_STATE/post-mutation")" = "$project" ]; then
    pass "db-reset-style post mutation remains inside the canonical lock boundary"
  else
    fail "post-readiness mutation released the ownership lock too early"
  fi
}

test_stale_lock_quarantine() {
  new_fixture stale-lock-quarantine
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project="wt_stale_lock"
  local lock_path="$MULTICA_COMPOSE_LOCK_ROOT/multica-compose-lock-$project"
  mkdir -p "$worktree" "$lock_path"
  write_env "$env_file" "$project" "$worktree" 56008
  printf '999999\n' > "$lock_path/owner.pid"
  cat > "$lock_path/owner.meta" <<META
project=$project
owner=worktree
worktree_path=$worktree
pid_start=unavailable
started_epoch=1
META

  if bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/stale.out" 2>&1 && \
    find "$MULTICA_COMPOSE_LOCK_ROOT" -maxdepth 1 -type d -name "multica-compose-lock-$project.stale.*" -print -quit | grep -q .; then
    pass "stale lock is quarantined by rename before a bounded recovery"
  else
    fail "stale lock was not safely quarantined and recovered"
  fi
}

test_first_initialization_and_readiness() {
  new_fixture first-initialization-and-readiness
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  local project="wt_first_init"
  local database="${project}_db"
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56009

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

  if bash "$REPO_ROOT/scripts/ensure-postgres.sh" "$env_file" > "$CASE_ROOT/ensure.out" 2>&1 && \
    [ "$(cat "$FAKE_DOCKER_STATE/initialized-db")" = "$database" ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/authenticated-db")" = "$database" ] && \
    [ "$(cat "$FAKE_DOCKER_STATE/authenticated-user")" = fixture_user ] && \
    ! grep -Fq fixture-password-not-for-output "$CASE_ROOT/ensure.out"; then
    pass "configured database is first-initialized and used by secret-safe authenticated SELECT 1"
  else
    fail "configured database initialization or authenticated readiness proof failed"
  fi
}

test_idempotency_and_production_identity() {
  new_fixture idempotency-and-production-identity
  local project="wt_idempotent"
  local worktree="$CASE_ROOT/worktree"
  local env_file="$CASE_ROOT/worktree.env"
  mkdir -p "$worktree"
  write_env "$env_file" "$project" "$worktree" 56010
  fixture_container multica-postgres-1 running multica deployment /deploy/live multica 5432
  printf 'production-fixed-id\n' > "$FAKE_DOCKER_STATE/containers/multica-postgres-1/id"
  printf 'production-start-time\n' > "$FAKE_DOCKER_STATE/containers/multica-postgres-1/started"
  cp -R "$FAKE_DOCKER_STATE/containers/multica-postgres-1" "$CASE_ROOT/production-before"

  if bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/first.out" 2>&1 && \
    bash "$REPO_ROOT/scripts/compose-guard-mustpass.sh" "$env_file" -- \
    docker compose --project-name "$project" up -d postgres > "$CASE_ROOT/second.out" 2>&1 && \
    cmp -s "$CASE_ROOT/production-before/id" "$FAKE_DOCKER_STATE/containers/multica-postgres-1/id" && \
    cmp -s "$CASE_ROOT/production-before/started" "$FAKE_DOCKER_STATE/containers/multica-postgres-1/started" && \
    cmp -s "$CASE_ROOT/production-before/labels" "$FAKE_DOCKER_STATE/containers/multica-postgres-1/labels"; then
    pass "idempotent worktree mutation preserves production identity fixture"
  else
    fail "idempotency or production-identity preservation failed"
  fi
}

test_static_safety_contract() {
  if rg -n -e '(^|[[:space:]])rm([[:space:]]|$)' \
    "$REPO_ROOT/scripts/compose-ownership-guard.sh" \
    "$REPO_ROOT/scripts/compose-guard-mustpass.sh" \
    "$REPO_ROOT/scripts/ensure-postgres.sh" \
    "$REPO_ROOT/scripts/test-compose-isolation.sh" > "$TEST_ROOT/delete-command.out"; then
    fail "Compose isolation scripts contain a permanent deletion command"
    return
  fi
  if rg -n 'docker rm|git checkout' "$REPO_ROOT/docs/recovery-worktree-compose-isolation.md" > "$TEST_ROOT/unsafe-runbook.out"; then
    fail "recovery runbook contains an unsafe direct deletion or checkout instruction"
    return
  fi
  if rg -n 'ALTER[[:space:]]+USER' \
    "$REPO_ROOT/docker-compose.yml" \
    "$REPO_ROOT/scripts/compose-ownership-guard.sh" \
    "$REPO_ROOT/scripts/compose-guard-mustpass.sh" \
    "$REPO_ROOT/scripts/ensure-postgres.sh"; then
    fail "isolation implementation contains a credential mutation"
    return
  fi
  pass "isolation scripts and rollback runbook retain the no-delete/no-credential-mutation boundary"
}

echo "=== Deterministic fake-Docker worktree Compose isolation tests ==="
echo "Artifacts retained at: $TEST_ROOT"
run_case canonical-identity test_canonical_identity
run_case linked-worktree-requires-owner test_linked_worktree_requires_owner
run_case wrapper-mutation test_wrapper_mutation
run_case stopped-foreign-refusal test_stopped_foreign_refusal
run_case strict-volume-labels test_strict_volume_labels
run_case path-alias-refusal test_path_alias_refusal
run_case port-refusal test_port_refusal
run_case concurrent-collision test_concurrent_collision
run_case post-mutation-boundary test_post_mutation_boundary
run_case stale-lock-quarantine test_stale_lock_quarantine
run_case first-initialization-and-readiness test_first_initialization_and_readiness
run_case idempotency-and-production-identity test_idempotency_and_production_identity
run_case static-safety-contract test_static_safety_contract

echo "=== Results: $PASS passed, $FAIL failed ==="
exit "$FAIL"
