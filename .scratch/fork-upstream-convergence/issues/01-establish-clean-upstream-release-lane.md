Status: ready-for-agent
Blocked by: None - can start immediately

# Establish the clean-upstream exact-SHA release lane

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

1-3, 6-11, and 33.

## What to build

Create a complete non-production release path that begins from a clean current-upstream generation, keeps target operations behind additive fork tooling, accepts one exact source revision, and produces one immutable arm64/amd64 OCI manifest. Source changes must invalidate prior CI, build, tag, manifest, and receipt authority. Official upstream deployment surfaces must remain replaceable.

The slice ends at artifact and dry-run deployment evidence. It must not switch Mini or imile-win.

## Acceptance criteria

- [ ] A clean-upstream generation can be recreated from documented behavior contracts without replaying the prior fork squash.
- [ ] Target-specific build, Compose, storage, activation, backup, and receipt behavior is invoked from additive fork tooling.
- [ ] Exact-head CI is a hard prerequisite for final image construction.
- [ ] One accepted source revision produces an immutable manifest containing verified arm64 and amd64 descriptors.
- [ ] Any source revision change invalidates all prior acceptance and artifact receipts.
- [ ] Receipt generation uses allowlisted redacted fields and owner-only permissions; raw container environments are never retained.
- [ ] Dry-run or disposable-host proof covers artifact selection, digest readback, and rollback inputs without modifying production runtimes.

## Blocked by

None - can start immediately.
