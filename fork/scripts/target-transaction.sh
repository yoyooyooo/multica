#!/usr/bin/env bash
set -euo pipefail

TARGET=""
BACKEND_IMAGE=""
WEB_IMAGE=""
SOURCE_SHA=""
PLAN=false

usage() {
  printf 'Usage: %s [--plan] --target <name> --backend-image <tag> --web-image <tag> --source-sha <sha>\n' "$0"
}
while (($#)); do
  case "$1" in
    --plan) PLAN=true; shift ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --backend-image) BACKEND_IMAGE="${2:-}"; shift 2 ;;
    --web-image) WEB_IMAGE="${2:-}"; shift 2 ;;
    --source-sha) SOURCE_SHA="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
if [[ -z "$TARGET" || -z "$BACKEND_IMAGE" || -z "$WEB_IMAGE" || ! "$SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  usage >&2
  exit 2
fi

for container in multica-backend-1 multica-frontend-1 multica-postgres-1; do
  if ! docker inspect "$container" >/dev/null 2>&1; then
    echo "required container is missing: $container" >&2
    exit 1
  fi
done

backend_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$BACKEND_IMAGE")"
web_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$WEB_IMAGE")"
if [[ "$backend_revision" != "$SOURCE_SHA" || "$web_revision" != "$SOURCE_SHA" ]]; then
  echo "candidate image revision does not match $SOURCE_SHA" >&2
  exit 1
fi

working_dir="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' multica-backend-1)"
config_csv="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project.config_files" }}' multica-backend-1)"
if [[ -z "$working_dir" || -z "$config_csv" ]]; then
  echo "current Compose authority labels are missing" >&2
  exit 1
fi
IFS=',' read -r -a config_files <<< "$config_csv"
compose_args=()
for file in "${config_files[@]}"; do
  if [[ ! -f "$file" ]]; then
    echo "current Compose file is missing: $file" >&2
    exit 1
  fi
  compose_args+=( -f "$file" )
done

old_backend_image="$(docker inspect -f '{{.Config.Image}}' multica-backend-1)"
old_web_image="$(docker inspect -f '{{.Config.Image}}' multica-frontend-1)"
old_backend_id="$(docker inspect -f '{{.Image}}' multica-backend-1)"
old_web_id="$(docker inspect -f '{{.Image}}' multica-frontend-1)"
old_uploads_source="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data/uploads"}}{{.Source}}{{end}}{{end}}' multica-backend-1)"
network="$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' multica-backend-1)"
external_pr_token="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' multica-backend-1 | sed -n 's/^MULTICA_EXTERNAL_PR_SERVICE_TOKEN=//p')"
case "$TARGET" in
  mini) external_pr_instance="mini" ;;
  imile-win) external_pr_instance="imile-win" ;;
  *) echo "unsupported deployment target: $TARGET" >&2; exit 2 ;;
esac
if [[ -z "$external_pr_token" ]]; then
  echo "current External PR service token is empty" >&2
  exit 1
fi

read -r active_tasks active_autopilots < <(
  docker exec multica-postgres-1 sh -lc 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F " " -c "SELECT (SELECT count(*) FROM agent_task_queue WHERE status IN ('\''queued'\'','\''dispatched'\'','\''running'\'','\''waiting_local_directory'\'')), (SELECT count(*) FROM autopilot_run WHERE status IN ('\''queued'\'','\''running'\''));"'
)
if ((active_tasks != 0 || active_autopilots != 0)); then
  echo "target is not idle: tasks=$active_tasks autopilots=$active_autopilots" >&2
  exit 1
fi

printf 'target=%s\nsource_sha=%s\nworking_dir=%s\nactive_tasks=%s\nactive_autopilots=%s\n' \
  "$TARGET" "$SOURCE_SHA" "$working_dir" "$active_tasks" "$active_autopilots"
printf 'old_backend_image=%s\nold_web_image=%s\nuploads_source=%s\nnetwork=%s\n' \
  "$old_backend_image" "$old_web_image" "$old_uploads_source" "$network"
