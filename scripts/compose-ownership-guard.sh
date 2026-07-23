#!/usr/bin/env bash
#
# compose-ownership-guard.sh — canonical ownership and lock boundary for
# worktree Compose mutations.
#
# Source this script and call compose_with_ownership_lock with the exact
# Docker mutation to run. The helper canonicalizes identity, acquires the
# project-scoped lock, checks ownership and port safety, performs the command,
# and releases the lock only after that command exits.
#
# Direct execution runs the read-only ownership check only.
set -euo pipefail

compose_guard_error() {
  echo "ERROR: $*" >&2
}

compose_canonical_path() {
  local requested_path="${1:-}"
  if [ -z "$requested_path" ] || [ ! -d "$requested_path" ]; then
    compose_guard_error "worktree path is missing or is not a directory"
    return 1
  fi
  (
    CDPATH= cd -P -- "$requested_path"
    pwd -P
  )
}

compose_validate_project_name() {
  local project_name="$1"
  if [[ ! "$project_name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
    compose_guard_error "COMPOSE_PROJECT_NAME must contain only letters, digits, dot, underscore, or dash"
    return 1
  fi
}

compose_is_linked_git_worktree() {
  local git_dir
  local common_dir
  git_dir="$(git rev-parse --git-dir 2>/dev/null || true)"
  common_dir="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  [ -n "$git_dir" ] && [ -n "$common_dir" ] && [ "$git_dir" != "$common_dir" ]
}

# compose_prepare_identity canonicalizes the worktree path before any label or
# lock comparison. A worktree identity is never derived from logical $PWD.
compose_prepare_identity() {
  local linked_git_worktree=false
  COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-multica}"
  if compose_is_linked_git_worktree; then
    linked_git_worktree=true
  fi
  if [ -z "${MULTICA_OWNER:-}" ]; then
    if [ "$linked_git_worktree" = true ]; then
      compose_guard_error "a linked Git worktree must set MULTICA_OWNER=worktree and use a generated .env.worktree"
      return 1
    fi
    MULTICA_OWNER=deployment
  fi
  if [ "$linked_git_worktree" = true ] && [ "$MULTICA_OWNER" != worktree ]; then
    compose_guard_error "a linked Git worktree cannot use deployment Compose ownership"
    return 1
  fi

  compose_validate_project_name "$COMPOSE_PROJECT_NAME" || return 1

  case "$MULTICA_OWNER" in
    worktree)
      WORKTREE_PATH="$(compose_canonical_path "${WORKTREE_PATH:-}")" || return 1
      ;;
    deployment)
      if [ -n "${WORKTREE_PATH:-}" ]; then
        WORKTREE_PATH="$(compose_canonical_path "$WORKTREE_PATH")" || return 1
      else
        WORKTREE_PATH=""
      fi
      ;;
    *)
      compose_guard_error "MULTICA_OWNER must be either worktree or deployment"
      return 1
      ;;
  esac

  export COMPOSE_PROJECT_NAME MULTICA_OWNER WORKTREE_PATH
}

