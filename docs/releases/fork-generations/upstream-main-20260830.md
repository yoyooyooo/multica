# Upstream/main fork generation - 2026-08-30

## Status and authority

**Candidate generation**, owner-authorized to freeze the latest `upstream/main`
rather than stop at the nearest official tag. It does not become runtime
authority until source verification and the separate Mini and imile-win
switch receipts pass.

- candidate generation branch: `fork/upstream-main-20260830`
- integration work branch: `feature/upstream-main-20260830-fork-replay`
- frozen upstream base: `upstream/main@15280617bc264e367ca9c5e5e5cefdb0988246b7`
- nearest official release: `v0.4.36@c1a61e1e8`
- unreleased upstream delta: five commits after `v0.4.36`
- prior accepted source and rollback: `fork/upstream-main-20260821@de66369a4908298ff1e7467b42d28f4b1c63bcf3`
- prior Mini tag: `fork-mini-upstream-main-20260821-r1`
- prior imile-win tag: `fork-imile-upstream-main-20260821-r1`

Expected clean history shape:

```text
[upstream/main @ 15280617b]
  feat(fork): replay generation on upstream main   <-- fork-owned source tip
  docs(fork): record Mini/imile runtime receipts   <-- optional receipt-only tips
```

No merge commit or force update is retained. The previous generation, tags,
images, database dumps, CLI binaries, and receipts stay immutable.

## Replay inventory

The candidate reapplies the net effective fork delta from the prior generation
onto the frozen upstream base, resolving overlaps against current upstream
ownership.

| Surface | Classification | Candidate decision |
| --- | --- | --- |
| External PR integration, durable reconciliation, current execution context, completion policy | keep / rework | Preserve provider locks, typed terminal facts, scheduler job, Issue projection, and retired-route fences; compose with current upstream Issue deletion, source-context storage intents, Plugin schedules, and billing behavior. |
| Fork migrations `272`-`316` | keep / dual-ledger | Keep full version names unchanged. The migration runner keys by the full basename; previously applied fork rows remain, while new upstream rows through `440` apply independently. |
| Workload assertion and delegated merge authority | retired | Do not restore routes or runtime authority. Historical migration recovery hooks remain only for old ledgers. |
| Pi process-tree supervision | keep / rework | Preserve process-group TERM-to-KILL cancellation and compose it with upstream process-group release cleanup. |
| Offline Google Fonts | keep | Preserve the vendored image-build font mock. |
| Local fork CLI installer and version admission | keep / rework | Preserve exact-source CLI/daemon activation while retaining current upstream Make targets and development environment commands. |
| AGS task shims, daemon identity, `MULTICA_RUN_ID` | keep | Preserve opt-in task environment behavior and execution-context daemon identity. |
| sqlc output | regenerate | Merge current query sources, then regenerate with sqlc `v1.31.1`; generated Go is never manually reconciled. |
| CI and mobile verification | keep / rework | Retain current upstream path gates and add the new generation branch to both workflows. |

## Migration boundary

Upstream owns migrations through
`440_github_pr_head_sha_index`. Fork migration files retain their distinct full
names under numeric prefixes `272`-`316`.

Deployment must prove:

- every pre-existing ledger row remains present;
- upstream migrations `398`-`440` apply on both live databases;
- `316_external_pr_reconcile_finalization_primary_key` remains present;
- backend `/readyz` reports both DB and migrations `ok`;
- rollback across forward-only fences uses the frozen database dump, not image
  replacement alone.

## Source gates

Before publication or deployment:

- no merge commit in the generation history;
- clean parser-compatible `git describe` version;
- sqlc regeneration is clean;
- focused Go tests for migrate/server/handler/service/daemon/agent pass;
- frontend core/views tests, typecheck, and build pass;
- self-host config, CLI installer, version admission, and completion-policy
  scripts pass;
- backend and frontend images build from one exact source commit.

## Deployment gates

Mini and imile-win are independent state transitions. Each must record:

- zero active daemon tasks and zero active DB queue/autopilot work immediately
  before switching;
- frozen database dump and SHA-256;
- old image IDs, CLI target, storage bind/volume, and network identity;
- exact candidate image revision/version labels;
- backend readiness, frontend HTTP status, CLI version, daemon version, and
  expected daemon device name;
- migration ledger preservation and rollback commands.

Both hosts must run the same exact source SHA. Native arm64 and amd64 image IDs
are expected to differ.

## Claim limit

This candidate file proves only the intended source and acceptance contract. It
does not claim source CI acceptance, immutable deployment tags, successful
image builds, database migration, runtime health, browser behavior, registry
publication, or that either host has switched.