printf 'candidate_backend_image=%s\ncandidate_web_image=%s\nexternal_pr_instance=%s\nexternal_pr_token=set\n' \
  "$BACKEND_IMAGE" "$WEB_IMAGE" "$external_pr_instance"
if "$PLAN"; then
  printf 'plan_only=true\n'
  exit 0
fi

timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
short_sha="${SOURCE_SHA:0:12}"
state_root="${MULTICA_FORK_STATE_ROOT:-$HOME/.local/state/multica}"
receipt_dir="$state_root/deployments/${timestamp}-clean-rebuild-${short_sha}-${TARGET}"
umask 077
mkdir -p "$receipt_dir"
override="$receipt_dir/compose.images.yml"
cat > "$override" <<'YAML'
services:
  backend:
    image: ${FORK_BACKEND_IMAGE:?}
    environment:
      MULTICA_EXTERNAL_PR_SERVICE_TOKEN: ${MULTICA_EXTERNAL_PR_SERVICE_TOKEN:?}
      MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID: ${MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID:?}
      MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS: ags
  frontend:
    image: ${FORK_WEB_IMAGE:?}
YAML
chmod 0600 "$override"

backup="$receipt_dir/pre-switch-frozen.pg_dump-Fc"
docker exec multica-postgres-1 sh -lc 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup"
chmod 0600 "$backup"
if [[ ! -s "$backup" ]]; then
  echo "database backup is empty" >&2
  exit 1
fi
backup_sha256="$(sha256sum "$backup" 2>/dev/null | awk '{print $1}' || shasum -a 256 "$backup" | awk '{print $1}')"

postgres_image="$(docker inspect -f '{{.Config.Image}}' multica-postgres-1)"
verify_volume="multica_restore_verify_${timestamp}_${short_sha}_${TARGET//-/_}"
verify_container="multica-restore-verify-${timestamp}-${short_sha}-${TARGET}"
docker volume create "$verify_volume" >/dev/null
docker run -d --name "$verify_container" \
  -e POSTGRES_PASSWORD=restore-verification-only \
  -v "$verify_volume:/var/lib/postgresql/data" \
  "$postgres_image" >/dev/null
for _ in $(seq 1 60); do
  if docker exec "$verify_container" pg_isready -U postgres >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$verify_container" createdb -U postgres multica_restore_verify
docker exec -i "$verify_container" pg_restore --exit-on-error -U postgres -d multica_restore_verify < "$backup"
read -r restored_links restored_upstream_ledger < <(
  docker exec "$verify_container" psql -v ON_ERROR_STOP=1 -U postgres -d multica_restore_verify -At -F ' ' \
    -c "SELECT (SELECT count(*) FROM external_pull_request_link), (SELECT count(*) FROM schema_migrations);"
)
if ((restored_upstream_ledger == 0)); then
  echo "restored database has no upstream migration ledger" >&2
  exit 1
fi
docker stop "$verify_container" >/dev/null

cd "$working_dir"
compose_fork() {
  FORK_BACKEND_IMAGE="$BACKEND_IMAGE" FORK_WEB_IMAGE="$WEB_IMAGE" \
  MULTICA_EXTERNAL_PR_SERVICE_TOKEN="$external_pr_token" \
  MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID="$external_pr_instance" \
    docker compose "${compose_args[@]}" -f "$override" "$@"
}
compose_fork config >/dev/null
compose_fork up -d --no-build

ready=""
for _ in $(seq 1 90); do
  ready="$(docker exec multica-backend-1 wget -qO- http://127.0.0.1:8080/readyz 2>/dev/null || true)"
  if [[ "$ready" == *'"status":"ok"'* ]]; then
    break
  fi
  sleep 2
done
if [[ "$ready" != *'"status":"ok"'* ]]; then
  echo "candidate backend did not become ready; restore verified dump before starting an old image" >&2
  exit 1
