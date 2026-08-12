# Fork generations

Each file records one Multica fork generation frozen on a synced upstream base
(prefer an official release tag).

- A candidate manifest states the frozen source, accepted delta inventory, migration boundary, verification requirements, rollback source, and claim limit.
- A generation becomes accepted only after its source PR, required CI, exact head, and review are complete.
- Deployment remains a separate state transition requiring immutable image digests, target approval, runtime readback, and rollback evidence.
- Previous generation branches and deployment artifacts remain immutable; never rebase, force-update, or repurpose them as the next generation.
- Clean history shape: upstream base first, **fork-only commits last at the tip**.

## Active generation (single current formal home)

- **[`v0.4.22`](v0.4.22.md)** — formal generation frozen on official tag **`v0.4.22`**, branch `fork/v0.4.22`.
  - shape: `v0.4.22@8bd49bba8` + 6 fork-only tip commits (bootstrap → replay → mobile-ci → remediate → docs → declare-active)
  - formal tip at freeze of this router: `66fdc1d5b` (`v0.4.22-6-g66fdc1d5b`)
  - includes best-effort daemon `MULTICA_RUN_ID` injection (ExecutionID or Task ID)
  - publication/CI/runtime acceptance is **not** implied by this router entry; until origin has `fork/v0.4.22` and exact-head checks, treat the tip as a local formal candidate only

## Previous generations (rollback / historical)

- [`upstream-main-20260808`](upstream-main-20260808.md) — previous live line; freeze base `2b35f8017…`; origin tip `b3118a340` (`fork-upstream-main-20260808-r1`)
- [`v0.4.12`](v0.4.12.md) — older accepted generation / deeper rollback at `fork/v0.4.12@d5ec9569e…`
