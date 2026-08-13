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
cat >"$fixture/fake-multica" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${MULTICA_SERVER_URL:-}" || -n "${MULTICA_APP_URL:-}" || -n "${MULTICA_WORKSPACE_ID:-}" ]]; then
  echo "profile env leaked" >&2
  exit 9
fi
case "$*" in
  *"auth status"*) printf 'Server:  http://profile.example\nUser:    test\n' ;;
  *"config set disable_auto_update true"*) : ;;
  *"daemon status --output json"*) printf '{"status":"running","active_task_count":0,"cli_version":"v1.2.3"}\n' ;;
  *"daemon start"*|*"daemon restart"*) : ;;
  *"--version"*) printf 'multica v1.2.3\n' ;;
  *) echo "unexpected fake CLI invocation: $*" >&2; exit 8 ;;
esac
FAKE
chmod +x "$fixture/fake-multica"
cat >"$fixture/Makefile" <<'MAKE'
build-cli:
	@mkdir -p server/bin
	@cp fake-multica server/bin/multica
MAKE

git -C "$fixture" init --quiet --initial-branch main
git -C "$fixture" config user.name test
git -C "$fixture" config user.email test@example.invalid
git -C "$fixture" add Makefile fake-multica scripts
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

install_output="$tmp/install.txt"
HOME="$tmp/home" \
LOCAL_BIN_PREFIX="$tmp/prefix" \
MULTICA_LOCAL_INSTALL_ROOT="$tmp/install-root" \
MULTICA_SERVER_URL="http://wrong.example" \
MULTICA_APP_URL="http://wrong.example" \
MULTICA_WORKSPACE_ID="wrong-workspace" \
"$fixture/scripts/install-local-fork-cli.sh" --profile test-profile >"$install_output"

test -L "$tmp/prefix/bin/multica"
grep -Fq "daemon:        profile test-profile running v1.2.3" "$install_output"
grep -Fq "auto-update:   disabled in profile config" "$install_output"

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
