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
  -e 's#^MULTICA_WORKLOAD_ASSERTION_ISSUER=.*#MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:selfhost-config-test#' \
  -e 's#^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=.*#MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-selfhost-config-test#' \
  .env.example >"$tmp_env"
printf '\nBACKEND_PORT=9100\nMULTICA_LARK_WS_PROXY_URL=http://proxy.test:7890\n' >>"$tmp_env"

if docker compose --env-file .env.example -f docker-compose.selfhost.yml config >/dev/null 2>&1; then
  echo "self-host compose unexpectedly accepted a missing workload assertion issuer"
  exit 1
fi
placeholder_env="$tmp_dir/placeholder.env"
sed 's/^MULTICA_WORKLOAD_ASSERTION_ISSUER=.*/MULTICA_WORKLOAD_ASSERTION_ISSUER=multica/' "$tmp_env" >"$placeholder_env"
# Compose interpolation can reject absence but cannot compare a non-empty
# placeholder. The supported preflight and server startup own that check.
if bash scripts/validate-workload-issuer.sh "$placeholder_env" >/dev/null 2>&1; then
  echo "self-host preflight unexpectedly accepted the shared placeholder issuer"
  exit 1
fi
for invalid_id in 'urn:multica:deployment:selfhost-config-test' 'change-me' 'multica 测试' 'mat_secret'; do
  invalid_env="$tmp_dir/invalid-id.env"
  sed "s#^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=.*#MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=$invalid_id#" "$tmp_env" >"$invalid_env"
  if bash scripts/validate-workload-issuer.sh "$invalid_env" >/dev/null 2>&1; then
    echo "self-host preflight unexpectedly accepted issuer instance ID: $invalid_id"
    exit 1
  fi
done

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
require_config "$config" 'MULTICA_WORKLOAD_ASSERTION_ISSUER: urn:multica:deployment:selfhost-config-test'
require_config "$config" 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID: multica-selfhost-config-test'
require_config "$config" 'MULTICA_LARK_WS_PROXY_URL: http://proxy.test:7890'
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

if ! command -v helm >/dev/null 2>&1; then
  echo "helm is required for the real self-host chart contract test"
  exit 1
fi
if helm template multica-selfhost deploy/helm/multica >/dev/null 2>&1; then
  echo "Helm unexpectedly accepted a missing workload assertion issuer"
  exit 1
fi
if helm template multica-selfhost deploy/helm/multica --set-string backend.config.workloadAssertionIssuer=multica --set-string backend.config.workloadAssertionIssuerInstanceId=multica-helm-test >/dev/null 2>&1; then
  echo "Helm unexpectedly accepted the shared placeholder issuer"
  exit 1
fi
if helm template multica-selfhost deploy/helm/multica --set-string backend.config.workloadAssertionIssuer=multica-helm-test --set-string backend.config.workloadAssertionIssuerInstanceId=multica-helm-test >/dev/null 2>&1; then
  echo "Helm unexpectedly accepted equal issuer identities"
  exit 1
fi
helm_config="$(helm template multica-selfhost deploy/helm/multica \
  --set-string backend.config.workloadAssertionIssuer=urn:multica:deployment:helm-config-test \
  --set-string backend.config.workloadAssertionIssuerInstanceId=multica-helm-test)"
require_config "$helm_config" 'MULTICA_WORKLOAD_ASSERTION_ISSUER: "urn:multica:deployment:helm-config-test"'
require_config "$helm_config" 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID: "multica-helm-test"'
for quickstart in \
  apps/docs/content/docs/self-host-quickstart.mdx \
  apps/docs/content/docs/self-host-quickstart.zh.mdx \
  apps/docs/content/docs/self-host-quickstart.ja.mdx \
  apps/docs/content/docs/self-host-quickstart.ko.mdx; do
  grep -Fq "helm install multica deploy/helm/multica -n multica" "$quickstart"
  grep -Fq -- "--set-string 'backend.config.workloadAssertionIssuer=urn:multica:deployment:<stable-instance-id>'" "$quickstart"
  grep -Fq -- "--set-string 'backend.config.workloadAssertionIssuerInstanceId=<ags-trusted-issuer-id>'" "$quickstart"
  grep -Fq -- "- '$quickstart'" .github/workflows/ci.yml
