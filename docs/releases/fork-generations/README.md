# Fork generations

This directory records the fork generation derived from the official v0.4.12 release tag. Later generations keep their manifests on their own branches.

- A candidate manifest states the frozen source, accepted delta inventory, migration boundary, verification requirements, rollback source, and claim limit.
- A generation becomes accepted only after its source PR, required CI, exact head, and review are complete.
- Deployment remains a separate state transition requiring immutable image digests, target approval, runtime readback, and rollback evidence.
- Previous generation branches and deployment artifacts remain immutable; never rebase, force-update, or repurpose them as the next generation.

- [`v0.4.12`](v0.4.12.md) records source acceptance and the historical Mini r1 and r2 deployments. Those deployments do not establish what runs today.
- For the later v0.11.1 rollout, read the [v0.11.1 generation manifest](https://github.com/yoyooyooo/multica/blob/fork/v0.11.1-main.20261121/fork/releases/v0.11.1-main.20261121.md) on its own branch. Its source and deployment receipts do not change the default branch or this branch's implementation contract.
