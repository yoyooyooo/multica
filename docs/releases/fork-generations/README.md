# Fork Generation Manifests

One manifest is retained for each accepted `fork/vX.Y.Z` generation.

A manifest must record:

- official upstream tag and exact SHA;
- exact accepted generation head;
- each accepted fork delta and its source PR;
- deltas classified as reworked, superseded, retired, or blocked;
- verification scope and claim limit;
- deployment tags, artifact digests, and runtime evidence links when deployed;
- rollback source;
- explicit not-claimed items.

Do not create a completion or deployment claim before the corresponding source, CI, artifact, approval, and runtime evidence exists. Mutable execution state remains in the tracker.

The first `v0.4.8.md` manifest is created when the generation has an accepted delta inventory; branch creation alone is not generation acceptance.
