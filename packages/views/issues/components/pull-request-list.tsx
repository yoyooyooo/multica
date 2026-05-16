"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  CheckCircle2,
  CircleDashed,
  GitMerge,
  GitPullRequest,
  GitPullRequestArrow,
  GitPullRequestClosed,
  GitPullRequestDraft,
  TriangleAlert,
  XCircle,
} from "lucide-react";
import {
  issuePullRequestsOptions,
  derivePullRequestStatusKind,
  derivePullRequestProgressSegments,
  shouldShowPullRequestStats,
  type PullRequestStatusKind,
  type PullRequestProgressSegment,
} from "@multica/core/github";
import type {
  GitHubPullRequest,
  GitHubPullRequestChecksConclusion,
  GitHubPullRequestState,
} from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// Card layout takes ~4× the vertical space of the legacy row. Past 3 cards
// the sidebar feels packed, so we expand the first N inline and collapse the
// rest behind a "Show more" toggle that renders them as the legacy compact
// rows. 4 is the threshold per Xeon's RFC v3 increment.
const CARD_LIMIT_BEFORE_COLLAPSE = 4;

const STATE_ICON: Record<
  GitHubPullRequestState,
  { icon: React.ComponentType<{ className?: string }>; className: string }
> = {
  open: { icon: GitPullRequestArrow, className: "text-emerald-600 dark:text-emerald-400" },
  draft: { icon: GitPullRequestDraft, className: "text-muted-foreground" },
  merged: { icon: GitMerge, className: "text-violet-600 dark:text-violet-400" },
  closed: { icon: GitPullRequestClosed, className: "text-rose-600 dark:text-rose-400" },
};

const CHECKS_ICON: Record<
  GitHubPullRequestChecksConclusion,
  { icon: React.ComponentType<{ className?: string }>; className: string }
> = {
  passed: { icon: CheckCircle2, className: "text-emerald-600 dark:text-emerald-400" },
  failed: { icon: XCircle, className: "text-rose-600 dark:text-rose-400" },
  pending: { icon: CircleDashed, className: "text-amber-600 dark:text-amber-400" },
};

