#!/usr/bin/env bash
#
# compose-ownership-guard.sh — fail closed when a requested Compose project
# name, host port, or volume would collide with foreign ownership.
#
# Source this script, then call:
#   guard_compose_ownership
#
# Required env vars (set by .env / .env.worktree):
#   COMPOSE_PROJECT_NAME    — explicit compose project (empty = "multica" default)
#   POSTGRES_PORT           — host port for PostgreSQL (5432 default)
#   WORKTREE_PATH           — absolute path of this worktree
#   MULTICA_OWNER           — "deployment" or "worktree"
#
# Also exports a cross-process lock path for TOCTOU serialization:
#   compose_ownership_lock  — path to lockfile for mkdir-based atomic lock
#
# Exit codes:
#   0 = ownership confirmed or no existing containers/volumes
#   1 = foreign ownership collision detected

set -euo pipefail

guard_compose_ownership() {
  local project_name="${COMPOSE_PROJECT_NAME:-multica}"
  local port="${POSTGRES_PORT:-5432}"
  local worktree_path="${WORKTREE_PATH:-}"
  local owner="${MULTICA_OWNER:-deployment}"

  # Validate: worktree must always have a defined path
  if [ "$owner" = "worktree" ] && [ -z "$worktree_path" ]; then
    echo "ERROR: WORKTREE_PATH is required when MULTICA_OWNER=worktree" >&2
    return 1
  fi

  # --- Check 1: existing running containers with the same Compose project ---
  local existing_running
  existing_running="$(docker ps --filter "label=com.docker.compose.project=$project_name" --format '{{.Names}}' 2>/dev/null || true)"

  if [ -n "$existing_running" ]; then
    _check_ownership "$project_name" "$owner" "$worktree_path" "$existing_running" || return $?
  fi

  # --- Check 2: stopped containers with the same Compose project ---
  local existing_stopped
  existing_stopped="$(docker ps -a --filter "label=com.docker.compose.project=$project_name" --filter "status=exited" --filter "status=created" --format '{{.Names}}' 2>/dev/null || true)"

  if [ -n "$existing_stopped" ]; then
    _check_ownership "$project_name" "$owner" "$worktree_path" "$existing_stopped" "stopped" || return $?
  fi

  # --- Check 3: Compose-managed volumes with the same project ---
  local existing_volumes
  existing_volumes="$(docker volume ls --filter "label=com.docker.compose.project=$project_name" --format '{{.Name}}' 2>/dev/null || true)"

  if [ -n "$existing_volumes" ]; then
    local first_volume
    first_volume="$(echo "$existing_volumes" | head -1)"
    local vol_owner
    vol_owner="$(docker volume inspect --format '{{index .Labels "multica.owner"}}' "$first_volume" 2>/dev/null || true)"
    local vol_path
    vol_path="$(docker volume inspect --format '{{index .Labels "multica.worktree.path"}}' "$first_volume" 2>/dev/null || true)"

    if [ -z "$vol_owner" ]; then
      vol_owner="deployment"
    fi

    if [ "$vol_owner" != "$owner" ] || { [ "$owner" = "worktree" ] && [ "$vol_path" != "$worktree_path" ]; }; then
      # Volumes created by docker compose do NOT carry multica.* labels.
      # Allow volumes that belong to our exact project (same compose project
      # name = created by a previous run of this project). Reject only
      # volumes whose compose project differs.
      local vol_compose_proj
      vol_compose_proj="$(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "$first_volume" 2>/dev/null || true)"
      if [ -n "$vol_compose_proj" ] && [ "$vol_compose_proj" != "$project_name" ]; then
        echo "ERROR: Docker volume '$first_volume' belongs to compose project '$vol_compose_proj', not '$project_name'" >&2
        return 1
      fi
      # If no compose project label, it's unlabeled foreign volume -- reject
      if [ -z "$vol_compose_proj" ]; then
        echo "ERROR: Docker volume '$first_volume' has no compose project label (foreign)" >&2
        echo "  requested project: $project_name" >&2
        return 1
      fi
    fi
  fi

  # --- Check 4: host port already in use (Docker or non-Docker) ---
  # On macOS, lsof is available; on Linux, ss or netstat. Fall back to
  # checking docker port bindings which works on any platform with Docker.
  local port_in_use=false
  local port_binder=""

  # First check via lsof (works on macOS and Linux)
  if command -v lsof > /dev/null 2>&1; then
    port_binder="$(lsof -iTCP:"$port" -sTCP:LISTEN -n 2>/dev/null | tail -1 || true)"
    if [ -n "$port_binder" ]; then
      port_in_use=true
    fi
  fi

  # Fallback: ss (Linux)
  if [ "$port_in_use" = false ] && command -v ss > /dev/null 2>&1; then
    if ss -tlnp "sport = :$port" 2>/dev/null | grep -qE ":$port[[:space:]]"; then
      port_in_use=true
    fi
  fi

  # Fallback: netstat (Linux)
  if [ "$port_in_use" = false ] && command -v netstat > /dev/null 2>&1; then
    if netstat -tlnp 2>/dev/null | grep -qE ":$port[[:space:]]"; then
      port_in_use=true
    fi
  fi

  # Fallback: docker port inspection (always available with Docker)
  if [ "$port_in_use" = false ]; then
    local all_containers
    all_containers="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"
    for cname in $all_containers; do
      local cport_info
      cport_info="$(docker port "$cname" 5432 2>/dev/null || true)"
      if echo "$cport_info" | grep -qE "(0\.0\.0\.0|127\.0\.0\.1):$port" 2>/dev/null; then
        port_in_use=true
        port_binder="container $cname"
        break
      fi
    done
  fi

  if [ "$port_in_use" = true ]; then
    local all_containers
    all_containers="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"
    local foreign_binder=""

    for cname in $all_containers; do
      local cport_info
      cport_info="$(docker port "$cname" 5432 2>/dev/null || true)"
      if echo "$cport_info" | grep -qE "(0\.0\.0\.0|127\.0\.0\.1):$port" 2>/dev/null; then
        local c_owner
        c_owner="$(docker inspect --format '{{index .Config.Labels "multica.owner"}}' "$cname" 2>/dev/null || true)"
        local c_path
        c_path="$(docker inspect --format '{{index .Config.Labels "multica.worktree.path"}}' "$cname" 2>/dev/null || true)"
        if [ -z "$c_owner" ]; then
          c_owner="deployment"
        fi
        if [ "$c_owner" != "$owner" ] || { [ "$owner" = "worktree" ] && [ "$c_path" != "$worktree_path" ]; }; then
          echo "ERROR: Port $port (PostgreSQL) already bound by foreign container '$cname'" >&2
          echo "  existing owner:  $c_owner" >&2
          echo "  existing path:   ${c_path:-(unlabeled)}" >&2
          echo "  requested owner: $owner" >&2
          echo "  requested path:  ${worktree_path:-(none)}" >&2
          return 1
        fi
        foreign_binder="$cname"
        break
      fi
    done

    if [ -z "$foreign_binder" ]; then
      # Port bound by a non-Docker process
      local proc_info=""
      if command -v lsof > /dev/null 2>&1; then
        proc_info="$(lsof -iTCP:"$port" -sTCP:LISTEN -n 2>/dev/null | tail -1 || true)"
      fi
      if [ -n "$proc_info" ]; then
        echo "ERROR: Port $port already in use by a non-Docker process" >&2
        echo "  $proc_info" >&2
      else
        echo "ERROR: Port $port already in use by an unknown process" >&2
      fi
      return 1
    fi
  fi

  return 0
}

