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
  - deployed code shape: `v0.4.22@8bd49bba8` + 30 linear fork-only commits through `f219f4513`
  - immutable Mini runtime authority: `fork-mini-v0.4.22-r1@f219f4513` (`v0.4.22-30-gf219f4513`)
  - the generation branch may contain later receipt-only documentation; it does not silently change deployed runtime revision
  - backend/frontend/CLI/daemon use exact source `f219f4513`; owner-only deployment receipt is recorded in [`v0.4.22.md`](v0.4.22.md)
  - includes best-effort daemon `MULTICA_RUN_ID` injection and durable External PR silent-continuation reconciliation
  - imile and registry publication remain separate, unclaimed transitions

## Previous generations (rollback / historical)

- [`upstream-main-20260808`](upstream-main-20260808.md) — previous live line; freeze base `2b35f8017…`; origin tip `b3118a340` (`fork-upstream-main-20260808-r1`)
- [`v0.4.12`](v0.4.12.md) — older accepted generation / deeper rollback at `fork/v0.4.12@d5ec9569e…`
