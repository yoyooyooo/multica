#!/usr/bin/env bash
set -euo pipefail

project=${MULTICA_CC_COMPOSE_PROJECT:?MULTICA_CC_COMPOSE_PROJECT is required}
case "$project" in
  cc_multica_v0412_*) ;;
  *) echo "compose project must start with cc_multica_v0412_" >&2; exit 2 ;;
esac

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
expected_root=${MULTICA_CC_EXPECTED_WORKTREE:?MULTICA_CC_EXPECTED_WORKTREE is required}
expected_root=$(cd "$expected_root" && pwd -P)
if [[ "$root" != "$expected_root" ]]; then
  echo "completion harness must run from the admitted v0.4.12 worktree" >&2
  exit 2
fi

compose_file="$root/docker-compose.context-continuity-test.yml"
port=55449
database=multica_cc_v0412
user=multica_cc_v0412
password=multica_cc_v0412_local_only
dsn="postgres://${user}:${password}@127.0.0.1:${port}/${database}?sslmode=disable"

if [[ "$dsn" == *":5432/"* || "$dsn" == *"/multica?"* ]]; then
  echo "unsafe completion test DSN" >&2
  exit 2
fi

if [[ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$project")" ]]; then
  echo "refusing to reuse an existing compose project container" >&2
  exit 2
fi
if [[ -n "$(docker volume ls -q --filter "label=com.docker.compose.project=$project")" ]]; then
  echo "refusing to reuse an existing compose project volume" >&2
  exit 2
fi

export MULTICA_CC_ROOT="$root" MULTICA_CC_PROJECT="$project" MULTICA_CC_PORT="$port" MULTICA_CC_DATABASE="$database"
docker compose -f "$compose_file" -p "$project" config --format json |
  python3 -c '
import json, os, sys
cfg=json.load(sys.stdin)
svc=cfg["services"]["postgres"]
env=svc.get("environment", {})
ports=svc.get("ports", [])
assert env.get("POSTGRES_DB")==os.environ["MULTICA_CC_DATABASE"]
assert env.get("POSTGRES_USER")=="multica_cc_v0412"
assert len(ports)==1
p=ports[0]
assert str(p.get("published"))==os.environ["MULTICA_CC_PORT"]
assert str(p.get("target"))=="5432"
assert p.get("host_ip")=="127.0.0.1"
print("compose_config_admission=pass")
'

started=0
stop_only() {
  if [[ "$started" == 1 ]]; then
    docker compose -f "$compose_file" -p "$project" stop postgres >/dev/null
    echo "disposable_postgres_stopped=true project=$project port=$port database=$database"
    echo "cleanup_required=docker_compose_down_after_audit volume_retained=true"
  fi
}
trap stop_only EXIT

docker compose -f "$compose_file" -p "$project" up -d postgres
started=1
container=$(docker compose -f "$compose_file" -p "$project" ps -q postgres)
if [[ -z "$container" ]]; then
  echo "compose did not return a postgres container" >&2
  exit 3
fi

label_project=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$container")
label_workdir=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$container")
label_service=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$container")
host_binding=$(docker inspect --format '{{ (index (index .HostConfig.PortBindings "5432/tcp") 0).HostIp }}:{{ (index (index .HostConfig.PortBindings "5432/tcp") 0).HostPort }}' "$container")
container_database=$(docker exec "$container" printenv POSTGRES_DB)
if [[ "$label_project" != "$project" || "$label_workdir" != "$root" || "$label_service" != postgres || "$host_binding" != "127.0.0.1:$port" || "$container_database" != "$database" ]]; then
  echo "container admission failed" >&2
  exit 3
fi
if [[ "$host_binding" == *":5432" || "$container_database" == multica ]]; then
  echo "container admission selected a forbidden live endpoint" >&2
  exit 3
fi
printf 'container_admission=pass project=%s container=%s working_dir=%s host_binding=%s database=%s\n' \
  "$project" "${container:0:12}" "$label_workdir" "$host_binding" "$container_database"

