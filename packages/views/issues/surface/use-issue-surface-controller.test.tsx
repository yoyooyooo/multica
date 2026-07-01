/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { issueKeys } from "@multica/core/issues/queries";
import {
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
} from "@multica/core/issues/stores/surface-view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import type {
  AgentTask,
  ListIssuesParams,
  ListIssuesResponse,
} from "@multica/core/types";
import { useIssueSurfaceController } from "./use-issue-surface-controller";

const updateIssueMutate = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useUpdateIssue: () => ({ mutate: updateIssueMutate }),
}));

vi.mock("../../i18n", () => ({
  useT: () => ({ t: () => "translated" }),
}));

function makeWrapper(qc: QueryClient, surfaceKey = "project:p1") {
  const store = getIssueSurfaceViewStore(surfaceKey);
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <ViewStoreProvider store={store}>{children}</ViewStoreProvider>
      </QueryClientProvider>
    );
  };
}

function never<T>() {
  return new Promise<T>(() => {});
}

describe("useIssueSurfaceController", () => {
  let qc: QueryClient;
  let listIssues: ReturnType<
    typeof vi.fn<(params?: ListIssuesParams) => Promise<ListIssuesResponse>>
  >;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    listIssues = vi.fn(() => never<ListIssuesResponse>());
    setApiInstance({
      listIssues,
      listGroupedIssues: vi.fn(() => never()),
      getAgentTaskSnapshot: vi.fn(() => never<AgentTask[]>()),
      getChildIssueProgress: vi.fn(() => never()),
    } as unknown as ApiClient);
    pruneIssueSurfaceViewStates([]);
    updateIssueMutate.mockClear();
  });

  afterEach(() => {
    cleanup();
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    vi.restoreAllMocks();
  });

  it("derives the project scope key, API filter, and sorted myList cache key", async () => {
    const store = getIssueSurfaceViewStore("project:p1");
    store.getState().setSortBy("priority");
    store.getState().setSortDirection("desc");

    const { result } = renderHook(
      () =>
        useIssueSurfaceController({
          scope: { type: "project", projectId: "p1" },
          modes: ["board", "list", "swimlane", "gantt"],
        }),
      { wrapper: makeWrapper(qc, "project:p1") },
    );

    await waitFor(() => expect(listIssues).toHaveBeenCalled());

    const expectedSort = { sort_by: "priority", sort_direction: "desc" } as const;
    const expectedFilter = { project_id: "p1" };

    expect(result.current.scopeKey).toBe("project:p1");
    expect(result.current.filter).toEqual(expectedFilter);
    expect(result.current.sort).toEqual(expectedSort);
    expect(
      qc.getQueryCache().find({
        queryKey: issueKeys.myListSorted(
          "ws-1",
          "project:p1",
          expectedFilter,
          expectedSort,
        ),
        exact: true,
      }),
    ).toBeDefined();
    expect(listIssues).toHaveBeenCalledWith(
      expect.objectContaining({
        project_id: "p1",
        sort_by: "priority",
        sort_direction: "desc",
      }),
    );
  });

  it("delegates movement through useUpdateIssue without rewriting the mutation path", () => {
    const { result } = renderHook(
      () =>
        useIssueSurfaceController({
          scope: { type: "project", projectId: "p1" },
          modes: ["board", "list", "swimlane", "gantt"],
        }),
      { wrapper: makeWrapper(qc, "project:p1") },
    );
    const onSettled = vi.fn();

    act(() => {
      result.current.moveIssue(
        "issue-1",
        { status: "in_progress", position: 42 },
        onSettled,
      );
    });

    expect(updateIssueMutate).toHaveBeenCalledWith(
      { id: "issue-1", status: "in_progress", position: 42 },
      expect.objectContaining({
        onError: expect.any(Function),
        onSettled: expect.any(Function),
      }),
    );

    const options = updateIssueMutate.mock.calls[0]?.[1] as
      | { onSettled?: () => void }
      | undefined;
    options?.onSettled?.();
    expect(onSettled).toHaveBeenCalled();
  });
});
