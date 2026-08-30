# Pi Process Tree Supervision

## Applicability

- Donor: `fork/v0.4.8`, source commit `22e845c2a728941287a02a2295d9c7142782bb7e`.
- Accepted replay target: `fork/v0.4.12` r2 at `8bbeb72887ddbc5a1409462e8d68dfd014414e7e`.
- Source acceptance, daemon deployment, real-Pi cleanup, and forced escalation remain separate receipts.

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

## Live acceptance and claim boundary

The Mini production daemon was upgraded to exact r2 CLI `v0.4.12-11-g8bbeb7288` at zero active tasks. Live acceptance then used two distinct supported cancellation proofs:

1. `MINI-1285` ran real Pi with observable detached Bash parent/child processes. After `multica issue cancel-task`, the Pi PID/group, both tool PIDs, and the detached tool group were absent.
2. `MINI-1288` used a reviewed pass-through wrapper around real Pi 0.80.10. The wrapper changed no Pi argument, recorded only PID/PGID/timestamps, and deliberately kept the process-group leader alive after TERM. Daemon logs show a 5.003-second TERM-to-finish interval, matching the five-second SIGKILL escalation contract; no wrapper or tool process group remained. The daemon was then restarted with the ordinary Pi executable path and zero active tasks.

Owning live receipt SHA-256: `cec67a38c0e67422277cdaef08168fd877267966670908a6406be1622e8862fc`.

The wrapper is a disposable harness for the escalation branch. It does not imply that every ordinary Pi cancellation reaches SIGKILL; ordinary Pi may terminate during the grace period. `MINI-1284` is negative evidence from the pre-r2 daemon and is not part of the positive claim.

## Rollback and retirement

Rollback restores the prior artifact and its prior lifecycle behavior. This delta can retire only when a selected official upstream release supplies equivalent group cancellation, escalation, race handling, and tests.
