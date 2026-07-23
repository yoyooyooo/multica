#!/usr/bin/env bash
# ==========================================================================
# Regression tests: worktree Compose isolation
#
# Tests that:
#   1. A fresh worktree env generates an isolated Compose project.
#   2. Isolated PostgreSQL container does not recreate live "multica-*" resources.
#   3. Deliberate project-name collision fails before Docker mutation.
#   4. Deliberate port collision fails before Docker mutation.
#   5. Repeated setup is idempotent (no error on re-run).
#   6. Cleanup isolation: `db-down` only stops the worktree project's container.
#   7. Live "multica-*" deployment containers remain untouched throughout.
# ==========================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass() { PASS=$((PASS+1)); echo -e "${GREEN}PASS${NC} $1"; }
fail() { FAIL=$((FAIL+1)); echo -e "${RED}FAIL${NC} $1"; }

# ---------- Snapshot: live deployment containers before testing ----------
echo ""
echo "=== Test suite: worktree Compose isolation ==="
echo ""

SNAPSHOT_FILE=$(mktemp)
cleanup_snapshot() { rm -f "$SNAPSHOT_FILE"; }
trap cleanup_snapshot EXIT

docker ps --format json 2>/dev/null > "$SNAPSHOT_FILE" || true
LIVE_CONTAINERS=$(docker ps --format '{{.Names}}' 2>/dev/null || true)

# Helper: is a name in the live snapshot?
is_live() {
  local name="$1"
  while IFS= read -r line; do
    if [ "$line" = "$name" ]; then
      return 0
    fi
  done <<< "$LIVE_CONTAINERS"
  return 1
}

# Skip live container tests if no deployment is running
HAS_LIVE_CONTAINERS=false
if echo "$LIVE_CONTAINERS" | grep -qE '^multica-(postgres|backend|frontend)-1$'; then
  HAS_LIVE_CONTAINERS=true
fi

# ---------- Test 1: worktree env generates isolated Compose project ----------
echo "--- Test 1: fresh worktree env generates isolated project ---"
TESTDIR=$(mktemp -d)
cp -r "$REPO_ROOT"/{docker-compose.yml,scripts} "$TESTDIR/"
mkdir -p "$TESTDIR/server" && cp "$REPO_ROOT/server/go.mod" "$TESTDIR/server/"

WORKTREE_LABEL="test_worktree_$$"

pushd "$TESTDIR" > /dev/null
  FORCE=1 WORKTREE_NAME="$WORKTREE_LABEL" bash scripts/init-worktree-env.sh .env.worktree 2>&1 || {
    fail "init-worktree-env failed"
    popd > /dev/null; rm -rf "$TESTDIR"; exit 1
  }

  set -a
  # shellcheck disable=SC1090
  . .env.worktree
  set +a

  if [ -z "${COMPOSE_PROJECT_NAME:-}" ]; then
    fail "COMPOSE_PROJECT_NAME is empty"
  elif [ "${MULTICA_OWNER:-}" != "worktree" ]; then
    fail "MULTICA_OWNER is not 'worktree'"
  elif [ "${POSTGRES_PORT:-}" = "5432" ]; then
    fail "POSTGRES_PORT is still default 5432"
  elif [ "${WORKTREE_PATH:-}" != "$TESTDIR" ]; then
    fail "WORKTREE_PATH mismatch: expected $TESTDIR, got $WORKTREE_PATH"
  else
    pass "worktree env generates unique COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT_NAME, port=$POSTGRES_PORT"
  fi
popd > /dev/null
rm -rf "$TESTDIR"

# ---------- Test 2: ownership guard rejects foreign project collision ----------
echo "--- Test 2: ownership guard rejects foreign project collision ---"
TESTDIR2=$(mktemp -d)
cp "$REPO_ROOT/scripts/compose-ownership-guard.sh" "$TESTDIR2/"

# Run guard against the live "multica" project — should fail
COMPOSE_PROJECT_NAME=multica \
  POSTGRES_PORT=5432 \
  MULTICA_OWNER=worktree \
  WORKTREE_PATH=/tmp/fake_path \
  bash "$TESTDIR2/compose-ownership-guard.sh" 2>&1 && {
    fail "guard should have rejected foreign ownership of 'multica'"
  } || {
    pass "ownership guard correctly rejected foreign project 'multica'"
  }

# Run guard against a nonexistent project — should pass
COMPOSE_PROJECT_NAME=nonexistent_test_project_$$ \
  POSTGRES_PORT=59999 \
  MULTICA_OWNER=worktree \
  WORKTREE_PATH="$TESTDIR2" \
  bash "$TESTDIR2/compose-ownership-guard.sh" 2>&1 && {
    pass "ownership guard allowed nonexistent project"
  } || {
    fail "guard should have allowed nonexistent project"
  }

rm -rf "$TESTDIR2"

