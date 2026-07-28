#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
docker_bin=${DOCKER_BIN:-docker}
project=${COMPOSE_PROJECT_NAME:-multica}
legacy_volume=${MULTICA_LEGACY_UPLOADS_VOLUME:-${project}_backend_uploads}
target_dir=$root/data/uploads
copy_script=$root/scripts/copy-legacy-uploads.sh
receipt=$root/data/.legacy-backend-uploads-migration-v1.receipt

if ! "$docker_bin" volume inspect "$legacy_volume" >/dev/null 2>&1; then
  echo "legacy uploads migration: no volume named $legacy_volume; bind authority is already clean"
  exit 0
fi

users=$("$docker_bin" ps -q --filter "volume=$legacy_volume")
if [ -n "$users" ]; then
  for container in $users; do
    owner_project=$("$docker_bin" inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$container")
    owner_service=$("$docker_bin" inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$container")
    if [ "$owner_project" != "$project" ] || [ "$owner_service" != "backend" ]; then
      echo "legacy uploads volume is mounted by an unexpected container; refusing migration" >&2
      exit 3
    fi
  done
  echo "legacy uploads migration: stopping the owning backend before the copy"
  "$docker_bin" compose -f "$root/docker-compose.selfhost.yml" stop backend
fi

mkdir -p "$target_dir" "$(dirname -- "$receipt")"
"$docker_bin" run --rm \
  --mount "type=volume,src=$legacy_volume,dst=/source,readonly" \
  --mount "type=bind,src=$target_dir,dst=/target" \
  --mount "type=bind,src=$copy_script,dst=/copy-legacy-uploads.sh,readonly" \
  alpine:3.20 sh /copy-legacy-uploads.sh /source /target

umask 077
{
  echo "schema=multica.selfhost-uploads-migration.v1"
  echo "legacy_volume=$legacy_volume"
  echo "target=./data/uploads"
  echo "completed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$receipt"
echo "legacy uploads migration: verified copy complete; source volume retained: $legacy_volume"
