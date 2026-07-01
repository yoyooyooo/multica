"use client";

import { useMemo } from "react";
import { ListTodo, Plus } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { getIssueSurfaceViewStore } from "@multica/core/issues/stores/surface-view-store";
import { issueScopeKey } from "@multica/core/issues/surface/scope";
import { BoardView } from "../components/board-view";
import { BatchActionToolbar } from "../components/batch-action-toolbar";
import { GanttView } from "../components/gantt-view";
import { IssuesHeader } from "../components/issues-header";
import { ListView } from "../components/list-view";
import { SwimLaneView } from "../components/swimlane-view";
import { useT } from "../../i18n";
import type { IssueSurfaceProps } from "./types";
import { useIssueSurfaceController } from "./use-issue-surface-controller";

export function IssueSurface({
  scope,
  modes,
  surfaceKey,
  createDefaults,
}: IssueSurfaceProps) {
  const resolvedSurfaceKey = surfaceKey ?? issueScopeKey(scope);
  const store = useMemo(
    () => getIssueSurfaceViewStore(resolvedSurfaceKey),
    [resolvedSurfaceKey],
  );

  return (
    <ViewStoreProvider store={store}>
      <IssueSurfaceContent
        scope={scope}
        modes={modes}
        createDefaults={createDefaults}
      />
    </ViewStoreProvider>
  );
}

function IssueSurfaceContent({
  scope,
  modes,
  createDefaults,
}: Omit<IssueSurfaceProps, "surfaceKey">) {
  const { t } = useT("projects");
  const controller = useIssueSurfaceController({
    scope,
    modes,
    createDefaults,
  });

  return (
    <>
      <IssuesHeader
        scopedIssues={controller.projectIssues}
        allowGantt={controller.allowGantt}
      />
      {controller.isEmpty ? (
        <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-3 text-muted-foreground">
          <ListTodo className="h-10 w-10 text-muted-foreground/40" />
          <p className="text-sm">{t(($) => $.detail.empty_issues_title)}</p>
          <p className="text-xs">{t(($) => $.detail.empty_issues_hint)}</p>
          <Button
            variant="outline"
            size="sm"
            className="mt-1"
            onClick={controller.openCreateIssue}
          >
            <Plus className="size-3.5 mr-1.5" />
            {t(($) => $.detail.empty_issues_new_button)}
          </Button>
        </div>
      ) : (
        <div className="flex flex-col flex-1 min-h-0">
          {controller.viewMode === "board" && (
            <BoardView
              issues={
                controller.assigneeGroups
                  ? controller.assigneeGroups.flatMap((group) => group.issues)
                  : controller.issues
              }
              assigneeGroups={controller.assigneeGroups}
              assigneeGroupQueryKey={controller.assigneeGroupQueryKey}
              assigneeGroupFilter={controller.assigneeGroupFilter}
              visibleStatuses={controller.visibleStatuses}
              hiddenStatuses={controller.hiddenStatuses}
              onMoveIssue={controller.moveIssue}
              childProgressMap={controller.childProgressMap}
              myIssuesScope={controller.scopeKey}
              myIssuesFilter={controller.filter}
              sort={controller.sort}
              projectId={controller.projectId}
            />
          )}
          {controller.viewMode === "list" && (
            <ListView
              issues={controller.issues}
              visibleStatuses={controller.visibleStatuses}
              childProgressMap={controller.childProgressMap}
              myIssuesScope={controller.scopeKey}
              myIssuesFilter={controller.filter}
              sort={controller.sort}
              projectId={controller.projectId}
              onMoveIssue={controller.moveIssue}
            />
          )}
          {controller.viewMode === "gantt" && (
            <GanttView issues={controller.filteredGanttIssues} />
          )}
          {controller.viewMode === "swimlane" && (
            <SwimLaneView
              issues={controller.issues}
              unfilteredIssues={controller.swimlaneIssues}
              activeFilters={controller.activeFilters}
              visibleStatuses={controller.visibleStatuses}
              hiddenStatuses={controller.hiddenStatuses}
              onMoveIssue={controller.moveIssue}
              childProgressMap={controller.childProgressMap}
              myIssuesScope={controller.scopeKey}
              myIssuesFilter={controller.filter}
              sort={controller.sort}
              projectId={controller.projectId}
            />
          )}
        </div>
      )}
      <BatchActionToolbar issues={controller.projectIssues} />
    </>
  );
}
