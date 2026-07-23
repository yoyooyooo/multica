#!/usr/bin/env bash
#
# compose-guard-mustpass.sh — execute one Compose mutation inside the canonical
# ownership lock boundary.
#
# Usage:
#   bash scripts/compose-guard-mustpass.sh .env.worktree -- \
#     docker compose up -d postgres
#
# The caller supplies only the allowed high-level intent. The guard binds the
# project, config, env file, and project directory to the current checkout.
set -euo pipefail

ENV_FILE="${1:-.env}"
if [ "$#" -gt 0 ]; then
  shift
fi

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE" >&2
  exit 1
fi

if [ "${1:-}" != "--" ]; then
  echo "Usage: $0 <env-file> -- <Docker mutation command>" >&2
  exit 1
fi
shift

if [ "$#" -eq 0 ]; then
  echo "Refusing to run an empty Compose mutation command" >&2
  exit 1
fi
if [ "$1" != docker ] || [ "${2:-}" != compose ]; then
  echo "Refusing a non-Docker-Compose mutation command" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MULTICA_COMPOSE_ENV_FILE="$ENV_FILE"
export MULTICA_COMPOSE_ENV_FILE
# shellcheck disable=SC1091
. "$SCRIPT_DIR/compose-ownership-guard.sh"

compose_with_ownership_lock "$@"
