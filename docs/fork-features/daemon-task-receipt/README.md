# Daemon task receipt

## Status

- Fork state: source accepted by replacement PR 46 at `main@5e8661b8efb30c0728fb515ea7fa9a9b631a0c02`; mini-runtime projection accepted by PR 47 at `mini-runtime@6f5a45ed05b7a986aa0befab1799d0da69073e70`.
- Runtime state: the exact mini-runtime CLI/daemon binary is deployed on mini and fresh live self-checks passed.
- Repair tracking: `MINI-691`; source child: `MINI-692`; apply child: `MINI-693`; proof child: `MINI-694`.
- Portability: general upstream candidate. The capability is independent of MATT Loop and AGS.
- Claim limit: live proof establishes task freshness provenance only; it does not establish review, merge, backup, Stage, or workflow acceptance.

## Problem and necessity

A running Agent sometimes has to prove that its task is a genuinely fresh execution rather than a resumed provider session or reused workdir. Multica already knows these facts and logs them at daemon task start, but the task workdir previously exposed only:

- `managed_by`;
- `agent_id`;
- `issue_id`.

During the MINI-688 review, the daemon recorded the correct current trigger and `resume_session=false reuse_workdir=false`. The critic could not read those authoritative facts itself and correctly stopped before repository review. Repeating the task would not repair the evidence gap, while accepting Issue metadata or run existence would weaken the authority boundary.

The feature is therefore necessary to make existing daemon truth safely consumable by the process whose execution it describes.

## Smallest complete design

The existing daemon-owned `.multica/daemon_task_context.json` marker remains the single workdir surface. No new API, database column, network call, credential channel, or MATT-specific integration is added.

Immediately after Multica resolves workdir reuse and all provider-session resume gates, and before starting the Agent backend, the daemon atomically refreshes that marker as:

```json
{
  "schema": "multica.daemon-task-receipt.v1",
  "managed_by": "multica-daemon-task",
  "task_id": "00000000-0000-0000-0000-000000000001",
  "agent_id": "00000000-0000-0000-0000-000000000002",
  "issue_id": "00000000-0000-0000-0000-000000000003",
  "trigger_comment_id": "00000000-0000-0000-0000-000000000004",
  "resume_session": false,
  "reuse_workdir": false
}
```

For assignment-triggered tasks, `trigger_comment_id` is present as an empty string. This distinguishes a real assignment receipt from a missing or partially written field.

The workspace-root marker used by daemon-managed CLI fail-closed discovery is unchanged in meaning and continues to contain only `managed_by`.

## Preflight launch-provenance semantics

The booleans record the daemon's effective state after all preflight gates and before the first backend launch:

- `reuse_workdir` states whether the selected execution directory is the prior managed workdir;
- `resume_session` states whether the daemon passes a prior provider session to the first backend launch;
- a fresh task reports `false/false`;
- a resumed task in the same managed workdir reports `true/true`;
- reuse without resume reports `false/true`;
- if the prior workdir is unavailable, Multica drops the prior session before launch and reports `false/false`;
- if a provider later rejects a resume and falls back to a fresh thread, the receipt deliberately remains `resume_session=true`. This conservative provenance records that the run attempted resume and prevents it from being reclassified as clean fresh evidence.

Task-specific fields are atomically complete before backend launch, so the Agent cannot race a partially initialized receipt. The receipt does not claim that a provider-side resume ultimately succeeded.

## Authority and security

The receipt is created by the local daemon, not by the Agent or Issue metadata. Existing foreign-marker refusal remains in place. Refresh uses same-directory temporary-file plus rename semantics so readers see either the prior complete marker or the complete receipt.

The schema intentionally excludes:

- provider session IDs;
- current or prior workdir paths;
- task tokens and credentials;
- workload assertions;
- environment or `custom_env` values;
- cache payloads;
- hashes, fingerprints, or credential-derived identifiers.

The receipt proves dispatch provenance and effective reuse state. It does not authorize repository access, review acceptance, merge, deployment, or any external provider action.

## Failure behavior

If the daemon cannot validate ownership of the existing marker or cannot atomically write the receipt, task startup fails before backend execution. The runtime must not silently launch an Agent without the receipt after claiming this evidence contract.

Agents consuming the receipt still fail closed when the task, actor, Issue, trigger, or required boolean state does not match their current operation.

## Implementation anchors

- `server/internal/daemon/execenv/context.go`
  - marker/receipt schema;
  - foreign-owner check;
  - atomic receipt write.
- `server/internal/daemon/execenv/execenv.go`
  - safe task-context fields used for rendering the receipt.
- `server/internal/daemon/daemon.go`
  - effective-state calculation and pre-backend write point.
- `server/internal/daemon/execenv/context_marker_test.go`
  - root-marker compatibility, exact safe field set, assignment/comment and resume/reuse cases.
- `server/internal/daemon/daemon_test.go`
  - effective resume/workdir gate coverage.
- `server/internal/daemon/task_context_receipt_run_test.go`
  - generic runTask-level assignment/comment, fresh, resumed+reused, reuse-without-resume, dropped-workdir, conservative fresh-fallback semantics, and backend-visible ordering proof.
- `server/internal/daemon/leader_workdir_reuse_test.go`
  - squad-leader managed-workdir provenance, containment, race-window reuse, and fail-closed rejection coverage retained from current main.

## Verification

Source verification for accepted MINI-692 replacement PR 46 includes:

- full Go test suite;
- daemon package and execenv focused tests;
- race tests for both packages;
- focused `go vet`;
- `git diff --check`.

Live acceptance completed in `MINI-694`: fresh assignment task `43f7efee-737e-49d8-89d0-00658829c6b7` read its own exact assignment receipt with `false/false`, explicit empty trigger, and the safe key set. MINI-688 then used fresh comment-triggered critic task `23aee993-dff1-44c1-9820-9762e4587cbe` with exact non-empty trigger and `false/false`, allowing normal independent review to proceed. Neither proof used host logs or supervisor-injected metadata as freshness authority.

## Deployment and rollback

Deployment built the exact `6f5a45ed...` arm64 CLI/daemon binary and preserved the prior `69b94aec...` binary plus symlink as rollback. After an immediate `active_task_count=0` readback and explicit operator approval, only the mini-profile host daemon was gracefully restarted so the new in-process receipt writer could load. Post-start health, executable path, PID change, and `active_task_count=0` were read back. Backend, Postgres, frontend, and the imile daemon were unchanged.

Rolling back restores the prior symlink/binary and gracefully restarts only the mini-profile daemon. It removes agent-visible receipt fields and returns consumers to fail-closed behavior; it must not be represented as freshness proof.

## Upstream path and retirement

This feature addresses a general provenance problem for any Multica workflow that needs to distinguish fresh, resumed, and reused executions. An upstream PR should keep the generic schema and tests, avoid references to MATT Loop or AGS, and present the workdir receipt as an extension of Multica's existing daemon-owned marker.

The fork delta can retire when the tracked upstream baseline provides an equivalent daemon-authoritative, secret-safe, effective-state receipt and the mini runtime has adopted it. The registry must then point to the upstream version and record the retirement commit.

## Non-goals

- changing task scheduling, retry, resume, or reuse policy;
- making every task fresh;
- exposing provider session or filesystem details;
- replacing Issue/run APIs;
- granting authority based on the receipt alone;
- encoding MATT Loop or AGS-specific terminal rules in Multica core.
