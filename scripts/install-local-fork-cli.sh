#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROFILE="${PROFILE:-mini}"
LOCAL_BIN_PREFIX="${LOCAL_BIN_PREFIX:-$HOME/.local}"
GLOBAL_BIN="$LOCAL_BIN_PREFIX/bin/multica"
INSTALL_ROOT="${MULTICA_LOCAL_INSTALL_ROOT:-$HOME/.local/lib/multica}"
PLAN=false

usage() {
  cat <<'EOF'
Build the current clean fork source, install it as the user-global Multica CLI,
and restart the selected daemon from the same binary.

Usage:
  scripts/install-local-fork-cli.sh [--plan] [--profile <name>]

Environment:
  PROFILE                     Daemon profile (default: mini)
  LOCAL_BIN_PREFIX            User-global prefix (default: ~/.local)
  MULTICA_LOCAL_INSTALL_ROOT  Immutable binary store (default: ~/.local/lib/multica)

The installer refuses dirty source or active daemon tasks, persists
disable_auto_update=true for the profile, preserves the previous global command,
and automatically restores it if daemon restart or version verification fails.
EOF
}

while (($# > 0)); do
  case "$1" in
    --plan)
      PLAN=true
      shift
      ;;
    --profile)
      if (($# < 2)); then
        echo "--profile requires a value" >&2
        exit 2
      fi
      PROFILE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

cd "$ROOT_DIR"

version="$(git describe --tags --match 'v[0-9]*' --always --dirty)"
commit="$(git rev-parse HEAD)"
short_commit="$(git rev-parse --short HEAD)"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "refusing local fork CLI install from a dirty worktree" >&2
  exit 1
fi
bash scripts/validate-cli-build-version.sh "$version"

target_dir="$INSTALL_ROOT/${commit}-${version}"
target_bin="$target_dir/multica"

cat <<EOF
source:       $ROOT_DIR
commit:       $commit
version:      $version
profile:      $PROFILE
global CLI:   $GLOBAL_BIN
immutable CLI: $target_bin
EOF

if "$PLAN"; then
  cat <<'EOF'
plan only: no build, config write, command switch, or daemon restart performed
EOF
  exit 0
fi

current_status='{"status":"stopped"}'
daemon_was_running=false
if [[ -x "$GLOBAL_BIN" ]]; then
  current_status="$($GLOBAL_BIN --profile "$PROFILE" daemon status --output json)"
fi
read -r daemon_status active_tasks < <(python3 -c '
import json, sys
value = json.load(sys.stdin)
print(value.get("status", "stopped"), int(value.get("active_task_count", 0) or 0))
' <<<"$current_status")
if [[ "$daemon_status" == "running" || "$daemon_status" == "starting" ]]; then
  daemon_was_running=true
fi
if ((active_tasks != 0)); then
  echo "refusing CLI replacement: daemon profile $PROFILE has $active_tasks active task(s)" >&2
  exit 1
fi

make build-cli
built_bin="$ROOT_DIR/server/bin/multica"
if [[ ! -x "$built_bin" ]]; then
  echo "expected CLI build output is missing: $built_bin" >&2
  exit 1
fi

mkdir -p "$target_dir" "$LOCAL_BIN_PREFIX/bin" "$INSTALL_ROOT/backups"
if [[ -e "$target_bin" ]]; then
  if ! cmp -s "$built_bin" "$target_bin"; then
    echo "immutable install target already exists with different bytes: $target_bin" >&2
    exit 1
  fi
else
  install -m 0755 "$built_bin" "$target_bin"
fi

"$target_bin" --profile "$PROFILE" config set disable_auto_update true >/dev/null

timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
backup_dir="$INSTALL_ROOT/backups/$timestamp-${short_commit}"
previous_bin=""
link_tmp="$LOCAL_BIN_PREFIX/bin/.multica-${timestamp}-$$"
ln -s "$target_bin" "$link_tmp"

if [[ -e "$GLOBAL_BIN" || -L "$GLOBAL_BIN" ]]; then
  mkdir -p "$backup_dir"
  previous_bin="$backup_dir/multica"
  mv "$GLOBAL_BIN" "$previous_bin"
fi
if ! mv "$link_tmp" "$GLOBAL_BIN"; then
  if [[ -n "$previous_bin" && -e "$previous_bin" ]]; then
    mv "$previous_bin" "$GLOBAL_BIN"
  fi
  echo "failed to activate $GLOBAL_BIN" >&2
  exit 1
fi

rollback() {
  local reason="$1"
  local failed_dir="$INSTALL_ROOT/backups/${timestamp}-${short_commit}-failed"
  echo "activation failed: $reason" >&2
  mkdir -p "$failed_dir"
  if [[ -e "$GLOBAL_BIN" || -L "$GLOBAL_BIN" ]]; then
    mv "$GLOBAL_BIN" "$failed_dir/multica"
  fi
  if [[ -n "$previous_bin" && ( -e "$previous_bin" || -L "$previous_bin" ) ]]; then
    mv "$previous_bin" "$GLOBAL_BIN"
    "$GLOBAL_BIN" --profile "$PROFILE" daemon restart >/dev/null 2>&1 || true
    echo "restored previous global CLI: $GLOBAL_BIN" >&2
  else
    echo "no previous global CLI existed; failed candidate retained at $failed_dir/multica" >&2
  fi
  exit 1
}

if "$daemon_was_running"; then
  "$GLOBAL_BIN" --profile "$PROFILE" daemon restart || rollback "daemon restart failed"
else
  "$GLOBAL_BIN" --profile "$PROFILE" daemon start || rollback "daemon start failed"
fi

after_status="$($GLOBAL_BIN --profile "$PROFILE" daemon status --output json)" || rollback "daemon status failed"
if ! python3 -c '
import json, sys
expected = sys.argv[1]
value = json.load(sys.stdin)
if value.get("status") != "running" or value.get("cli_version") != expected:
    raise SystemExit(1)
' "$version" <<<"$after_status"
then
  rollback "daemon did not report running version $version"
fi

cat <<EOF
installed:     $GLOBAL_BIN -> $target_bin
daemon:        profile $PROFILE running $version
auto-update:   disabled in profile config
previous CLI:  ${previous_bin:-none}
EOF
