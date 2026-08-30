# Fork capabilities

The active generation is **`fork/upstream-main-20260830`**, frozen on `upstream/main@15280617b` (nearest official tag **`v0.4.36`**). Mini and imile-win currently run exact source `ad25aa66b` (`v0.4.36-10-gad25aa66b`) under their immutable `20260830-r1` deployment tags; runtime evidence and claim limits live in the [generation manifest](../../releases/fork-generations/upstream-main-20260830.md).

See [fork generations](../../releases/fork-generations/README.md).

## Current active generation

**`fork/upstream-main-20260830`** owns these additive capabilities:

- [External PR integration, durable silent continuation and current execution context](external-pr-integration/README.md)
- [Pi process tree supervision](pi-process-tree-supervision/README.md)
- [Offline Google Fonts for web image builds](offline-google-fonts/README.md)
- Access-Grant-only Multica/AGS authority cutover (no Workload Assertion / public Session / legacy merge gateway)
- Best-effort Multica daemon `MULTICA_RUN_ID` injection (ExecutionID or Task ID; never blocks task start)

Clean history expectation: upstream base first; **fork-only commits always at the tip**.

## Previous generations

- `fork/upstream-main-20260821` (previous Mini/imile live / rollback)
- `fork/v0.4.22` (previous Mini live / rollback)
- `fork/upstream-main-20260808` (v0.4.21-era freeze)
- `fork/v0.4.12` and older

They remain immutable rollback/donor evidence, not current deploy authority.

## Procedure

Branch/build/deployment procedure is owned by the [Fork Development Standard](../../standards/fork-development.md).
