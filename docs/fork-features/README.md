# Fork features

This directory is the narrative registry for capabilities maintained by this fork that are not yet part of the tracked upstream baseline.

A feature directory must explain:

1. the observed user or operator problem and evidence that the change is necessary;
2. why the selected design is the smallest complete change surface;
3. current behavior, authority boundaries, failure behavior, and explicit non-goals;
4. implementation and test anchors;
5. deployment/projection state and rollback boundary;
6. whether the feature is local-only or a general upstream candidate;
7. the condition under which the fork delta can be retired.

A source change is not considered complete in this fork until its feature narrative is current. Historical implementation evidence belongs in issues, commits, PRs, and runtime receipts; feature docs link to those records instead of copying raw logs or secrets.

## Registry

| Feature | Status | Portability |
|---|---|---|
| [Daemon task receipt](daemon-task-receipt/README.md) | deployed and live-proven on mini | general upstream candidate |
| [External PR record-only completion](external-pr-record-only-completion/README.md) | source convergence candidate; not deployed | general upstream candidate |
| [Work coordination store V1–V5](work-coordination-store/README.md) | V1–V5 source accepted; not deployed | general upstream candidate |

The historical fork-feature inventory and documentation backfill is tracked by Multica issue `MINI-695`.
