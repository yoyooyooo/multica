#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"

if [[ "$version" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
   [[ "$version" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+-[0-9]+-g[0-9a-fA-F]+$ ]]; then
  exit 0
fi

cat >&2 <<EOF
invalid multica CLI build version: ${version:-<empty>}
expected a clean release or git-describe value:
  vX.Y.Z
  vX.Y.Z-N-g<hex-sha>
arbitrary labels and dirty builds are not deployable because daemon capability gates reject them
EOF
exit 1
