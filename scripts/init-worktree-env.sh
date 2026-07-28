#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env.worktree}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -f "$ENV_FILE" ] && [ "${FORCE:-0}" != "1" ]; then
  bash "$SCRIPT_DIR/ensure-workload-issuer.sh" "$ENV_FILE" worktree
  echo "Preserved existing $ENV_FILE and reconciled its workload assertion issuer."
  exit 0
fi

worktree_name="${WORKTREE_NAME:-$(basename "$PWD")}"
slug="$(printf '%s' "$worktree_name" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
if [ -z "$slug" ]; then
  slug="multica"
fi

hash_value="$(printf '%s' "$PWD" | cksum | awk '{print $1}')"
offset=$((hash_value % 1000))

postgres_db="multica_${slug}_${offset}"
postgres_port=5432
backend_port=$((18080 + offset))
frontend_port=$((13000 + offset))
frontend_origin="http://localhost:${frontend_port}"
workload_assertion_issuer="urn:multica:worktree:${slug}:${hash_value}"
workload_assertion_issuer_instance_id="multica-worktree-${slug}-${hash_value}"

cat > "$ENV_FILE" <<EOF
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
MULTICA_WORKLOAD_ASSERTION_ISSUER=${workload_assertion_issuer}
MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=${workload_assertion_issuer_instance_id}
MULTICA_WORKLOAD_ASSERTION_KEY_ID=multica-workload-assertion-v1

GOOGLE_CLIENT_ID=
GOOGLE_CLIENT_SECRET=
GOOGLE_REDIRECT_URI=${frontend_origin}/auth/callback

FRONTEND_PORT=${frontend_port}
FRONTEND_ORIGIN=${frontend_origin}
NEXT_PUBLIC_API_URL=http://localhost:${backend_port}
NEXT_PUBLIC_WS_URL=ws://localhost:${backend_port}/ws
EOF

echo "Generated $ENV_FILE for worktree '$worktree_name'"
echo "  Shared Postgres: localhost:${postgres_port}"
echo "  Database: ${postgres_db}"
echo "  Backend:  http://localhost:${backend_port}"
echo "  Frontend: ${frontend_origin}"
echo ""
echo "Next steps:"
echo "  make setup-worktree"
echo "  make start-worktree"
