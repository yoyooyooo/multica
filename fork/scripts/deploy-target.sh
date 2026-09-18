#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET=""
BACKEND_IMAGE=""
WEB_IMAGE=""
SOURCE_SHA=""
PLAN=false
usage() { printf 'Usage: %s [--plan] --target <mini|imile-win> --backend-image <tag> --web-image <tag> --source-sha <sha>\n' "$0"; }
while (($#)); do
  case "$1" in
    --plan) PLAN=true; shift ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --backend-image) BACKEND_IMAGE="${2:-}"; shift 2 ;;
    --web-image) WEB_IMAGE="${2:-}"; shift 2 ;;
    --source-sha) SOURCE_SHA="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$TARGET" in mini|imile-win) ;; *) usage >&2; exit 2 ;; esac
if [[ -z "$BACKEND_IMAGE" || -z "$WEB_IMAGE" || ! "$SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  usage >&2
  exit 2
fi

args=(--target "$TARGET" --backend-image "$BACKEND_IMAGE" --web-image "$WEB_IMAGE" --source-sha "$SOURCE_SHA")
if "$PLAN"; then
  args=(--plan "${args[@]}")
fi
if [[ "$TARGET" == "mini" ]]; then
  exec "$ROOT_DIR/fork/scripts/target-transaction.sh" "${args[@]}"
fi

if ! "$PLAN"; then
  for image in "$BACKEND_IMAGE" "$WEB_IMAGE"; do
    docker image inspect "$image" >/dev/null
  done
  docker save "$BACKEND_IMAGE" "$WEB_IMAGE" | gzip -1 | \
    ssh -o BatchMode=yes imile-win 'gzip -d | docker load >/dev/null'
fi
ssh -o BatchMode=yes imile-win bash -s -- "${args[@]}" < "$ROOT_DIR/fork/scripts/target-transaction.sh"