for _ in $(seq 1 60); do
  health=$(docker inspect --format '{{.State.Health.Status}}' "$container")
  [[ "$health" == healthy ]] && break
  sleep 1
done
if [[ "${health:-}" != healthy ]]; then
  echo "dedicated postgres did not become healthy" >&2
  exit 3
fi

(
  cd "$root/server"
  DATABASE_URL="$dsn" go run ./cmd/migrate up
  DATABASE_URL="$dsn" go test -tags=contextcontinuitydb ./cmd/migrate -run TestContextContinuityMigrationConvergence -count=1
  DATABASE_URL="$dsn" go test ./internal/handler -run 'Test(CompletionPolicy|PullRequestCompletion|ConcurrentTopology|WorkspaceTopology|UpdateIssueRejectsCycle|PublicCompletion|CompleteIssueFromExternalPRPublic|CompleteIssueFromExternalPRCompletes|PublicExternal|ExternalPRPublic|ExternalPRConcurrent|ExternalPRActivity|ExternalPRInfrastructure|ExternalPRCompletionFailure|ExternalPRServiceToken|RegisterExternalTerminalFact|BatchDeleteSerializes|WorkspaceDeleteSerializes|GitHubWebhook(Serializes|Rejects|Returns)|GitHubMultiIssue|GitHubInstallationDelete|DeleteIssueSerializes|Webhook_(MergedPR_(RecordOnly|Advances|Waits|OnlyCloses)|ClosedSibling|LinkOnlySibling|HiddenBody)|VCSWebhook(Transaction|Serializes|_(RecordOnly|ForgejoMirrors|ReferenceOnly|GitlabMergeRequest|StaleEventDoesNotRewriteLink))|VCSConnectionDelete|ValidateWorkloadAssertionIssuer)' -count=1
  DATABASE_URL="$dsn" go test ./internal/handler -run 'Test(CreateWorkloadAssertion|GetCurrentExecutionContext|WorkspaceWorkloadAuthority)' -count=1
  DATABASE_URL="$dsn" go test ./cmd/server -run 'Test(WorkloadAndExternalPRRoutesUseRealAuthBoundaries|CompletionStageChainUsesRealHTTPAndPublicReadAPIs|SelfhostWorkloadAssertionIssuerFailsClosed|ActivityIssueUpdated_StatusActivityAlreadyRecorded)' -count=1
)

if [[ "${MULTICA_CC_RACE:-0}" == 1 ]]; then
  (
    cd "$root/server"
    DATABASE_URL="$dsn" go test -race ./internal/handler -run 'Test(CompletionPolicy|PullRequestCompletion|ConcurrentTopology|WorkspaceTopology|UpdateIssueRejectsCycle|PublicCompletion|CompleteIssueFromExternalPRPublic|CompleteIssueFromExternalPRCompletes|PublicExternal|ExternalPRPublic|ExternalPRConcurrent|ExternalPRActivity|ExternalPRInfrastructure|ExternalPRCompletionFailure|ExternalPRServiceToken|RegisterExternalTerminalFact|BatchDeleteSerializes|WorkspaceDeleteSerializes|GitHubWebhook(Serializes|Rejects|Returns)|GitHubMultiIssue|GitHubInstallationDelete|DeleteIssueSerializes|VCSWebhook(Transaction|Serializes|_StaleEventDoesNotRewriteLink)|VCSConnectionDelete)' -count=1
  )
fi

if [[ "${MULTICA_CC_FULL_HANDLER:-0}" == 1 ]]; then
  (
    cd "$root/server"
    DATABASE_URL="$dsn" go test ./internal/handler -count=1
  )
fi

if [[ "${MULTICA_CC_FULL_GO:-0}" == 1 ]]; then
  (
    cd "$root/server"
    DATABASE_URL="$dsn" go test ./internal/handler -count=1
    DATABASE_URL="$dsn" go test ./cmd/server -count=1
  )
  DATABASE_URL="$dsn" bash "$root/scripts/test-go.sh"
fi
