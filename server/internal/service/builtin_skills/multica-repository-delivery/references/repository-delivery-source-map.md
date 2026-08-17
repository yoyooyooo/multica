# Repository delivery source map

Re-check these symbols before changing the Agent-facing command map.

| Behavior | Source authority |
|---|---|
| Optional complete `git`+`gh` Shim PATH prepend | `server/internal/daemon/daemon.go` (`prependTaskToolShimPath`); official path is direct `ags-cli git` / `ags-cli pr` |
| Retired Multica assertion-authority / `pr.merge` delegation routes | `server/cmd/server/router.go`; those routes return `404` |
| Workload merge marker | Agent `custom_env` `AGS_ACCESS_ROLE=maintainer` or `admin` adds only `pr.merge` |
| Current-execution-context is not repository authority | `server/internal/handler/current_execution_context.go`; facts are Workspace/Agent/Task ids, not repo credentials |

Multica does not teach Context selection, profile files, provider mapping, or
raw Forgejo/GitLab API as Agent workflow. Those stay off this skill.
