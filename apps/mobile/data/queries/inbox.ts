import { queryOptions } from "@tanstack/react-query";
import { api } from "@/data/api";

/**
 * Inbox query — keyed on wsId so switching workspaces auto-invalidates the
 * cache (TQ sees a new key and refetches). Same pattern as web/desktop's
 * inboxKeys.list(wsId) in packages/core/inbox/queries.ts.
 */
export const inboxListOptions = (wsId: string | null) =>
  queryOptions({
    queryKey: ["inbox", wsId] as const,
    queryFn: ({ signal }) => api.listInbox({ signal }),
    enabled: !!wsId,
  });
