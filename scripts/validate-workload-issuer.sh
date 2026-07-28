#!/usr/bin/env bash
set -euo pipefail

env_file=${1:?usage: validate-workload-issuer.sh ENV_FILE}
issuer="$(sed -n 's/^MULTICA_WORKLOAD_ASSERTION_ISSUER=//p' "$env_file" | tail -1)"
trimmed_issuer="$(printf '%s' "$issuer" | awk '{$1=$1; print}')"
if [ -z "$trimmed_issuer" ] || [ "$trimmed_issuer" = "multica" ]; then
  echo "MULTICA_WORKLOAD_ASSERTION_ISSUER must be deployment-unique; run make selfhost-env" >&2
  exit 1
fi

issuer_id="$(sed -n 's/^MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=//p' "$env_file" | tail -1)"
trimmed_id="$(printf '%s' "$issuer_id" | awk '{$1=$1; print}')"
if [ "$issuer_id" != "$trimmed_id" ] || ! grep -Eq '^[A-Za-z0-9][A-Za-z0-9._:@/+\-]{0,254}$' <<<"$trimmed_id"; then
  echo "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID must be a canonical secret-free safe ID" >&2
  exit 1
fi
case "$(printf '%s' "$trimmed_id" | tr '[:upper:]' '[:lower:]')" in
  multica|placeholder|example|change-me|changeme|replace-me|issuer-instance-id)
    echo "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID must not be a placeholder" >&2
    exit 1
    ;;
esac
if [ "$trimmed_id" = "$trimmed_issuer" ]; then
  echo "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID must differ from MULTICA_WORKLOAD_ASSERTION_ISSUER" >&2
  exit 1
fi
if grep -Eq '(mat_[A-Za-z0-9_-]+|ags_sess_[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----)' <<<"$trimmed_id"; then
  echo "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID must be secret-free" >&2
  exit 1
fi
