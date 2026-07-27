#!/usr/bin/env bash
set -euo pipefail

env_file=${1:?usage: ensure-workload-issuer.sh ENV_FILE [development|deployment|worktree]}
kind=${2:-deployment}

if [ ! -f "$env_file" ]; then
  echo "environment file does not exist: $env_file" >&2
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required to generate stable workload assertion identity" >&2
  exit 1
fi

replace_or_append() {
  local name=$1 value=$2 tmp
  tmp="${env_file}.${name}.$$"
  awk -v name="$name" -v value="$value" '
    BEGIN { replaced=0 }
    index($0, name "=") == 1 {
      if (!replaced) print name "=" value
      replaced=1
      next
    }
    { print }
    END { if (!replaced) print name "=" value }
  ' "$env_file" >"$tmp"
  chmod --reference="$env_file" "$tmp" 2>/dev/null || chmod 600 "$tmp"
  mv "$tmp" "$env_file"
}

current_issuer="$(sed -n 's/^MULTICA_WORKLOAD_ASSERTION_ISSUER=//p' "$env_file" | tail -1)"
trimmed_issuer="$(printf '%s' "$current_issuer" | awk '{$1=$1; print}')"
if [ -z "$trimmed_issuer" ] || [ "$trimmed_issuer" = "multica" ]; then
  trimmed_issuer="urn:multica:${kind}:$(openssl rand -hex 16)"
  replace_or_append MULTICA_WORKLOAD_ASSERTION_ISSUER "$trimmed_issuer"
  echo "generated a stable ${kind}-unique workload assertion JWT issuer"
else
  echo "workload assertion JWT issuer already configured"
fi

current_id="$(sed -n 's/^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=//p' "$env_file" | tail -1)"
trimmed_id="$(printf '%s' "$current_id" | awk '{$1=$1; print}')"
case "$(printf '%s' "$trimmed_id" | tr '[:upper:]' '[:lower:]')" in
  ""|multica|placeholder|example|change-me|changeme|replace-me|issuer-instance-id)
    trimmed_id="multica-$(openssl rand -hex 16)"
    replace_or_append MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID "$trimmed_id"
    echo "generated a stable secret-free workload assertion issuer instance ID; configure this exact ID as AGS trusted_issuers[].id"
    ;;
  *)
    echo "workload assertion issuer instance ID already configured"
    ;;
esac
