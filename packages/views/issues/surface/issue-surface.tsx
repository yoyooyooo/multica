"use client";

import { useCallback, useMemo, type ReactNode } from "react";
import { ListTodo, Plus } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { getIssueSurfaceViewStore } from "@multica/core/issues/stores/surface-view-store";
import { issueScopeKey } from "@multica/core/issues/surface/scope";
import type { CreateIssueRequest, Issue } from "@multica/core/types";
import { BoardView } from "../components/board-view";
import { BatchActionToolbar } from "../components/batch-action-toolbar";
import { GanttView } from "../components/gantt-view";
import { IssuesHeader } from "../components/issues-header";
import { ListView } from "../components/list-view";
import { SwimLaneView } from "../components/swimlane-view";
import { useT } from "../../i18n";
import { IssueSurfaceActionsProvider } from "./actions-context";
import { IssueSurfaceSelectionProvider } from "./selection-context";
import type { IssueSurfaceProps } from "./types";
import {
  useIssueSurfaceController,
  type IssueSurfaceController,
} from "./use-issue-surface-controller";

export interface IssueSurfaceRenderContext {
  controller: IssueSurfaceController;
  issues: Issue[];
}

interface IssueSurfaceComponentProps extends IssueSurfaceProps {
  renderHeader?: (context: IssueSurfaceRenderContext) => ReactNode;
  renderEmpty?: (context: IssueSurfaceRenderContext) => ReactNode;
  renderLoading?: (context: IssueSurfaceRenderContext) => ReactNode;
  clientFilter?: (issue: Issue) => boolean;
  showClientEmpty?: (context: IssueSurfaceRenderContext) => boolean;
  batchToolbar?: "always" | "list" | "never";
  contentClassName?: string;
}

export function IssueSurface({
  scope,
  modes,
  surfaceKey,
  createDefaults,
  renderHeader,
  renderEmpty,
  renderLoading,
  clientFilter,
  showClientEmpty,
  batchToolbar = "always",
  contentClassName,
}: IssueSurfaceComponentProps) {
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
        renderHeader={renderHeader}
        renderEmpty={renderEmpty}
        renderLoading={renderLoading}
        clientFilter={clientFilter}
        showClientEmpty={showClientEmpty}
        batchToolbar={batchToolbar}
        contentClassName={contentClassName}
      />
    </ViewStoreProvider>
  );
}