done
if grep -Eq 'JWT_SECRET:|MULTICA_WORKLOAD_ASSERTION_SECRET:|MULTICA_EXTERNAL_PR_SERVICE_TOKEN:' <<<"$helm_config"; then
  echo "Helm rendered secret values into a manifest instead of existingSecret references"
  exit 1
fi

write_issuer_case() {
  local path=$1
  local case_name=$2
  case "$case_name" in
    missing) printf 'JWT_SECRET=test\n' >"$path" ;;
    empty) printf 'MULTICA_WORKLOAD_ASSERTION_ISSUER=\n' >"$path" ;;
    whitespace) printf 'MULTICA_WORKLOAD_ASSERTION_ISSUER=   \n' >"$path" ;;
    multica) printf 'MULTICA_WORKLOAD_ASSERTION_ISSUER=multica\n' >"$path" ;;
    valid) printf 'MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:keep-me\nMULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-keep-me\n' >"$path" ;;
  esac
}

for case_name in missing empty whitespace multica valid; do
  env_path="$tmp_dir/helper-$case_name.env"
  write_issuer_case "$env_path" "$case_name"
  bash scripts/ensure-workload-issuer.sh "$env_path" deployment >/dev/null
  bash scripts/validate-workload-issuer.sh "$env_path"
  grep -Eq '^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-[A-Za-z0-9_-]+$' "$env_path"
  if [ "$case_name" = valid ]; then
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:keep-me' "$env_path"
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-keep-me' "$env_path"
  fi

  generator_root="$tmp_dir/generator-$case_name"
  mkdir -p "$generator_root/scripts"
  cp Makefile .env.example "$generator_root/"
  cp scripts/ensure-workload-issuer.sh scripts/validate-workload-issuer.sh "$generator_root/scripts/"
  if [ "$case_name" != missing ]; then
    write_issuer_case "$generator_root/.env" "$case_name"
  fi
  make -s -C "$generator_root" selfhost-env >/dev/null
  bash scripts/validate-workload-issuer.sh "$generator_root/.env"
  grep -Eq '^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-[A-Za-z0-9_-]+$' "$generator_root/.env"
  if [ "$case_name" = valid ]; then
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:keep-me' "$generator_root/.env"
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-keep-me' "$generator_root/.env"
  fi

  dev_root="$tmp_dir/dev-$case_name"
  mkdir -p "$dev_root/scripts"
  printf 'gitdir: test\n' >"$dev_root/.git"
  cp scripts/dev.sh scripts/init-worktree-env.sh scripts/ensure-workload-issuer.sh "$dev_root/scripts/"
  if [ "$case_name" != missing ]; then
    write_issuer_case "$dev_root/.env.worktree" "$case_name"
  fi
  (cd "$dev_root" && MULTICA_ENV_ONLY=1 bash scripts/dev.sh >/dev/null)
  bash scripts/validate-workload-issuer.sh "$dev_root/.env.worktree"
  grep -Eq '^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-[A-Za-z0-9_-]+$' "$dev_root/.env.worktree"
  if [ "$case_name" = valid ]; then
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:keep-me' "$dev_root/.env.worktree"
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-keep-me' "$dev_root/.env.worktree"
  fi

  worktree_root="$tmp_dir/worktree-$case_name"
  mkdir -p "$worktree_root/scripts"
  cp scripts/init-worktree-env.sh scripts/ensure-workload-issuer.sh "$worktree_root/scripts/"
  if [ "$case_name" != missing ]; then
    write_issuer_case "$worktree_root/.env.worktree" "$case_name"
  fi
  (cd "$worktree_root" && WORKTREE_NAME="issuer-$case_name" bash scripts/init-worktree-env.sh .env.worktree >/dev/null)
  bash scripts/validate-workload-issuer.sh "$worktree_root/.env.worktree"
  grep -Eq '^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-[A-Za-z0-9_-]+$' "$worktree_root/.env.worktree"
  if [ "$case_name" = valid ]; then
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:keep-me' "$worktree_root/.env.worktree"
    grep -Fxq 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-keep-me' "$worktree_root/.env.worktree"
  fi
done

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
