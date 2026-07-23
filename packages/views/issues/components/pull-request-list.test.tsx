import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ExternalPullRequestLink, GitHubPullRequest } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

vi.mock("@multica/core/github/queries", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/github/queries")>(
    "@multica/core/github/queries",
  );
  return {
    ...actual,
    issuePullRequestsOptions: (issueId: string) => ({
      queryKey: ["github", "pull-requests", issueId],
      queryFn: async () => ({ pull_requests: mockPRs }),
      enabled: !!issueId,
    }),
    issueExternalPullRequestsOptions: (wsId: string, issueId: string) => ({
      queryKey: ["external-prs", wsId, issueId],
      queryFn: async () => ({ external_pull_requests: mockExternalPRs }),
      enabled: !!wsId && !!issueId,
    }),
  };
});

import { ExternalPullRequestList, PullRequestList } from "./pull-request-list";

let mockPRs: GitHubPullRequest[] = [];
let mockExternalPRs: ExternalPullRequestLink[] = [];

function makePR(overrides: Partial<GitHubPullRequest> = {}): GitHubPullRequest {
  return {
    id: "pr-1",
    workspace_id: "ws-1",
    repo_owner: "acme",
    repo_name: "widget",
    number: 1,
    title: "Test PR",
    state: "open",
    html_url: "https://example.test/pr/1",
    branch: "feat/x",
    author_login: "octocat",
    author_avatar_url: null,
    merged_at: null,
    closed_at: null,
    pr_created_at: "2026-01-01T00:00:00Z",
    pr_updated_at: "2026-01-01T00:00:00Z",
    mergeable_state: null,
    checks_conclusion: null,
    checks_passed: 0,
    checks_failed: 0,
    checks_pending: 0,
    additions: 0,
    deletions: 0,
    changed_files: 0,
    ...overrides,
  };
}

function makeExternalPR(overrides: Partial<ExternalPullRequestLink> = {}): ExternalPullRequestLink {
  return {
    id: "external-pr-1",
    workspace_id: "ws-1",
    issue_id: "issue-1",
    provider: "ags",
    external_repo: "jackie/ags-team-share",
    external_number: 4,
    external_url: "http://mini:6666/jackie/ags-team-share/pull/4",
    state: "merged",
    link_confidence: "authoritative",
    completion_intent: true,
    merge_provider: "forgejo",
    merge_repo: "jackie/ags-team-share",
    merge_number: 4,
    merge_url: "http://forgejo.local/jackie/ags-team-share/pulls/4",
    merged_sha: "11384b43b138b2a2d79cd7eb3c8c2e533900cfeb",
    created_at: "2026-07-07T00:00:00Z",
    updated_at: "2026-07-07T00:05:00Z",
    ...overrides,
  };
}

function renderWithI18n(node: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={TEST_RESOURCES} locale="en">
        {node}
      </I18nProvider>
    </QueryClientProvider>,
  );
}

function renderList() {
  return renderWithI18n(<PullRequestList issueId="issue-1" />);
}

async function waitForRender() {
  return screen.findAllByRole("link");
}

describe("ExternalPullRequestList sidebar rows", () => {
  it("renders provider-neutral linked and merged details", async () => {
    mockExternalPRs = [makeExternalPR()];
    renderWithI18n(<ExternalPullRequestList wsId="ws-1" issueId="issue-1" />);
    await screen.findByTestId("external-pull-request-row");

    expect(screen.getByText("ags:jackie/ags-team-share#4")).toBeInTheDocument();
    expect(screen.getByText("Merged · authoritative · completion intent")).toBeInTheDocument();
    expect(screen.getByText(/merged SHA 11384b43b138/)).toBeInTheDocument();
  });

  it("renders external and merge URLs as separate links when both are present", async () => {
    mockExternalPRs = [makeExternalPR()];
    renderWithI18n(<ExternalPullRequestList wsId="ws-1" issueId="issue-1" />);
    await screen.findByTestId("external-pull-request-row");

    expect(screen.getByRole("link", { name: "ags:jackie/ags-team-share#4" })).toHaveAttribute(
      "href",
      "http://mini:6666/jackie/ags-team-share/pull/4",
    );
    expect(screen.getByRole("link", { name: "merge forgejo:jackie/ags-team-share#4" })).toHaveAttribute(
      "href",
      "http://forgejo.local/jackie/ags-team-share/pulls/4",
    );
  });

  it("keeps the merge URL clickable when the provider PR URL is absent", async () => {
    mockExternalPRs = [makeExternalPR({ external_url: null })];
    renderWithI18n(<ExternalPullRequestList wsId="ws-1" issueId="issue-1" />);
    await screen.findByTestId("external-pull-request-row");

    expect(screen.queryByRole("link", { name: "ags:jackie/ags-team-share#4" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "merge forgejo:jackie/ags-team-share#4" })).toHaveAttribute(
      "href",
      "http://forgejo.local/jackie/ags-team-share/pulls/4",
    );
  });
});

