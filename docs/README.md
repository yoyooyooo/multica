# Documentation Index

This directory routes repository documentation. It does not replace source code, tests, tracker state, CI, artifacts, or runtime evidence.

## Authority order

1. [`CLAUDE.md`](../CLAUDE.md) for binding repository rules and package constraints.
2. [`docs/standards/**`](standards/README.md) for expanded executable procedures.
3. Code, migrations, tests, and generated contracts for implemented behavior.
4. [`docs/features/**`](features/README.md) for current capability narratives.
5. [`docs/releases/**`](releases/README.md) for retained generation and deployment evidence.
6. Plans, proposals, and external notes for non-authoritative context.

A document cannot prove that code is accepted, an artifact is deployed, or a runtime is healthy. Those claims require their owning PR, CI, image, provider, and runtime receipts.

## Working with this fork

- To open or review a fork PR, read the [Fork Development Standard](standards/fork-development.md#pull-request-rules). It defines the base, the verification evidence, and the impact to report. [Local setup and checks](../CONTRIBUTING.md#testing) are in the Contributing Guide.
- To change a capability, start at the [fork capability index](features/fork/README.md). Follow its source and test references for the behavior in this branch.
- To check release or rollout evidence, start at the [generation index](releases/fork-generations/README.md). Check the target's image and daemon revision before applying a procedure. The repository default is not a runtime version.
- To install Multica or assign your first issue, start at the [user README](../README.md#getting-started).

## Current general documentation

- [Custom runtimes](custom-runtimes.md)
- [Feature flags](feature-flags.md)
- [Analytics](analytics.md)
- [Design](design.md)

## Historical and planning material

The [April 2026 product survey](product-overview.md) is retained as a dated source. Use the [product docs in this branch](../apps/docs/content/docs/index.mdx) for current concepts and the code for exact behavior. Files named `*-plan.md`, `docs-outline.md`, and `docs-rewrite-plan.md` are planning or historical context unless a current authority explicitly adopts them.
