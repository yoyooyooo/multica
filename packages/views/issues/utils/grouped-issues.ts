import type { IssueAssigneeGroup } from "@multica/core/types";

/**
 * Apply the Working filter to an assignee-grouped query result so the
 * board's grouped path matches what `filterIssues` does for the flat path.
 *
 * Rewrites `group.total` to the filtered count — that field drives the
 * column-header number (`board-view.tsx` → `board-column.tsx`), so leaving
 * the original total intact would render "cards shrank but header didn't".
 *
 * Empty groups are preserved on purpose: collapsing the columns would shift
 * the layout every time the user toggles Working, and the existing
 * "no issues in this column" empty state covers the empty case cleanly.
 */
export function applyWorkingFilterToGroups(
  groups: IssueAssigneeGroup[] | undefined,
  workingOnly: boolean,
  workingIssueIds: Set<string> | undefined,
): IssueAssigneeGroup[] | undefined {
  if (!groups) return groups;
  if (!workingOnly) return groups;
  if (!workingIssueIds) {
    return groups.map((g) => ({ ...g, issues: [], total: 0 }));
  }
  return groups.map((g) => {
    const filtered = g.issues.filter((i) => workingIssueIds.has(i.id));
    return { ...g, issues: filtered, total: filtered.length };
  });
}