describe("PullRequestList sidebar rows", () => {
  it("uses the sidebar list-row surface instead of a card surface", async () => {
    mockPRs = [makePR({ title: "Visual row" })];
    renderList();
    await waitForRender();
    const row = screen.getByTestId("pull-request-row");
    expect(row).toHaveClass("rounded-md", "-mx-2", "hover:bg-accent/50");
    expect(row).not.toHaveClass("rounded-lg", "border", "bg-card");
  });

  it("renders All-checks-passed status when only passed counts are non-zero", async () => {
    mockPRs = [makePR({ checks_passed: 3 })];
    renderList();
    await waitForRender();
    expect(screen.getByText("All checks passed")).toBeInTheDocument();
  });

  it("renders Some-checks-failed when any failed count is non-zero", async () => {
    mockPRs = [makePR({ checks_failed: 1, checks_passed: 5 })];
    renderList();
    await waitForRender();
    expect(screen.getByText("Some checks failed")).toBeInTheDocument();
  });

  it("renders pending status when only pending suites remain", async () => {
    mockPRs = [makePR({ checks_pending: 2, checks_passed: 1 })];
    renderList();
    await waitForRender();
    expect(screen.getByText("Some checks haven't completed yet")).toBeInTheDocument();
  });

  it("renders conflicts status when mergeable_state=dirty", async () => {
    mockPRs = [makePR({ mergeable_state: "dirty" })];
    renderList();
    await waitForRender();
    expect(screen.getByText("Has merge conflicts")).toBeInTheDocument();
  });

  it("renders Ready-to-merge when mergeable=clean and no suites observed", async () => {
    mockPRs = [makePR({ mergeable_state: "clean" })];
    renderList();
    await waitForRender();
    expect(screen.getByText("Ready to merge")).toBeInTheDocument();
  });

  it("renders Merged status for merged PRs, suppressing conflict/check text", async () => {
    mockPRs = [
      makePR({
        state: "merged",
        mergeable_state: "dirty",
        checks_conclusion: "failed",
        checks_failed: 5,
      }),
    ];
    renderList();
    await waitForRender();
    expect(screen.getByText("Merged")).toBeInTheDocument();
    expect(screen.queryByText("Has merge conflicts")).not.toBeInTheDocument();
    expect(screen.queryByText("Some checks failed")).not.toBeInTheDocument();
    expect(screen.queryByText("Conflicts")).not.toBeInTheDocument();
    expect(screen.queryByText("Checks failed")).not.toBeInTheDocument();
  });

  it("renders Closed-without-merging status for closed PRs, suppressing conflict/check badges", async () => {
    mockPRs = [
      makePR({
        state: "closed",
        mergeable_state: "clean",
        checks_conclusion: "passed",
        checks_passed: 3,
      }),
    ];
    renderList();
    await waitForRender();
    expect(screen.getByText("Closed without merging")).toBeInTheDocument();
    expect(screen.queryByText("Ready to merge")).not.toBeInTheDocument();
    expect(screen.queryByText("All checks passed")).not.toBeInTheDocument();
    expect(screen.queryByText("No conflicts")).not.toBeInTheDocument();
    expect(screen.queryByText("Checks passed")).not.toBeInTheDocument();
  });

  it("hides stats row when all stats are 0 (legacy backend)", async () => {
    mockPRs = [makePR()];
    renderList();
    await waitForRender();
    expect(screen.queryByText(/files?$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^\+0/)).not.toBeInTheDocument();
  });

  it("shows stats row with additions / deletions / file count when present", async () => {
    mockPRs = [makePR({ additions: 437, deletions: 6, changed_files: 6 })];
    renderList();
    await waitForRender();
    expect(screen.getByText("+437")).toBeInTheDocument();
    expect(screen.getByText("−6")).toBeInTheDocument();
    expect(screen.getByText("6 files")).toBeInTheDocument();
  });

  it("uses singular file copy when changed_files=1", async () => {
    mockPRs = [makePR({ additions: 1, changed_files: 1 })];
    renderList();
    await waitForRender();
    expect(screen.getByText("1 file")).toBeInTheDocument();
  });

  it("collapses extra PR rows past the visible limit behind Show more toggle", async () => {
    mockPRs = [
      makePR({ id: "a", number: 1, title: "PR-A" }),
      makePR({ id: "b", number: 2, title: "PR-B" }),
      makePR({ id: "c", number: 3, title: "PR-C" }),
      makePR({ id: "d", number: 4, title: "PR-D" }),
      makePR({ id: "e", number: 5, title: "PR-E" }),
    ];
    renderList();
    await waitForRender();
    expect(screen.getByText("PR-A")).toBeInTheDocument();
    expect(screen.getByText("PR-B")).toBeInTheDocument();
    expect(screen.getByText("PR-C")).toBeInTheDocument();
    expect(screen.queryByText("PR-D")).not.toBeInTheDocument();
    expect(screen.queryByText("PR-E")).not.toBeInTheDocument();
    expect(screen.getByText("Show 2 more")).toBeInTheDocument();
  });

  it("collapses to 3 rows + hidden tail when count == threshold", async () => {
    mockPRs = [
      makePR({ id: "a", number: 1, title: "PR-A" }),
      makePR({ id: "b", number: 2, title: "PR-B" }),
      makePR({ id: "c", number: 3, title: "PR-C" }),
      makePR({ id: "d", number: 4, title: "PR-D" }),
    ];
    renderList();
    await waitForRender();
    expect(screen.getByText("PR-A")).toBeInTheDocument();
    expect(screen.getByText("PR-B")).toBeInTheDocument();
    expect(screen.getByText("PR-C")).toBeInTheDocument();
    expect(screen.queryByText("PR-D")).not.toBeInTheDocument();
    expect(screen.getByText("Show 1 more")).toBeInTheDocument();
  });
});
