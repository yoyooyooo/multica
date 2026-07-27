# working-on-issues source map

Evidence layer for `SKILL.md`. Citations use stable file + symbol/query names
rather than line numbers, because line offsets drift whenever handlers are
extended. Re-run the verification commands at the bottom before depending on a
symbol after a refactor.

## `multica issue pull-requests` — read PR links from Multica

| Behavior | Source authority |
|---|---|
| CLI command `pull-requests <id>` (alias `prs`) | `server/cmd/multica/cmd_issue.go` (`issuePullRequestsCmd`) |
| CLI request/formatting | `server/cmd/multica/cmd_issue.go` (`runIssuePullRequests`) |
| API route registration | `server/cmd/server/router.go` (`NewRouterWithOptions`) |
| Handler and query | `server/internal/handler/github.go` (`ListPullRequestsForIssue`), `server/pkg/db/queries/github.sql` (`ListPullRequestsByIssue`) |
| Row → response mapping | `server/internal/handler/github.go` (`issuePullRequestRowToResponse`) |

The CLI resolves the issue ref, GETs `/api/issues/<id>/pull-requests`, and for
`--output json` prints the raw `{"pull_requests": [...]}` body. The default
`table` output shows number, state, title, and URL.

## PR response shape

`GitHubPullRequestResponse` in `server/internal/handler/github.go` owns the JSON
shape. Agent-relevant fields include:

- `provider`, `number`, `html_url`, `title`, `state`;
- `merged_at`, `closed_at`, `mergeable_state`;
- `snapshot_available`, `mergeable`, `merge_state_status`;
- `checks_rollup`, the four run counts, `failed_check_names`, and
  `checks_conclusion`.

There is no standalone `draft` or `merged` boolean. `derivePRState` in the same
file folds provider facts into `merged`, `closed`, `draft`, or `open` and the
list endpoint returns that state. Current-head snapshot availability is decided
by `currentGitHubSnapshotAvailable`; VCS check state is folded by
`aggregateChecksConclusion`.

## AGS workload assertion bridge

| Behavior | Source authority |
|---|---|
| Strict request, purpose/audience/TTL signing | `server/internal/handler/workload_assertion.go` (`CreateWorkloadAssertion`, `normalizeRequestedTTL`) |
| Default team-v4 operation constraints | same file (`normalizeRequestedOperation`, `normalizePRCreateConstraints`, `normalizePRReadConstraints`, `normalizePRRebaseConstraints`, `normalizeReviewReadConstraints`, `normalizeCIReadConstraints`) |
| JWT issuer vs AGS issuer ID separation | same file (`ValidateWorkloadAssertionConfiguration`, `enrichSessionExchangeWorkload`) |
| Startup and readiness fail closed | `server/cmd/server/main.go` (`main`), `server/cmd/server/health.go` (`newServerHealth`, `computeReadiness`) |
| Eight-operation signed/normalization matrix | `server/internal/handler/workload_assertion_test.go` (`TestCreateWorkloadAssertionSessionExchangeSignsAgentKitProductionConstraintFixtures`, `TestNormalizeSessionExchangeScopeMatchesDefaultTeamV4AgentKitOperations`) |
| AgentKit Forgejo list/runs/log shapes and negative matrix | same file (`TestNormalizeAgentKitForgejoCommandConstraintFixtures`, `TestNormalizeRequestedOperationDefaultTeamV4NegativeMatrix`) |
| Deferred-operation signer rejection | same file (`TestCreateWorkloadAssertionSessionExchangeRejectsDeferredOperationsBeforeSigning`) |

`MULTICA_WORKLOAD_ASSERTION_ISSUER` owns JWT `iss` only. The distinct required
secret-free `MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID` must exactly match
AGS `trusted_issuers[].id` and is signed only as
`workload.workload_context.issuer_instance_id`. Optional canonical
`requested_ttl` is copied to the JWT top level only for AGS session exchange,
is limited to `15m`, and remains absent when omitted.

