# Standards

## Owns

- Binding repository procedures that expand the hard rules in `CLAUDE.md`.
- Repeatable commands, gates, naming rules, integration rules, and stop conditions.

## Must not own

- Product capability availability.
- Mutable Issue, PR, CI, rollout, or proof status.
- Runtime claims without owning evidence.

## Conflict behavior

`CLAUDE.md` is the higher repository instruction. Code and tests determine implemented behavior. If a Standard conflicts with either, stop the affected operation and repair the authority chain in the same change.

## Read next

- [Fork Development Standard](fork-development.md)
