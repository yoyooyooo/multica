package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/completionpolicy"
	"github.com/multica-ai/multica/server/internal/scheduler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	externalPRReconcileJobName      = "external_pr_reconcile"
	externalPRReconcileBatchSize    = 50
	externalPRFinalizationBatchSize = 50
	externalPRReconcileLeaseSeconds = 90
)

var externalPRReconcileSecretPattern = regexp.MustCompile(`(?is)(authorization\s*[:=]\s*bearer\s+[^\s,}]+|(?:token|secret|password|api[_-]?key|credential)\s*[:=]\s*["']?[^\s,}"']+|bearer\s+[A-Za-z0-9._~+/=-]+|mat_[A-Za-z0-9_-]+|ags_sess_[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----)`)

var errExternalPRReconcileLeaseLost = errors.New("external PR reconcile lease lost")

// ExternalPRReconcileJob is the single scheduler composition point for the
// typed external-PR continuation. sys_cron_executions only leases this bounded
// sweep; business work state remains in external_pr_reconcile_work.
func ExternalPRReconcileJob(pool *pgxpool.Pool, h *Handler) scheduler.JobSpec {
	return scheduler.JobSpec{
		Name:              externalPRReconcileJobName,
		Cadence:           time.Minute,
		CatchUpMode:       scheduler.CatchUpLatestOnly,
		RunTimeout:        45 * time.Second,
		StaleTimeout:      2 * time.Minute,
		HeartbeatInterval: 20 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       4,
		RetryBackoff:      []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute},
		Scopes:            scheduler.StaticScopes(scheduler.ScopeGlobal),
		Handler: func(ctx context.Context, in scheduler.HandlerInput) (scheduler.HandlerResult, error) {
			return h.reconcileExternalPRWork(ctx, pool, in)
		},
	}
}

type externalPRFinalizationSweepResult struct {
	Processed int64
	Expired   int64
	Dead      int64
}