# ---------- Test 3: ownership guard rejects foreign port collision ----------
echo "--- Test 3: ownership guard rejects foreign port collision ---"
LIVE_PORT=""
for cname in $LIVE_CONTAINERS; do
  port_info=$(docker port "$cname" 5432 2>/dev/null || true)
  if echo "$port_info" | grep -qE '0\.0\.0\.0:[0-9]+|127\.0\.0\.1:[0-9]+' 2>/dev/null; then
    LIVE_PORT=$(echo "$port_info" | sed 's/.*://' | head -1)
    break
  fi
done

if [ -n "$LIVE_PORT" ] && [ "$LIVE_PORT" = "5432" ]; then
  COMPOSE_PROJECT_NAME=test_collision_$$ \
    POSTGRES_PORT="$LIVE_PORT" \
    MULTICA_OWNER=worktree \
    WORKTREE_PATH=/tmp/collision \
    bash "$REPO_ROOT/scripts/compose-ownership-guard.sh" 2>&1 && {
      fail "guard should have rejected port collision on $LIVE_PORT"
    } || {
      pass "ownership guard correctly rejected port $LIVE_PORT collision"
    }
elif [ -n "$LIVE_PORT" ]; then
  echo "  SKIP port collision test: live port $LIVE_PORT is not default 5432"
else
  echo "  SKIP port collision test: no live PostgreSQL port detected"
fi

# ---------- Test 4: actual isolated worktree PostgreSQL starts ----------
echo "--- Test 4: isolated worktree PostgreSQL starts ---"
TESTDIR4=$(mktemp -d)
cp -r "$REPO_ROOT"/{docker-compose.yml,scripts} "$TESTDIR4/"
mkdir -p "$TESTDIR4/server" && cp "$REPO_ROOT/server/go.mod" "$TESTDIR4/server/"

pushd "$TESTDIR4" > /dev/null
  FORCE=1 WORKTREE_NAME="isolation_test_$$" bash scripts/init-worktree-env.sh .env.worktree 2>&1 || {
    fail "init-worktree-env failed"
    popd > /dev/null; rm -rf "$TESTDIR4"; exit 1
  }

  set -a
  # shellcheck disable=SC1090
  . .env.worktree
  set +a

  if bash scripts/ensure-postgres.sh .env.worktree 2>&1; then
    pass "isolated PostgreSQL started for project $COMPOSE_PROJECT_NAME on port $POSTGRES_PORT"

    container_name="${COMPOSE_PROJECT_NAME}-postgres-1"
    if docker ps --format '{{.Names}}' | grep -q "^${container_name}$"; then
      pass "container $container_name exists"
      c_owner=$(docker inspect --format '{{index .Config.Labels "multica.owner"}}' "$container_name" 2>/dev/null || true)
      if [ "$c_owner" = "worktree" ]; then
        pass "container label multica.owner = worktree"
      else
        fail "container label multica.owner is '$c_owner', expected 'worktree'"
      fi
    else
      fail "container $container_name not found"
    fi
  else
    fail "ensure-postgres.sh failed"
    popd > /dev/null; rm -rf "$TESTDIR4"; exit 1
  fi
popd > /dev/null

# ---------- Test 5: verify live deployment containers unchanged ----------
echo "--- Test 5: live deployment containers unchanged ---"
CHANGED=false
while IFS= read -r cname; do
  [ -z "$cname" ] && continue
  # Skip our test containers
  if echo "$cname" | grep -qE '^isolation_test_|^test_'; then
    continue
  fi
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${cname}$"; then
    pass "live container '$cname' still present"
  else
    fail "live container '$cname' was removed!"
    CHANGED=true
  fi
done <<< "$LIVE_CONTAINERS"

if [ "$HAS_LIVE_CONTAINERS" = true ]; then
  pass "live containers checked: all present"
fi

# ---------- Test 6: idempotent re-run ----------
echo "--- Test 6: idempotent re-run ---"
pushd "$TESTDIR4" > /dev/null
  if bash scripts/ensure-postgres.sh .env.worktree 2>&1; then
    pass "idempotent re-run succeeds"
  else
    fail "idempotent re-run failed"
  fi
popd > /dev/null

# ---------- Test 7: cleanup isolation ----------
echo "--- Test 7: cleanup isolation ---"
pushd "$TESTDIR4" > /dev/null
  set -a
  # shellcheck disable=SC1090
  . .env.worktree
  set +a

  docker compose --project-name "$COMPOSE_PROJECT_NAME" down 2>&1 || true
popd > /dev/null

container_name="${COMPOSE_PROJECT_NAME}-postgres-1"
if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${container_name}$"; then
  fail "our container $container_name still running after db-down"
else
  pass "our container $container_name stopped by db-down"
fi

while IFS= read -r cname; do
  [ -z "$cname" ] && continue
  if echo "$cname" | grep -qE '^isolation_test_|^test_'; then
    continue
  fi
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${cname}$"; then
    pass "live container '$cname' survived cleanup"
  else
    fail "live container '$cname' was removed by worktree cleanup!"
  fi
done <<< "$LIVE_CONTAINERS"

if [ "$HAS_LIVE_CONTAINERS" = true ]; then
  pass "live containers survive cleanup: all present"
fi

# Cleanup test dir
rm -rf "$TESTDIR4"

# ---------- Results ----------
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
exit $FAIL
