package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// External PR completion keeps a stable system-comment identity so a crash
// between insert and the work-row flag can retry without a second comment.
// v0.8.2 removed the global child-done comment file; status changes now record
// issue_child_event and the child_done wakeup consumes that event. These
// helpers serve only the External PR reconciler.

func commentFromCreateRow(row db.CreateCommentRow) db.Comment {
	return db.Comment{
		ID: row.ID, IssueID: row.IssueID, AuthorType: row.AuthorType, AuthorID: row.AuthorID,
		Content: row.Content, Type: row.Type, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		ParentID: row.ParentID, WorkspaceID: row.WorkspaceID, ResolvedAt: row.ResolvedAt,
		ResolvedByType: row.ResolvedByType, ResolvedByID: row.ResolvedByID,
		SourceTaskID: row.SourceTaskID, QuickActionID: row.QuickActionID, ViaPluginID: row.ViaPluginID,
		Revision: row.Revision, RecoverySettledAt: row.RecoverySettledAt, DeletedAt: row.DeletedAt,
		SuppressedAgentIds: row.SuppressedAgentIds,
	}
}

func (h *Handler) postChildDoneCommentWithID(ctx context.Context, parent, completed db.Issue, children []db.Issue, staged bool, closedStage int32, batch bool, statuses resolvedChildStatuses, batchCompleted []db.Issue, commentID pgtype.UUID) (db.Comment, error) {
	prefix := h.getIssuePrefix(ctx, completed.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(completed.Number))
	childID := uuidToString(completed.ID)
	title := sanitizeChildTitleForSystemComment(completed.Title)
	parentID := uuidToString(parent.ID)
	completedStatus := statuses.status(completed)

	// Build the parent-assignee mention prefix. Empty when the parent has no
	// assignee or the assignee row is missing (deleted member, archived
	// agent the workspace lost track of, etc.).
	mentionPrefix := h.buildParentAssigneeMention(ctx, parent)

	var content string
	if staged {
		stageCancelled := stageHasCancelled(children, closedStage, statuses.status)
		advanceHasCancelled := stageCancelled
		if batch {
			// A single batch can close several stages. Always preserve cancellation
			// already present in the named stage, and also account for lower stages
			// newly cancelled by this same batch without repeating older lower-stage
			// warnings.
			advanceHasCancelled = stageCancelled || batchClosedScopeHasCancelled(children, batchCompleted, closedStage, statuses.status)
		}
		summary, nextStage := stageProgressSummary(children, closedStage, statuses.status)
		advance := stageAdvanceInstruction(nextStage, parentID, advanceHasCancelled)
		if !stageCancelled {
			// Keep the historical no-cancellation wording byte-identical for the
			// named stage. A lower stage cancelled in the same batch can still add
			// the dependency warning through advanceHasCancelled above.
			if batch {
				content = fmt.Sprintf(
					"%sStage %d of this issue is complete — its sub-issues just finished together in a batch update, most recently [%s](mention://issue/%s) — \"%s\". Stage progress — %s.%s",
					mentionPrefix, closedStage, identifier, childID, title, summary, advance,
				)
			} else {
				content = fmt.Sprintf(
					"%sStage %d of this issue is complete — its last sub-issue [%s](mention://issue/%s) — \"%s\" — just finished. Stage progress — %s.%s",
					mentionPrefix, closedStage, identifier, childID, title, summary, advance,
				)
			}
		} else if batch {
			lastAction := "finished"
			if completedStatus == "cancelled" {
				lastAction = "was cancelled"
			}
			content = fmt.Sprintf(
				"%sStage %d of this issue is closed — its sub-issues reached terminal states together in a batch update; most recently, [%s](mention://issue/%s) — \"%s\" — %s. Stage progress — %s.%s",
				mentionPrefix, closedStage, identifier, childID, title, lastAction, summary, advance,
			)
		} else {
			lastAction := "just finished"
			if completedStatus == "cancelled" {
				lastAction = "was just cancelled"
			}
			content = fmt.Sprintf(
				"%sStage %d of this issue is closed — its last sub-issue [%s](mention://issue/%s) — \"%s\" — %s. Stage progress — %s.%s",
				mentionPrefix, closedStage, identifier, childID, title, lastAction, summary, advance,
			)
		}
	} else {
		hasCancelled := anyCancelledChildren(children, statuses.status)
		if !hasCancelled {
			// Keep the historical no-cancellation wording byte-identical.
			if batch {
				content = fmt.Sprintf(
					"%sAll sub-issues are complete — they just finished together in a batch update, most recently [%s](mention://issue/%s) — \"%s\". Continue the parent: synthesize the children's results and move it forward, or — if nothing remains — run `multica issue status %s in_review` to mark the parent ready for review.",
					mentionPrefix, identifier, childID, title, parentID,
				)
			} else {
				content = fmt.Sprintf(
					"%sAll sub-issues are complete — the last one, [%s](mention://issue/%s) — \"%s\", just finished. Continue the parent: synthesize the children's results and move it forward, or — if nothing remains — run `multica issue status %s in_review` to mark the parent ready for review.",
					mentionPrefix, identifier, childID, title, parentID,
				)
			}
		} else {
			lastAction := "finished"
			if completedStatus == "cancelled" {
				lastAction = "was cancelled"
			}
			warning := unstagedCancellationInstruction()
			if !batch {
				lastAction = "just finished"
				if completedStatus == "cancelled" {
					lastAction = "was just cancelled"
				}
				content = fmt.Sprintf(
					"%sAll sub-issues are closed — the last one, [%s](mention://issue/%s) — \"%s\", %s.%s Continue the parent: synthesize the children's results and move it forward, or — if nothing remains — run `multica issue status %s in_review` to mark the parent ready for review.",
					mentionPrefix, identifier, childID, title, lastAction, warning, parentID,
				)
			} else {
				content = fmt.Sprintf(
					"%sAll sub-issues are closed — they reached terminal states together in a batch update; most recently, [%s](mention://issue/%s) — \"%s\" — %s.%s Continue the parent: synthesize the children's results and move it forward, or — if nothing remains — run `multica issue status %s in_review` to mark the parent ready for review.",
					mentionPrefix, identifier, childID, title, lastAction, warning, parentID,
				)
			}
		}
	}

	// author_type='system', author_id=zero UUID. The zero UUID is a valid 16
	// byte value and the column is NOT NULL; frontend code should branch on
	// author_type === 'system' rather than on the UUID value.
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          commentID,
		IssueID:     parent.ID,
		WorkspaceID: parent.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	var comment db.Comment
	var issueRevision int64
	if err == nil {
		comment = commentFromCreateRow(created)
		issueRevision = created.IssueRevision
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
			return db.Comment{}, err
		}
		comment, err = h.Queries.GetComment(ctx, commentID)
		if err != nil {
			return db.Comment{}, fmt.Errorf("load durable child-done comment: %w", err)
		}
		if comment.IssueID != parent.ID || comment.WorkspaceID != parent.WorkspaceID {
			return db.Comment{}, errors.New("durable child-done comment identity mismatch")
		}
		issueRevision = parent.Revision
	}

	h.publish(protocol.EventCommentCreated, uuidToString(parent.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         parent.Title,
		"issue_assignee_type": textToPtr(parent.AssigneeType),
		"issue_assignee_id":   uuidToPtr(parent.AssigneeID),
		"issue_status":        parent.Status,
		"issue_revision":      issueRevision,
	})

	// Parent continuation is the current child_done wakeup. UpdateIssueStatus
	// already records issue_child_event, and the platform sweeper owns the
	// assignee run. This comment is only the durable External PR identity;
	// dispatching the removed v0.5 comment trigger would wake the parent twice.
	return comment, nil
}

