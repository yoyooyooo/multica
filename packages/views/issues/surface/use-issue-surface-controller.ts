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
import {
  useBatchDeleteIssues,
  useBatchUpdateIssues,
  useUpdateIssue,
} from "@multica/core/issues/mutations";
import {
  childIssueProgressOptions,
  myIssueAssigneeGroupsOptions,
  myIssueListOptions,
  projectGanttIssuesOptions,
  type AssigneeGroupedIssuesFilter,
  type IssueSortParam,
  type MyIssuesFilter,
} from "@multica/core/issues/queries";
import {
  issueScopeKey,
  issueScopeToApiParams,
  issueScopeToCreateDefaults,
  type IssueScope,
} from "@multica/core/issues/surface/scope";
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

export interface IssueSurfaceController {
  scopeKey: string;
  projectId?: string;
  createDefaults: Partial<CreateIssueRequest>;
  viewMode: IssueSurfaceMode;
  allowGantt: boolean;
  projectIssues: Issue[];
  issues: Issue[];
  swimlaneIssues: Issue[];
  filteredGanttIssues: Issue[];
  assigneeGroups?: IssueAssigneeGroup[];
  assigneeGroupQueryKey?: QueryKey;
  assigneeGroupFilter?: AssigneeGroupedIssuesFilter;
  filter: MyIssuesFilter;
  sort: IssueSortParam;
  ganttIssues: Issue[];
  visibleStatuses: typeof BOARD_STATUSES;
  hiddenStatuses: typeof BOARD_STATUSES;
  activeFilters: Omit<IssueFilters, "statusFilters" | "runningIssueIds">;
  activity: IssueSurfaceActivity;
  actions: IssueSurfaceActions;
  selection: IssueSurfaceSelection;
  childProgressMap: Map<string, ChildProgress>;
  isEmpty: boolean;
  openCreateIssue: () => void;
  moveIssue: (
    issueId: string,
    updates: MoveIssueUpdates,
    onSettled?: () => void,
  ) => void;
}

