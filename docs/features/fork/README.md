# Fork capabilities

The active generation is **`fork/v0.4.22`**, frozen on official upstream **`v0.4.22`**. Mini currently runs exact generation tip `f219f4513` under immutable deployment tag `fork-mini-v0.4.22-r1`; runtime evidence and claim limits live in the [generation manifest](../../releases/fork-generations/v0.4.22.md).

See [fork generations](../../releases/fork-generations/README.md).

## Current active generation

**`fork/v0.4.22`** owns these additive capabilities:

- [External PR integration, durable silent continuation and current execution context](external-pr-integration/README.md)
- [Pi process tree supervision](pi-process-tree-supervision/README.md)
- Access-Grant-only Multica/AGS authority cutover (no Workload Assertion / public Session / legacy merge gateway)
- Best-effort Multica daemon `MULTICA_RUN_ID` injection (ExecutionID or Task ID; never blocks task start)

Clean history expectation: upstream base first; **fork-only commits always at the tip**.

## Previous generations

- `fork/upstream-main-20260808` (v0.4.21-era freeze)
- `fork/v0.4.12` and older

They remain immutable rollback/donor evidence, not current deploy authority.

## Procedure

Branch/build/deployment procedure is owned by the [Fork Development Standard](../../standards/fork-development.md).
