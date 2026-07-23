#!/usr/bin/env bash
# Shared, canonical worktree identity derivation for generated env files and
# the Compose ownership guard. Source this file; do not execute it directly.

worktree_identity_hash() {
  local physical_path="$1"
  local digest

  # Keep Compose project/database identity independent of the bounded host-port
  # allocation. A missing SHA-256 implementation fails closed rather than
  # silently falling back to the collision-prone port slot.
  if command -v shasum > /dev/null 2>&1; then
    digest="$(printf '%s' "$physical_path" | shasum -a 256 | awk '{print substr($1, 1, 20)}')"
  elif command -v sha256sum > /dev/null 2>&1; then
    digest="$(printf '%s' "$physical_path" | sha256sum | awk '{print substr($1, 1, 20)}')"
  else
    echo "ERROR: cannot derive a worktree identity: shasum or sha256sum is required" >&2
    return 1
  fi

  if [[ ! "$digest" =~ ^[0-9a-f]{20}$ ]]; then
    echo "ERROR: could not derive a SHA-256 worktree identity" >&2
    return 1
  fi
  printf '%s' "$digest"
}

worktree_identity_derive() {
  local requested_path="$1"
  local worktree_name
  local slug
  local path_hash
  local port_hash

  WORKTREE_IDENTITY_PATH="$(CDPATH='' cd -P -- "$requested_path" && pwd -P)" || return 1
  worktree_name="$(basename "$WORKTREE_IDENTITY_PATH")"
  slug="$(printf '%s' "$worktree_name" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
  [ -n "$slug" ] || slug="multica"

  # PostgreSQL identifiers are limited to 63 bytes. Reserve `wt_`, one
  # separator, and the 80-bit physical-path digest, while retaining a readable
  # basename prefix.
  slug="${slug:0:39}"
  path_hash="$(worktree_identity_hash "$WORKTREE_IDENTITY_PATH")" || return 1
  port_hash="$(printf '%s' "$WORKTREE_IDENTITY_PATH" | cksum | awk '{print $1}')" || return 1

  WORKTREE_IDENTITY_PROJECT="wt_${slug}_${path_hash}"
  WORKTREE_IDENTITY_DATABASE="$WORKTREE_IDENTITY_PROJECT"
  WORKTREE_IDENTITY_PORT_OFFSET=$((port_hash % 1000))
  WORKTREE_IDENTITY_PORT=$((15432 + WORKTREE_IDENTITY_PORT_OFFSET))
}
