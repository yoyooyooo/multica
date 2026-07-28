# Fork generations

Each file records one Multica fork generation derived from an exact official upstream release tag.

- A candidate manifest states the frozen source, accepted delta inventory, migration boundary, verification requirements, rollback source, and claim limit.
- A generation becomes accepted only after its source PR, required CI, exact head, and review are complete.
- Deployment remains a separate state transition requiring immutable image digests, target approval, runtime readback, and rollback evidence.
- Previous generation branches and deployment artifacts remain immutable; never rebase, force-update, or repurpose them as the next generation.

Active generation:

- [`v0.4.12`](v0.4.12.md) — r1 deployed; r2 corrective convergence is required after donor-delta omissions were found.
