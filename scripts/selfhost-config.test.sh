#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

require_config() {
  local config=$1
  local expected=$2

  if ! grep -Fq "$expected" <<<"$config"; then
    echo "Missing expected docker compose config value:"
    echo "  $expected"
    exit 1
  fi
}

require_env() {
  local output=$1
  local expected=$2

  if ! grep -Fxq "$expected" <<<"$output"; then
    echo "Missing expected derived env value:"
    echo "  $expected"
    echo "Observed:"
    echo "$output"
    exit 1
  fi
}

tmp_dir="$(mktemp -d)"
tmp_env="$tmp_dir/render.env"
trap 'rm -rf "$tmp_dir"' EXIT
sed \
  -e 's/^FRONTEND_PORT=.*/FRONTEND_PORT=3100/' \
  .env.example >"$tmp_env"
printf '\nBACKEND_PORT=9100\nMULTICA_LARK_WS_PROXY_URL=http://proxy.test:7890\n' >>"$tmp_env"

config="$(
  docker compose \
    --env-file "$tmp_env" \
    -f docker-compose.selfhost.yml \
    config
)"

require_config "$config" 'published: "3100"'
require_config "$config" 'published: "9100"'
require_config "$config" 'FRONTEND_ORIGIN: http://localhost:3100'
require_config "$config" 'GOOGLE_REDIRECT_URI: http://localhost:3100/auth/callback'
require_config "$config" 'MULTICA_APP_URL: http://localhost:3100'
require_config "$config" 'MULTICA_LARK_WS_PROXY_URL: http://proxy.test:7890'
if grep -Eq 'MULTICA_(WORKLOAD_ASSERTION|DELEGATED_PR_MERGE)' <<<"$config" || grep -Eq 'MULTICA_(WORKLOAD_ASSERTION|DELEGATED_PR_MERGE)' .env.example; then
  echo "retired workload assertion or delegated merge configuration is still exposed"
  exit 1
fi
if grep -Eq 'workloadAssertionIssuer|workloadAssertionIssuerInstanceId' \
  SELF_HOSTING.md \
  apps/docs/content/docs/self-host-quickstart*.mdx \
  apps/docs/content/docs/getting-started/self-hosting.zh.mdx; then
  echo "self-host documentation still requires retired workload assertion chart values"
  exit 1
fi
for proxy_key in HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; do
  require_config "$config" "$proxy_key: \"\""
done
require_config "$config" "source: $ROOT_DIR/data/uploads"
require_config "$config" 'target: /app/data/uploads'
if grep -Fq 'source: multica_backend_uploads' <<<"$config"; then
  echo "self-host compose must preserve uploads through the repository bind mount"
  exit 1
fi

build_config="$(
  GOPROXY='https://goproxy.example,direct' docker compose \
    --env-file "$tmp_env" \
    -f docker-compose.selfhost.yml \
    -f docker-compose.selfhost.build.yml \
    config
)"
require_config "$build_config" 'GOPROXY: https://goproxy.example,direct'
require_config "$(cat Dockerfile)" 'ARG GOPROXY=https://proxy.golang.org,direct'
require_config "$(cat Dockerfile)" 'ENV GOPROXY=${GOPROXY}'

legacy_uploads="$tmp_dir/legacy-uploads"
bind_uploads="$tmp_dir/bind-uploads"
mkdir -p "$legacy_uploads/nested" "$bind_uploads"
printf 'legacy payload\n' >"$legacy_uploads/nested/asset.txt"
printf 'bind-owned payload\n' >"$bind_uploads/current.txt"
ln -s nested/asset.txt "$legacy_uploads/asset-link"
bash scripts/copy-legacy-uploads.sh "$legacy_uploads" "$bind_uploads"
cmp "$legacy_uploads/nested/asset.txt" "$bind_uploads/nested/asset.txt"
cmp <(printf 'bind-owned payload\n') "$bind_uploads/current.txt"
[[ "$(readlink "$bind_uploads/asset-link")" == 'nested/asset.txt' ]]
# A second pass must be idempotent and preserve the verified content.
bash scripts/copy-legacy-uploads.sh "$legacy_uploads" "$bind_uploads"
cmp "$legacy_uploads/nested/asset.txt" "$bind_uploads/nested/asset.txt"

