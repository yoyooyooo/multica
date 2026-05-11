/**
 * "My Issues" list, server-filtered by scope. Mirrors the three scopes web
 * exposes in `packages/views/my-issues/components/my-issues-page.tsx` (50-70):
 *   - assigned: issues where assignee_id = me
 *   - created:  issues where creator_id  = me
 *   - agents:   issues assigned to any agent I own
 *
 * Cache key shape is `issueKeys.myList(wsId, scope, filter)` — same prefix
 * as web's `packages/core/issues/queries.ts` so a future WS handler can
 * invalidate `issueKeys.myAll(wsId)` and reach both clients.
 */
import { queryOptions } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { api } from "@/data/api";
import {
  issueKeys,
  type MyIssuesFilter,
  type MyIssuesScope,
} from "./issue-keys";

export function buildMyIssuesFilter(
  scope: MyIssuesScope,
  userId: string,
  agents: Agent[],
): MyIssuesFilter {
  switch (scope) {
    case "assigned":
      return { assignee_id: userId };
    case "created":
      return { creator_id: userId };
    case "agents":
      return {
        assignee_ids: agents
          .filter((a) => a.owner_id === userId)
          .map((a) => a.id)
          .sort(),
      };
  }
}

/**
 * The `agents` scope sends `assignee_ids` as a comma-joined query string
 * (api.ts). When the user has zero owned agents the param would be empty,
 * which the backend currently treats as "no filter" and would return the
 * whole workspace. Disable the query in that case so the empty-state UI
 * renders instead.
 */
function isEmptyAgentsScope(scope: MyIssuesScope, filter: MyIssuesFilter) {
  return scope === "agents" && (filter.assignee_ids?.length ?? 0) === 0;
}

export const myIssueListOptions = (
  wsId: string | null,
  scope: MyIssuesScope,
  filter: MyIssuesFilter,
) =>
  queryOptions({
    queryKey: issueKeys.myList(wsId, scope, filter),
    queryFn: async ({ signal }) => {
      const res = await api.listIssues(filter, { signal });
      return res.issues;
    },
    enabled: !!wsId && !isEmptyAgentsScope(scope, filter),
  });
