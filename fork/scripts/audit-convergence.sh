#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PREVIOUS=""
UPSTREAM=""
SOURCE="HEAD"

usage() {
  printf 'Usage: %s --previous <fork-ref> --upstream <upstream-ref> [--source <ref>]\n' "$0"
}

while (($#)); do
  case "$1" in
    --previous) PREVIOUS="${2:-}"; shift 2 ;;
    --upstream) UPSTREAM="${2:-}"; shift 2 ;;
    --source) SOURCE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
if [[ -z "$PREVIOUS" || -z "$UPSTREAM" ]]; then
  usage >&2
  exit 2
fi

cd "$ROOT_DIR"
BASELINE="$(tr -d '[:space:]' < fork/UPSTREAM_BASELINE)"
for ref in "$PREVIOUS" "$UPSTREAM" "$SOURCE" "$BASELINE"; do
  if ! git cat-file -e "$ref^{commit}" 2>/dev/null; then
    echo "missing commit ref: $ref" >&2
    exit 1
  fi
done
SOURCE_SHA="$(git rev-parse "$SOURCE^{commit}")"
UPSTREAM_SHA="$(git rev-parse "$UPSTREAM^{commit}")"
PREVIOUS_SHA="$(git rev-parse "$PREVIOUS^{commit}")"
PREVIOUS_BASE="$(git merge-base "$PREVIOUS_SHA" "$UPSTREAM_SHA")"
if ! git merge-base --is-ancestor "$BASELINE" "$SOURCE_SHA"; then
  echo "source $SOURCE_SHA is not descended from baseline $BASELINE" >&2
  exit 1
fi

count_lines() {
  awk 'NF { n++ } END { print n + 0 }'
}

side_metrics() {
  local label="$1" base="$2" tip="$3"
  local paths statuses overlap invasion numstat merge_output merge_status conflict_paths
  paths="$(git diff --name-only "$base..$tip" | sort -u)"
  statuses="$(git diff --name-status "$base..$tip")"
  overlap="$(comm -12 \
    <(printf '%s\n' "$paths" | awk 'NF' | sort -u) \
    <(git diff --name-only "$base..$UPSTREAM_SHA" | sort -u))"
  invasion="$(while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    if git cat-file -e "$base:$path" 2>/dev/null; then
      printf '%s\n' "$path"
    fi
  done <<< "$paths")"
  numstat="$(git diff --numstat "$base..$tip" | awk '
    BEGIN { additions=0; deletions=0; binaries=0 }
    $1 == "-" || $2 == "-" { binaries++; next }
    { additions += $1; deletions += $2 }
    END { print additions, deletions, binaries }
  ')"

  set +e
  merge_output="$(git merge-tree --write-tree --messages "$tip" "$UPSTREAM_SHA" 2>&1)"
  merge_status=$?
  set -e
  if ((merge_status > 1)); then
    printf '%s\n' "$merge_output" >&2
    echo "merge-tree failed for $label" >&2
    exit "$merge_status"
  fi
  conflict_paths="$(printf '%s\n' "$merge_output" |
    awk 'NF >= 4 && $3 ~ /^[123]$/ { print $4 }' | sort -u)"

  read -r additions deletions binaries <<< "$numstat"
  printf '[%s]\n' "$label"
  printf 'base=%s\n' "$base"
  printf 'tip=%s\n' "$tip"
  printf 'nearest_tag=%s\n' "$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$base")"
  printf 'commits=%s\n' "$(git rev-list --count "$base..$tip")"
  printf 'merge_commits=%s\n' "$(git rev-list --count --merges "$base..$tip")"
  printf 'changed_files=%s\n' "$(printf '%s\n' "$paths" | count_lines)"
  printf 'added_files=%s\n' "$(printf '%s\n' "$statuses" | awk '$1 ~ /^A/ {n++} END {print n+0}')"
  printf 'modified_existing_files=%s\n' "$(printf '%s\n' "$invasion" | count_lines)"
  printf 'fork_boundary_files=%s\n' "$(printf '%s\n' "$paths" | awk '$0 ~ /^fork\// {n++} END {print n+0}')"
  printf 'insertions=%s\n' "$additions"
  printf 'deletions=%s\n' "$deletions"
  printf 'binary_files=%s\n' "$binaries"
  printf 'upstream_overlap_files=%s\n' "$(printf '%s\n' "$overlap" | count_lines)"
  printf 'merge_tree_status=%s\n' "$merge_status"
  printf 'text_conflict_files=%s\n' "$(printf '%s\n' "$conflict_paths" | count_lines)"
  printf 'upstream_overlap_paths<<EOF\n%s\nEOF\n' "$overlap"
  printf 'modified_existing_paths<<EOF\n%s\nEOF\n' "$invasion"
  printf 'text_conflict_paths<<EOF\n%s\nEOF\n' "$conflict_paths"
}

printf 'schema=multica.fork-convergence-audit.v1\n'
printf 'upstream=%s\n' "$UPSTREAM_SHA"
printf 'source=%s\n' "$SOURCE_SHA"
printf 'previous=%s\n' "$PREVIOUS_SHA"
side_metrics previous "$PREVIOUS_BASE" "$PREVIOUS_SHA"
side_metrics current "$BASELINE" "$SOURCE_SHA"
