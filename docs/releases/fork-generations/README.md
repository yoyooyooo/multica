# Fork generations

Each file records one Multica fork generation frozen on a synced upstream base
(prefer an official release tag).

- A candidate manifest states the frozen source, accepted delta inventory, migration boundary, verification requirements, rollback source, and claim limit.
- A generation becomes accepted only after its source PR, required CI, exact head, and review are complete.
- Deployment remains a separate state transition requiring immutable image digests, target approval, runtime readback, and rollback evidence.
- Previous generation branches and deployment artifacts remain immutable; never rebase, force-update, or repurpose them as the next generation.
- Clean history shape: upstream base first, **fork-only commits last at the tip**.

## Active generation (single current formal home)

- **[`upstream-main-20260821`](upstream-main-20260821.md)** — owner-authorized freeze of `upstream/main@8f9c766e4` (nearest tag `v0.4.32`), branch `fork/upstream-main-20260821`.
  - immutable Mini runtime authority: `fork-mini-upstream-main-20260821-r1@de66369a4` (`v0.4.32-3-gde66369a4`)
  - receipt-only documentation may follow the live tag; it does not silently change Mini runtime revision
  - imile and registry publication remain separate, unclaimed transitions

## Previous generations (rollback / historical)

- [`v0.4.22`](v0.4.22.md) — previous Mini live line; immutable tag `fork-mini-v0.4.22-r4@409bdc0ee`
- [`upstream-main-20260808`](upstream-main-20260808.md) — previous live line; freeze base `2b35f8017…`; origin tip `b3118a340` (`fork-upstream-main-20260808-r1`)
- [`v0.4.12`](v0.4.12.md) — older accepted generation / deeper rollback at `fork/v0.4.12@d5ec9569e…`