// isTerminalChildStatus reports whether a child issue status counts as
// "finished" for stage-barrier purposes. Cancelled counts as terminal: a
// cancelled sibling will never complete, so it must not hold a stage open.
//
// Takes a CANONICAL status. Callers that hold a raw `issue.status` must pass it
// through childStatusResolver first, so a custom status in the done or
// cancelled category closes a stage exactly like Done and Cancelled do.
func isTerminalChildStatus(status string) bool {
	return status == "done" || status == "cancelled"
}

// childStatusResolver shares each workspace's catalog across the guards,
// sibling scans and progress summary of one completion notification pass.
// It must not outlive that pass: later notifications need a fresh catalog.
//
// Resolve without rewriting the issue rows, which also supply the original
// user-selected status to downstream rendering. Built-in keys need no I/O.
// Unlike display-oriented Resolver callers, this side-effecting path must
// reject unresolved custom keys: parent or sibling rows can be newer than the
// catalog snapshot. A miss must not bypass a parked/terminal parent's guard.
func (h *Handler) childStatusResolver(ctx context.Context) func(db.Issue) (string, error) {
	resolvers := make(map[pgtype.UUID]*issuestatus.Resolver)
	return func(c db.Issue) (string, error) {
		if issuestatus.IsBuiltIn(c.Status) {
			return c.Status, nil
		}
		resolver := resolvers[c.WorkspaceID]
		if resolver == nil {
			resolver = issuestatus.NewResolver(c.WorkspaceID)
			resolvers[c.WorkspaceID] = resolver
		}
		status := resolver.Effective(ctx, h.issueStatusCatalog(), c.Status)
		if err := resolver.Err(); err != nil {
			return "", err
		}
		if !issuestatus.IsCategory(resolver.Category(ctx, h.issueStatusCatalog(), c.Status)) {
			return "", fmt.Errorf("unresolved status %q in workspace %s", c.Status, uuidToString(c.WorkspaceID))
		}
		return status, nil
	}
}