fi
frontend_status="$(docker exec multica-frontend-1 wget -qSO- http://127.0.0.1:3000/login -O /dev/null 2>&1 | awk '/HTTP\// {code=$2} END {print code}')"
if [[ "$frontend_status" != "200" ]]; then
  echo "candidate frontend login returned $frontend_status" >&2
  exit 1
fi
external_pr_unauthorized_status="$(docker exec multica-backend-1 sh -lc 'wget -S -O /dev/null --header="Content-Type: application/json" --post-data="{}" http://127.0.0.1:8080/api/integrations/external-pr/links 2>&1 || true' | awk '/HTTP\// {code=$2} END {print code}')"
external_pr_authenticated_status="$(docker exec multica-backend-1 sh -lc 'wget -S -O /dev/null --header="Content-Type: application/json" --header="Authorization: Bearer $MULTICA_EXTERNAL_PR_SERVICE_TOKEN" --post-data="{}" http://127.0.0.1:8080/api/integrations/external-pr/links 2>&1 || true' | awk '/HTTP\// {code=$2} END {print code}')"
if [[ "$external_pr_unauthorized_status" != "401" || "$external_pr_authenticated_status" != "400" ]]; then
  echo "External PR authenticated route smoke failed: unauthorized=$external_pr_unauthorized_status authenticated=$external_pr_authenticated_status" >&2
  exit 1
fi

new_backend_id="$(docker inspect -f '{{.Image}}' multica-backend-1)"
new_web_id="$(docker inspect -f '{{.Image}}' multica-frontend-1)"
new_backend_revision="$(docker inspect -f '{{ index .Config.Labels "org.opencontainers.image.revision" }}' multica-backend-1)"
new_web_revision="$(docker inspect -f '{{ index .Config.Labels "org.opencontainers.image.revision" }}' multica-frontend-1)"
new_uploads_source="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data/uploads"}}{{.Source}}{{end}}{{end}}' multica-backend-1)"
new_external_pr_instance="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' multica-backend-1 | sed -n 's/^MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID=//p')"
new_external_pr_token="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' multica-backend-1 | sed -n 's/^MULTICA_EXTERNAL_PR_SERVICE_TOKEN=//p')"
if [[ "$new_backend_revision" != "$SOURCE_SHA" || "$new_web_revision" != "$SOURCE_SHA" || "$new_uploads_source" != "$old_uploads_source" ||
      "$new_external_pr_instance" != "$external_pr_instance" || -z "$new_external_pr_token" ]]; then
  echo "runtime revision, uploads, or External PR configuration readback failed" >&2
  exit 1
fi
read -r upstream_ledger fork_ledger live_links < <(
  docker exec multica-postgres-1 sh -lc 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F " " -c "SELECT (SELECT count(*) FROM schema_migrations), (SELECT count(*) FROM fork_schema_migrations), (SELECT count(*) FROM external_pull_request_link l JOIN issue i ON i.id=l.issue_id JOIN workspace w ON w.id=l.workspace_id);"'
)
if ((fork_ledger == 0)); then
  echo "fork migration ledger readback is empty" >&2
  exit 1
fi

