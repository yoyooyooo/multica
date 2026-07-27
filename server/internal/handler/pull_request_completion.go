package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/completionpolicy"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type pullRequestCompletionResult struct {
	Outcome string
	Reason  string
}

type completionActivitySpec struct {
	action  string
	details []byte
}

type committedPullRequestCompletion struct {
	previous   db.Issue
	updated    db.Issue
	source     string
	activities []db.ActivityLog
}

// lockCompletionIssues takes the shared Issue-scoped advisory locks in UUID
// lexical order. Provider workspace and identity locks, when needed, are always
// acquired before this function; no Issue writer acquires either provider lock.
func lockCompletionIssues(ctx context.Context, qtx *db.Queries, issueIDs []pgtype.UUID) error {
	unique := make(map[string]pgtype.UUID, len(issueIDs))
	for _, issueID := range issueIDs {
		if issueID.Valid {
			unique[uuidToString(issueID)] = issueID
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := qtx.LockIssueCompletionTransition(ctx, unique[key]); err != nil {
			return err
		}
	}
	return nil
}

// lockProviderWorkspaces serializes provider fact creation with workspace or
// integration teardown. The order is provider-workspace -> provider identity ->
// Issue advisory -> row locks, preventing deletes from holding a parent row
// while waiting on an Issue lock owned by a provider insert.
func lockProviderWorkspaces(ctx context.Context, tx pgx.Tx, workspaceIDs []pgtype.UUID) error {
	unique := make(map[string]struct{}, len(workspaceIDs))
	for _, workspaceID := range workspaceIDs {
		if workspaceID.Valid {
			unique[uuidToString(workspaceID)] = struct{}{}
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 88492134))`, "provider-workspace:"+key); err != nil {
			return err
		}
	}
	return nil
}

func lockPullRequestIdentity(ctx context.Context, tx pgx.Tx, provider, identity string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 88492133))`, provider+":"+identity)
	return err
}

func (h *Handler) createStatusActivity(
	ctx context.Context,
	qtx *db.Queries,
	previous, updated db.Issue,
	source, actorType string,
	actorID pgtype.UUID,
) (db.ActivityLog, error) {
	details, err := json.Marshal(map[string]string{
		"from": previous.Status, "to": updated.Status, "source": source,
	})
	if err != nil {
		return db.ActivityLog{}, err
	}
	writer := h.CompletionActivityWriter
	if writer == nil {
		writer = func(ctx context.Context, queries *db.Queries, params db.CreateActivityParams) (db.ActivityLog, error) {
			return queries.CreateActivity(ctx, params)
		}
	}
	return writer(ctx, qtx, db.CreateActivityParams{
		WorkspaceID: previous.WorkspaceID,
		IssueID:     previous.ID,
		ActorType:   strToText(actorType),
		ActorID:     actorID,
		Action:      "status_changed",
		Details:     details,
	})
}

// evaluatePullRequestCompletionLocked evaluates and materializes one terminal
// transition inside a caller-owned transaction after the Issue advisory lock
// has been acquired. The status activity and any provider-specific completion
// lineage are inserted in the same transaction as the status change. A durable
// activity failure therefore rolls the transition back and prevents parent
// release; this is deliberately narrower than a generic outbox.
func (h *Handler) evaluatePullRequestCompletionLocked(
	ctx context.Context,
	qtx *db.Queries,
	issue db.Issue,
	source string,
	extraActivities []completionActivitySpec,
) (pullRequestCompletionResult, *committedPullRequestCompletion, error) {
	current, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, nil, err
	}

	if h.PullRequestFactHook != nil {
		h.PullRequestFactHook("completion", "current_loaded_before_terminal_update")
	}

	switch completionpolicy.Parse(current.Metadata) {
	case completionpolicy.RecordOnly:
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "record_only"}, nil, nil
	case completionpolicy.Unsupported:
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "completion_policy_unsupported"}, nil, nil
	}

	updated, err := qtx.CompleteIssueFromPullRequest(ctx, db.CompleteIssueFromPullRequestParams{
		ID:          current.ID,
		WorkspaceID: current.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if current.Status == "done" {
			return pullRequestCompletionResult{Outcome: "already_done"}, nil, nil
		}
		if current.Status == "cancelled" {
			return pullRequestCompletionResult{Outcome: "recorded", Reason: "cancelled"}, nil, nil
		}
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "guard_not_satisfied"}, nil, nil
	}
	if err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, nil, err
	}

	statusActivity, err := h.createStatusActivity(ctx, qtx, current, updated, source, "system", pgtype.UUID{})
	if err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "activity_failed"}, nil, err
	}
	activities := []db.ActivityLog{statusActivity}
	for _, spec := range extraActivities {
		details := spec.details
		if len(details) == 0 {
			details = []byte("{}")
		}
		writer := h.CompletionActivityWriter
		if writer == nil {
			writer = func(ctx context.Context, queries *db.Queries, params db.CreateActivityParams) (db.ActivityLog, error) {
				return queries.CreateActivity(ctx, params)
			}
		}
		activity, createErr := writer(ctx, qtx, db.CreateActivityParams{
			WorkspaceID: current.WorkspaceID,
			IssueID:     current.ID,
			ActorType:   strToText("system"),
			ActorID:     pgtype.UUID{},
			Action:      spec.action,
			Details:     details,
		})
		if createErr != nil {
			return pullRequestCompletionResult{Outcome: "recorded", Reason: "activity_failed"}, nil, createErr
		}
		activities = append(activities, activity)
	}

	return pullRequestCompletionResult{Outcome: "completed"}, &committedPullRequestCompletion{
		previous:   current,
		updated:    updated,
		source:     source,
		activities: activities,
	}, nil
}

