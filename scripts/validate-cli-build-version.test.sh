#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
validator="$ROOT_DIR/scripts/validate-cli-build-version.sh"

valid=(
  "v0.4.8"
  "0.4.8"
  "v0.4.8-12-g2c749ddf9"
  "0.4.8-1-gABC123"
)

invalid=(
  ""
  "dev"
  "c798fa83"
  "mini-runtime-c798fa83"
  "v0.4.8-dirty"
  "v0.4.8-12-g2c749ddf9-dirty"
  "v0.4.8-12-g2c749ddf9-extra"
)

for version in "${valid[@]}"; do
  "$validator" "$version"
done

for version in "${invalid[@]}"; do
  if "$validator" "$version" >/dev/null 2>&1; then
    echo "expected version to be rejected: ${version:-<empty>}" >&2
    exit 1
  fi
done

echo "validate-cli-build-version tests passed"