func (h *Handler) reconcileExternalPRWork(ctx context.Context, pool *pgxpool.Pool, in scheduler.HandlerInput) (scheduler.HandlerResult, error) {
	q := db.New(pool)
	// Finalization is an independent durable source. It must run before work
	// recovery because the status transaction and the finalizer have a
	// post-commit crash window, including intents with NULL work_id.
	finalizationSweep, finalizationSweepErr := h.reconcileDueExternalPRFinalizationsWithAudit(ctx, q, in.RunnerID)
	if finalizationSweepErr != nil && !errors.Is(finalizationSweepErr, errExternalPRFinalizationDead) {
		return scheduler.HandlerResult{Result: map[string]any{
			"finalizations_processed": finalizationSweep.Processed,
			"finalizations_expired":   finalizationSweep.Expired,
			"finalizations_dead":      finalizationSweep.Dead,
		}}, fmt.Errorf("external PR finalization sweep: %w", finalizationSweepErr)
	}
	// This source sweep is deterministic and repairs both a lost Bus nudge and
	// a fact commit that happened before a process restart. It shares the
	// provider-workspace/Issue fence with every delete path.
	if _, err := h.sweepExternalPRTerminalWork(ctx, pool); err != nil {
		return scheduler.HandlerResult{Result: map[string]any{
			"finalizations_processed": finalizationSweep.Processed,
			"finalizations_expired":   finalizationSweep.Expired,
			"finalizations_dead":      finalizationSweep.Dead,
		}}, fmt.Errorf("external PR source sweep: %w", err)
	}
	if _, err := q.ExpireExternalPRReconcileWork(ctx); err != nil {
		return scheduler.HandlerResult{}, fmt.Errorf("expire exhausted external PR work: %w", err)
	}

	var processed, deadFinalizations int64
	for processed < externalPRReconcileBatchSize {
		work, err := q.ClaimExternalPRReconcileWork(ctx, db.ClaimExternalPRReconcileWorkParams{
			LeaseOwner: pgtype.Text{String: in.RunnerID, Valid: true},
			Secs:       float64(externalPRReconcileLeaseSeconds),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return scheduler.HandlerResult{RowsAffected: processed}, fmt.Errorf("claim external PR reconcile work: %w", err)
		}
		processed++
		result, finalizationID, reconcileErr := h.reconcileOneExternalPRWork(ctx, work)
		if finalizationID.Valid {
			finalizationErr := error(nil)
			_, finalizationErr = h.finalizePullRequestCompletionIntentWithOutcome(ctx, finalizationID)
			linkedState, settleErr := h.settleExternalPRReconcileWorkAfterFinalization(ctx, q, work, finalizationID, finalizationErr)
			if linkedState == "deferred" {
				// Finalization is still pending/retry_wait or held by another
				// finalizer. The dedicated DB-clock defer path clears this work
				// lease without consuming its own attempt budget.
				continue
			}
			if settleErr != nil {
				reconcileErr = settleErr
			} else if linkedState == "succeeded" || linkedState == "recorded" {
				rows, completeErr := q.CompleteExternalPRReconcileWork(ctx, db.CompleteExternalPRReconcileWorkParams{
					ID: work.ID, LeaseToken: work.LeaseToken, State: linkedState,
				})
				if completeErr != nil {
					return scheduler.HandlerResult{RowsAffected: processed}, fmt.Errorf("complete external PR reconcile work after finalization: %w", completeErr)
				}
				if rows != 1 {
					return scheduler.HandlerResult{RowsAffected: processed}, errExternalPRReconcileLeaseLost
				}
				continue
			}
		}
		if errors.Is(reconcileErr, errExternalPRFinalizationDead) {
			rows, deadErr := q.CompleteExternalPRReconcileWork(ctx, db.CompleteExternalPRReconcileWorkParams{
				ID: work.ID, LeaseToken: work.LeaseToken, State: "dead",
			})
			if deadErr != nil {
				return scheduler.HandlerResult{RowsAffected: processed}, fmt.Errorf("mark external PR work dead after finalization failure: %w", deadErr)
			}
			if rows == 0 {
				// A finalization sweep may have won the linked-work CAS before this
				// worker reached its settle branch. Accept only that same typed
				// finalization terminal state; unrelated lease loss remains fatal.
				linked, readErr := q.GetExternalPRReconcileWork(ctx, work.ID)
				intent, intentErr := q.GetExternalPRFinalization(ctx, finalizationID)
				if readErr != nil || intentErr != nil || linked.State != "dead" || linked.LastErrorCode.String != "finalization_dead" || intent.State != "dead" || intent.WorkID != work.ID {
					return scheduler.HandlerResult{RowsAffected: processed}, errExternalPRReconcileLeaseLost
				}
			} else if rows != 1 {
				return scheduler.HandlerResult{RowsAffected: processed}, errExternalPRReconcileLeaseLost
			}
			deadFinalizations++
			continue
		}
		if reconcileErr == nil {
			state := "succeeded"
			if result.Outcome != "completed" {
				// already_done without a matching finalization intent is a
				// recorded/no-op observation, not proof that this work's
				// completion side effects ran.
				state = "recorded"
			}
			rows, completeErr := q.CompleteExternalPRReconcileWork(ctx, db.CompleteExternalPRReconcileWorkParams{
				ID: work.ID, LeaseToken: work.LeaseToken, State: state,
			})
			if completeErr != nil {
				return scheduler.HandlerResult{RowsAffected: processed}, fmt.Errorf("complete external PR reconcile work: %w", completeErr)
			}
			if rows != 1 {
				return scheduler.HandlerResult{RowsAffected: processed}, errExternalPRReconcileLeaseLost
			}
			continue
		}

		delaySeconds := externalPRReconcileRetryDelaySeconds(work.Attempt)
		rows, failErr := q.FailExternalPRReconcileWork(ctx, db.FailExternalPRReconcileWorkParams{
			ID: work.ID, LeaseToken: work.LeaseToken, DelaySeconds: delaySeconds,
			LastErrorCode:     pgtype.Text{String: externalPRReconcileErrorCode(reconcileErr), Valid: true},
			LastRedactedError: pgtype.Text{String: externalPRReconcileErrorSummary(reconcileErr), Valid: true},
		})
		if failErr != nil {
			return scheduler.HandlerResult{RowsAffected: processed}, fmt.Errorf("fail external PR reconcile work: %w", failErr)
		}
		if rows != 1 {
			return scheduler.HandlerResult{RowsAffected: processed}, errExternalPRReconcileLeaseLost
		}
	}
	result := scheduler.HandlerResult{RowsAffected: processed, Result: map[string]any{
		"processed":               processed,
		"finalizations_processed": finalizationSweep.Processed,
		"finalizations_expired":   finalizationSweep.Expired,
		"finalizations_dead":      finalizationSweep.Dead + deadFinalizations,
	}}
	if deadFinalizations > 0 {
		return result, fmt.Errorf("external PR finalization dead for %d reconcile work item(s)", deadFinalizations)
	}
	if finalizationSweepErr != nil {
		return result, fmt.Errorf("external PR finalization dead: %w", finalizationSweepErr)
	}
	return result, nil
}

func (h *Handler) settleExternalPRReconcileWorkAfterFinalization(
	ctx context.Context,
	q *db.Queries,
	work db.ExternalPrReconcileWork,
	intentID pgtype.UUID,
	finalizationErr error,
) (string, error) {
	intent, err := q.GetExternalPRFinalization(ctx, intentID)
	if err != nil {
		if finalizationErr != nil {
			return "", finalizationErr
		}
		return "", err
	}
	switch intent.State {
	case "pending", "retry_wait":
		rows, deferErr := q.DeferExternalPRReconcileWorkForFinalization(ctx, db.DeferExternalPRReconcileWorkForFinalizationParams{
			ID: work.ID, LeaseToken: work.LeaseToken, ID_2: intentID,
		})
		if deferErr != nil {
			return "", deferErr
		}
		if rows != 1 {
			return "", errExternalPRReconcileLeaseLost
		}
		return "deferred", nil
	case "succeeded", "recorded":
		return intent.State, nil
	case "dead":
		return "dead", &externalPRFinalizationDeadError{IntentID: intent.ID, Code: intent.LastErrorCode.String}
	default:
		if finalizationErr != nil {
			return "", finalizationErr
		}
		return "", fmt.Errorf("external PR finalization %s has unsupported state %q", uuidToString(intent.ID), intent.State)
	}
}

func (h *Handler) reconcileDueExternalPRFinalizations(ctx context.Context, q *db.Queries, runnerID string) (int64, error) {
	result, err := h.reconcileDueExternalPRFinalizationsWithAudit(ctx, q, runnerID)
	return result.Processed, err
}

func (h *Handler) reconcileDueExternalPRFinalizationsWithAudit(ctx context.Context, q *db.Queries, _ string) (externalPRFinalizationSweepResult, error) {
	var result externalPRFinalizationSweepResult
	var deadErr error
	propagated, err := q.MarkExternalPRReconcileWorksDeadForDeadFinalizations(ctx)
	if err != nil {
		return result, err
	}
	if propagated > 0 {
		result.Dead += propagated
		deadErr = errors.Join(deadErr, errExternalPRFinalizationDead)
	}
	expired, err := q.ExpireExternalPRFinalization(ctx)
	if err != nil {
		return result, fmt.Errorf("expire exhausted external PR finalization leases: %w", err)
	}
	result.Expired = int64(len(expired))
	for _, row := range expired {
		// Expiry itself is a terminal finalization failure. Surface it even when
		// the intent has no linked continuation work: such rows are otherwise no
		// longer due and would make the scheduler appear healthy after a crash at
		// the retry ceiling.
		result.Dead++
		deadErr = errors.Join(deadErr, &externalPRFinalizationDeadError{
			IntentID: row.ID,
			Code:     "lease_expired_max_attempts",
		})
		if row.WorkID.Valid {
			if _, markErr := q.MarkExternalPRReconcileWorkDeadAfterFinalization(ctx, row.WorkID); markErr != nil {
				deadErr = errors.Join(deadErr, fmt.Errorf("mark external PR work dead after expired finalization %s: %w", uuidToString(row.ID), markErr))
			}
		}
		slog.Error("external PR finalization expired at retry ceiling", "intent_id", uuidToString(row.ID), "work_id", uuidToString(row.WorkID))
	}
	ids, err := q.ListDueExternalPRFinalizationIDs(ctx, int32(externalPRFinalizationBatchSize))
	if err != nil {
		return result, errors.Join(deadErr, err)
	}
	for _, intentID := range ids {
		claimed, err := h.finalizePullRequestCompletionIntentWithOutcome(ctx, intentID)
		if err != nil {
			if errors.Is(err, errExternalPRFinalizationDead) {
				result.Dead++
				deadErr = errors.Join(deadErr, err)
				slog.Error("external PR finalization is dead", "error", err, "intent_id", uuidToString(intentID))
				continue
			}
			// The error path has already persisted DB-clock retry/dead state. Keep
			// sweeping other intents in this tick instead of starving the queue.
			slog.Warn("external PR finalization failed", "error", err, "intent_id", uuidToString(intentID))
			continue
		}
		if claimed {
			result.Processed++
		}
	}
	if deadErr != nil {
		return result, deadErr
	}
	return result, nil
}

// sweepExternalPRTerminalWork takes a stable scope snapshot, acquires the
// same provider-workspace and Issue advisory locks as deletes, then rereads
// the link set before inserting work. A delete-first race therefore leaves no
// orphan continuation row without relying on a foreign key or cascade.
func (h *Handler) sweepExternalPRTerminalWork(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	workspaceIDs := make([]pgtype.UUID, 0)
	rows, err := tx.Query(ctx, `SELECT DISTINCT workspace_id FROM external_pull_request_link WHERE state IN ('closed','merged') ORDER BY workspace_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		workspaceIDs = append(workspaceIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if err := lockProviderWorkspaces(ctx, tx, workspaceIDs); err != nil {
		return 0, err
	}
	issueIDs := make([]pgtype.UUID, 0)
	rows, err = tx.Query(ctx, `SELECT DISTINCT issue_id FROM external_pull_request_link WHERE state IN ('closed','merged') ORDER BY issue_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		issueIDs = append(issueIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if err := lockCompletionIssues(ctx, qtx, issueIDs); err != nil {
		return 0, err
	}
	count, err := qtx.SweepExternalPRTerminalWork(ctx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func externalPRReconcileRetryDelaySeconds(attempt int32) float64 {
	switch {
	case attempt <= 1:
		return 60
	case attempt == 2:
		return 300
	default:
		return 900
	}
}

// reconcileOneExternalPRWork owns the provider-workspace -> identity -> Issue
// lock sequence. Every link, revision, Issue, and policy read used for
// authorization happens after those locks in this transaction; no pre-lock
// terminal snapshot is ever used to call the completion kernel.
func (h *Handler) reconcileOneExternalPRWork(ctx context.Context, work db.ExternalPrReconcileWork) (pullRequestCompletionResult, pgtype.UUID, error) {
	if work.Kind != "external_pr_terminal" {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "unsupported_work_kind"}, pgtype.UUID{}, nil
	}
	if !work.WorkspaceID.Valid || !work.IssueID.Valid || !work.LinkID.Valid || strings.TrimSpace(work.Provider) == "" || strings.TrimSpace(work.ExternalRepo) == "" || work.ExternalNumber <= 0 {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "invalid_work_identity"}, pgtype.UUID{}, nil
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{work.WorkspaceID}); err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}
	identity := fmt.Sprintf("%s:%s:%s:%d", uuidToString(work.WorkspaceID), work.Provider, work.ExternalRepo, work.ExternalNumber)
	if err := lockPullRequestIdentity(ctx, tx, "external", identity); err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}
	if err := lockCompletionIssues(ctx, qtx, []pgtype.UUID{work.IssueID}); err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}

	var (
		linkID, workspaceID, issueID                                                       pgtype.UUID
		provider, externalRepo, mergeProvider, mergeRepo, externalURL, mergeURL, mergedSHA string
		externalNumber, mergeNumber                                                        int32
		confidence, state, factsRevision                                                   string
		completionIntent                                                                   bool
	)
	row := tx.QueryRow(ctx, `
SELECT id, workspace_id, issue_id, provider, external_repo, external_number,
       COALESCE(external_url, ''), COALESCE(merge_provider, ''), COALESCE(merge_repo, ''),
       COALESCE(merge_number, 0), COALESCE(merge_url, ''), COALESCE(merged_sha, ''),
       link_confidence, completion_intent, state,
       `+externalPRLinkEffectiveRevisionExpr+`
FROM external_pull_request_link
WHERE workspace_id=$1 AND id=$2
FOR UPDATE`, work.WorkspaceID, work.LinkID)
	if err := row.Scan(&linkID, &workspaceID, &issueID, &provider, &externalRepo, &externalNumber,
		&externalURL, &mergeProvider, &mergeRepo, &mergeNumber, &mergeURL, &mergedSHA,
		&confidence, &completionIntent, &state, &factsRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pullRequestCompletionResult{Outcome: "recorded", Reason: "link_deleted"}, pgtype.UUID{}, nil
		}
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}
	if workspaceID != work.WorkspaceID || issueID != work.IssueID || linkID != work.LinkID || provider != work.Provider || externalRepo != work.ExternalRepo || externalNumber != work.ExternalNumber {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "work_identity_mismatch"}, pgtype.UUID{}, nil
	}
	if strings.TrimSpace(factsRevision) != strings.TrimSpace(work.SourceRevision) {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "stale_source_revision"}, pgtype.UUID{}, nil
	}
	// closed-unmerged facts are intentionally record-only. A merged fact is the
	// only external terminal state allowed to enter the completion kernel.
	if state != "merged" {
		reason := "closed_unmerged"
		if state != "closed" {
			reason = "non_terminal_fact"
		}
		return pullRequestCompletionResult{Outcome: "recorded", Reason: reason}, pgtype.UUID{}, nil
	}
	if strings.EqualFold(confidence, "inferred") || !completionIntent {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "unverified_link"}, pgtype.UUID{}, nil
	}

	issue, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pullRequestCompletionResult{Outcome: "recorded", Reason: "issue_deleted"}, pgtype.UUID{}, nil
		}
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}
	policy := completionpolicy.Parse(issue.Metadata)
	if policy == completionpolicy.RecordOnly {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "record_only"}, pgtype.UUID{}, nil
	}
	if policy == completionpolicy.Unsupported {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "completion_policy_unsupported"}, pgtype.UUID{}, nil
	}

	completionReq := externalPullRequestLinkRequest{
		Provider: provider, IssueID: uuidToString(issueID), WorkspaceID: uuidToString(workspaceID),
		ExternalRepo: externalRepo, ExternalNumber: externalNumber, ExternalURL: externalURL,
		MergeProvider: mergeProvider, MergeRepo: mergeRepo, MergeNumber: mergeNumber, MergeURL: mergeURL,
		MergedSHA: mergedSHA, LinkConfidence: confidence, State: state, CompletionIntent: &completionIntent,
	}
	result, committed, err := h.evaluatePullRequestCompletionLocked(ctx, qtx, issue, "external_pr_terminal_reconcile", []completionActivitySpec{externalPRCompletionActivitySpec(completionReq)}, work.ID, factsRevision)
	if err != nil {
		return result, pgtype.UUID{}, err
	}
	var finalizationID pgtype.UUID
	if committed != nil {
		finalizationID = committed.finalization.ID
	} else if result.Outcome == "already_done" {
		intent, intentErr := qtx.GetExternalPRFinalizationForWork(ctx, db.GetExternalPRFinalizationForWorkParams{WorkspaceID: work.WorkspaceID, WorkID: work.ID})
		if intentErr == nil {
			finalizationID = intent.ID
		} else if !errors.Is(intentErr, pgx.ErrNoRows) {
			return result, pgtype.UUID{}, intentErr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return pullRequestCompletionResult{Outcome: "recorded", Reason: "update_failed"}, pgtype.UUID{}, err
	}
	return result, finalizationID, nil
}

func externalPRReconcileErrorCode(err error) string {
	if errors.Is(err, errExternalPRReconcileLeaseLost) {
		return "lease_lost"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var conflict externalPRConflictError
	if errors.As(err, &conflict) {
		return "authority_conflict"
	}
	return "reconcile_error"
}

// Only fixed, structured summaries are persisted. The scrubber remains useful
// for logs/tests but durable work never stores arbitrary provider/DB details.
func externalPRReconcileErrorSummary(err error) string {
	switch externalPRReconcileErrorCode(err) {
	case "lease_lost":
		return "external PR reconciliation lease lost"
	case "timeout":
		return "external PR reconciliation timed out"
	case "authority_conflict":
		return "external PR reconciliation authority conflict"
	default:
		return "external PR reconciliation failed"
	}
}

func redactExternalPRReconcileError(err error) string {
	message := strings.TrimSpace(err.Error())
	message = externalPRReconcileSecretPattern.ReplaceAllString(message, "[redacted]")
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		return "external PR reconciliation failed"
	}
	return message
}
