#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASELINE="$(tr -d '[:space:]' < "$ROOT_DIR/fork/UPSTREAM_BASELINE")"
TAG_FILE="$ROOT_DIR/fork/UPSTREAM_BASELINE_TAG"
cd "$ROOT_DIR"

if [[ -z "$BASELINE" ]] || ! git cat-file -e "$BASELINE^{commit}" 2>/dev/null; then
  echo "invalid fork upstream baseline: ${BASELINE:-<empty>}" >&2
  exit 1
fi
if [[ ! -f "$TAG_FILE" ]]; then
  echo "missing pinned upstream baseline tag: $TAG_FILE" >&2
  exit 1
fi
BASELINE_TAG=""
BASELINE_TAG_COMMIT=""
while IFS='=' read -r key value; do
  case "$key" in
    tag) BASELINE_TAG="$value" ;;
    commit) BASELINE_TAG_COMMIT="$value" ;;
  esac
done < "$TAG_FILE"
if [[ ! "$BASELINE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
   [[ ! "$BASELINE_TAG_COMMIT" =~ ^[0-9a-f]{40}$ ]]; then
  echo "invalid pinned baseline tag identity in $TAG_FILE" >&2
  exit 1
fi
if ! git show-ref --verify --quiet "refs/tags/$BASELINE_TAG"; then
  echo "missing pinned upstream tag $BASELINE_TAG; fetch complete upstream tags before verification" >&2
  exit 1
fi
ACTUAL_TAG_COMMIT="$(git rev-parse "$BASELINE_TAG^{commit}")"
if [[ "$ACTUAL_TAG_COMMIT" != "$BASELINE_TAG_COMMIT" ]]; then
  echo "pinned upstream tag $BASELINE_TAG resolves to $ACTUAL_TAG_COMMIT, want $BASELINE_TAG_COMMIT" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$BASELINE_TAG_COMMIT" "$BASELINE"; then
  echo "pinned upstream tag $BASELINE_TAG is not an ancestor of baseline $BASELINE" >&2
  exit 1
fi
NEAREST_TAG="$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$BASELINE")"
if [[ "$NEAREST_TAG" != "$BASELINE_TAG" ]]; then
  echo "nearest baseline tag is $NEAREST_TAG, want pinned $BASELINE_TAG" >&2
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
if [[ "$VERSION" != "$BASELINE_TAG" && "$VERSION" != "$BASELINE_TAG-"* ]]; then
  echo "fork version $VERSION is not derived from pinned baseline tag $BASELINE_TAG" >&2
  exit 1
fi

printf 'baseline=%s\nbaseline_tag=%s\nbaseline_tag_commit=%s\nbaseline_distance=%s\nsource_sha=%s\nversion=%s\n' \
  "$BASELINE" "$BASELINE_TAG" "$BASELINE_TAG_COMMIT" \
  "$(git rev-list --count "$BASELINE_TAG_COMMIT..$BASELINE")" "$SHA" "$VERSION"