// resolvedChildStatuses keeps the canonical status of every child needed by
// the barrier and comment builders. Keeping the three-state information here
// (open / done / cancelled) prevents the reporting path from mistaking
// "terminal" for "done" while still letting the barrier ask its narrower
// terminality question.
type resolvedChildStatuses map[pgtype.UUID]string

func (s resolvedChildStatuses) status(child db.Issue) string {
	return s[child.ID]
}

func (s resolvedChildStatuses) isTerminal(child db.Issue) bool {
	return isTerminalChildStatus(s.status(child))
}

// resolveChildStatuses checks every status needed by the stage barrier and
// progress summary before either can produce a notification. Stored status
// keys stay untouched; the returned snapshot contains their canonical values.
func resolveChildStatuses(children []db.Issue, effective func(db.Issue) (string, error)) (resolvedChildStatuses, error) {
	statuses := make(resolvedChildStatuses, len(children))
	staged := siblingsAreStaged(children)
	for _, child := range children {
		if staged && !child.Stage.Valid {
			continue // Neither stage helper considers unstaged siblings.
		}
		status, err := effective(child)
		if err != nil {
			return nil, fmt.Errorf("resolve child %s status %q: %w", uuidToString(child.ID), child.Status, err)
		}
		statuses[child.ID] = status
	}
	return statuses, nil
}

// siblingsAreStaged reports whether any child in the set carries an explicit
// stage. A set with no stages is treated as a single implicit stage.
func siblingsAreStaged(children []db.Issue) bool {
	for _, c := range children {
		if c.Stage.Valid {
			return true
		}
	}
	return false
}