compose_resource_label() {
  local resource_kind="$1"
  local resource_name="$2"
  local label_key="$3"
  local value=""

  if [ "$resource_kind" = container ]; then
    value="$(docker inspect --format "{{index .Config.Labels \"$label_key\"}}" "$resource_name" 2>/dev/null || true)"
  else
    value="$(docker volume inspect --format "{{index .Labels \"$label_key\"}}" "$resource_name" 2>/dev/null || true)"
  fi

  # Docker's Go template renderer uses <no value> for some missing map keys.
  # Normalize it so all absent custom labels fail closed consistently.
  [ "$value" = '<no value>' ] && value=""
  printf '%s' "$value"
}

# compose_assert_resource_ownership fails closed for a resource that shares
# this project. Worktree resources require all custom labels and their raw path
# label must already be the canonical path, not merely resolve to one.
compose_assert_resource_ownership() {
  local resource_kind="$1"
  local resource_name="$2"
  local existing_owner
  local existing_path
  local existing_project
  local existing_compose_project

  existing_owner="$(compose_resource_label "$resource_kind" "$resource_name" multica.owner)"
  existing_path="$(compose_resource_label "$resource_kind" "$resource_name" multica.worktree.path)"
  existing_project="$(compose_resource_label "$resource_kind" "$resource_name" multica.worktree.project)"
  existing_compose_project="$(compose_resource_label "$resource_kind" "$resource_name" com.docker.compose.project)"

  if [ "$MULTICA_OWNER" = worktree ]; then
    if [ "$existing_owner" != worktree ] || [ -z "$existing_path" ] || [ -z "$existing_project" ]; then
      compose_guard_error "$resource_kind '$resource_name' lacks exact worktree ownership labels"
      return 1
    fi

    local canonical_existing_path
    canonical_existing_path="$(compose_canonical_path "$existing_path" 2>/dev/null || true)"
    if [ -z "$canonical_existing_path" ] || [ "$existing_path" != "$canonical_existing_path" ]; then
      compose_guard_error "$resource_kind '$resource_name' has a malformed or noncanonical worktree path label"
      return 1
    fi
  fi

  if [ "$existing_owner" != "$MULTICA_OWNER" ] || \
    [ "$existing_path" != "$WORKTREE_PATH" ] || \
    [ "$existing_project" != "$COMPOSE_PROJECT_NAME" ] || \
    [ "$existing_compose_project" != "$COMPOSE_PROJECT_NAME" ]; then
    compose_guard_error "$resource_kind '$resource_name' belongs to a different owner, worktree, or project"
    compose_guard_error "  existing owner:    ${existing_owner:-(missing)}"
    compose_guard_error "  existing worktree: ${existing_path:-(missing)}"
    compose_guard_error "  existing project:  ${existing_project:-(missing)}"
    compose_guard_error "  Compose project:   ${existing_compose_project:-(missing)}"
    compose_guard_error "  requested owner:   $MULTICA_OWNER"
    compose_guard_error "  requested worktree:${WORKTREE_PATH:-(none)}"
    compose_guard_error "  requested project: $COMPOSE_PROJECT_NAME"
    return 1
  fi
}

compose_check_project_resources() {
  local resource_name
  local containers
  local volumes
  local expected_container="${COMPOSE_PROJECT_NAME}-postgres-1"
  local expected_volume="${COMPOSE_PROJECT_NAME}_pgdata"

  # Inspect the deterministic Compose names as well as label-filtered lists.
  # An unlabeled resource at either name is still reusable by Compose and must
  # be rejected rather than becoming an implicit ownership inference.
  if docker inspect "$expected_container" > /dev/null 2>&1; then
    compose_assert_resource_ownership container "$expected_container" || return 1
  fi
  if docker volume inspect "$expected_volume" > /dev/null 2>&1; then
    compose_assert_resource_ownership volume "$expected_volume" || return 1
  fi

  containers="$(docker ps -a --filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME" --format '{{.Names}}' 2>/dev/null || true)"
  while IFS= read -r resource_name; do
    [ -z "$resource_name" ] && continue
    compose_assert_resource_ownership container "$resource_name" || return 1
  done <<< "$containers"

  volumes="$(docker volume ls --filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME" --format '{{.Name}}' 2>/dev/null || true)"
  while IFS= read -r resource_name; do
    [ -z "$resource_name" ] && continue
    compose_assert_resource_ownership volume "$resource_name" || return 1
  done <<< "$volumes"
}

compose_port_matches() {
  local bindings="$1"
  local expected_port="$2"
  printf '%s\n' "$bindings" | grep -qE "(^|[[:space:]])(0\\.0\\.0\\.0|127\\.0\\.0\\.1|::|\\[::\\]|\\[::1\\]):${expected_port}([[:space:]]|$)"
}