export function PullRequestList({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const [expanded, setExpanded] = useState(false);
  const { data, isLoading } = useQuery(issuePullRequestsOptions(issueId));
  const prs = data?.pull_requests ?? [];

  if (isLoading) {
    return <p className="text-xs text-muted-foreground px-2">{t(($) => $.detail.pull_requests_loading)}</p>;
  }
  if (prs.length === 0) {
    return (
      <p className="text-xs text-muted-foreground px-2">
        {t(($) => $.detail.pull_requests_empty)}
      </p>
    );
  }

  // Render rule:
  //   - <= CARD_LIMIT_BEFORE_COLLAPSE: every PR as a card.
  //   - >  CARD_LIMIT_BEFORE_COLLAPSE: first (LIMIT - 1) as cards, the
  //     remainder as compact rows behind a toggle. Keeping LIMIT-1 visible
  //     before the toggle leaves room for the toggle itself without overflow.
  const useCollapse = prs.length > CARD_LIMIT_BEFORE_COLLAPSE;
  const expandedHead = useCollapse ? prs.slice(0, CARD_LIMIT_BEFORE_COLLAPSE - 1) : prs;
  const collapsedTail = useCollapse ? prs.slice(CARD_LIMIT_BEFORE_COLLAPSE - 1) : [];

  return (
    <div className="space-y-2">
      {expandedHead.map((pr) => (
        <PullRequestCard key={pr.id} pr={pr} />
      ))}
      {useCollapse ? (
        <div className="space-y-1">
          {expanded
            ? collapsedTail.map((pr) => <PullRequestCompactRow key={pr.id} pr={pr} />)
            : null}
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="text-[11px] text-muted-foreground hover:text-foreground px-2 py-1 rounded-md hover:bg-accent/40 transition-colors w-full text-left"
          >
            {expanded
              ? t(($) => $.detail.pull_request_card_show_less)
              : t(($) => $.detail.pull_request_card_show_more, { count: collapsedTail.length })}
          </button>
        </div>
      ) : null}
    </div>
  );
}

function PullRequestCard({ pr }: { pr: GitHubPullRequest }) {
  const { t } = useT("issues");
  const cfg = STATE_ICON[pr.state] ?? { icon: GitPullRequest, className: "" };
  const StateIcon = cfg.icon;
  const kind = derivePullRequestStatusKind({
    state: pr.state,
    mergeable_state: pr.mergeable_state,
    checks_failed: pr.checks_failed,
    checks_pending: pr.checks_pending,
    checks_passed: pr.checks_passed,
  });
  const segments = derivePullRequestProgressSegments({
    state: pr.state,
    checks_failed: pr.checks_failed,
    checks_pending: pr.checks_pending,
    checks_passed: pr.checks_passed,
  });
  const showStats = shouldShowPullRequestStats({
    additions: pr.additions,
    deletions: pr.deletions,
    changed_files: pr.changed_files,
  });
  const statusText = useStatusText(kind);
  const draftPrefix = pr.state === "draft";

  return (
    <a
      href={pr.html_url}
      target="_blank"
      rel="noreferrer noopener"
      className={cn(
        "block rounded-lg border bg-card px-3 py-2 hover:bg-accent/40 transition-colors group",
        draftPrefix ? "opacity-80" : null,
      )}
    >
      <div className="flex items-start gap-2">
        <StateIcon className={cn("h-4 w-4 mt-0.5 shrink-0", cfg.className)} />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="text-xs font-semibold leading-snug line-clamp-2 group-hover:text-foreground">
            {pr.title}
          </p>
          <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            {pr.author_avatar_url ? (
              // Plain <img>: avatars are external URLs (github.com), already
              // served at a small size, and Next.js's optimizer doesn't run
              // in @multica/views shared code.
              // eslint-disable-next-line @next/next/no-img-element
              <img
                src={pr.author_avatar_url}
                alt=""
                className="h-4 w-4 rounded-full shrink-0 object-cover"
              />
            ) : null}
            <span className="truncate">
              {pr.author_login ? `${pr.author_login} · ` : ""}#{pr.number}
            </span>
          </div>
          {showStats ? (
            <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
              <span className="text-emerald-600 dark:text-emerald-400 tabular-nums">
                +{pr.additions ?? 0}
              </span>
              <span className="text-rose-600 dark:text-rose-400 tabular-nums">
                −{pr.deletions ?? 0}
              </span>
              <span aria-hidden="true">·</span>
              <span>
                {t(($) => $.detail.pull_request_card_files_count, {
                  count: pr.changed_files ?? 0,
                })}
              </span>
            </div>
          ) : null}
          <PullRequestProgressBar state={pr.state} segments={segments} />
          <p className="text-[11px] text-muted-foreground">
            {draftPrefix
              ? t(($) => $.detail.pull_request_card_draft_prefix, { status: statusText })
              : statusText}
          </p>
        </div>
      </div>
    </a>
  );
}

function PullRequestProgressBar({
  state,
  segments,
}: {
  state: GitHubPullRequestState;
  segments: PullRequestProgressSegment[] | null;
}) {
  // Terminal PRs: solid colored bar with no segments — the status text below
  // already says "Merged" / "Closed", the bar exists for shape parity with
  // open PRs only.
  if (state === "merged") {
    return <div className="h-1 rounded-full bg-violet-500/80 dark:bg-violet-400/80" />;
  }
  if (state === "closed") {
    return <div className="h-1 rounded-full bg-rose-500/70 dark:bg-rose-400/70" />;
  }
  // No suite reported yet — hide the bar entirely (per RFC v3 §2 boundary).
  if (!segments) return null;
  return (
    <div className="flex h-1 w-full overflow-hidden rounded-full bg-muted">
      {segments.map((seg) => (
        <span
          key={seg.kind}
          className={cn(
            "h-full block",
            seg.kind === "failed" && "bg-rose-500 dark:bg-rose-400",
            seg.kind === "pending" && "bg-amber-500 dark:bg-amber-400",
            seg.kind === "passed" && "bg-emerald-500 dark:bg-emerald-400",
          )}
          style={{ width: `${seg.ratio * 100}%` }}
        />
      ))}
    </div>
  );
}

// PullRequestCompactRow renders the legacy "icon + title + state · badges"
// row used for collapsed PRs beyond the card limit. The pre-card behaviour
// (hide status row for terminal PRs) is preserved here, so a fully merged
// older PR collapses to a single line without misleading badges.
function PullRequestCompactRow({ pr }: { pr: GitHubPullRequest }) {
  const { t } = useT("issues");
  const cfg = STATE_ICON[pr.state] ?? { icon: GitPullRequest, className: "" };
  const Icon = cfg.icon;
  const label =
    pr.state === "open"
      ? t(($) => $.detail.pull_request_state_open)
      : pr.state === "draft"
        ? t(($) => $.detail.pull_request_state_draft)
        : pr.state === "merged"
          ? t(($) => $.detail.pull_request_state_merged)
          : pr.state === "closed"
            ? t(($) => $.detail.pull_request_state_closed)
            : pr.state;
  const showStatus = pr.state === "open" || pr.state === "draft";
  return (
    <a
      href={pr.html_url}
      target="_blank"
      rel="noreferrer noopener"
      className="flex items-start gap-2 rounded-md px-2 py-1.5 -mx-2 hover:bg-accent/50 transition-colors group"
    >
      <Icon className={cn("h-3.5 w-3.5 mt-0.5 shrink-0", cfg.className)} />
      <div className="min-w-0 flex-1">
        <p className="text-xs font-medium truncate group-hover:text-foreground">{pr.title}</p>
        <p className="text-[11px] text-muted-foreground truncate">
          {pr.repo_owner}/{pr.repo_name}#{pr.number} · {label}
          {pr.author_login ? ` · @${pr.author_login}` : null}
        </p>
        {showStatus ? <PullRequestCompactStatusRow pr={pr} /> : null}
      </div>
    </a>
  );
}

function PullRequestCompactStatusRow({ pr }: { pr: GitHubPullRequest }) {
  const { t } = useT("issues");
  const checks = pr.checks_conclusion ?? null;
  const mergeable = pr.mergeable_state ?? null;
  const conflictsBadge =
    mergeable === "dirty"
      ? { icon: TriangleAlert, label: t(($) => $.detail.pull_request_conflicts_dirty), className: "text-rose-600 dark:text-rose-400" }
      : mergeable === "clean"
        ? { icon: CheckCircle2, label: t(($) => $.detail.pull_request_conflicts_clean), className: "text-emerald-600 dark:text-emerald-400" }
        : null;
  const checksBadge =
    checks && CHECKS_ICON[checks]
      ? {
          icon: CHECKS_ICON[checks].icon,
          className: CHECKS_ICON[checks].className,
          label:
            checks === "passed"
              ? t(($) => $.detail.pull_request_checks_passed)
              : checks === "failed"
                ? t(($) => $.detail.pull_request_checks_failed)
                : t(($) => $.detail.pull_request_checks_pending),
        }
      : null;
  if (!conflictsBadge && !checksBadge) return null;
  return (
    <div className="flex items-center gap-3 mt-0.5">
      {checksBadge ? (
        <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
          <checksBadge.icon className={cn("h-3 w-3", checksBadge.className)} />
          {checksBadge.label}
        </span>
      ) : null}
      {conflictsBadge ? (
        <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
          <conflictsBadge.icon className={cn("h-3 w-3", conflictsBadge.className)} />
          {conflictsBadge.label}
        </span>
      ) : null}
    </div>
  );
}

function useStatusText(kind: PullRequestStatusKind): string {
  const { t } = useT("issues");
  switch (kind) {
    case "closed":
      return t(($) => $.detail.pull_request_card_status_closed);
    case "merged":
      return t(($) => $.detail.pull_request_card_status_merged);
    case "conflicts":
      return t(($) => $.detail.pull_request_card_status_conflicts);
    case "checks_failed":
      return t(($) => $.detail.pull_request_card_status_checks_failed);
    case "checks_pending":
      return t(($) => $.detail.pull_request_card_status_checks_pending);
    case "checks_passed":
      return t(($) => $.detail.pull_request_card_status_checks_passed);
    case "ready":
      return t(($) => $.detail.pull_request_card_status_ready);
    case "unknown":
      return t(($) => $.detail.pull_request_card_status_unknown);
  }
}
