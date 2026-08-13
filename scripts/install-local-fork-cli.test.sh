#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
installer="$ROOT_DIR/scripts/install-local-fork-cli.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fixture="$tmp/repo"
mkdir -p "$fixture/scripts"
cp "$installer" "$fixture/scripts/install-local-fork-cli.sh"
cp "$ROOT_DIR/scripts/validate-cli-build-version.sh" "$fixture/scripts/validate-cli-build-version.sh"
chmod +x "$fixture/scripts/"*.sh
git -C "$fixture" init --quiet --initial-branch main
git -C "$fixture" config user.name test
git -C "$fixture" config user.email test@example.invalid
git -C "$fixture" add scripts
git -C "$fixture" commit --quiet -m fixture
git -C "$fixture" tag v1.2.3

output="$tmp/plan.txt"
HOME="$tmp/home" \
LOCAL_BIN_PREFIX="$tmp/prefix" \
MULTICA_LOCAL_INSTALL_ROOT="$tmp/install-root" \
"$fixture/scripts/install-local-fork-cli.sh" --plan --profile test-profile >"$output"

grep -Fq "profile:      test-profile" "$output"
grep -Fq "global CLI:   $tmp/prefix/bin/multica" "$output"
grep -Fq "plan only: no build, config write, command switch, or daemon restart performed" "$output"

if [[ -e "$tmp/prefix/bin/multica" || -e "$tmp/install-root" ]]; then
  echo "plan mode must not mutate install paths" >&2
  exit 1
fi

printf 'dirty\n' >>"$fixture/README.md"
if HOME="$tmp/home" "$fixture/scripts/install-local-fork-cli.sh" --plan >/dev/null 2>&1; then
  echo "dirty source must fail even in plan mode" >&2
  exit 1
fi

help="$($installer --help)"
grep -Fq -- "--profile <name>" <<<"$help"
grep -Fq "refuses dirty source or active daemon tasks" <<<"$help"

if "$installer" --unknown >/dev/null 2>&1; then
  echo "unknown flags must fail" >&2
  exit 1
fi

echo "install-local-fork-cli tests passed"
