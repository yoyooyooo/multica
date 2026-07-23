#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env.worktree}"

if [ -f "$ENV_FILE" ] && [ "${FORCE:-0}" != "1" ]; then
  echo "Refusing to overwrite existing $ENV_FILE. Re-run with FORCE=1 if you want to regenerate it."
  exit 1
fi

worktree_name="${WORKTREE_NAME:-$(basename "$PWD")}"
slug="$(printf '%s' "$worktree_name" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
if [ -z "$slug" ]; then
  slug="multica"
fi

hash_value="$(printf '%s' "$PWD" | cksum | awk '{print $1}')"
offset=$((hash_value % 1000))

# Deterministic unique identities per worktree: each worktree gets its own
# Compose project, PostgreSQL host port, database name, and Docker volume so
# no worktree ever touches "multica-*" production resources by default.
compose_project="wt_${slug}_${offset}"
postgres_port=$((15432 + (offset % 1000) % 1000))
postgres_db="wt_${slug}_${offset}"
backend_port=$((18080 + offset))
frontend_port=$((13000 + offset))
frontend_origin="http://localhost:${frontend_port}"
multica_owner="worktree"
worktree_path="$PWD"

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
