package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/completionpolicy"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
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
	previous     db.Issue
	updated      db.Issue
	source       string
	activities   []db.ActivityLog
	finalization db.ExternalPrReconcileFinalization
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
		ID:          dbid.NewV7(),
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
	workID pgtype.UUID,
	sourceRevision string,
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

	// A work/source revision may materialize a terminal transition only once.
	// Reconcile work can be retried after the status transaction committed but
	// finalization failed; in that case the durable intent is the authority for
	// the already-consumed source revision. Do not close a reopened Issue or
	// create a second status activity: hand the existing intent to the normal
	// independent finalizer instead.
	if workID.Valid {
		existing, getErr := qtx.GetExternalPRFinalizationForWork(ctx, db.GetExternalPRFinalizationForWorkParams{
			WorkspaceID: current.WorkspaceID,
			WorkID:      workID,
		})
		if getErr == nil {
			return pullRequestCompletionResult{Outcome: "already_done", Reason: "source_revision_consumed"}, &committedPullRequestCompletion{
				previous:     current,
				updated:      current,
				source:       source,
				finalization: existing,
			}, nil
		}
		if !errors.Is(getErr, pgx.ErrNoRows) {
			return pullRequestCompletionResult{Outcome: "recorded", Reason: "finalization_intent_read_failed"}, nil, getErr
		}
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
			ID:          dbid.NewV7(),
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

	if strings.TrimSpace(sourceRevision) == "" {
		sourceRevision = uuidToString(statusActivity.ID)
	}
	finalization, err := qtx.CreateExternalPRFinalization(ctx, db.CreateExternalPRFinalizationParams{
		WorkspaceID:      current.WorkspaceID,
		IssueID:          current.ID,
		WorkID:           workID,
		SourceRevision:   sourceRevision,
		Source:           source,
		PreviousStatus:   current.Status,
		TerminalStatus:   updated.Status,
		StatusActivityID: statusActivity.ID,
		IntendedParentID: current.ParentIssueID,
		ActivityIds:      activityIDs(activities),
	})
	if errors.Is(err, pgx.ErrNoRows) && workID.Valid {
		finalization, err = qtx.GetExternalPRFinalizationForWork(ctx, db.GetExternalPRFinalizationForWorkParams{
			WorkspaceID: current.WorkspaceID,
			WorkID:      workID,
		})
	}
	if err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "finalization_intent_failed"}, nil, err
	}

	return pullRequestCompletionResult{Outcome: "completed"}, &committedPullRequestCompletion{
		previous:     current,
		updated:      updated,
		source:       source,
		activities:   activities,
		finalization: finalization,
	}, nil
}

// evaluatePullRequestCompletion is the only provider-driven issue terminal
// kernel for callers that do not already own a provider fact transaction.
func activityIDs(activities []db.ActivityLog) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, len(activities))
	for _, activity := range activities {
		ids = append(ids, activity.ID)
	}
	return ids
}

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
	return h.evaluatePullRequestCompletionWithActivitiesResultAndWork(ctx, issue, source, extraActivities, pgtype.UUID{}, "")
}

func (h *Handler) evaluatePullRequestCompletionWithActivitiesResultAndWork(
	ctx context.Context,
	issue db.Issue,
	source string,
	extraActivities []completionActivitySpec,
	workID pgtype.UUID,
	sourceRevision string,
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
	result, committed, err := h.evaluatePullRequestCompletionLocked(ctx, qtx, issue, source, extraActivities, workID, sourceRevision)
	if err != nil {
		slog.Warn("pull request completion: terminal transaction failed", "error", err, "issue_id", uuidToString(issue.ID), "source", source)
		return result, err
	}
	if committed == nil {
		if err := tx.Commit(ctx); err != nil {
			return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, err
		}
		if result.Outcome == "already_done" && workID.Valid {
			intent, intentErr := h.Queries.GetExternalPRFinalizationForWork(ctx, db.GetExternalPRFinalizationForWorkParams{
				WorkspaceID: issue.WorkspaceID,
				WorkID:      workID,
			})
			if intentErr == nil {
				if err := h.finalizePullRequestCompletionIntent(ctx, intent.ID); err != nil {
					return result, err
				}
			} else if !errors.Is(intentErr, pgx.ErrNoRows) {
				return result, intentErr
			}
		}
		return result, nil
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("pull request completion: commit failed", "error", err, "issue_id", uuidToString(issue.ID), "source", source)
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, err
	}

	if err := h.finalizePullRequestCompletionIntent(ctx, committed.finalization.ID); err != nil {
		return result, err
	}
	return result, nil
}

func (h *Handler) publishCommittedCompletionActivity(workspaceID string, activity db.ActivityLog) {
	h.publishCommittedCompletionActivityWithDeliveryKey(workspaceID, activity, "")
}

func (h *Handler) publishCommittedCompletionActivityWithDeliveryKey(workspaceID string, activity db.ActivityLog, deliveryKey string) {
	actorType := ""
	if activity.ActorType.Valid {
		actorType = activity.ActorType.String
	}
	h.Bus.Publish(events.Event{
		Type:        protocol.EventActivityCreated,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		DeliveryKey: deliveryKey,
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
