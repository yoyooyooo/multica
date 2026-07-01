import { describe, expect, it } from "vitest";
import {
  UnsupportedIssueScopeError,
  issueScopeKey,
  issueScopeToApiParams,
  issueScopeToCreateDefaults,
  issueScopeToGroupedApiParams,
} from "./scope";

describe("issue surface scope", () => {
  it("builds stable surface keys", () => {
    expect(issueScopeKey({ type: "workspace" })).toBe("workspace:all");
    expect(issueScopeKey({ type: "workspace", actorKind: "agents" })).toBe(
      "workspace:agents",
    );
    expect(
      issueScopeKey({ type: "my", relation: "assigned", userId: "u1" }),
    ).toBe("my:u1:assigned");
    expect(issueScopeKey({ type: "project", projectId: "p1" })).toBe(
      "project:p1",
    );
    expect(
      issueScopeKey({
        type: "actor",
        actorType: "agent",
        actorId: "a1",
        relation: "created",
      }),
    ).toBe("actor:agent:a1:created");
    expect(issueScopeKey({ type: "team", teamId: "t1" })).toBe("team:t1");
  });

  it("maps supported list API params without changing API shape", () => {
    expect(issueScopeToApiParams({ type: "workspace" })).toEqual({});
    expect(
      issueScopeToApiParams({
        type: "my",
        relation: "assigned",
        userId: "u1",
      }),
    ).toEqual({ assignee_id: "u1" });
    expect(
      issueScopeToApiParams({ type: "my", relation: "created", userId: "u1" }),
    ).toEqual({ creator_id: "u1" });
    expect(
      issueScopeToApiParams({
        type: "my",
        relation: "involved",
        userId: "u1",
      }),
    ).toEqual({ involves_user_id: "u1" });
    expect(issueScopeToApiParams({ type: "project", projectId: "p1" })).toEqual(
      { project_id: "p1" },
    );
    expect(
      issueScopeToApiParams({
        type: "actor",
        actorType: "agent",
        actorId: "a1",
        relation: "assigned",
      }),
    ).toEqual({ assignee_id: "a1" });
  });

  it("maps grouped API params where the current endpoint can express them", () => {
    expect(
      issueScopeToGroupedApiParams({
        type: "workspace",
        actorKind: "members",
      }),
    ).toEqual({ group_by: "assignee", assignee_types: ["member"] });
    expect(
      issueScopeToGroupedApiParams({
        type: "workspace",
        actorKind: "agents",
      }),
    ).toEqual({ group_by: "assignee", assignee_types: ["agent", "squad"] });
    expect(
      issueScopeToGroupedApiParams({ type: "project", projectId: "p1" }),
    ).toEqual({ group_by: "assignee", project_id: "p1" });
  });

  it("maps create defaults only for writable defaults", () => {
    expect(
      issueScopeToCreateDefaults({ type: "project", projectId: "p1" }),
    ).toEqual({ project_id: "p1" });
    expect(
      issueScopeToCreateDefaults({
        type: "my",
        relation: "assigned",
        userId: "u1",
      }),
    ).toEqual({ assignee_type: "member", assignee_id: "u1" });
    expect(
      issueScopeToCreateDefaults({
        type: "actor",
        actorType: "agent",
        actorId: "a1",
        relation: "assigned",
      }),
    ).toEqual({ assignee_type: "agent", assignee_id: "a1" });
    expect(
      issueScopeToCreateDefaults({
        type: "actor",
        actorType: "agent",
        actorId: "a1",
        relation: "created",
      }),
    ).toEqual({});
  });

  it("throws for team until the issue API has a team filter", () => {
    const scope = { type: "team" as const, teamId: "t1" };
    expect(() => issueScopeToApiParams(scope)).toThrow(
      UnsupportedIssueScopeError,
    );
    expect(() => issueScopeToGroupedApiParams(scope)).toThrow(
      UnsupportedIssueScopeError,
    );
    expect(() => issueScopeToCreateDefaults(scope)).toThrow(
      UnsupportedIssueScopeError,
    );
  });
});