conflict_source="$tmp_dir/conflict-source"
conflict_target="$tmp_dir/conflict-target"
mkdir -p "$conflict_source" "$conflict_target"
printf 'legacy\n' >"$conflict_source/same.txt"
printf 'bind\n' >"$conflict_target/same.txt"
if bash scripts/copy-legacy-uploads.sh "$conflict_source" "$conflict_target" >/dev/null 2>&1; then
  echo "legacy uploads migration overwrote or accepted a conflicting bind-owned file"
  exit 1
fi

python3 - <<'PY'
from pathlib import Path
import re
text = Path("Makefile").read_text()
for target in ("selfhost", "selfhost-build"):
    match = re.search(rf"^{target}:.*?(?=^[A-Za-z0-9_.-]+:|\Z)", text, re.M | re.S)
    assert match, target
    block = match.group(0)
    assert block.index("scripts/migrate-selfhost-uploads.sh") < block.index("up -d"), target
print("legacy uploads preflight order ok")
PY

if ! command -v helm >/dev/null 2>&1; then
  echo "helm is required for the real self-host chart contract test"
  exit 1
fi
helm_config="$(helm template multica-selfhost deploy/helm/multica)"
for quickstart in \
  apps/docs/content/docs/self-host-quickstart.mdx \
  apps/docs/content/docs/self-host-quickstart.zh.mdx \
  apps/docs/content/docs/self-host-quickstart.ja.mdx \
  apps/docs/content/docs/self-host-quickstart.ko.mdx; do
  grep -Fq "helm install multica deploy/helm/multica -n multica" "$quickstart"
  grep -Fq -- "- '$quickstart'" .github/workflows/ci.yml
done
if grep -Eq 'JWT_SECRET:|MULTICA_EXTERNAL_PR_SERVICE_TOKEN:' <<<"$helm_config"; then
  echo "Helm rendered secret values into a manifest instead of existingSecret references"
  exit 1
fi
if grep -Eq 'MULTICA_(WORKLOAD_ASSERTION|DELEGATED_PR_MERGE)' <<<"$helm_config"; then
  echo "Helm rendered retired workload assertion or delegated merge configuration"
  exit 1
fi

for script in scripts/dev.sh scripts/check.sh; do
  if ! grep -Fq '. scripts/local-env.sh' "$script"; then
    echo "$script must source scripts/local-env.sh for shared local env derivation."
    exit 1
  fi
done

local_env="$(
  env -i PATH="$PATH" bash -c '
    set -euo pipefail
    env_file=$1
    set -a
    # shellcheck disable=SC1090
    . "$env_file"
    set +a
    # shellcheck disable=SC1091
    . scripts/local-env.sh
    printf "%s\n" \
      "PORT=${PORT}" \
      "FRONTEND_PORT=${FRONTEND_PORT}" \
      "FRONTEND_ORIGIN=${FRONTEND_ORIGIN}" \
      "MULTICA_APP_URL=${MULTICA_APP_URL}" \
      "GOOGLE_REDIRECT_URI=${GOOGLE_REDIRECT_URI}" \
      "MULTICA_SERVER_URL=${MULTICA_SERVER_URL}" \
      "LOCAL_UPLOAD_BASE_URL=${LOCAL_UPLOAD_BASE_URL}" \
      "PLAYWRIGHT_BASE_URL=${PLAYWRIGHT_BASE_URL}"
  ' _ "$tmp_env"
)"

require_env "$local_env" 'PORT=9100'
require_env "$local_env" 'FRONTEND_PORT=3100'
require_env "$local_env" 'FRONTEND_ORIGIN=http://localhost:3100'
require_env "$local_env" 'MULTICA_APP_URL=http://localhost:3100'
require_env "$local_env" 'GOOGLE_REDIRECT_URI=http://localhost:3100/auth/callback'
require_env "$local_env" 'MULTICA_SERVER_URL=ws://localhost:9100/ws'
require_env "$local_env" 'LOCAL_UPLOAD_BASE_URL=http://localhost:9100'
require_env "$local_env" 'PLAYWRIGHT_BASE_URL=http://localhost:3100'

echo "self-host env derivation ok"
