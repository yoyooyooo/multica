#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GOOS_TARGET=""
GOARCH_TARGET=""
SOURCE_SHA=""
STATE_ROOT="${MULTICA_FORK_STATE_ROOT:-$HOME/.local/state/multica}"
usage() {
  printf 'Usage: %s --goos <darwin|linux> --goarch <arm64|amd64> --source-sha <sha>\n' "$0"
}
while (($#)); do
  case "$1" in
    --goos) GOOS_TARGET="${2:-}"; shift 2 ;;
    --goarch) GOARCH_TARGET="${2:-}"; shift 2 ;;
    --source-sha) SOURCE_SHA="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$GOOS_TARGET/$GOARCH_TARGET" in
  darwin/arm64|linux/amd64) ;;
  *) usage >&2; exit 2 ;;
esac
if [[ ! "$SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  usage >&2
  exit 2
fi

cd "$ROOT_DIR"
"$ROOT_DIR/fork/scripts/verify-source.sh" >/dev/null
if [[ "$(git rev-parse HEAD)" != "$SOURCE_SHA" ]]; then
  echo "selected source does not match $SOURCE_SHA" >&2
  exit 1
fi

version="$(git describe --tags --match 'v[0-9]*' --always)"
timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
build_dir="$STATE_ROOT/builds/${timestamp}-${SOURCE_SHA}-${GOOS_TARGET}-${GOARCH_TARGET}"
binary="$build_dir/multica"
umask 077
mkdir -p "$build_dir"
(
  cd server
  GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET" CGO_ENABLED=0 go build \
    -ldflags "-X main.version=$version -X main.commit=$SOURCE_SHA -X main.date=$timestamp" \
    -o "$binary" ./cmd/multica
)
chmod 0755 "$binary"
version_output="$($binary --version)"
if [[ "$version_output" != *"(commit: $SOURCE_SHA,"* ]]; then
  echo "candidate CLI commit readback failed" >&2
  exit 1
fi
binary_sha256="$(shasum -a 256 "$binary" | awk '{print $1}')"
receipt="$build_dir/build-receipt.json"
RECEIPT_SHA="$SOURCE_SHA" RECEIPT_VERSION="$version" RECEIPT_TIMESTAMP="$timestamp" \
RECEIPT_GOOS="$GOOS_TARGET" RECEIPT_GOARCH="$GOARCH_TARGET" \
RECEIPT_BINARY="$binary" RECEIPT_BINARY_SHA256="$binary_sha256" \
python3 - "$receipt" <<'PY'
import json, os, sys
value = {
    "schema": "multica.clean-fork-cli-build.v1",
    "source_sha": os.environ["RECEIPT_SHA"],
    "version": os.environ["RECEIPT_VERSION"],
    "built_at": os.environ["RECEIPT_TIMESTAMP"],
    "target": {"goos": os.environ["RECEIPT_GOOS"], "goarch": os.environ["RECEIPT_GOARCH"]},
    "binary": os.environ["RECEIPT_BINARY"],
    "binary_sha256": os.environ["RECEIPT_BINARY_SHA256"],
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, indent=2)
    handle.write("\n")
os.chmod(sys.argv[1], 0o600)
PY
printf 'binary=%s\nversion=%s\nsource_sha=%s\nsha256=%s\nreceipt=%s\n' \
  "$binary" "$version" "$SOURCE_SHA" "$binary_sha256" "$receipt"