// stageBarrierClosed reports whether the completion of `completed` closed a
// stage barrier among `children` — the full sibling set under one parent,
// already reflecting completed's terminal status.
//
//   - Unstaged sibling set (no child carries a stage): a single implicit
//     stage. The barrier closes only when every child is terminal — the "wake
//     once when the last sub-issue finishes" default.
//   - Staged sibling set: only children that carry a stage form stages.
//     Unstaged children do NOT participate (matches migration 123: a NULL
//     stage does not take part in staged grouping) — completing one closes
//     nothing, and a non-terminal unstaged child never holds a stage open.
//     The completed child's stage S closes when every *staged* child with
//     stage <= S is terminal (frontier closure). Later stages are normally
//     parked in `backlog`, so they cannot fire out of order; the caller's
//     idempotency guard collapses any duplicate wake.
func stageBarrierClosed(children []db.Issue, completed db.Issue, isTerminal func(db.Issue) bool) bool {
	if !siblingsAreStaged(children) {
		for _, c := range children {
			if !isTerminal(c) {
				return false
			}
		}
		return true
	}
	// Staged set: an unstaged completed child belongs to no stage, so it closes
	// nothing.
	if !completed.Stage.Valid {
		return false
	}
	s := completed.Stage.Int32
	for _, c := range children {
		if !c.Stage.Valid {
			continue // unstaged children are ignored by the frontier
		}
		if c.Stage.Int32 <= s && !isTerminal(c) {
			return false
		}
	}
	return true
}

