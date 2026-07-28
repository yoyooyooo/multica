# Pi Process Tree Supervision

## Applicability

- Donor: `fork/v0.4.8`, source commit `22e845c2a728941287a02a2295d9c7142782bb7e`.
- Current replay target: `fork/v0.4.12` r2 corrective revision.
- Source acceptance and live runtime proof remain separate.

## Problem

`exec.CommandContext` kills only the direct Pi process. Pi can spawn tool subprocesses; cancellation could therefore leave descendants alive and writing into a closed stdout pipe.

## Current behavior

The Pi backend:

1. starts Pi in its own process group;
2. owns cancellation instead of using the leader-only default kill;
3. sends `SIGTERM` to the group on cancellation or timeout;
4. waits five seconds for graceful exit;
5. escalates to group `SIGKILL`;
6. closes stdout only after signalling the tree;
7. uses `procDone` to make normal-exit/cancel races idempotent.

Unix process-group helpers own the guarantee. Equivalent Windows descendant-tree termination is not claimed.

## Source and tests

- `server/pkg/agent/pi.go`
- `server/pkg/agent/pi_cancel_unix_test.go`
  - graceful leader/grandchild termination;
  - SIGKILL escalation;
  - timeout cleanup;
  - normal-exit/cancel race;
  - secret-safe cancellation result.

## Live claim boundary

Source tests establish process-group behavior in the test environment. Live acceptance requires a Task using the deployed exact runtime, an observable Pi descendant, supported Task cancellation, and readback that both leader and descendant exited.

## Rollback and retirement

Rollback restores the prior artifact and its prior lifecycle behavior. This delta can retire only when a selected official upstream release supplies equivalent group cancellation, escalation, race handling, and tests.
