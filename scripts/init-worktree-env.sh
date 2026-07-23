#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env.worktree}"
SCRIPT_DIR="$(CDPATH='' cd -P -- "$(dirname "$0")" && pwd -P)"
. "$SCRIPT_DIR/worktree-identity.sh"

if [ -f "$ENV_FILE" ] && [ "${FORCE:-0}" != "1" ]; then
  echo "Refusing to overwrite existing $ENV_FILE. Re-run with FORCE=1 if you want to regenerate it."
  exit 1
fi

# Canonicalize the physical checkout root before deriving every worktree
# identity. A symlinked checkout or a call from one of its subdirectories must
# generate the same project, port, database, labels, and lock key.
git_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -n "$git_root" ]; then
  worktree_path="$(cd "$git_root" && pwd -P)"
else
  worktree_path="$(pwd -P)"
fi
worktree_identity_derive "$worktree_path"
worktree_path="$WORKTREE_IDENTITY_PATH"
worktree_name="$(basename "$worktree_path")"

# Compose project and PostgreSQL database use an 80-bit SHA-256 prefix of the
# physical worktree path. The host port deliberately remains in a bounded
# 1000-slot range and is serialized by the port-keyed ownership lock.
compose_project="$WORKTREE_IDENTITY_PROJECT"
postgres_port="$WORKTREE_IDENTITY_PORT"
postgres_db="$WORKTREE_IDENTITY_DATABASE"
offset="$WORKTREE_IDENTITY_PORT_OFFSET"
backend_port=$((18080 + offset))
frontend_port=$((13000 + offset))
frontend_origin="http://localhost:${frontend_port}"
multica_owner="worktree"
cat > "$ENV_FILE" <<EOF
COMPOSE_PROJECT_NAME=${compose_project}
MULTICA_OWNER=${multica_owner}
WORKTREE_PATH=${worktree_path}

POSTGRES_DB=${postgres_db}
POSTGRES_USER=multica
POSTGRES_PASSWORD=multica
POSTGRES_PORT=${postgres_port}
DATABASE_URL=postgres://multica:multica@localhost:${postgres_port}/${postgres_db}?sslmode=disable

PORT=${backend_port}
JWT_SECRET=change-me-in-production
MULTICA_DEV_VERIFICATION_CODE=888888
MULTICA_SERVER_URL=ws://localhost:${backend_port}/ws
MULTICA_APP_URL=${frontend_origin}

GOOGLE_CLIENT_ID=
GOOGLE_CLIENT_SECRET=
GOOGLE_REDIRECT_URI=${frontend_origin}/auth/callback

FRONTEND_PORT=${frontend_port}
FRONTEND_ORIGIN=${frontend_origin}
NEXT_PUBLIC_API_URL=http://localhost:${backend_port}
NEXT_PUBLIC_WS_URL=ws://localhost:${backend_port}/ws
EOF

echo "Generated $ENV_FILE for worktree '$worktree_name'"
echo "  Compose project:  ${compose_project}"
echo "  Postgres port:    localhost:${postgres_port}"
echo "  Postgres volume:  ${compose_project}_pgdata"
echo "  Database:         ${postgres_db}"
echo "  Backend:          http://localhost:${backend_port}"
echo "  Frontend:         ${frontend_origin}"
echo "  Owner:            ${multica_owner}"
echo ""
echo "Next steps:"
echo "  make setup-worktree"
echo "  make start-worktree"
