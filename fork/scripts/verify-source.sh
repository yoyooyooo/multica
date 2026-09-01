#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASELINE="$(tr -d '[:space:]' < "$ROOT_DIR/fork/UPSTREAM_BASELINE")"
cd "$ROOT_DIR"

if [[ -z "$BASELINE" ]] || ! git cat-file -e "$BASELINE^{commit}" 2>/dev/null; then
  echo "invalid fork upstream baseline: ${BASELINE:-<empty>}" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$BASELINE" HEAD; then
  echo "fork source is not descended from baseline $BASELINE" >&2
  exit 1
fi
if [[ -n "$(git rev-list --merges "$BASELINE"..HEAD)" ]]; then
  echo "fork source contains a merge commit after baseline $BASELINE" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "fork source must be clean before acceptance or image build" >&2
  exit 1
fi

SHA="$(git rev-parse HEAD)"
VERSION="$(git describe --tags --match 'v[0-9]*' --always)"
if [[ ! "$VERSION" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9]+-g[0-9a-fA-F]+)?$ ]]; then
  echo "untraceable fork version: $VERSION" >&2
  exit 1
fi

printf 'baseline=%s\nsource_sha=%s\nversion=%s\n' "$BASELINE" "$SHA" "$VERSION"
