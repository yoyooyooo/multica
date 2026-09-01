Status: ready-for-agent
Blocked by: None - can start immediately
Track: optional sidecar; does not block the three-ticket clean rebuild DAG

# Correct Pi process-group cancellation

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

27 and 28.

## What to build

Implement a narrow, upstream-oriented Pi cancellation path that owns the full process group. Cancellation sends TERM, waits for process-group disappearance for a bounded interval, and escalates to KILL when descendants survive, including the race where the leader exits before its children.

The patch should reuse existing cross-platform process primitives and remain removable after equivalent upstream behavior is released.

## Simplification guardrails

- Change only Pi process-group cancellation and the minimal shared primitive it already uses. Do not redesign process supervision across every agent backend.
- Cover the known leader/descendant race and ordinary graceful/escalated paths; do not build an unbounded signal-order matrix without a reproduced failure.

## Acceptance criteria

- [ ] A cooperative process group exits after TERM without receiving unnecessary KILL.
- [ ] A non-cooperative process group receives KILL after the bounded grace period.
- [ ] Descendants are still detected and killed when the leader exits immediately after TERM.
- [ ] Concurrent cancellation, natural exit, timeout, and repeated cancellation do not leak processes or deadlock.
- [ ] Unrelated process groups are never signaled.
- [ ] Unix integration tests use real subprocess groups and pass under the race detector where supported.
- [ ] The change is isolated and documented as an upstream candidate rather than an unnamed permanent fork behavior.

## Blocked by

None - can start immediately.
