# Fork capabilities

The active generation is the fork line frozen on the **latest synced upstream version** this fork tracks. See [fork generations](../../releases/fork-generations/README.md).

## Current active generation

**`upstream-main-20260808`** (nearest official tag **`v0.4.21`**, generation branch `fork/upstream-main-20260808`) owns these additive capabilities:

- [External PR integration and current execution context](external-pr-integration/README.md)
- [Pi process tree supervision](pi-process-tree-supervision/README.md)
- Access-Grant-only Multica/AGS authority cutover (no Workload Assertion / public Session / legacy merge gateway)

Clean history expectation: upstream base first; **fork-only commits always at the tip**.

## Previous generation

`fork/v0.4.12` remains the previous accepted generation and primary rollback source. `fork/v0.4.9` / `fork/v0.4.8` remain older donor evidence. They are not current deploy authority.

## Procedure

Branch/build/deployment procedure is owned by the [Fork Development Standard](../../standards/fork-development.md). Generation inventory and claim limits live under [`docs/releases/fork-generations/`](../../releases/fork-generations/README.md).
