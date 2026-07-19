#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_DIR="$ROOT_DIR/server"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

export WORK_COORDINATION_DB_REQUIRED=1

packages=(
  internal/migrations
  cmd/migrate
  internal/service
  internal/handler
)

declare -A seen_pass=()
declare -A required_tests=(
  [internal/service]="TestWorkCoordinationInspectLifecycleReceiptWindowAndNoSideEffects TestWorkCoordinationInspectResolvedDependencyKeepsOpenBlockerEvidence TestWorkCoordinationInspectReceiptOrderIgnoresTransactionStartTime TestWorkCoordinationInspectUsesRepeatableReadSnapshot TestWorkCoordinationInspectFactBoundsOrderingAndReceiptAllowlist TestWorkCoordinationInspectCrossScopeOwnerIsolation TestWorkCoordinationInspectAgentRootAuthorityAndRevocation"
  [internal/handler]="TestWorkCoordinationInspectStrictWireAndReceiptCursor"
)
status=0
for pkg in "${packages[@]}"; do
  out="$TMP_DIR/$(echo "$pkg" | tr '/.' '__').ndjson"
  if ! (cd "$SERVER_DIR" && go test -count=1 -json -run '^TestWorkCoordination' ./$pkg) >"$out" 2>&1; then
    cat "$out"
    status=1
    break
  fi
  if grep -q '"Action":"skip"' "$out" || grep -q -- '--- SKIP:' "$out" || grep -q 'Skipping tests:' "$out"; then
    cat "$out"
    status=1
    break
  fi
  if ! grep -Eq '"Action":"pass".*"Test":"TestWorkCoordination' "$out"; then
    cat "$out"
    status=1
    break
  fi
  for required_test in ${required_tests[$pkg]:-}; do
    if ! grep -Eq '"Action":"pass".*"Test":"'"$required_test"'"' "$out"; then
      cat "$out"
      printf 'missing V4 pass evidence for %s in %s\n' "$required_test" "$pkg" >&2
      status=1
      break
    fi
  done
  if [[ $status -ne 0 ]]; then
    break
  fi
  seen_pass["$pkg"]=1
  grep -E '"Action":"pass".*"Test":"TestWorkCoordination' "$out" \
    | sed -E 's/.*"Test":"([^"]+)".*/pass \1/'
  printf 'ok %s\n' "$pkg"
done

if [[ $status -ne 0 ]]; then
  exit $status
fi

for pkg in "${packages[@]}"; do
  if [[ -z "${seen_pass[$pkg]:-}" ]]; then
    printf 'missing pass evidence for %s\n' "$pkg" >&2
    exit 1
  fi
done
