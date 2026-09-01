#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET=""
OUTPUT=""
usage() { printf 'Usage: %s --target <mini|imile-win> [--output <path>]\n' "$0"; }
while (($#)); do
  case "$1" in
    --target) TARGET="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$TARGET" in
  mini) expected_live=178 ;;
  imile-win) expected_live=62 ;;
  *) echo "--target must be mini or imile-win" >&2; exit 2 ;;
esac
if [[ -z "$OUTPUT" ]]; then
  timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
  OUTPUT="${MULTICA_FORK_STATE_ROOT:-$HOME/.local/state/multica}/census/${timestamp}-${TARGET}.json"
fi
umask 077
mkdir -p "$(dirname "$OUTPUT")"

if [[ "$TARGET" == "mini" ]]; then
  docker exec -i multica-postgres-1 sh -lc \
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At' \
    < "$ROOT_DIR/fork/sql/external-pr-census.sql" > "$OUTPUT"
else
  ssh -o BatchMode=yes imile-win \
    'docker exec -i multica-postgres-1 sh -lc '\''psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At'\''' \
    < "$ROOT_DIR/fork/sql/external-pr-census.sql" > "$OUTPUT"
fi
chmod 0600 "$OUTPUT"
node "$ROOT_DIR/fork/scripts/validate-census.mjs" "$OUTPUT" "$expected_live"
printf 'target=%s\nreceipt=%s\n' "$TARGET" "$OUTPUT"
