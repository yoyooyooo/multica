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
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { BOARD_STATUSES } from "@multica/core/issues/config";
import { useUpdateIssue } from "@multica/core/issues/mutations";
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
import { filterIssues, filterRunningAssigneeGroups, type IssueFilters } from "../utils/filter";
import type { ChildProgress } from "../components/list-row";
import type { IssueSurfaceMode } from "./types";
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

  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  const runningIssueIds = useMemo(() => {
    const ids = new Set<string>();
    for (const task of snapshot) {
      if (task.status === "running" && task.issue_id) ids.add(task.issue_id);
    }
    return ids;
  }, [snapshot]);

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

  const issues = useMemo(
    () =>
      filterIssues(projectIssues, {
        statusFilters,
        priorityFilters,
        assigneeFilters,
        includeNoAssignee,
        creatorFilters,
        projectFilters: [],
        includeNoProject: false,
        labelFilters,
        agentRunningFilter,
        runningIssueIds,
      }),
    [
      projectIssues,
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      labelFilters,
      agentRunningFilter,
      runningIssueIds,
    ],
  );

  const swimlaneIssues = useMemo(
    () =>
      filterIssues(projectIssues, {
        statusFilters: [],
        priorityFilters,
        assigneeFilters,
        includeNoAssignee,
        creatorFilters,
        projectFilters: [],
        includeNoProject: false,
        labelFilters,
        agentRunningFilter,
        runningIssueIds,
      }),
    [
      projectIssues,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      labelFilters,
      agentRunningFilter,
      runningIssueIds,
    ],
  );

  const filteredGanttIssues = useMemo(
    () =>
      filterIssues(ganttIssues, {
        statusFilters,
        priorityFilters,
        assigneeFilters,
        includeNoAssignee,
        creatorFilters,
        projectFilters: [],
        includeNoProject: false,
        labelFilters,
        agentRunningFilter,
        runningIssueIds,
      }),
    [
      ganttIssues,
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      labelFilters,
      agentRunningFilter,
      runningIssueIds,
    ],
  );

  const filteredAssigneeGroups = useMemo(
    () =>
      filterRunningAssigneeGroups(
        assigneeGroupsQuery.data?.groups,
        agentRunningFilter,
        runningIssueIds,
      ),
    [assigneeGroupsQuery.data?.groups, agentRunningFilter, runningIssueIds],
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
  const moveIssue = useCallback(
    (
      issueId: string,
      updates: MoveIssueUpdates,
      onSettled?: () => void,
    ) => {
      updateIssueMutation.mutate(
        { id: issueId, ...updates },
        {
          onError: (err) =>
            toast.error(
              err instanceof Error && err.message
                ? err.message
                : t(($) => $.detail.toast_move_issue_failed),
            ),
          onSettled: () => onSettled?.(),
        },
      );
    },
    [updateIssueMutation, t],
  );

  const openCreateIssue = useCallback(() => {
    useModalStore.getState().open("create-issue", resolvedCreateDefaults);
  }, [resolvedCreateDefaults]);

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
    childProgressMap,
    isEmpty:
      effectiveViewMode !== "gantt" &&
      effectiveViewMode !== "swimlane" &&
      projectIssues.length === 0,
    openCreateIssue,
    moveIssue,
  };
}
