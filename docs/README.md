# Documentation Index

This directory routes repository documentation. It does not replace source code, tests, tracker state, or runtime evidence.

## Authority order

When claims conflict, use this order:

1. `CLAUDE.md` for binding repository rules and package constraints;
2. `docs/standards/**` for expanded executable procedures;
3. code, migrations, tests, and generated contracts for implemented behavior;
4. `docs/features/**`, architecture, and protocol documents for current capability narratives;
5. `docs/releases/**` and reports for retained release or validation evidence;
6. plans, proposals, and external notes for non-authoritative context.

A document cannot prove that code is merged, an artifact is deployed, or a runtime is healthy. Those claims require their owning source, CI, artifact, tracker, and runtime evidence.

## Read first

- [Repository instructions](../CLAUDE.md)
- [Standards](standards/README.md)
- [Fork Development Standard](standards/fork-development.md)

## Layer routes

- [Standards](standards/README.md) — binding executable procedures.
- [Features](features/README.md) — current capability narratives.
- [Fork Capabilities](features/fork/README.md) — current fork capability registry.
- [Releases](releases/README.md) — immutable release and generation evidence.
- [Fork Generation Manifests](releases/fork-generations/README.md) — accepted generation inventories.

## Current documentation

- [Product Overview](product-overview.md)
- [Custom Runtimes](custom-runtimes.md)
- [Feature Flags](feature-flags.md)
- [Analytics](analytics.md)
- [Design](design.md)

## Planning and historical material

Documents such as `*-plan.md`, `docs-outline.md`, and `docs-rewrite-plan.md` are planning or historical context unless a current authority document explicitly adopts one of their rules. They must not override `CLAUDE.md`, Standards, code, tests, or accepted runtime evidence.

## Fork documentation placement

- Branch, generation, integration, build, and deployment rules: `docs/standards/**`.
- Current fork capability narratives: `docs/features/fork/<capability>/README.md`.
- Immutable generation manifests and release evidence: `docs/releases/fork-generations/**`.
- Active migration, PR, proof, and deployment progress: the configured tracker and PR system; docs link to those records rather than copying their mutable status.

Every durable docs layer must provide a local `README.md` that states what the layer owns and must not own.
