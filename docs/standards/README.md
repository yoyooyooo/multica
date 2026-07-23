# Standards

## Owns

- Binding repository procedures that expand the hard rules in `CLAUDE.md`.
- Repeatable commands, gates, naming rules, integration rules, and acceptance requirements.
- Failure behavior and stop conditions for operations governed by those procedures.

## Must not own

- Product meaning or capability availability.
- Active Issue, PR, rollout, or proof status.
- Historical logs and deployment evidence.
- Unaccepted future architecture.

## Conflict behavior

`CLAUDE.md` is the higher repository instruction. Code and tests determine implemented behavior. If a Standard conflicts with either, stop the affected operation and repair the authority chain in the same change; do not choose whichever text is more convenient.

## Read next

- [Fork Development Standard](fork-development.md)

A Standard becomes current only after it is adopted on the active fork generation. Proposed rules belong in the tracker or proposal workflow until accepted.