The fixed default team-v4 operations are `repo.read`, `git.read`, `git.push`,
`pr.create`, `pr.read`, `pr.rebase`, `review.read`, and `ci.read`. Their
constraints are operation-specific: the repository/Git operations are exact
empty; PR create has both required canonical branch refs; PR read has one exact
number-or-head variant; rebase retains its exact four-key intent; review read
has both positive safe-integer PR numbers; and CI read accepts exactly a
repository-wide empty shape, a positive safe-integer `run_id`, or those two PR
numbers with optional lowercase-40 `head_sha`. The Forgejo fixtures pin actual
PR-list, runs, and log command shapes: state-only PR list, event-only, SHA-only,
mixed, and unknown variants fail before signing. Unknown/mixed/legacy
`exact_head`, wrong/null/secret-shaped values also fail. Deferred `pr.merge`,
`review.submit`, `repo.admin`, and `repo.create` are rejected by the default
signer even with exact empty constraints.

## Two distinct webhook paths: link vs close-intent

Both paths are implemented in `mirrorPullRequestForWorkspace` in
`server/internal/handler/github.go` and gated by
`workspaceAutoLinkPRsEnabled`.

### Path 1 — link (title OR body OR branch)

- identifier authority: `identifierRe` and `extractIdentifiers`;
- persistence authority: `LinkIssueToPullRequest` in
  `server/pkg/db/queries/github.sql`;
- body-only bare mentions are marked `reference_only`; a title, branch, or
  closing-keyword reference qualifies the link for normal PR display/aggregate use.

`reference_only` links remain audit facts but are excluded from the displayed PR
list and from completion blockers/enablers.

### Path 2 — close intent (title OR body, keyword-adjacent)

- closing authority: `closingIdentifierRe` and
  `extractClosingIdentifiers` in `server/internal/handler/github.go`;
- branch names are deliberately excluded;
- only an adjacent `Closes`/`Fixes`/`Resolves PREFIX-N` declaration sets
  `close_intent`.

A title/branch identifier can therefore link without completing. A bare body
mention is reference-only. A closing keyword both links and records intent.

### Exact provider completion boundary

- parser authority: `server/internal/completionpolicy/policy.go` (`Parse`);
- provider adapters: `mirrorPullRequestForWorkspace`, `mirrorVCSPullRequest`,
  `RegisterExternalPullRequestLink`, and `CompleteIssueFromExternalPR`;
- final materialization authority:
  `server/internal/handler/pull_request_completion.go`
  (`evaluatePullRequestCompletionLocked`, wrapped by `evaluatePullRequestCompletion`);
- atomic predicate:
  `server/pkg/db/queries/pull_request_completion.sql`
  (`CompleteIssueFromPullRequest`);
- transaction serialization authority: migration
  `224_external_pr_integration_reconcile` plus
  `LockIssueCompletionTransition` in `server/pkg/db/queries/issue.sql`.

Absent, exact empty string, and exact `leaf_child_only` allow provider-driven
leaf-child completion. Exact `record_only` records facts without terminal.
Unknown strings, non-string values, case variants, and whitespace variants fail
closed. The terminal SQL repeats the policy predicate and aggregates GitHub,
native VCS, and external provider facts.

Provider facts, explicit single/batch terminal writes, and child topology
writes use the same Issue-scoped advisory locks. In addition, all child create
and reparent writers take `LockWorkspaceIssueTopology`, so topology validation
uses a serialized workspace snapshot and concurrent disjoint reparent sets
cannot create a cycle. Child create then locks its parent; reparent locks the
child and old/new parents in UUID order and revalidates parent state/topology.
Terminal status and its activity commit in one
transaction; activity failure rolls back status and prevents task/parent
release. Provider facts take a workspace-scoped provider lock before identity and Issue locks. Workspace/GitHub-installation/VCS-connection deletion takes that same provider lock before freezing workspace rows, enumerating all affected Issues, and taking UUID-sorted Issue locks; Issue/batch deletion takes its exact Issue locks before row/FK/application cleanup. This preserves provider-workspace → identity → Issue advisory → row-lock order. GitHub PR fact transaction errors return non-2xx so the provider retries instead of accepting a rolled-back multi-Issue delivery. Only after commit are activity/Issue events published and
`notifyParentOfChildDone` invoked. The design does not claim an outbox guarantee
for a process crash after that commit.

