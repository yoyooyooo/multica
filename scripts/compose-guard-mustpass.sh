#!/usr/bin/env bash
#
# compose-guard-mustpass.sh — sourced by Make targets to enforce the
# Compose ownership guard before any Docker mutation path.
#
# Usage: in a Make target that calls `docker compose` directly (not
# through ensure-postgres.sh), source this file first:
#
#   bash scripts/compose-guard-mustpass.sh .env.worktree
#
# Exit 0 if guard passes, 1 (with error message) if not.

set -euo pipefail

ENV_FILE="${1:-.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# shellcheck disable=SC1091
. "$SCRIPT_DIR/compose-ownership-guard.sh"

if ! guard_compose_ownership; then
  echo "ERROR: Compose ownership guard rejected the operation. Aborting." >&2
  exit 1
fi
