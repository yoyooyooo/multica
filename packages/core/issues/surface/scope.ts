import type {
  CreateIssueRequest,
  IssueAssigneeType,
  ListGroupedIssuesParams,
  ListIssuesParams,
} from "../../types";

export type WorkspaceIssueActorKind = "all" | "members" | "agents";

export type IssueScope =
  | { type: "workspace"; actorKind?: WorkspaceIssueActorKind }
  | {
      type: "my";
      relation: "all" | "assigned" | "created" | "involved";
      userId: string;
    }
  | { type: "project"; projectId: string }
  | {
      type: "actor";
      actorType: Extract<IssueAssigneeType, "member" | "agent">;
      actorId: string;
      relation: "assigned" | "created";
    }
  | { type: "team"; teamId: string };

export class UnsupportedIssueScopeError extends Error {
  constructor(scope: IssueScope, operation: string) {
    super(`Issue scope "${issueScopeKey(scope)}" is not supported for ${operation}.`);
    this.name = "UnsupportedIssueScopeError";
  }
}

export function issueScopeKey(scope: IssueScope): string {
  switch (scope.type) {
    case "workspace":
      return `workspace:${scope.actorKind ?? "all"}`;
    case "my":
      return `my:${scope.userId}:${scope.relation}`;
    case "project":
      return `project:${scope.projectId}`;
    case "actor":
      return `actor:${scope.actorType}:${scope.actorId}:${scope.relation}`;
    case "team":
      return `team:${scope.teamId}`;
  }
}

export function issueScopeToApiParams(scope: IssueScope): ListIssuesParams {
  switch (scope.type) {
    case "workspace":
      return {};
    case "my":
      switch (scope.relation) {
        case "assigned":
          return { assignee_id: scope.userId };
        case "created":
          return { creator_id: scope.userId };
        case "involved":
          return { involves_user_id: scope.userId };
        case "all":
          // The current API cannot express this OR query in one request.
          // Callers keep using the existing union adapter keyed by scope.
          return {};
      }
      break;
    case "project":
      return { project_id: scope.projectId };
    case "actor":
      return scope.relation === "assigned"
        ? { assignee_id: scope.actorId }
        : { creator_id: scope.actorId };
    case "team":
      throw new UnsupportedIssueScopeError(scope, "list API params");
  }
}

export function issueScopeToGroupedApiParams(
  scope: IssueScope,
): ListGroupedIssuesParams {
  const base: ListGroupedIssuesParams = { group_by: "assignee" };
  switch (scope.type) {
    case "workspace":
      if (scope.actorKind === "members") {
        return { ...base, assignee_types: ["member"] };
      }
      if (scope.actorKind === "agents") {
        return { ...base, assignee_types: ["agent", "squad"] };
      }
      return base;
    case "my":
      switch (scope.relation) {
        case "assigned":
          return { ...base, assignee_id: scope.userId };
        case "created":
          return { ...base, creator_id: scope.userId };
        case "involved":
          return { ...base, involves_user_id: scope.userId };
        case "all":
          return base;
      }
      break;
    case "project":
      return { ...base, project_id: scope.projectId };
    case "actor":
      return scope.relation === "assigned"
        ? { ...base, assignee_id: scope.actorId }
        : { ...base, creator_id: scope.actorId };
    case "team":
      throw new UnsupportedIssueScopeError(scope, "grouped API params");
  }
}

export function issueScopeToCreateDefaults(
  scope: IssueScope,
): Partial<CreateIssueRequest> {
  switch (scope.type) {
    case "workspace":
      return {};
    case "my":
      return scope.relation === "assigned"
        ? { assignee_type: "member", assignee_id: scope.userId }
        : {};
    case "project":
      return { project_id: scope.projectId };
    case "actor":
      return scope.relation === "assigned"
        ? { assignee_type: scope.actorType, assignee_id: scope.actorId }
        : {};
    case "team":
      throw new UnsupportedIssueScopeError(scope, "create defaults");
  }
}
