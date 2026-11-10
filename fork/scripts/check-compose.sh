#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

JWT_SECRET=config-check-only \
FORK_BACKEND_IMAGE=multica-fork-backend:immutable-test \
FORK_WEB_IMAGE=multica-fork-web:immutable-test \
MULTICA_EXTERNAL_PR_SERVICE_TOKEN=redacted-test-token \
docker compose \
  -f docker-compose.selfhost.yml \
  -f fork/compose.yml \
  config --format json |
node fork/scripts/check-compose.mjs