// evaluatePullRequestCompletion is the only provider-driven issue terminal
// kernel for callers that do not already own a provider fact transaction.
func (h *Handler) evaluatePullRequestCompletion(ctx context.Context, issue db.Issue, source string) pullRequestCompletionResult {
	return h.evaluatePullRequestCompletionWithActivities(ctx, issue, source, nil)
}

func (h *Handler) evaluatePullRequestCompletionWithActivities(
	ctx context.Context,
	issue db.Issue,
	source string,
	extraActivities []completionActivitySpec,
) pullRequestCompletionResult {
	result, _ := h.evaluatePullRequestCompletionWithActivitiesResult(ctx, issue, source, extraActivities)
	return result
}

// evaluatePullRequestCompletionWithActivitiesResult exposes infrastructure
// failure to provider HTTP adapters so they can return a retryable 5xx instead
// of acknowledging a rolled-back terminal fact.
func (h *Handler) evaluatePullRequestCompletionWithActivitiesResult(
	ctx context.Context,
	issue db.Issue,
	source string,
	extraActivities []completionActivitySpec,
) (pullRequestCompletionResult, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("pull request completion: begin transaction failed", "error", err, "issue_id", uuidToString(issue.ID), "source", source)
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	if err := lockCompletionIssues(ctx, qtx, []pgtype.UUID{issue.ID}); err != nil {
		slog.Warn("pull request completion: acquire issue lock failed", "error", err, "issue_id", uuidToString(issue.ID), "source", source)
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, err
	}
	result, committed, err := h.evaluatePullRequestCompletionLocked(ctx, qtx, issue, source, extraActivities)
	if err != nil {
		slog.Warn("pull request completion: terminal transaction failed", "error", err, "issue_id", uuidToString(issue.ID), "source", source)
		return result, err
	}
	if committed == nil {
		return result, nil
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("pull request completion: commit failed", "error", err, "issue_id", uuidToString(issue.ID), "source", source)
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, err
	}

	h.finalizePullRequestCompletion(ctx, *committed)
	return result, nil
}

// finalizePullRequestCompletion runs only after the status+activity transaction
// commits. It broadcasts already-durable activity rows, then the Issue update,
// and only then creates the parent comment/task/wake. This does not claim the
// post-commit crash window between those realtime/release effects.
func (h *Handler) finalizePullRequestCompletion(ctx context.Context, completion committedPullRequestCompletion) {
	issue := completion.previous
	updated := completion.updated
	workspaceID := uuidToString(issue.WorkspaceID)
	for _, activity := range completion.activities {
		h.publishCommittedCompletionActivity(workspaceID, activity)
	}
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	h.publish(protocol.EventIssueUpdated, workspaceID, "system", "", map[string]any{
		"issue":                    issueToResponse(updated, prefix),
		"status_changed":           true,
		"status_activity_recorded": true,
		"prev_status":              issue.Status,
		"creator_type":             issue.CreatorType,
		"creator_id":               uuidToString(issue.CreatorID),
		"source":                   completion.source,
	})
	h.notifyParentOfChildDone(ctx, issue, updated)
}

func (h *Handler) publishCommittedCompletionActivity(workspaceID string, activity db.ActivityLog) {
	actorType := ""
	if activity.ActorType.Valid {
		actorType = activity.ActorType.String
	}
	h.Bus.Publish(events.Event{
		Type:        protocol.EventActivityCreated,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"issue_id": uuidToString(activity.IssueID),
			"entry": map[string]any{
				"type":       "activity",
				"id":         uuidToString(activity.ID),
				"actor_type": actorType,
				"actor_id":   uuidToString(activity.ActorID),
				"action":     activity.Action,
				"details":    json.RawMessage(activity.Details),
				"created_at": timestampToString(activity.CreatedAt),
			},
		},
	})
}
