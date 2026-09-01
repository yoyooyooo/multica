Status: wontfix
Resolution: The minimal offline font overlay and license check are merged into [01 Rebuild from latest upstream](01-establish-clean-upstream-release-lane.md); no separate font pipeline is authorized.
Blocked by: None - superseded

# Build the web application offline with licensed fonts

## Parent

[Fork Upstream Convergence Program](../PRD.md)

## User stories covered

29 and 30.

## What to build

Provide a deterministic production web build that does not fetch Google Fonts or other required font assets from the network. Prefer a generally useful upstream local-font solution; when an interim fork overlay is required, keep it additive and include the exact distributable license and copyright material for every shipped font asset.

## Simplification guardrails

- Choose the smallest legal offline font solution that preserves acceptable rendering. Do not build a font asset pipeline, theme system, or visual redesign.
- Upstream submission is optional follow-up and does not block the fork build once the local solution and licenses pass.

## Acceptance criteria

- [ ] A production web build succeeds with outbound network access disabled after declared dependencies are available.
- [ ] Runtime pages render the intended font stack without missing assets or layout breakage.
- [ ] Every redistributed font file has traceable source, license, and copyright material in the artifact or accompanying notices.
- [ ] The fork does not modify official product layouts, package scripts, or Dockerfiles solely to inject target-specific font behavior when an overlay can do so.
- [ ] Build tests fail when a required font asset or license file is missing.
- [ ] The reusable portion is prepared as an upstream candidate; any interim fork layer is named and removable.

## Blocked by

None - can start immediately.
