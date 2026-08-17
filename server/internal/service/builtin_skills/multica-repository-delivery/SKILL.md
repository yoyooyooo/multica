---
name: multica-repository-delivery
description: "Use when changing a git repository or opening, updating, checking, or merging a PR. Issue status, PR-to-issue linking, and close intent stay in multica-working-on-issues."
user-invocable: false
allowed-tools: Bash(ags-cli *), Bash(git *), Bash(gh *), Bash(glab *)
---

# Repository delivery

AGS remotes use AGS as the Git/PR authority. The runtime already supplies
workload identity; do not configure credentials, profiles, or route URLs.
Issue linking and close intent stay in `multica-working-on-issues`.

Use the CLI that matches the repository origin.

```text
AGS origin    -> ags-cli git / ags-cli pr
GitHub origin -> git / gh
GitLab origin -> git / glab
```

Rules:

- Official Daemon plus the matching CLI is enough. A Shim PATH is optional.
- On AGS remotes, identify PRs by the AGS PR number. Do not guess another
  host's PR number.
- If a write result is unknown, GET / readback the same object. Do not blindly
  retry a create or merge.
- Merge only when the task explicitly asks to merge, and only with
  `--match-head-commit <40-sha>`.
- `AGS_ACCESS_ROLE=maintainer` or `admin` is the only extra Agent marker for
  `pr.merge`. It adds that one operation.

Do not treat Multica task tokens, issue metadata, or external-PR link tokens as
repository authority.

## Incorrect → correct

```text
wrong: source a durable AGS profile or set route URLs
right: use the origin-matched CLI; identity comes from the runtime

wrong: guess the Forgejo or GitLab PR number from the AGS number
right: use the AGS PR number on AGS remotes

wrong: merge because CI looks green
right: merge only when asked, with --match-head-commit
```

## References

`references/repository-delivery-source-map.md` — Daemon PATH convenience vs
direct `ags-cli`, retired Multica merge routes, and the maintainer-only merge
marker.
