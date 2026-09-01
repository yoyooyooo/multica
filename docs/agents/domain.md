# Domain Docs

Engineering skills consume this repository's domain documentation using a single-context layout.

## Before exploring

Read these files when they exist and are relevant:

- `CONTEXT.md` at the repository root for domain vocabulary and invariants.
- ADRs under `docs/adr/` that affect the area being changed.

If either location does not exist, proceed silently. Domain-modeling skills create these artifacts lazily when terms or decisions are resolved.

## Layout

```text
/
|-- CONTEXT.md
`-- docs/adr/
    |-- 0001-example-decision.md
    `-- ...
```

Do not introduce `CONTEXT-MAP.md` or context-scoped ADR directories unless the repository develops independently owned domain contexts that cannot share one vocabulary.

## Vocabulary

Use terms as defined in `CONTEXT.md` in issue titles, specifications, refactor proposals, tests, and documentation. If a needed concept is absent, reconsider whether existing project language already covers it before proposing a new term.

## ADR conflicts

If proposed work contradicts an existing ADR, identify the conflict explicitly and explain why the decision should be reopened. Do not silently override an ADR.