function IssueSurfaceContent({
  scope,
  modes,
  createDefaults,
  renderHeader,
  renderEmpty,
  renderLoading,
  clientFilter,
  showClientEmpty,
  batchToolbar,
  contentClassName,
}: Omit<IssueSurfaceComponentProps, "surfaceKey">) {
  const { t } = useT("projects");
  const controller = useIssueSurfaceController({
    scope,
    modes,
    createDefaults,
  });
  const issues = useMemo(
    () =>
      clientFilter
        ? controller.issues.filter((issue) => clientFilter(issue))
        : controller.issues,
    [clientFilter, controller.issues],
  );
  const swimlaneIssues = useMemo(
    () =>
      clientFilter
        ? controller.swimlaneIssues.filter((issue) => clientFilter(issue))
        : controller.swimlaneIssues,
    [clientFilter, controller.swimlaneIssues],
  );
  const renderContext = useMemo(
    () => ({ controller, issues }),
    [controller, issues],
  );
  const openCreateIssue = useCallback(
    (defaults?: Record<string, unknown>) => {
      controller.openCreateIssue(defaults as Partial<CreateIssueRequest>);
    },
    [controller],
  );
  const shouldShowClientEmpty =
    !!clientFilter &&
    issues.length === 0 &&
    (showClientEmpty ? showClientEmpty(renderContext) : true);
  const shouldShowBatchToolbar =
    batchToolbar !== "never" &&
    (batchToolbar === "always" || controller.viewMode === "list");

  return (
    <IssueSurfaceActionsProvider actions={controller.actions}>
      <IssueSurfaceSelectionProvider selection={controller.selection}>
        {renderHeader ? (
          renderHeader(renderContext)
        ) : (
          <IssuesHeader
            scopedIssues={controller.surfaceIssues}
            allowGantt={controller.allowGantt}
          />
        )}
        {controller.isLoading ? (
          renderLoading ? (
            renderLoading(renderContext)
          ) : (
            <IssueSurfaceSkeleton mode={controller.viewMode} />
          )
        ) : controller.isEmpty || shouldShowClientEmpty ? (
          renderEmpty ? (
            renderEmpty(renderContext)
          ) : (
            <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-3 text-muted-foreground">
              <ListTodo className="h-10 w-10 text-muted-foreground/40" />
              <p className="text-sm">{t(($) => $.detail.empty_issues_title)}</p>
              <p className="text-xs">{t(($) => $.detail.empty_issues_hint)}</p>
              <Button
                variant="outline"
                size="sm"
                className="mt-1"
                onClick={() => controller.openCreateIssue()}
              >
                <Plus className="size-3.5 mr-1.5" />
                {t(($) => $.detail.empty_issues_new_button)}
              </Button>
            </div>
          )
        ) : (
          <div className={cn("flex flex-col flex-1 min-h-0", contentClassName)}>
            {controller.viewMode === "board" && (
              <BoardView
                issues={
                  controller.assigneeGroups
                    ? controller.assigneeGroups.flatMap((group) => group.issues)
                    : issues
                }
                assigneeGroups={controller.assigneeGroups}
                assigneeGroupQueryKey={controller.assigneeGroupQueryKey}
                assigneeGroupFilter={controller.assigneeGroupFilter}
                visibleStatuses={controller.visibleStatuses}
                hiddenStatuses={controller.hiddenStatuses}
                onMoveIssue={controller.moveIssue}
                childProgressMap={controller.childProgressMap}
                myIssuesScope={controller.loadMoreScope}
                myIssuesFilter={controller.loadMoreFilter}
                sort={controller.sort}
                projectId={controller.projectId}
                onCreateIssue={openCreateIssue}
              />
            )}
            {controller.viewMode === "list" && (
              <ListView
                issues={issues}
                visibleStatuses={controller.visibleStatuses}
                childProgressMap={controller.childProgressMap}
                myIssuesScope={controller.loadMoreScope}
                myIssuesFilter={controller.loadMoreFilter}
                sort={controller.sort}
                projectId={controller.projectId}
                onMoveIssue={controller.moveIssue}
                onCreateIssue={openCreateIssue}
              />
            )}
            {controller.viewMode === "gantt" && (
              <GanttView issues={controller.filteredGanttIssues} />
            )}
            {controller.viewMode === "swimlane" && (
              <SwimLaneView
                issues={issues}
                unfilteredIssues={swimlaneIssues}
                activeFilters={controller.activeFilters}
                visibleStatuses={controller.visibleStatuses}
                hiddenStatuses={controller.hiddenStatuses}
                onMoveIssue={controller.moveIssue}
                childProgressMap={controller.childProgressMap}
                myIssuesScope={controller.loadMoreScope}
                myIssuesFilter={controller.loadMoreFilter}
                sort={controller.sort}
                projectId={controller.projectId}
                activityByIssueId={controller.activity.activityByIssueId}
                onCreateIssue={openCreateIssue}
              />
            )}
          </div>
        )}
        {shouldShowBatchToolbar && <BatchActionToolbar issues={issues} />}
      </IssueSurfaceSelectionProvider>
    </IssueSurfaceActionsProvider>
  );
}

function IssueSurfaceSkeleton({ mode }: { mode: string }) {
  if (mode === "list") {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto p-2 space-y-1">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-10 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  return (
    <div className="flex flex-1 min-h-0 gap-4 overflow-x-auto p-4">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex min-w-52 flex-1 flex-col gap-2">
          <Skeleton className="h-4 w-20" />
          <Skeleton className="h-24 w-full rounded-lg" />
          <Skeleton className="h-24 w-full rounded-lg" />
        </div>
      ))}
    </div>
  );
}
