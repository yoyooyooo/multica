/**
 * Issue detail + timeline queries. Mobile-owned; mirrors a strict subset of
 * packages/core/issues/queries.ts (issueDetailOptions and
 * issueTimelineInfiniteOptions). Mobile v1 only needs latest-mode for the
 * initial page and before-cursor for older pages — no around-mode (no deep
 * link jump) and no after-mode (no WS prepend yet).
 *
 * Query keys live in ./issue-keys so they share a prefix with the my-issues
 * list cache — WS handlers can later invalidate the whole `issues` subtree
 * with one call.
 */
import {
  infiniteQueryOptions,
  queryOptions,
} from "@tanstack/react-query";
import type { TimelinePage } from "@multica/core/types";
import { api } from "@/data/api";
import { issueKeys } from "./issue-keys";

type TimelineCursor = { mode: "before"; cursor: string } | null;

export { issueKeys } from "./issue-keys";

export const issueDetailOptions = (wsId: string | null, id: string) =>
  queryOptions({
    queryKey: issueKeys.detail(wsId, id),
    queryFn: ({ signal }) => api.getIssue(id, { signal }),
    enabled: !!wsId && !!id,
  });

export const issueTimelineInfiniteOptions = (
  wsId: string | null,
  id: string,
) =>
  infiniteQueryOptions<
    TimelinePage,
    Error,
    { pages: TimelinePage[]; pageParams: TimelineCursor[] },
    readonly unknown[],
    TimelineCursor
  >({
    queryKey: issueKeys.timeline(wsId, id),
    queryFn: ({ pageParam, signal }) =>
      api.listTimeline(id, pageParam, undefined, { signal }),
    initialPageParam: null,
    getNextPageParam: (lastPage) =>
      lastPage.has_more_before && lastPage.next_cursor
        ? { mode: "before" as const, cursor: lastPage.next_cursor }
        : undefined,
    enabled: !!wsId && !!id,
  });
