# Issue tracker: Local Markdown

Issues and PRDs for this repo live as Markdown files in `.scratch/`. External pull requests are not a triage request surface.

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`.
- The PRD is `.scratch/<feature-slug>/PRD.md`.
- Implementation issues are `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`.
- Triage state is recorded as a `Status:` line near the top of each issue file. See `triage-labels.md` for the role strings.
- Dependency state is recorded as a `Blocked by:` line containing issue paths or `None - can start immediately`.
- Comments and conversation history append to the bottom of the file under a `## Comments` heading.

## Publishing

When a skill says "publish to the issue tracker", create a new file under `.scratch/<feature-slug>/`, creating the directory if needed.

When a skill says "fetch the relevant ticket", read the referenced local Markdown file. The user will normally pass its path or issue number.

## Wayfinding operations

Used by `/wayfinder`. The map is a file with one child file per ticket.

- **Map:** `.scratch/<effort>/map.md`, containing Notes, Decisions-so-far, and Fog.
- **Child ticket:** `.scratch/<effort>/issues/NN-<slug>.md`, with a `Type:` line and a `Status:` line.
- **Blocking:** a `Blocked by: NN, NN` line. A ticket is unblocked when every listed issue is resolved.
- **Frontier:** open, unblocked, unclaimed files under `.scratch/<effort>/issues/`; first by number wins.
- **Claim:** set `Status: claimed` and save before work.
- **Resolve:** append the answer under `## Answer`, set `Status: resolved`, then append a context pointer to the map's Decisions-so-far.
