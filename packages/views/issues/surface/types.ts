import type { IssueScope } from "@multica/core/issues/surface/scope";
import type { CreateIssueRequest } from "@multica/core/types";
import type { ViewMode } from "@multica/core/issues/stores/view-store";

export type IssueSurfaceMode = Extract<
  ViewMode,
  "board" | "list" | "swimlane" | "gantt"
>;

export interface IssueSurfaceProps {
  scope: IssueScope;
  modes: IssueSurfaceMode[];
  surfaceKey?: string;
  createDefaults?: Partial<CreateIssueRequest>;
}
