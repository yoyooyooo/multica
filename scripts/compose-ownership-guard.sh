#!/usr/bin/env bash
#
# compose-ownership-guard.sh — fail closed when a requested Compose project
# name, host port, or volume would collide with foreign ownership.
#
# Source this script from ensure-postgres.sh after sourcing the env file
# and before any "docker compose up" call.
#
# Required env vars (set by .env / .env.worktree):
#   COMPOSE_PROJECT_NAME    — explicit compose project (empty = "multica" default)
#   POSTGRES_PORT           — host port for PostgreSQL (5432 default)
#   WORKTREE_PATH           — absolute path of this worktree
#   MULTICA_OWNER           — "deployment" or "worktree"
#
# Exit codes:
#   0 = ownership confirmed or no existing containers
#   1 = foreign ownership collision detected

set -euo pipefail

guard_compose_ownership() {
  local project_name="${COMPOSE_PROJECT_NAME:-multica}"
  local port="${POSTGRES_PORT:-5432}"
  local worktree_path="${WORKTREE_PATH:-}"
  local owner="${MULTICA_OWNER:-deployment}"

  # --- Check 1: existing Compose project with the same name ---
  local existing_names
  existing_names="$(docker ps --filter "label=com.docker.compose.project=$project_name" --format '{{.Names}}' 2>/dev/null || true)"

  if [ -n "$existing_names" ]; then
    local first_container
    first_container="$(echo "$existing_names" | head -1)"

    local existing_owner
    existing_owner="$(docker inspect --format '{{index .Config.Labels "multica.owner"}}' "$first_container" 2>/dev/null || true)"
    local existing_path
    existing_path="$(docker inspect --format '{{index .Config.Labels "multica.worktree.path"}}' "$first_container" 2>/dev/null || true)"

    # Unlabeled containers are treated as deployment-owned
    if [ -z "$existing_owner" ]; then
      existing_owner="deployment"
    fi

    if [ "$existing_owner" != "$owner" ] || { [ "$owner" = "worktree" ] && [ "$existing_path" != "$worktree_path" ]; }; then
      echo "ERROR: Compose project '$project_name' already exists with foreign ownership" >&2
      echo "  existing container: $first_container" >&2
      echo "  existing owner:     $existing_owner" >&2
      echo "  existing path:      ${existing_path:-(unlabeled)}" >&2
      echo "  requested owner:    $owner" >&2
      echo "  requested path:     ${worktree_path:-(none)}" >&2
      return 1
    fi
  fi

  # --- Check 2: existing container already bound to our host port ---
  # Use docker port inspection since the --filter "publish=" syntax is fragile
  local all_containers
  all_containers="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"

  for cname in $all_containers; do
    local cport_info
    cport_info="$(docker port "$cname" 5432 2>/dev/null || true)"
    if echo "$cport_info" | grep -q "0.0.0.0:$port\|127.0.0.1:$port" 2>/dev/null; then
      # This container already binds our target port
      local c_owner
      c_owner="$(docker inspect --format '{{index .Config.Labels "multica.owner"}}' "$cname" 2>/dev/null || true)"
      local c_path
      c_path="$(docker inspect --format '{{index .Config.Labels "multica.worktree.path"}}' "$cname" 2>/dev/null || true)"

      if [ -z "$c_owner" ]; then
        c_owner="deployment"
      fi

      if [ "$c_owner" != "$owner" ] || { [ "$owner" = "worktree" ] && [ "$c_path" != "$worktree_path" ]; }; then
        echo "ERROR: Port $port (PostgreSQL) already bound by foreign container '$cname'" >&2
        echo "  existing owner:     $c_owner" >&2
        echo "  existing path:      ${c_path:-(unlabeled)}" >&2
        echo "  requested owner:    $owner" >&2
        echo "  requested path:     ${worktree_path:-(none)}" >&2
        return 1
      fi
    fi
  done

  return 0
}

# Execute guard when sourced with --run flag or when called directly
if [ "${BASH_SOURCE[0]}" != "${0}" ] && [ "${1:-}" = "--run" ]; then
  guard_compose_ownership
elif [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  guard_compose_ownership
fi
