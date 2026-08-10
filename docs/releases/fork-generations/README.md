# Fork generations

Each file records one Multica fork generation frozen on a synced upstream base
(prefer an official release tag; otherwise an owner-frozen upstream commit with
nearest-tag metadata).

- A candidate manifest states the frozen source, accepted delta inventory, migration boundary, verification requirements, rollback source, and claim limit.
- A generation becomes accepted only after its source PR, required CI, exact head, and review are complete.
- Deployment remains a separate state transition requiring immutable image digests, target approval, runtime readback, and rollback evidence.
- Previous generation branches and deployment artifacts remain immutable; never rebase, force-update, or repurpose them as the next generation.
- Clean history shape: upstream base first, **fork-only commits last at the tip**.

## Active generation

- **[`upstream-main-20260808`](upstream-main-20260808.md)** — formal/live generation for the latest **synced** upstream line around **`v0.4.21`** (frozen base `upstream/main@2b35f8017…`, nearest tag `v0.4.21`).
  - generation branch: `fork/upstream-main-20260808` (source PR/work may land via `feature/upstream-main-20260808-fork-replay` then fast-forward into the generation branch)
  - tip shape (as of origin head `b3118a340`): **4 fork-only commits at tip** after the frozen upstream base
  - live mini/imile-win CLI/daemon readback has included `v0.4.21-14-gb3118a340` and the best-effort Run ID fix tip `v0.4.21-15-g5354e11c0` on the same line

## Previous generation (rollback)

- **[`v0.4.12`](v0.4.12.md)** — previous accepted tag-derived generation at `fork/v0.4.12@d5ec9569e…`; remains immutable rollback evidence, not current deploy authority.

## Freshness note (must re-check before next generation)

As of 2026-08-10 fetch:

- official upstream latest tag: **`v0.4.22`**
- `upstream/main` is ahead of the current generation base
- next clean generation should **replay onto `v0.4.22` (or the next chosen synced tip)** with fork-only commits again at the tip; do not silently treat the 2026-08-08 freeze as permanently “latest”
