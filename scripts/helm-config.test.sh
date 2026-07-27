#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="$ROOT_DIR/deploy/helm/multica"
HELM_TEST_ARGS=(
  --set-string backend.config.workloadAssertionIssuer=urn:multica:deployment:helm-config-test
  --set-string backend.config.workloadAssertionIssuerInstanceId=multica-helm-test
)

require_rendered_value() {
  local rendered=$1
  local expected=$2

  if ! grep -Fq "$expected" <<<"$rendered"; then
    echo "Missing expected Helm-rendered config value:"
    echo "  $expected"
    exit 1
  fi
}

helm lint "$CHART_DIR" "${HELM_TEST_ARGS[@]}"

default_config="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/configmap.yaml \
    "${HELM_TEST_ARGS[@]}"
)"
require_rendered_value "$default_config" 'MULTICA_VCS_INTEGRATION_ENABLED: "true"'
require_rendered_value "$default_config" 'MULTICA_WORKLOAD_ASSERTION_ISSUER: "urn:multica:deployment:helm-config-test"'
require_rendered_value "$default_config" 'MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID: "multica-helm-test"'

disabled_config="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/configmap.yaml \
    "${HELM_TEST_ARGS[@]}" \
    --set backend.config.vcsIntegrationEnabled=false
)"
require_rendered_value "$disabled_config" 'MULTICA_VCS_INTEGRATION_ENABLED: "false"'

echo "helm config rendering ok"
