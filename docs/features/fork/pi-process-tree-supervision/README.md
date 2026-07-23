# Pi Process Tree Supervision

## Applicability

- Fork generation source: `fork/v0.4.8`.
- Tracker: `MINI-974`; source recovery child: `MINI-981`.
- Runtime availability: not claimed by source acceptance. The mini daemon requires a separate exact-build, task-drain, restart, and live process proof.

## Problem and necessity

The Pi backend previously relied on `exec.CommandContext` cancellation and closed its stdout reader as soon as the run context ended. That path could terminate only the direct Pi process while leaving tool subprocesses alive. A surviving descendant could continue writing into the closed pipe, spin on `EPIPE`, and remain after the Multica task was cancelled.

Task cancellation therefore did not reliably imply process-tree cancellation.

## Current behavior

The Pi backend now follows the process-group supervision pattern already used by other process-spawning backends:

1. start Pi in its own process group;
2. take ownership of command cancellation instead of allowing `exec.CommandContext` to kill only the leader;
3. on cancellation or timeout, send `SIGTERM` to the process group;
4. wait up to five seconds for graceful exit;
5. send `SIGKILL` to the group if it remains alive;
6. close stdout only after signalling the tree, so scanner unblocking cannot create a live writer on a closed pipe.

Normal completion wins the cancellation race through `procDone` and is not signalled after exit. The result remains `aborted` for caller cancellation and `timeout` for deadline expiry.

Process-group signalling is Unix-specific through the existing platform adapter. This narrative does not claim equivalent Windows descendant-tree termination.

## Failure behavior

- A process that handles `SIGTERM` exits within the grace window.
- A process group that ignores `SIGTERM` is escalated to `SIGKILL`.
- `cmd.WaitDelay` remains a hard backstop for stuck pipe/child cleanup.
- Cancellation cleanup must not double-signal a normally exited process or deadlock concurrent exit and cancellation.

## Authority and non-goals

This capability controls local child-process lifecycle only. It does not:

- change Multica task cancellation authorization or state transitions;
- prove that every external process outside Pi's process group is terminated;
- change provider session semantics;
- authorize daemon restart or deployment;
- claim Windows job-object supervision.

## Source and test anchors

- `server/pkg/agent/pi.go`
  - process-group configuration;
  - owned cancellation;
  - `SIGTERM` to `SIGKILL` escalation;
  - scanner and `cmd.Wait` ordering.
- `server/pkg/agent/pi_cancel_unix_test.go`
  - graceful whole-group termination;
  - forced `SIGKILL` escalation;
  - timeout cleanup;
  - concurrent normal-exit/cancel behavior;
  - cleanup receipt proving leader and grandchild termination.
- Platform process-group helpers used by `configureProcessGroup` and `signalProcessGroup`.

## Verification and claim limit

The source PR must run the focused Pi cancellation tests and the broader `server/pkg/agent` gate required by CI. Source tests establish backend process-group behavior in the test environment only.

Live acceptance requires a fresh mini daemon task that spawns an observable descendant, cancellation through the normal Multica path, and post-cancel proof that both leader and descendant are gone. Until that evidence exists, the claim is limited to source implementation and tests.

## Deployment and rollback

Deployment uses an exact accepted `fork/v0.4.8` commit and a clean parser-compatible git-describe CLI version. It waits for `active_task_count=0`, switches only the mini-profile daemon artifact, restarts that daemon, and verifies runtime identity before a cancellation canary.

Rollback restores the previously evidenced daemon artifact and its prior process lifecycle behavior. It must not be represented as preserving process-tree cancellation.

## Retirement condition

This fork delta can retire when a later selected upstream release provides equivalent Pi process-group cancellation, escalation, race handling, and tests, and the next fork generation classifies the behavior as `superseded` with evidence.
