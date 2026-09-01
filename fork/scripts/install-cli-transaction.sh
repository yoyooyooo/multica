#!/usr/bin/env bash
set -euo pipefail

CANDIDATE=""
PROFILE=""
SOURCE_SHA=""
GLOBAL_BIN=""
INSTALL_ROOT="${MULTICA_LOCAL_INSTALL_ROOT:-$HOME/.local/lib/multica}"
STATE_ROOT="${MULTICA_FORK_STATE_ROOT:-$HOME/.local/state/multica}"
PLAN=false
usage() {
  printf 'Usage: %s [--plan] --candidate <path> --profile <name> --source-sha <sha> --global-bin <path>\n' "$0"
}
while (($#)); do
  case "$1" in
    --plan) PLAN=true; shift ;;
    --candidate) CANDIDATE="${2:-}"; shift 2 ;;
    --profile) PROFILE="${2:-}"; shift 2 ;;
    --source-sha) SOURCE_SHA="${2:-}"; shift 2 ;;
    --global-bin) GLOBAL_BIN="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
if [[ ! -x "$CANDIDATE" || -z "$PROFILE" || -z "$GLOBAL_BIN" || ! "$SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  usage >&2
  exit 2
fi
if [[ ! -x "$GLOBAL_BIN" ]]; then
  echo "a previous CLI is required for transactional activation: $GLOBAL_BIN" >&2
  exit 1
fi

profile_cli() {
  env -u MULTICA_SERVER_URL -u MULTICA_APP_URL -u MULTICA_WORKSPACE_ID "$@" --profile "$PROFILE"
}
parse_status() {
  python3 -c 'import json,sys; v=json.load(sys.stdin); print(v.get("status", "stopped"), int(v.get("active_task_count", 0) or 0), int(v.get("pid", 0) or 0), v.get("cli_version", ""))'
}
daemon_executable() {
  local pid="$1"
  if [[ "$(uname -s)" == "Darwin" ]]; then
    lsof -a -p "$pid" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -1
  else
    readlink -f "/proc/$pid/exe"
  fi
}

candidate_version_output="$($CANDIDATE --version)"
if [[ "$candidate_version_output" != *"(commit: $SOURCE_SHA,"* ]]; then
  echo "candidate does not report source commit $SOURCE_SHA" >&2
  exit 1
fi
candidate_version="$(printf '%s\n' "$candidate_version_output" | awk 'NR == 1 {print $2}')"
current_status="$(profile_cli "$GLOBAL_BIN" daemon status --output json)"
read -r daemon_status active_tasks _ _ < <(printf '%s' "$current_status" | parse_status)
if ((active_tasks != 0)); then
  echo "daemon profile $PROFILE has $active_tasks active task(s)" >&2
  exit 1
fi
printf 'profile=%s\nsource_sha=%s\nversion=%s\nglobal_bin=%s\nactive_tasks=%s\n' \
  "$PROFILE" "$SOURCE_SHA" "$candidate_version" "$GLOBAL_BIN" "$active_tasks"
if "$PLAN"; then
  printf 'plan_only=true\n'
  exit 0
fi

auth_status="$(profile_cli "$CANDIDATE" auth status 2>&1)"
if [[ "$auth_status" != *"Server:"* || "$auth_status" != *"User:"* ]]; then
  echo "candidate failed profile authentication preflight" >&2
  exit 1
fi
profile_cli "$CANDIDATE" config set disable_auto_update true >/dev/null
latest_status="$(profile_cli "$GLOBAL_BIN" daemon status --output json)"
read -r _ latest_active_tasks _ _ < <(printf '%s' "$latest_status" | parse_status)
if ((latest_active_tasks != 0)); then
  echo "daemon profile $PROFILE claimed $latest_active_tasks task(s) during preflight" >&2
  exit 1
fi

timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
target_dir="$INSTALL_ROOT/${SOURCE_SHA}-${candidate_version}"
target_bin="$target_dir/multica"
backup_dir="$INSTALL_ROOT/backups/${timestamp}-${SOURCE_SHA:0:12}"
receipt_dir="$STATE_ROOT/deployments/${timestamp}-clean-rebuild-${SOURCE_SHA:0:12}-cli-${PROFILE}"
umask 077
mkdir -p "$target_dir" "$backup_dir" "$receipt_dir" "$(dirname "$GLOBAL_BIN")"
if [[ -e "$target_bin" ]]; then
  if ! cmp -s "$CANDIDATE" "$target_bin"; then
    echo "immutable CLI target exists with different bytes: $target_bin" >&2
    exit 1
  fi
else
  install -m 0755 "$CANDIDATE" "$target_bin"
fi
previous_bin="$backup_dir/multica"
cp -pP "$GLOBAL_BIN" "$previous_bin"
activate_tmp="$(dirname "$GLOBAL_BIN")/.multica-${timestamp}-$$"
ln -s "$target_bin" "$activate_tmp"
mv -f "$activate_tmp" "$GLOBAL_BIN"

rollback() {
  local reason="$1"
  echo "CLI activation failed: $reason" >&2
  cp -pP "$previous_bin" "$activate_tmp"
  mv -f "$activate_tmp" "$GLOBAL_BIN"
  profile_cli "$GLOBAL_BIN" daemon restart >/dev/null 2>&1 || true
  exit 1
}
if [[ "$daemon_status" == "running" || "$daemon_status" == "starting" ]]; then
  profile_cli "$GLOBAL_BIN" daemon restart || rollback "daemon restart failed"
else
  profile_cli "$GLOBAL_BIN" daemon start || rollback "daemon start failed"
fi
after_status="$(profile_cli "$GLOBAL_BIN" daemon status --output json)" || rollback "daemon status failed"
read -r after_state after_tasks after_pid after_version < <(printf '%s' "$after_status" | parse_status)
if [[ "$after_state" != "running" || "$after_version" != "$candidate_version" || "$after_pid" == 0 || "$after_tasks" != 0 ]]; then
  rollback "daemon status readback did not match candidate"
fi
running_exe="$(daemon_executable "$after_pid")"
if [[ "$(realpath "$running_exe")" != "$(realpath "$target_bin")" ]]; then
  rollback "daemon process image does not match immutable candidate"
fi
if [[ "$("$running_exe" --version)" != *"(commit: $SOURCE_SHA,"* ]]; then
  rollback "daemon process image commit readback failed"
fi

receipt="$receipt_dir/deployment-receipt.json"
RECEIPT_PROFILE="$PROFILE" RECEIPT_SHA="$SOURCE_SHA" RECEIPT_VERSION="$candidate_version" \
RECEIPT_TIMESTAMP="$timestamp" RECEIPT_GLOBAL_BIN="$GLOBAL_BIN" RECEIPT_TARGET_BIN="$target_bin" \
RECEIPT_PREVIOUS_BIN="$previous_bin" RECEIPT_PID="$after_pid" RECEIPT_RUNNING_EXE="$running_exe" \
python3 - "$receipt" <<'PY'
import json, os, sys
value = {
    "schema": "multica.clean-fork-cli-deployment.v1",
    "profile": os.environ["RECEIPT_PROFILE"],
    "source_sha": os.environ["RECEIPT_SHA"],
    "version": os.environ["RECEIPT_VERSION"],
    "completed_at": os.environ["RECEIPT_TIMESTAMP"],
    "global_command": os.environ["RECEIPT_GLOBAL_BIN"],
    "immutable_binary": os.environ["RECEIPT_TARGET_BIN"],
    "previous_command_backup": os.environ["RECEIPT_PREVIOUS_BIN"],
    "daemon": {"status": "running", "pid": int(os.environ["RECEIPT_PID"]), "executable": os.environ["RECEIPT_RUNNING_EXE"]},
    "rollback": "reactivate previous_command_backup and restart the same profile",
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, indent=2)
    handle.write("\n")
os.chmod(sys.argv[1], 0o600)
PY
printf 'status=deployed\nreceipt=%s\ndaemon_pid=%s\ndaemon_executable=%s\n' \
  "$receipt" "$after_pid" "$running_exe"