function projectFilterFromScope(scope: IssueScope): MyIssuesFilter {
  if (scope.type !== "project") {
    throw new Error("IssueSurface currently supports project scope only.");
  }
  const params = issueScopeToApiParams(scope);
  return { project_id: params.project_id };
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
  if (!projectId) {
    throw new Error("IssueSurface currently supports project scope only.");
  }

  const viewMode = useViewStore((s) => s.viewMode);
  const setViewMode = useViewStore((s) => s.setViewMode);
  const grouping = useViewStore((s) => s.grouping);
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const statusFilters = useViewStore((s) => s.statusFilters);
  const priorityFilters = useViewStore((s) => s.priorityFilters);
  const assigneeFilters = useViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useViewStore((s) => s.creatorFilters);
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

  const filter = useMemo(() => projectFilterFromScope(scope), [scope]);
  const resolvedCreateDefaults = useMemo(
    () => ({ ...issueScopeToCreateDefaults(scope), ...createDefaults }),
    [createDefaults, scope],
  );

  const sort = useMemo<IssueSortParam>(
    () => ({
      sort_by: sortBy,
      sort_direction: sortBy !== "position" ? sortDirection : undefined,
    }),
    [sortBy, sortDirection],
  );

  const activity = useIssueSurfaceActivity(scope);
  const selection = useCreateIssueSurfaceSelection(scopeKey);
  const filterContext = useMemo(
    () => ({ activityByIssueId: activity.activityByIssueId }),
    [activity.activityByIssueId],
  );

  const usesAssigneeBoard =
    effectiveViewMode === "board" && grouping === "assignee";
  const usesGantt = effectiveViewMode === "gantt";

  const assigneeGroupFilter = useMemo<AssigneeGroupedIssuesFilter>(
    () => ({
      ...filter,
      statuses: statusFilters.length > 0 ? statusFilters : [...BOARD_STATUSES],
      priorities: priorityFilters,
      assignee_filters: assigneeFilters,
      include_no_assignee: includeNoAssignee,
      creator_filters: creatorFilters,
      label_ids: labelFilters,
    }),
    [
      assigneeFilters,
      creatorFilters,
      filter,
      includeNoAssignee,
      labelFilters,
      priorityFilters,
      statusFilters,
    ],
  );
  const assigneeGroupsOptions = myIssueAssigneeGroupsOptions(
    wsId,
    scopeKey,
    assigneeGroupFilter,
    undefined,
    sort,
  );
  const statusIssuesQuery = useQuery({
    ...myIssueListOptions(wsId, scopeKey, filter, undefined, sort),
    enabled: !usesAssigneeBoard && !usesGantt,
  });
  const assigneeGroupsQuery = useQuery({
    ...assigneeGroupsOptions,
    enabled: usesAssigneeBoard,
  });
  const ganttIssuesQuery = useQuery({
    ...projectGanttIssuesOptions(wsId, projectId),
    enabled: usesGantt,
  });

  const bucketedIssues = usesAssigneeBoard
    ? (assigneeGroupsQuery.data?.groups.flatMap((group) => group.issues) ?? [])
    : (statusIssuesQuery.data ?? EMPTY_ISSUES);
  const ganttIssues = ganttIssuesQuery.data ?? EMPTY_ISSUES;
  const projectIssues = usesGantt ? ganttIssues : bucketedIssues;

  const baseFilterState = useMemo<IssueFilterState>(
    () => ({
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters: [],
      includeNoProject: false,
      labelFilters,
      workingOnly: agentRunningFilter,
    }),
    [
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      labelFilters,
      agentRunningFilter,
    ],
  );

  const issues = useMemo(
    () =>
      applyIssueFilters(projectIssues, baseFilterState, filterContext),
    [
      projectIssues,
      baseFilterState,
      filterContext,
    ],
  );

  const statuslessFilterState = useMemo<IssueFilterState>(
    () => ({
      ...baseFilterState,
      statusFilters: [],
    }),
    [baseFilterState],
  );

  const swimlaneIssues = useMemo(
    () =>
      applyIssueFilters(projectIssues, statuslessFilterState, filterContext),
    [
      projectIssues,
      statuslessFilterState,
      filterContext,
    ],
  );

  const filteredGanttIssues = useMemo(
    () =>
      applyIssueFilters(ganttIssues, baseFilterState, filterContext),
    [
      ganttIssues,
      baseFilterState,
      filterContext,
    ],
  );

  const filteredAssigneeGroups = useMemo(
    () =>
      filterRunningAssigneeGroups(
        assigneeGroupsQuery.data?.groups,
        agentRunningFilter,
        activity.runningIssueIds,
      ),
    [assigneeGroupsQuery.data?.groups, agentRunningFilter, activity.runningIssueIds],
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
      projectFilters: [],
      includeNoProject: false,
      labelFilters,
      agentRunningFilter,
    }),
    [
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      labelFilters,
      agentRunningFilter,
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
                : (options?.errorMessage ?? t(($) => $.detail.toast_move_issue_failed)),
            );
            options?.onError?.(err);
          },
          onSettled: () => options?.onSettled?.(),
        },
      );
    },
    [updateIssueMutation, t],
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
    [updateIssue, t],
  );

  const openCreateIssue = useCallback(() => {
    useModalStore.getState().open("create-issue", resolvedCreateDefaults);
  }, [resolvedCreateDefaults]);

  const actions = useMemo<IssueSurfaceActions>(
    () => ({
      isPending:
        updateIssueMutation.isPending ||
        batchUpdateMutation.isPending ||
        batchDeleteMutation.isPending,
      createIssue: (defaults) => {
        useModalStore
          .getState()
          .open("create-issue", { ...resolvedCreateDefaults, ...defaults });
      },
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
      resolvedCreateDefaults,
      t,
      updateIssue,
      updateIssueMutation.isPending,
    ],
  );

  return {
    scopeKey,
    projectId,
    createDefaults: resolvedCreateDefaults,
    viewMode: effectiveViewMode,
    allowGantt: allowedModes.has("gantt"),
    projectIssues,
    issues,
    swimlaneIssues,
    filteredGanttIssues,
    assigneeGroups: usesAssigneeBoard ? filteredAssigneeGroups : undefined,
    assigneeGroupQueryKey: usesAssigneeBoard
      ? assigneeGroupsOptions.queryKey
      : undefined,
    assigneeGroupFilter: usesAssigneeBoard ? assigneeGroupFilter : undefined,
    filter,
    sort,
    ganttIssues,
    visibleStatuses,
    hiddenStatuses,
    activeFilters,
    activity,
    actions,
    selection,
    childProgressMap,
    isEmpty:
      effectiveViewMode !== "gantt" &&
      effectiveViewMode !== "swimlane" &&
      projectIssues.length === 0,
    openCreateIssue,
    moveIssue,
  };
}