compose_check_port() {
  local port="${POSTGRES_PORT:-5432}"
  if [[ ! "$port" =~ ^[0-9]+$ ]] || [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
    compose_guard_error "POSTGRES_PORT must be an integer from 1 through 65535"
    return 1
  fi

  local container_name
  local bindings
  local containers
  local docker_binding_found=false
  containers="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"
  while IFS= read -r container_name; do
    [ -z "$container_name" ] && continue
    bindings="$(docker port "$container_name" 5432 2>/dev/null || true)"
    if compose_port_matches "$bindings" "$port"; then
      docker_binding_found=true
      compose_assert_resource_ownership container "$container_name" || return 1
    fi
  done <<< "$containers"

  if [ "$docker_binding_found" = true ]; then
    return 0
  fi

  # Docker did not own the binding. Check host listeners in a portable order.
  local listener=""
  if command -v lsof > /dev/null 2>&1; then
    listener="$(lsof -iTCP:"$port" -sTCP:LISTEN -n 2>/dev/null || true)"
    if [ -n "$listener" ]; then
      compose_guard_error "port $port is already bound by a non-Docker process"
      return 1
    fi
  fi

  if command -v ss > /dev/null 2>&1 && ss -ltn 2>/dev/null | grep -qE "[:.]${port}([[:space:]]|$)"; then
    compose_guard_error "port $port is already bound by a non-Docker process"
    return 1
  fi

  if command -v netstat > /dev/null 2>&1 && netstat -an 2>/dev/null | grep -qE "[:.]${port}([[:space:]]|$)"; then
    compose_guard_error "port $port is already bound by a non-Docker process"
    return 1
  fi
}

# guard_compose_ownership is read-only. Production mutation paths must use
# compose_with_ownership_lock rather than invoking this check on its own.
guard_compose_ownership() {
  compose_prepare_identity || return 1
  compose_check_project_resources || return 1
  compose_check_port || return 1
}

compose_lock_path_mtime() {
  local lock_path="$1"
  if stat -f %m "$lock_path" > /dev/null 2>&1; then
    stat -f %m "$lock_path"
  else
    stat -c %Y "$lock_path" 2>/dev/null || true
  fi
}

compose_lock_metadata_value() {
  local lock_path="$1"
  local key="$2"
  [ -f "$lock_path/owner.meta" ] || return 0
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$lock_path/owner.meta"
}

compose_lock_owner_summary() {
  local lock_path="$1"
  local owner_pid
  local owner_project
  local owner_path
  owner_pid="$(compose_lock_metadata_value "$lock_path" pid)"
  owner_project="$(compose_lock_metadata_value "$lock_path" project)"
  owner_path="$(compose_lock_metadata_value "$lock_path" worktree_path)"
  printf 'pid=%s project=%s worktree=%s' "${owner_pid:-(unknown)}" "${owner_project:-(unknown)}" "${owner_path:-(unknown)}"
}

compose_lock_quarantine() {
  local lock_path="$1"
  local reason="$2"
  local timestamp
  local quarantine_path
  timestamp="$(date +%s)"
  quarantine_path="${lock_path}.stale.${timestamp}.$$.${RANDOM}"

  if mv "$lock_path" "$quarantine_path" 2>/dev/null; then
    echo "==> Quarantined stale Compose lock ($reason): $quarantine_path" >&2
    return 0
  fi
  return 1
}

# Return success only when a stale lock was safely quarantined. A missing or
# partial owner record is given a short initialization grace period so another
# contender cannot steal a lock while its owner is still writing evidence.
compose_lock_recover_stale() {
  local lock_path="$1"
  local now
  local created
  local owner_pid
  local expected_start
  local actual_start
  local grace_seconds="${MULTICA_COMPOSE_LOCK_INITIALIZATION_GRACE_SECONDS:-2}"

  now="$(date +%s)"
  created="$(cat "$lock_path/created_epoch" 2>/dev/null || true)"
  if [[ ! "$created" =~ ^[0-9]+$ ]]; then
    created="$(compose_lock_path_mtime "$lock_path")"
  fi
  if [[ ! "$created" =~ ^[0-9]+$ ]]; then
    return 1
  fi

  owner_pid="$(compose_lock_metadata_value "$lock_path" pid)"
  expected_start="$(compose_lock_metadata_value "$lock_path" pid_start)"
  if [[ ! "$owner_pid" =~ ^[0-9]+$ ]] || [ -z "$expected_start" ]; then
    if [ $((now - created)) -ge "$grace_seconds" ]; then
      compose_lock_quarantine "$lock_path" "missing owner evidence"
      return $?
    fi
    return 1
  fi

  if ! kill -0 "$owner_pid" 2>/dev/null; then
    compose_lock_quarantine "$lock_path" "owner process is no longer live"
    return $?
  fi

  actual_start="$(ps -o lstart= -p "$owner_pid" 2>/dev/null | tr -s ' ' | sed 's/^ //; s/ $//' || true)"
  if [ -z "$actual_start" ] || [ "$actual_start" != "$expected_start" ]; then
    compose_lock_quarantine "$lock_path" "owner PID evidence does not match"
    return $?
  fi

  return 1
}

compose_lock_acquire() {
  compose_prepare_identity || return 1

  local lock_root="${MULTICA_COMPOSE_LOCK_ROOT:-${TMPDIR:-/tmp}/multica-compose-locks}"
  local timeout_seconds="${MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS:-10}"
  local deadline
  local lock_path

  if [[ ! "$timeout_seconds" =~ ^[0-9]+$ ]] || [ "$timeout_seconds" -lt 1 ] || [ "$timeout_seconds" -gt 300 ]; then
    compose_guard_error "MULTICA_COMPOSE_LOCK_TIMEOUT_SECONDS must be an integer from 1 through 300"
    return 1
  fi

  mkdir -p "$lock_root"
  lock_root="$(compose_canonical_path "$lock_root")" || return 1
  lock_path="$lock_root/multica-compose-lock-$COMPOSE_PROJECT_NAME"
  deadline=$(( $(date +%s) + timeout_seconds ))

  while :; do
    if mkdir "$lock_path" 2>/dev/null; then
      local now
      local owner_pid
      local pid_start
      local pending_metadata
      now="$(date +%s)"
      owner_pid="${BASHPID:-$$}"
      printf '%s\n' "$now" > "$lock_path/created_epoch"
      pid_start="$(ps -o lstart= -p "$owner_pid" 2>/dev/null | tr -s ' ' | sed 's/^ //; s/ $//' || true)"
      if [ -z "$pid_start" ]; then
        compose_guard_error "could not record current process start evidence for Compose lock"
        compose_lock_quarantine "$lock_path" "incomplete owner evidence" || true
        return 1
      fi
      printf '%s\n' "$owner_pid" > "$lock_path/owner.pid"
      pending_metadata="$lock_path/owner.meta.pending.$owner_pid"
      cat > "$pending_metadata" <<METADATA
pid=$owner_pid
pid_start=$pid_start
project=$COMPOSE_PROJECT_NAME
owner=$MULTICA_OWNER
worktree_path=$WORKTREE_PATH
started_epoch=$now
METADATA
      mv "$pending_metadata" "$lock_path/owner.meta"
      COMPOSE_OWNERSHIP_LOCK_PATH="$lock_path"
      export COMPOSE_OWNERSHIP_LOCK_PATH
      return 0
    fi

    if compose_lock_recover_stale "$lock_path"; then
      continue
    fi

    if [ "$(date +%s)" -ge "$deadline" ]; then
      compose_guard_error "timed out waiting for Compose lock '$lock_path' ($(compose_lock_owner_summary "$lock_path"))"
      return 1
    fi
    sleep 1
  done
}

compose_lock_release() {
  local lock_path="${COMPOSE_OWNERSHIP_LOCK_PATH:-}"
  [ -n "$lock_path" ] || return 0
  [ -d "$lock_path" ] || return 0

  local timestamp
  local released_path
  timestamp="$(date +%s)"
  released_path="${lock_path}.released.${timestamp}.$$.${RANDOM}"
  if ! mv "$lock_path" "$released_path"; then
    compose_guard_error "could not release Compose lock '$lock_path'; leaving it fail-closed"
    return 1
  fi
  COMPOSE_OWNERSHIP_LOCK_PATH=""
  export COMPOSE_OWNERSHIP_LOCK_PATH
}

# compose_with_ownership_lock provides the only mutation boundary used by the
# worktree scripts: identity -> lock -> guard -> exact command -> quarantine
# release. The subshell keeps its EXIT trap scoped to this invocation.
compose_with_ownership_lock() (
  compose_prepare_identity
  compose_lock_acquire
  trap 'status=$?; compose_lock_release || true; exit "$status"' EXIT
  guard_compose_ownership
  "$@"
)

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  guard_compose_ownership
fi
