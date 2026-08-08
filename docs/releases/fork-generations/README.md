# Fork generations

Each file records one Multica fork generation derived from an exact official upstream release tag.

- A candidate manifest states the frozen source, accepted delta inventory, migration boundary, verification requirements, rollback source, and claim limit.
- A generation becomes accepted only after its source PR, required CI, exact head, and review are complete.
- Deployment remains a separate state transition requiring immutable image digests, target approval, runtime readback, and rollback evidence.
- Previous generation branches and deployment artifacts remain immutable; never rebase, force-update, or repurpose them as the next generation.

Generation authority:

- [`upstream-main-20260808`](upstream-main-20260808.md) — one-time owner-authorized exception candidate based on frozen `upstream/main@2b35f8017ab3b773e0356e562ecb04e55a7a9bd7`; it becomes active only after exact-head source and deployment receipts.
- [`v0.4.12`](v0.4.12.md) — current accepted rollback generation at `fork/v0.4.12@d5ec9569ede6e48e3caced031254259f48b83f41` until the exception candidate is accepted and deployed.
