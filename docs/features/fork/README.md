# Fork Capabilities

This is the registry for fork-owned capabilities implemented in the current generation.

## Owns

- Links to one narrative per current fork capability.
- Capability applicability and retirement condition.

## Must not own

- Mutable PR, Issue, CI, rollout, or runtime status.
- Capabilities that exist only on a prior generation or legacy deployment branch.
- General branch and deployment procedure; see [Fork Development Standard](../../standards/fork-development.md).

## Current registry

| Capability | Generation state | Runtime state |
|---|---|---|
| [External PR Integration](external-pr-integration/README.md) | source and tests present | not deployed |
| [Pi Process Tree Supervision](pi-process-tree-supervision/README.md) | source and tests present | not deployed |

Legacy capabilities from `mini-runtime@c798fa83…` enter this registry only after their implementation, tests, and narrative are replayed and accepted on this generation. Source presence does not imply runtime availability.
