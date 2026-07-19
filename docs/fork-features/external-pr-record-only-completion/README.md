# External PR record-only completion

## Status

- Fork state: implementation is preserved in the Work Coordination runtime-convergence source candidate; it is not yet accepted or deployed. Original delivery is tracked by `MINI-697` / `MINI-698`.
- Runtime apply: blocked on exact-head convergence acceptance and a fresh target-specific deployment approval; tracked by `MINI-699`.
- Live proof: tracked by `MINI-700`.
- Portability: general upstream candidate. The policy is provider- and workflow-neutral.
- Claim limit: source tests do not prove mini deployment or a successful backup-gated canary.

## Problem and necessity

Multica can accept an authoritative external PR completion callback, record the merged PR, transition a linked leaf Issue to `done`, and notify its parent. The terminal transition immediately participates in the native Stage barrier.

Some workflows have additional terminal gates after provider merge. During the MINI-688 canary, provider merge auto-completed the source leaf before an independently verified outward-backup receipt existed. The resulting parent Stage-complete comment and coordinator wake were real side effects. Restoring the leaf to `in_progress` prevented later work from running but could not erase the premature Stage release.

A prompt-only “reopen after merge” rule is therefore insufficient. The status transition itself must be preventable before the external PR callback reaches the Stage reducer.

## Smallest complete design

Issue metadata gains a generic external PR completion policy:

```yaml
external_pr_completion_policy: leaf_child_only | record_only
```

Policy strings are normalized with lowercase plus the explicit ASCII whitespace cutset `space/tab/newline/carriage-return/vertical-tab/form-feed` in both Go and SQL.

- absent or `leaf_child_only`: retain existing authoritative leaf auto-completion;
- `record_only`: persist external PR link, merge state, merged SHA, and system activity, but do not change Issue status;
- any unknown, JSON `null`, or non-string value: fail closed and do not change status.

The callback returns a stable skipped reason for record-only or unsupported policies. It does not claim the Issue is complete.

The workflow that owns an extra terminal gate sets `record_only` before PR linkage. After all independent evidence is current and accepted, an authorized workflow actor explicitly closes the Issue. That ordinary terminal transition—not provider merge alone—may release the native Stage barrier.

## Race and failure behavior

The handler performs two guards:

1. a decoded metadata precheck returns a stable reason without attempting completion;
2. the atomic `UPDATE issue ... SET status='done'` independently permits only an absent key or a JSON string normalized to empty / `leaf_child_only`.

If policy changes to `record_only`, an unknown string, JSON `null`, or another JSON type between the precheck and update, the SQL type/value predicate prevents completion. A concurrent removal after the precheck remains conservatively skipped for that callback and can be retried explicitly.

The public metadata handler accepts only a small flat primitive metadata object, while the SQL guard also defends against legacy rows or privileged/direct database writes containing JSON `null` or non-string values.

## Authority and security

The policy controls only whether Multica converts an authoritative merged-PR callback into an Issue terminal transition. It does not:

- verify backup, deployment, review, or other workflow evidence;
- grant repository or provider authority;
- change external PR authentication, URL validation, or link confidence;
- expose tokens, credentials, environment values, paths, hashes, fingerprints, or receipt payloads.

External PR links and merge activity remain authoritative records even when Issue completion is skipped.

## Implementation anchors

- `server/internal/handler/external_pr_integration.go`
  - normalized policy read;
  - stable skipped outcomes;
  - atomic SQL guard before status mutation and parent notification.
- `server/internal/handler/external_pr_integration_test.go`
  - normal leaf auto-completion remains supported;
  - record-only merge keeps the leaf active;
  - merged link and activity remain recorded;
  - no parent Stage comment is emitted;
  - unknown policy fails closed.

## Verification

Required source checks:

- focused external PR handler tests;
- full handler or server Go tests;
- race/vet where applicable;
- `gofmt`;
- `git diff --check`;
- exact-head CI and independent review.

Live acceptance additionally requires a new canary to prove that provider merge leaves a record-only source leaf active with no Stage wake, then a later explicit close after independent evidence releases the Stage exactly once.

## Deployment and rollback

This handler runs in the Multica backend. Feature-local apply requires an exact accepted convergence build and backend replacement after `active_task_count=0`. The current convergence rollout also carries Work Coordination migrations and CLI source, so its eventual target-specific plan—not this feature-local note—must define every database, server, CLI, restart, backup, and rollback action. No source acceptance implicitly authorizes those actions.

Rollback restores the previous auto-completion behavior. Workflows requiring extra terminal gates must remain parked while rolled back; they must not reinterpret the absence of a premature wake as proof.

## Upstream path and retirement

The policy is useful for any external PR integration whose Issue has terminal gates beyond provider merge. An upstream proposal should retain the generic `record_only` vocabulary and fail-closed unknown-policy behavior rather than encode AGS, GitHub backup, MATT Loop, or a specific receipt schema.

The fork delta can retire when the tracked upstream baseline provides equivalent record-only external PR completion semantics and the mini runtime plus workflow contracts have adopted it.

## Non-goals

- teaching Multica how to verify an outward backup;
- delaying or modifying provider merge;
- automatically closing record-only Issues later;
- changing native Stage barrier semantics;
- inferring policy from Issue text, status, run existence, or provider metadata;
- coupling Multica core to `backup_requirement` or any AGS-specific field.