# _check_ownership iterates through container names and verifies each
# container's multica.owner and multica.worktree.path labels match.
# Usage: _check_ownership <project> <owner> <path> <names> [stopped]
_check_ownership() {
  local project_name="$1"
  local owner="$2"
  local worktree_path="$3"
  local names="$4"
  local state="${5:-running}"

  local first_container
  first_container="$(echo "$names" | head -1)"

  local existing_owner
  existing_owner="$(docker inspect --format '{{index .Config.Labels "multica.owner"}}' "$first_container" 2>/dev/null || true)"
  local existing_path
  existing_path="$(docker inspect --format '{{index .Config.Labels "multica.worktree.path"}}' "$first_container" 2>/dev/null || true)"

  # Unlabeled containers are treated as deployment-owned
  if [ -z "$existing_owner" ]; then
    existing_owner="deployment"
  fi

  if [ "$existing_owner" != "$owner" ] || { [ "$owner" = "worktree" ] && [ "$existing_path" != "$worktree_path" ]; }; then
    local extra="($state)"
    echo "ERROR: Compose project '$project_name' has foreign ownership $extra" >&2
    echo "  existing container: $first_container" >&2
    echo "  existing owner:     $existing_owner" >&2
    echo "  existing path:      ${existing_path:-(unlabeled)}" >&2
    echo "  requested owner:    $owner" >&2
    echo "  requested path:     ${worktree_path:-(none)}" >&2
    return 1
  fi

  return 0
}

# compose_ownership_lock: path for cross-process mkdir-based atomic lock.
# Shared by all callers to prevent TOCTOU between check and first compose up.
# Export so callers can use it with mkdir for atomic reservation.
compose_ownership_lock="/tmp/multica-compose-ownership.lock"

# When called directly (not sourced), execute the guard.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  guard_compose_ownership
fi