// stageProgressSummary renders a compact per-stage breakdown for the
// child-done system comment. `done` counts only genuinely completed children;
// canonical `cancelled` children are surfaced separately. nextStage is the
// lowest stage above closedStage that still has non-terminal children, or 0
// when none remain. Unstaged children are skipped.
func stageProgressSummary(children []db.Issue, closedStage int32, statusOf func(db.Issue) string) (summary string, nextStage int32) {
	type agg struct{ total, done, cancelled, terminal int }
	byStage := map[int32]*agg{}
	order := []int32{}
	for _, c := range children {
		if !c.Stage.Valid {
			continue // unstaged children do not belong to any stage
		}
		s := c.Stage.Int32
		a, ok := byStage[s]
		if !ok {
			a = &agg{}
			byStage[s] = a
			order = append(order, s)
		}
		a.total++
		switch statusOf(c) {
		case "done":
			a.done++
			a.terminal++
		case "cancelled":
			a.cancelled++
			a.terminal++
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	parts := make([]string, 0, len(order))
	for _, s := range order {
		a := byStage[s]
		label := fmt.Sprintf("Stage %d: %d/%d done", s, a.done, a.total)
		if a.cancelled > 0 {
			label += fmt.Sprintf(", %d cancelled", a.cancelled)
		}
		if nextStage == 0 && s > closedStage && a.terminal < a.total {
			nextStage = s
			label += " (next)"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; "), nextStage
}

func stageHasCancelled(children []db.Issue, stage int32, statusOf func(db.Issue) string) bool {
	for _, child := range children {
		if child.Stage.Valid && child.Stage.Int32 == stage && statusOf(child) == "cancelled" {
			return true
		}
	}
	return false
}

// batchClosedScopeHasCancelled reports whether this batch newly cancelled work
// in any stage up to the highest closed stage represented by the aggregated
// notification. It reads stage/status from the final sibling snapshot, using
// the batch rows only as an ID set, so a concurrently moved child cannot make a
// stale stage number leak into the decision.
func batchClosedScopeHasCancelled(children, batchCompleted []db.Issue, closedStage int32, statusOf func(db.Issue) string) bool {
	completedIDs := make(map[pgtype.UUID]struct{}, len(batchCompleted))
	for _, child := range batchCompleted {
		completedIDs[child.ID] = struct{}{}
	}
	for _, child := range children {
		if _, ok := completedIDs[child.ID]; !ok {
			continue
		}
		if child.Stage.Valid && child.Stage.Int32 <= closedStage && statusOf(child) == "cancelled" {
			return true
		}
	}
	return false
}

func anyCancelledChildren(children []db.Issue, statusOf func(db.Issue) string) bool {
	for _, child := range children {
		if statusOf(child) == "cancelled" {
			return true
		}
	}
	return false
}

// stageAdvanceInstruction returns the trailing instruction appended to a
// staged child-done system comment, given the next stage with pending work
// among the sub-issues that currently exist (nextStage, 0 = none).
//
//   - nextStage > 0: a later stage with unfinished work already exists, so
//     point the leader at it.
//   - nextStage == 0: no later stage exists *among the sub-issues created so
//     far*. This deliberately does NOT assert that the workflow is finished.
//     The server has no declarative workflow model — stages are agent-driven
//     and often created lazily (stage N+1's sub-issues are only written after
//     stage N produces the inputs they depend on), so an intermediate stage in
//     such a pipeline reaches nextStage == 0 exactly like a true final stage
//     does. The old wording ("This was the final stage. Wrap up the parent")
//     asserted a finality the server cannot know and pushed leaders to wrap up
//     mid-workflow (MUL-4062 / #4927). The message now names both possibilities
//     and hands the create-next-vs-wrap-up decision back to the leader.
//   - hasCancelled: one of the stages just closed contains cancelled work, so
//     the instruction asks the assignee to confirm it is not a dependency
//     before advancing. The server still does not decide that question itself.
func stageAdvanceInstruction(nextStage int32, parentID string, hasCancelled bool) string {
	var instruction string
	if nextStage > 0 {
		instruction = fmt.Sprintf(
			" Stage %d is next. Review the full layout with `multica issue children %s`, and if Stage %d's dependencies are satisfied promote its `backlog` sub-issues to `todo` to continue. Read each sub-issue's description first and only promote items whose stated dependencies are already met — do not rely on this parent's higher-level breakdown alone. If a description conflicts with that breakdown, leave it `backlog` and post a comment to confirm first.",
			nextStage, parentID, nextStage,
		)
	} else {
		instruction = fmt.Sprintf(" Completing this stage does not mean the whole issue is done. Decide whether the issue is actually complete — if so, synthesize the results and run `multica issue status %s in_review` to mark the parent ready for review — or whether the next stage still needs to be created, in which case create that stage and its sub-issues now.", parentID)
	}
	if !hasCancelled {
		return instruction
	}
	return instruction + " The just-closed work includes cancelled items: confirm that the cancelled work is not a dependency of whatever comes next before advancing. If unsure, do not promote or create the next stage yet; post a comment to confirm first."
}

func unstagedCancellationInstruction() string {
	return " Before moving the parent forward, confirm that the cancelled work is not required by whatever comes next. If unsure, leave the parent as-is and post a comment to confirm first."
}

// sanitizeChildTitleForSystemComment removes mention-style markdown from a
// child issue's title before it is embedded into the parent's system
// comment. Smuggled mentions are already harmless on the listener path
// (notification + subscriber listeners both skip system comments), but the
// timeline still renders the title verbatim — stripping the markdown keeps
// the rendered comment readable and stops a maliciously titled child issue
// from looking like a directive ("@all please look").
func sanitizeChildTitleForSystemComment(title string) string {
	// Replace any markdown link target so the regex no longer matches it,
	// while preserving the human-readable label text. `]` and `(` are the
	// minimum delimiters of the mention regex; replacing the `(` is enough
	// to break the match without mangling the label.
	cleaned := strings.ReplaceAll(title, "](mention://", "] (mention-stripped://")
	return cleaned
}

func (h *Handler) buildParentAssigneeMention(ctx context.Context, parent db.Issue) string {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return ""
	}
	label, ok := h.resolveAssigneeMentionLabel(ctx, parent.WorkspaceID, parent.AssigneeType.String, parent.AssigneeID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("[@%s](mention://%s/%s) ", label, parent.AssigneeType.String, uuidToString(parent.AssigneeID))
}

func (h *Handler) resolveAssigneeMentionLabel(ctx context.Context, workspaceID pgtype.UUID, assigneeType string, assigneeID pgtype.UUID) (string, bool) {
	switch assigneeType {
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID: assigneeID, WorkspaceID: workspaceID,
		})
		if err != nil {
			return "", false
		}
		return sanitizeMentionLabel(agent.Name), true
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID: assigneeID, WorkspaceID: workspaceID,
		})
		if err != nil {
			return "", false
		}
		return sanitizeMentionLabel(squad.Name), true
	}
	return "", false
}

func sanitizeMentionLabel(name string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(name, "]", ""))
	if cleaned == "" {
		return "assignee"
	}
	return cleaned
}