## Status side effects

| Behavior | Source authority |
|---|---|
| Create/update/batch enqueue predicate | `server/internal/handler/issue.go`, `server/internal/service/issue_trigger.go` (`WillEnqueueRun`) |
| `backlog` parks assigned work | `WillEnqueueRun` and its callers |
| Parent barrier notification | `server/internal/handler/issue_child_done.go` (`notifyParentOfChildDone`) |
| Issue deletion cancels tasks; status updates do not | `server/internal/handler/issue.go`, `server/internal/service/task.go` (`CancelTasksForIssue`) |
| Task start/complete do not own Issue status | `server/internal/service/task.go` (`StartTask`, `CompleteTask`) |
| Assignment brief | `server/internal/daemon/execenv/runtime_config_sections.go` (`writeWorkflowAssignment`) |
| Failed-task rollback | `server/internal/service/task.go` (`HandleFailedTasks`) |

## Sub-issue stages (barrier wake)

| Behavior | Source authority |
|---|---|
| Nullable `issue.stage >= 1` | `server/migrations/123_issue_stage.up.sql` |
| Barrier closure | `server/internal/handler/issue_child_done.go` (`stageBarrierClosed`) |
| Progress summary / next stage | same file (`stageProgressSummary`) |
| `--stage` create/update flags and children CLI | `server/cmd/multica/cmd_issue.go` |
| Children route | `server/cmd/server/router.go`, `server/internal/handler/issue.go` (`ListChildIssues`) |

The server detects a closed barrier and wakes the parent assignee. Promoting the
next stage remains the agent's decision.

## Metadata and custom properties

| Behavior | Source authority |
|---|---|
| Metadata set/delete CLI | `server/cmd/multica/cmd_issue_metadata.go` |
| Metadata routes | `server/cmd/server/router.go` |
| Property and issue-property CLI | `server/cmd/multica/cmd_property.go` |
| Property admin/type/icon validation | `server/internal/handler/property.go` |
| Property routes | `server/cmd/server/router.go` |

## Verification commands

```bash
cd server
grep -n 'issuePullRequestsCmd\|runIssuePullRequests' cmd/multica/cmd_issue.go
grep -n 'ListPullRequestsForIssue' cmd/server/router.go internal/handler/github.go
grep -n 'func issuePullRequestRowToResponse\|type GitHubPullRequestResponse struct\|func derivePRState\|func extractIdentifiers\|func extractClosingIdentifiers\|closingIdentifierRe' internal/handler/github.go
grep -n 'qualifyingIdents\|reference_only\|ReferenceOnly' internal/handler/github.go pkg/db/queries/github.sql
grep -n 'evaluatePullRequestCompletion\|CompleteIssueFromPullRequest\|LockIssueCompletionTransition\|LockWorkspaceIssueTopology' internal/handler/{pull_request_completion.go,external_pr_integration.go,github.go,vcs_webhook.go,issue.go} internal/service/issue.go pkg/db/queries/{pull_request_completion.sql,issue.sql}
grep -n 'normalizeRequestedOperation\|normalizePRCreateConstraints\|normalizePRReadConstraints\|normalizePRRebaseConstraints\|normalizeReviewReadConstraints\|normalizeCIReadConstraints' internal/handler/workload_assertion.go internal/handler/workload_assertion_test.go
grep -n 'ValidateWorkloadAssertionConfiguration\|normalizeRequestedTTL\|issuer_instance_id\|requested_ttl' internal/handler/workload_assertion.go internal/handler/workload_assertion_test.go cmd/server/{main.go,health.go}
grep -n 'func (h \*Handler) notifyParentOfChildDone\|func stageBarrierClosed\|func stageProgressSummary' internal/handler/issue_child_done.go
grep -n 'func (s \*IssueService) WillEnqueueRun' internal/service/issue_trigger.go
```
