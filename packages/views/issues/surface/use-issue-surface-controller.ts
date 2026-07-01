"use client";

import { useCallback, useEffect, useMemo } from "react";
import { useQuery, type QueryKey } from "@tanstack/react-query";
import { toast } from "sonner";
import type {
  CreateIssueRequest,
  Issue,
  IssueAssigneeGroup,
  UpdateIssueRequest,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { BOARD_STATUSES } from "@multica/core/issues/config";
import { dateOnlyToLocalDate } from "@multica/core/issues/date";
import {
  useBatchDeleteIssues,
  useBatchUpdateIssues,
  useUpdateIssue,
} from "@multica/core/issues/mutations";
import {
  childIssueProgressOptions,
  issueAssigneeGroupsOptions,
  issueListOptions,
  myIssueAssigneeGroupsOptions,
  myIssueListOptions,
  projectGanttIssuesOptions,
  type AssigneeGroupedIssuesFilter,
  type IssueSortParam,
  type MyIssuesFilter,
} from "@multica/core/issues/queries";
import {
  issueScopeKey,
  issueScopeToCreateDefaults,
  type IssueScope,
  type WorkspaceIssueActorKind,
} from "@multica/core/issues/surface/scope";
import type { IssueDateFilter } from "@multica/core/issues/stores/view-store";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { useModalStore } from "@multica/core/modals";
import {
  applyIssueFilters,
  filterRunningAssigneeGroups,
  type IssueFilterState,
  type IssueFilters,
} from "../utils/filter";
import type { ChildProgress } from "../components/list-row";
import type { IssueSurfaceMode } from "./types";
import {
  useIssueSurfaceActivity,
  type IssueSurfaceActivity,
} from "./activity";
import {
  type IssueSurfaceActions,
  type IssueSurfaceMutationOptions,
} from "./actions-context";
import {
  type IssueSurfaceSelection,
  useCreateIssueSurfaceSelection,
} from "./selection-context";
import { useT } from "../../i18n";

const EMPTY_ISSUES: Issue[] = [];

export type MoveIssueUpdates = Pick<
  UpdateIssueRequest,
  "status" | "assignee_type" | "assignee_id" | "position" | "parent_issue_id"
>;

interface UseIssueSurfaceControllerInput {
  scope: IssueScope;
  modes: IssueSurfaceMode[];
  createDefaults?: Partial<CreateIssueRequest>;
}

type SurfaceQueryPlan =
  | {
      kind: "workspace";
      queryScope: undefined;
      queryFilter: MyIssuesFilter;
      groupedScopeFilter: AssigneeGroupedIssuesFilter;
      loadMoreScope: undefined;
      loadMoreFilter: undefined;
      userId: undefined;
      postFilter: (issue: Issue) => boolean;
    }
  | {
      kind: "scoped";
      queryScope: string;
      queryFilter: MyIssuesFilter;
      groupedScopeFilter: AssigneeGroupedIssuesFilter;
      loadMoreScope: string;
      loadMoreFilter: MyIssuesFilter;
      userId?: string;
      postFilter: (issue: Issue) => boolean;
    };

export interface IssueSurfaceController {
  scopeKey: string;
  projectId?: string;
  createDefaults: Partial<CreateIssueRequest>;
  viewMode: IssueSurfaceMode;
  allowGantt: boolean;
  surfaceIssues: Issue[];
  projectIssues: Issue[];
  issues: Issue[];
  swimlaneIssues: Issue[];
  filteredGanttIssues: Issue[];
  assigneeGroups?: IssueAssigneeGroup[];
  assigneeGroupQueryKey?: QueryKey;
  assigneeGroupFilter?: AssigneeGroupedIssuesFilter;
  filter: MyIssuesFilter;
  loadMoreScope?: string;
  loadMoreFilter?: MyIssuesFilter;
  sort: IssueSortParam;
  ganttIssues: Issue[];
  visibleStatuses: typeof BOARD_STATUSES;
  hiddenStatuses: typeof BOARD_STATUSES;
  activeFilters: Omit<IssueFilters, "statusFilters" | "runningIssueIds">;
  activity: IssueSurfaceActivity;
  actions: IssueSurfaceActions;
  selection: IssueSurfaceSelection;
  childProgressMap: Map<string, ChildProgress>;
  isLoading: boolean;
  isEmpty: boolean;
  openCreateIssue: (defaults?: Partial<CreateIssueRequest>) => void;
  moveIssue: (
    issueId: string,
    updates: MoveIssueUpdates,
    onSettled?: () => void,
  ) => void;
}

function issueDateFilterToApiParams(filter: IssueDateFilter | null) {
  if (!filter) return {};

  const from = dateOnlyToLocalDate(filter.from);
  const to = dateOnlyToLocalDate(filter.to);
  if (!from || !to) return {};

  const start = from <= to ? from : to;
  const endSource = from <= to ? to : from;
  const end = new Date(endSource);
  end.setDate(end.getDate() + 1);

  return {
    date_field: filter.field,
    date_start: start.toISOString(),
    date_end: end.toISOString(),
  };
}

function workspaceActorPostFilter(actorKind?: WorkspaceIssueActorKind) {
  return (issue: Issue) => {
    if (actorKind === "members") return issue.assignee_type === "member";
    if (actorKind === "agents") {
      return issue.assignee_type === "agent" || issue.assignee_type === "squad";
    }
    return true;
  };
}

function workspaceGroupedFilter(actorKind?: WorkspaceIssueActorKind) {
  if (actorKind === "members") {
    return { assignee_types: ["member"] } satisfies AssigneeGroupedIssuesFilter;
  }
  if (actorKind === "agents") {
    return {
      assignee_types: ["agent", "squad"],
    } satisfies AssigneeGroupedIssuesFilter;
  }
  return {} satisfies AssigneeGroupedIssuesFilter;
}

function myRelationQuery(scope: Extract<IssueScope, { type: "my" }>) {
  switch (scope.relation) {
    case "assigned":
      return {
        queryScope: "assigned",
        queryFilter: { assignee_id: scope.userId },
        userId: undefined,
      } satisfies Pick<SurfaceQueryPlan & { kind: "scoped" }, "queryScope" | "queryFilter" | "userId">;
    case "created":
      return {
        queryScope: "created",
        queryFilter: { creator_id: scope.userId },
        userId: undefined,
      } satisfies Pick<SurfaceQueryPlan & { kind: "scoped" }, "queryScope" | "queryFilter" | "userId">;
    case "involved":
      return {
        queryScope: "agents",
        queryFilter: { involves_user_id: scope.userId },
        userId: undefined,
      } satisfies Pick<SurfaceQueryPlan & { kind: "scoped" }, "queryScope" | "queryFilter" | "userId">;
    case "all":
      return {
        queryScope: "all",
        queryFilter: {},
        userId: scope.userId,
      } satisfies Pick<SurfaceQueryPlan & { kind: "scoped" }, "queryScope" | "queryFilter" | "userId">;
  }
}

function queryPlanForScope(scope: IssueScope, scopeKey: string): SurfaceQueryPlan {
  switch (scope.type) {
    case "workspace":
      return {
        kind: "workspace",
        queryScope: undefined,
        queryFilter: {},
        groupedScopeFilter: workspaceGroupedFilter(scope.actorKind),
        loadMoreScope: undefined,
        loadMoreFilter: undefined,
        userId: undefined,
        postFilter: workspaceActorPostFilter(scope.actorKind),
      };
    case "project": {
      const queryFilter = { project_id: scope.projectId };
      return {
        kind: "scoped",
        queryScope: scopeKey,
        queryFilter,
        groupedScopeFilter: queryFilter,
        loadMoreScope: scopeKey,
        loadMoreFilter: queryFilter,
        userId: undefined,
        postFilter: () => true,
      };
    }
    case "my": {
      const query = myRelationQuery(scope);
      return {
        kind: "scoped",
        ...query,
        groupedScopeFilter: query.queryFilter,
        loadMoreScope: query.queryScope,
        loadMoreFilter: query.queryFilter,
        postFilter: () => true,
      };
    }
    case "actor": {
      const queryFilter =
        scope.relation === "assigned"
          ? { assignee_id: scope.actorId }
          : { creator_id: scope.actorId };
      return {
        kind: "scoped",
        queryScope: scopeKey,
        queryFilter,
        groupedScopeFilter: queryFilter,
        loadMoreScope: scopeKey,
        loadMoreFilter: queryFilter,
        userId: undefined,
        postFilter: (issue) =>
          scope.relation === "assigned"
            ? issue.assignee_type === scope.actorType &&
              issue.assignee_id === scope.actorId
            : issue.creator_type === scope.actorType &&
              issue.creator_id === scope.actorId,
      };
    }
    case "team":
      throw new Error("IssueSurface does not support team scope without a Team issues API.");
  }
}

export function useIssueSurfaceController({
  scope,
  modes,
  createDefaults,
}: UseIssueSurfaceControllerInput): IssueSurfaceController {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const scopeKey = issueScopeKey(scope);
  const projectId = scope.type === "project" ? scope.projectId : undefined;
  const queryPlan = useMemo(() => queryPlanForScope(scope, scopeKey), [scope, scopeKey]);

  const viewMode = useViewStore((s) => s.viewMode);
  const setViewMode = useViewStore((s) => s.setViewMode);
  const grouping = useViewStore((s) => s.grouping);
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const dateFilter = useViewStore((s) => s.dateFilter);
  const statusFilters = useViewStore((s) => s.statusFilters);
  const priorityFilters = useViewStore((s) => s.priorityFilters);
  const assigneeFilters = useViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useViewStore((s) => s.creatorFilters);
  const projectFilters = useViewStore((s) => s.projectFilters);
  const includeNoProject = useViewStore((s) => s.includeNoProject);
  const labelFilters = useViewStore((s) => s.labelFilters);
  const agentRunningFilter = useViewStore((s) => s.agentRunningFilter);

  const allowedModes = useMemo(() => new Set<IssueSurfaceMode>(modes), [modes]);
  const fallbackMode = modes[0] ?? "list";
  const effectiveViewMode = allowedModes.has(viewMode as IssueSurfaceMode)
    ? (viewMode as IssueSurfaceMode)
    : fallbackMode;

  useEffect(() => {
    if (!allowedModes.has(viewMode as IssueSurfaceMode)) {
      setViewMode(fallbackMode);
    }
  }, [allowedModes, fallbackMode, setViewMode, viewMode]);

  const resolvedCreateDefaults = useMemo(
    () => ({ ...issueScopeToCreateDefaults(scope), ...createDefaults }),
    [createDefaults, scope],
  );

  const dateParams = useMemo(
    () => issueDateFilterToApiParams(dateFilter),
    [dateFilter],
  );
  const sort = useMemo<IssueSortParam>(
    () => ({
      sort_by: sortBy,
      sort_direction: sortBy !== "position" ? sortDirection : undefined,
      ...dateParams,
    }),
    [dateParams, sortBy, sortDirection],
  );

  const activity = useIssueSurfaceActivity(scope);
  const selection = useCreateIssueSurfaceSelection(
    scopeKey,
    `${scopeKey}:${effectiveViewMode}`,
  );
  const filterContext = useMemo(
    () => ({ activityByIssueId: activity.activityByIssueId }),
    [activity.activityByIssueId],
  );

  const usesAssigneeBoard =
    effectiveViewMode === "board" && grouping === "assignee";
  const usesGantt = effectiveViewMode === "gantt" && !!projectId;

  const projectFilterState = useMemo(
    () => ({
      projectFilters: scope.type === "project" ? [] : projectFilters,
      includeNoProject: scope.type === "project" ? false : includeNoProject,
    }),
    [includeNoProject, projectFilters, scope.type],
  );
  const { projectFilters: viewProjectFilters, includeNoProject: viewIncludeNoProject } =
    projectFilterState;

  const assigneeGroupFilter = useMemo<AssigneeGroupedIssuesFilter>(
    () => ({
      ...queryPlan.groupedScopeFilter,
      statuses: statusFilters.length > 0 ? statusFilters : [...BOARD_STATUSES],
      priorities: priorityFilters,
      assignee_filters: assigneeFilters,
      include_no_assignee: includeNoAssignee,
      creator_filters: creatorFilters,
      project_ids: viewProjectFilters,
      include_no_project: viewIncludeNoProject,
      label_ids: labelFilters,
    }),
    [
      assigneeFilters,
      creatorFilters,
      includeNoAssignee,
      labelFilters,
      priorityFilters,
      queryPlan.groupedScopeFilter,
      statusFilters,
      viewIncludeNoProject,
      viewProjectFilters,
    ],
  );

  const workspaceAssigneeGroupsOptions = issueAssigneeGroupsOptions(
    wsId,
    assigneeGroupFilter,
    sort,
  );
  const scopedAssigneeGroupsOptions = myIssueAssigneeGroupsOptions(
    wsId,
    queryPlan.queryScope ?? scopeKey,
    assigneeGroupFilter,
    queryPlan.userId,
    sort,
  );
  const activeAssigneeGroupsOptions =
    queryPlan.kind === "workspace"
      ? workspaceAssigneeGroupsOptions
      : scopedAssigneeGroupsOptions;

  const workspaceStatusIssuesQuery = useQuery({
    ...issueListOptions(wsId, sort),
    enabled: queryPlan.kind === "workspace" && !usesAssigneeBoard && !usesGantt,
  });
  const scopedStatusIssuesQuery = useQuery({
    ...myIssueListOptions(
      wsId,
      queryPlan.queryScope ?? scopeKey,
      queryPlan.queryFilter,
      queryPlan.userId,
      sort,
    ),
    enabled: queryPlan.kind === "scoped" && !usesAssigneeBoard && !usesGantt,
  });
  const assigneeGroupsQuery = useQuery({
    ...activeAssigneeGroupsOptions,
    enabled: usesAssigneeBoard,
  });
  const ganttIssuesQuery = useQuery({
    ...projectGanttIssuesOptions(wsId, projectId ?? ""),
    enabled: usesGantt,
  });

  const bucketedIssues = useMemo(() => {
    const raw = usesAssigneeBoard
      ? (assigneeGroupsQuery.data?.groups.flatMap((group) => group.issues) ?? [])
      : queryPlan.kind === "workspace"
        ? (workspaceStatusIssuesQuery.data ?? EMPTY_ISSUES)
        : (scopedStatusIssuesQuery.data ?? EMPTY_ISSUES);
    return raw.filter(queryPlan.postFilter);
  }, [
    assigneeGroupsQuery.data?.groups,
    queryPlan,
    scopedStatusIssuesQuery.data,
    usesAssigneeBoard,
    workspaceStatusIssuesQuery.data,
  ]);

  const ganttIssues = ganttIssuesQuery.data ?? EMPTY_ISSUES;
  const surfaceIssues = usesGantt ? ganttIssues : bucketedIssues;

  const baseFilterState = useMemo<IssueFilterState>(
    () => ({
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters: viewProjectFilters,
      includeNoProject: viewIncludeNoProject,
      labelFilters,
      workingOnly: agentRunningFilter,
    }),
    [
      agentRunningFilter,
      assigneeFilters,
      creatorFilters,
      includeNoAssignee,
      labelFilters,
      priorityFilters,
      statusFilters,
      viewIncludeNoProject,
      viewProjectFilters,
    ],
  );

  const issues = useMemo(
    () => applyIssueFilters(surfaceIssues, baseFilterState, filterContext),
    [baseFilterState, filterContext, surfaceIssues],
  );

  const statuslessFilterState = useMemo<IssueFilterState>(
    () => ({
      ...baseFilterState,
      statusFilters: [],
    }),
    [baseFilterState],
  );

  const swimlaneIssues = useMemo(
    () => applyIssueFilters(surfaceIssues, statuslessFilterState, filterContext),
    [filterContext, statuslessFilterState, surfaceIssues],
  );

  const filteredGanttIssues = useMemo(
    () => applyIssueFilters(ganttIssues, baseFilterState, filterContext),
    [baseFilterState, filterContext, ganttIssues],
  );

  const filteredAssigneeGroups = useMemo(
    () =>
      filterRunningAssigneeGroups(
        assigneeGroupsQuery.data?.groups,
        agentRunningFilter,
        activity.runningIssueIds,
      ),
    [
      activity.runningIssueIds,
      agentRunningFilter,
      assigneeGroupsQuery.data?.groups,
    ],
  );

  const { data: childProgressMap = new Map<string, ChildProgress>() } = useQuery(
    childIssueProgressOptions(wsId),
  );

  const visibleStatuses = useMemo(() => {
    if (statusFilters.length > 0) {
      return BOARD_STATUSES.filter((s) => statusFilters.includes(s));
    }
    return BOARD_STATUSES;
  }, [statusFilters]);

  const hiddenStatuses = useMemo(
    () => BOARD_STATUSES.filter((s) => !visibleStatuses.includes(s)),
    [visibleStatuses],
  );

  const activeFilters = useMemo(
    () => ({
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters: viewProjectFilters,
      includeNoProject: viewIncludeNoProject,
      labelFilters,
      agentRunningFilter,
    }),
    [
      agentRunningFilter,
      assigneeFilters,
      creatorFilters,
      includeNoAssignee,
      labelFilters,
      priorityFilters,
      viewIncludeNoProject,
      viewProjectFilters,
    ],
  );

  const updateIssueMutation = useUpdateIssue();
  const batchUpdateMutation = useBatchUpdateIssues();
  const batchDeleteMutation = useBatchDeleteIssues();

  const updateIssue = useCallback(
    (
      issueId: string,
      updates: Partial<UpdateIssueRequest>,
      options?: IssueSurfaceMutationOptions,
    ) => {
      updateIssueMutation.mutate(
        { id: issueId, ...updates },
        {
          onSuccess: () => options?.onSuccess?.(),
          onError: (err) => {
            toast.error(
              err instanceof Error && err.message
                ? err.message
                : (options?.errorMessage ??
                    t(($) => $.detail.toast_move_issue_failed)),
            );
            options?.onError?.(err);
          },
          onSettled: () => options?.onSettled?.(),
        },
      );
    },
    [t, updateIssueMutation],
  );

  const moveIssue = useCallback(
    (
      issueId: string,
      updates: MoveIssueUpdates,
      onSettled?: () => void,
    ) => {
      updateIssue(issueId, updates, {
        errorMessage: t(($) => $.detail.toast_move_issue_failed),
        onSettled,
      });
    },
    [t, updateIssue],
  );

  const openCreateIssue = useCallback(
    (defaults?: Partial<CreateIssueRequest>) => {
      useModalStore
        .getState()
        .open("create-issue", { ...resolvedCreateDefaults, ...defaults });
    },
    [resolvedCreateDefaults],
  );

  const actions = useMemo<IssueSurfaceActions>(
    () => ({
      isPending:
        updateIssueMutation.isPending ||
        batchUpdateMutation.isPending ||
        batchDeleteMutation.isPending,
      createIssue: openCreateIssue,
      updateIssue,
      moveIssue: (issueId, updates, options) =>
        updateIssue(issueId, updates, {
          errorMessage: t(($) => $.detail.toast_move_issue_failed),
          ...options,
        }),
      batchUpdate: async (issueIds, updates) => {
        await batchUpdateMutation.mutateAsync({ ids: issueIds, updates });
      },
      batchDelete: async (issueIds) => {
        await batchDeleteMutation.mutateAsync(issueIds);
      },
    }),
    [
      batchDeleteMutation,
      batchUpdateMutation,
      openCreateIssue,
      t,
      updateIssue,
      updateIssueMutation.isPending,
    ],
  );

  const isLoading = usesAssigneeBoard
    ? assigneeGroupsQuery.isLoading
    : usesGantt
      ? ganttIssuesQuery.isLoading
      : queryPlan.kind === "workspace"
        ? workspaceStatusIssuesQuery.isLoading
        : scopedStatusIssuesQuery.isLoading;

  return {
    scopeKey,
    projectId,
    createDefaults: resolvedCreateDefaults,
    viewMode: effectiveViewMode,
    allowGantt: allowedModes.has("gantt") && !!projectId,
    surfaceIssues,
    projectIssues: surfaceIssues,
    issues,
    swimlaneIssues,
    filteredGanttIssues,
    assigneeGroups: usesAssigneeBoard ? filteredAssigneeGroups : undefined,
    assigneeGroupQueryKey: usesAssigneeBoard
      ? activeAssigneeGroupsOptions.queryKey
      : undefined,
    assigneeGroupFilter: usesAssigneeBoard ? assigneeGroupFilter : undefined,
    filter: queryPlan.queryFilter,
    loadMoreScope: queryPlan.loadMoreScope,
    loadMoreFilter: queryPlan.loadMoreFilter,
    sort,
    ganttIssues,
    visibleStatuses,
    hiddenStatuses,
    activeFilters,
    activity,
    actions,
    selection,
    childProgressMap,
    isLoading,
    isEmpty: !isLoading && surfaceIssues.length === 0,
    openCreateIssue,
    moveIssue,
  };
}
