#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PLAN=false
ARCH=""
PREFIX="${FORK_IMAGE_PREFIX:-multica-fork}"

usage() {
  printf 'Usage: %s [--plan] --arch <arm64|amd64> [--prefix <image-prefix>]\n' "$0"
}

while (($#)); do
  case "$1" in
    --plan) PLAN=true; shift ;;
    --arch) ARCH="${2:-}"; shift 2 ;;
    --prefix) PREFIX="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$ARCH" in arm64|amd64) ;; *) echo "--arch must be arm64 or amd64" >&2; exit 2 ;; esac

cd "$ROOT_DIR"
"$ROOT_DIR/fork/scripts/verify-source.sh" >/dev/null
SHA="$(git rev-parse HEAD)"
SHORT_SHA="$(git rev-parse --short=12 HEAD)"
VERSION="$(git describe --tags --match 'v[0-9]*' --always)"
DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
BACKEND_TAG="${PREFIX}-backend:${SHORT_SHA}-${ARCH}"
WEB_TAG="${PREFIX}-web:${SHORT_SHA}-${ARCH}"

printf 'source_sha=%s\narchitecture=%s\nbackend_image=%s\nweb_image=%s\n' \
  "$SHA" "$ARCH" "$BACKEND_TAG" "$WEB_TAG"
if "$PLAN"; then
  printf 'plan_only=true\n'
  exit 0
fi

common_args=(
  --platform "linux/$ARCH"
  --load
  --build-arg "VERSION=$VERSION"
  --build-arg "COMMIT=$SHA"
  --build-arg "DATE=$DATE"
)
docker buildx build "${common_args[@]}" -f fork/backend.Dockerfile -t "$BACKEND_TAG" .
docker buildx build "${common_args[@]}" --build-arg "NEXT_PUBLIC_APP_VERSION=$VERSION" -f fork/web.Dockerfile -t "$WEB_TAG" .

backend_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$BACKEND_TAG")"
web_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$WEB_TAG")"
if [[ "$backend_revision" != "$SHA" || "$web_revision" != "$SHA" ]]; then
  echo "image revision readback does not match source $SHA" >&2
  exit 1
fi

backend_id="$(docker image inspect --format '{{.Id}}' "$BACKEND_TAG")"
web_id="$(docker image inspect --format '{{.Id}}' "$WEB_TAG")"
state_root="${MULTICA_FORK_STATE_ROOT:-$HOME/.local/state/multica/fork-builds}"
receipt_dir="$state_root/${DATE//[:]/}-${SHORT_SHA}-${ARCH}"
umask 077
mkdir -p "$receipt_dir"
node - "$receipt_dir/build-receipt.json" "$SHA" "$VERSION" "$DATE" "$ARCH" "$BACKEND_TAG" "$backend_id" "$WEB_TAG" "$web_id" <<'NODE'
const fs = require("node:fs");
const [path, sourceSha, version, builtAt, architecture, backendTag, backendId, webTag, webId] = process.argv.slice(2);
fs.writeFileSync(path, JSON.stringify({
  schema: "multica.fork-build-receipt.v1",
  source_sha: sourceSha,
  version,
  built_at: builtAt,
  architecture,
  images: {
    backend: { tag: backendTag, id: backendId },
    web: { tag: webTag, id: webId },
  },
}, null, 2) + "\n", { mode: 0o600 });
NODE
chmod 0600 "$receipt_dir/build-receipt.json"
printf 'receipt=%s\nbackend_id=%s\nweb_id=%s\n' "$receipt_dir/build-receipt.json" "$backend_id" "$web_id"