RECEIPT_TARGET="$TARGET" \
RECEIPT_TIMESTAMP="$timestamp" \
RECEIPT_SOURCE_SHA="$SOURCE_SHA" \
RECEIPT_BACKEND_IMAGE="$BACKEND_IMAGE" \
RECEIPT_BACKEND_ID="$new_backend_id" \
RECEIPT_WEB_IMAGE="$WEB_IMAGE" \
RECEIPT_WEB_ID="$new_web_id" \
RECEIPT_OLD_BACKEND_IMAGE="$old_backend_image" \
RECEIPT_OLD_BACKEND_ID="$old_backend_id" \
RECEIPT_OLD_WEB_IMAGE="$old_web_image" \
RECEIPT_OLD_WEB_ID="$old_web_id" \
RECEIPT_BACKUP="$backup" \
RECEIPT_BACKUP_SHA256="$backup_sha256" \
RECEIPT_VERIFY_CONTAINER="$verify_container" \
RECEIPT_VERIFY_VOLUME="$verify_volume" \
RECEIPT_RESTORED_LINKS="$restored_links" \
RECEIPT_UPSTREAM_LEDGER="$upstream_ledger" \
RECEIPT_FORK_LEDGER="$fork_ledger" \
RECEIPT_FRONTEND_STATUS="$frontend_status" \
RECEIPT_EXTERNAL_PR_INSTANCE="$new_external_pr_instance" \
RECEIPT_EXTERNAL_PR_UNAUTHORIZED_STATUS="$external_pr_unauthorized_status" \
RECEIPT_EXTERNAL_PR_AUTHENTICATED_STATUS="$external_pr_authenticated_status" \
RECEIPT_LIVE_LINKS="$live_links" \
RECEIPT_UPLOADS_SOURCE="$new_uploads_source" \
RECEIPT_NETWORK="$network" \
python3 - "$receipt_dir/deployment-receipt.json" <<'PY'
import json, os, sys
path = sys.argv[1]
env = os.environ
value = {
  "schema": "multica.clean-fork-deployment-receipt.v1",
  "target": env["RECEIPT_TARGET"],
  "completed_at": env["RECEIPT_TIMESTAMP"],
  "source_sha": env["RECEIPT_SOURCE_SHA"],
  "images": {
    "backend": {"tag": env["RECEIPT_BACKEND_IMAGE"], "id": env["RECEIPT_BACKEND_ID"]},
    "web": {"tag": env["RECEIPT_WEB_IMAGE"], "id": env["RECEIPT_WEB_ID"]},
  },
  "previous_images": {
    "backend": {"tag": env["RECEIPT_OLD_BACKEND_IMAGE"], "id": env["RECEIPT_OLD_BACKEND_ID"]},
    "web": {"tag": env["RECEIPT_OLD_WEB_IMAGE"], "id": env["RECEIPT_OLD_WEB_ID"]},
  },
  "database": {
    "backup": os.path.basename(env["RECEIPT_BACKUP"]),
    "backup_sha256": env["RECEIPT_BACKUP_SHA256"],
    "restore_verify_container": env["RECEIPT_VERIFY_CONTAINER"],
    "restore_verify_volume": env["RECEIPT_VERIFY_VOLUME"],
    "restored_external_pr_rows": int(env["RECEIPT_RESTORED_LINKS"]),
    "upstream_ledger_rows": int(env["RECEIPT_UPSTREAM_LEDGER"]),
    "fork_ledger_rows": int(env["RECEIPT_FORK_LEDGER"]),
  },
  "runtime": {
    "readyz": "ok", "frontend_login_http_status": int(env["RECEIPT_FRONTEND_STATUS"]),
    "live_external_pr_rows": int(env["RECEIPT_LIVE_LINKS"]),
    "uploads_source": env["RECEIPT_UPLOADS_SOURCE"], "network": env["RECEIPT_NETWORK"],
    "external_pr": {
      "instance": env["RECEIPT_EXTERNAL_PR_INSTANCE"], "service_token": "set",
      "unauthorized_http_status": int(env["RECEIPT_EXTERNAL_PR_UNAUTHORIZED_STATUS"]),
      "authenticated_validation_http_status": int(env["RECEIPT_EXTERNAL_PR_AUTHENTICATED_STATUS"]),
    },
  },
  "rollback": {
    "database_restore_source": os.path.basename(env["RECEIPT_BACKUP"]),
    "backend_image": env["RECEIPT_OLD_BACKEND_IMAGE"], "web_image": env["RECEIPT_OLD_WEB_IMAGE"],
    "rule": "stop app, restore verified database dump, then start previous images",
  },
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(value, handle, indent=2)
    handle.write("\n")
os.chmod(path, 0o600)
PY
printf 'status=deployed\nreceipt=%s\nbackup_sha256=%s\n' "$receipt_dir/deployment-receipt.json" "$backup_sha256"
