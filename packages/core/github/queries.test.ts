import { describe, expect, it } from "vitest";
import { githubKeys, issueExternalPullRequestsOptions } from "./queries";

describe("External PR query keys", () => {
  it("isolates the Issue projection by workspace and Issue", () => {
    expect(githubKeys.externalPullRequests("ws-1", "issue-1")).toEqual([
      "external-prs",
      "ws-1",
      "issue-1",
    ]);
    expect(githubKeys.externalPullRequests("ws-2", "issue-1")).not.toEqual(
      githubKeys.externalPullRequests("ws-1", "issue-1"),
    );
  });

  it("does not fetch without both authority coordinates", () => {
    expect(issueExternalPullRequestsOptions("", "issue-1").enabled).toBe(false);
    expect(issueExternalPullRequestsOptions("ws-1", "").enabled).toBe(false);
    expect(issueExternalPullRequestsOptions("ws-1", "issue-1").enabled).toBe(true);
  });
});
