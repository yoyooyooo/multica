package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/scheduler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	externalPRReconcileBatchSize    = 50
	externalPRReconcileLeaseSeconds = 90
)

type externalPRWork struct {
	ID                pgtype.UUID
	WorkspaceID       pgtype.UUID
	IssueID           pgtype.UUID
	LinkID            pgtype.UUID
	SourceRevision    string
	State             string
	Attempt           int32
	MaxAttempts       int32
	LeaseToken        pgtype.UUID
	PreviousStatus    pgtype.Text
	StatusActivityID  pgtype.UUID
	IntendedParentID  pgtype.UUID
	ActivityPublished bool
	IssuePublished    bool
	ParentCommentID   pgtype.UUID
	ParentWakeDone    bool
}

func ExternalPRReconcileJob(pool *pgxpool.Pool, h *Handler) scheduler.JobSpec {
	return scheduler.JobSpec{
		Name:              "external_pr_reconcile",
		Cadence:           time.Minute,
		CatchUpMode:       scheduler.CatchUpLatestOnly,
		RunTimeout:        45 * time.Second,
		StaleTimeout:      2 * time.Minute,
		HeartbeatInterval: 20 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       4,
		RetryBackoff:      []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute},
		Scopes:            scheduler.StaticScopes(scheduler.ScopeGlobal),
		Handler: func(ctx context.Context, input scheduler.HandlerInput) (scheduler.HandlerResult, error) {
			processed, err := h.reconcileExternalPRBatch(ctx, pool, input.RunnerID)
			return scheduler.HandlerResult{RowsAffected: processed}, err
		},
	}
}

func (h *Handler) reconcileExternalPRBatch(ctx context.Context, pool *pgxpool.Pool, runnerID string) (int64, error) {
	if _, err := pool.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET state='dead', lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL,
    last_error_code='lease_expired_max_attempts', completed_at=now(), updated_at=now()
WHERE state='claimed' AND lease_expires_at < now() AND attempt >= max_attempts`); err != nil {
		return 0, err
	}
	var processed int64
	for processed < externalPRReconcileBatchSize {
		work, err := claimExternalPRWork(ctx, pool, runnerID)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return processed, err
		}
		processed++
		if err := h.processExternalPRWork(ctx, work); err != nil {
			if retryErr := failExternalPRWork(ctx, pool, work, externalPRReconcileErrorCode(err)); retryErr != nil {
				return processed, errors.Join(err, retryErr)
			}
			slog.Warn("external PR reconciliation failed", "work_id", uuidToString(work.ID), "error", err)
		}
	}
	return processed, nil
}

func claimExternalPRWork(ctx context.Context, pool *pgxpool.Pool, runnerID string) (externalPRWork, error) {
	var work externalPRWork
	err := pool.QueryRow(ctx, `
WITH candidate AS (
    SELECT id FROM external_pr_reconcile_work
    WHERE attempt < max_attempts AND (
        (state IN ('pending','retry_wait') AND next_attempt_at <= now())
        OR (state='claimed' AND lease_expires_at < now())
    )
    ORDER BY next_attempt_at, updated_at, id
    FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE external_pr_reconcile_work AS work
SET state='claimed', attempt=work.attempt+1, lease_owner=$1,
    lease_token=gen_random_uuid(), lease_expires_at=now()+make_interval(secs => $2),
    updated_at=now(), last_error_code=NULL
FROM candidate WHERE work.id=candidate.id
RETURNING work.id, work.workspace_id, work.issue_id, work.link_id,
    work.source_revision, work.state, work.attempt, work.max_attempts,
    work.lease_token, work.previous_status, work.status_activity_id,
    work.intended_parent_id, work.activity_published, work.issue_published,
    work.parent_comment_id, work.parent_wake_done`, runnerID, float64(externalPRReconcileLeaseSeconds)).Scan(
		&work.ID, &work.WorkspaceID, &work.IssueID, &work.LinkID, &work.SourceRevision,
		&work.State, &work.Attempt, &work.MaxAttempts, &work.LeaseToken,
		&work.PreviousStatus, &work.StatusActivityID, &work.IntendedParentID,
		&work.ActivityPublished, &work.IssuePublished, &work.ParentCommentID,
		&work.ParentWakeDone,
	)
	return work, err
}

func (h *Handler) processExternalPRWork(ctx context.Context, work externalPRWork) error {
	if work.StatusActivityID.Valid {
		return h.finalizeExternalPRWork(ctx, work)
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 88492133))`, "external-work:"+uuidToString(work.LinkID)); err != nil {
		return err
	}
	link, err := readExternalPRLink(ctx, tx, work.LinkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return recordExternalPRWork(ctx, tx, work, "link_deleted")
	}
	if err != nil {
		return err
	}
	if link.WorkspaceID != work.WorkspaceID || link.IssueID != work.IssueID || link.FactRevision != work.SourceRevision {
		return recordExternalPRWork(ctx, tx, work, "stale_or_mismatched_fact")
	}
	if link.State != "merged" || !link.CompletionIntent {
		reason := "closed_unmerged"
		if link.State == "merged" {
			reason = "record_only_fact"
		}
		return recordExternalPRWork(ctx, tx, work, reason)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM issue WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, work.IssueID, work.WorkspaceID); err != nil {
		return err
	}
	issue, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: work.IssueID, WorkspaceID: work.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return recordExternalPRWork(ctx, tx, work, "issue_deleted")
	}
	if err != nil {
		return err
	}
	if allowed, reason := externalPRCompletionPolicy(issue.Metadata); !allowed {
		return recordExternalPRWork(ctx, tx, work, reason)
	}
	terminal := issuestatus.Effective(ctx, qtx, issue.WorkspaceID, issue.Status)
	if terminal == "done" || terminal == "cancelled" {
		return recordExternalPRWork(ctx, tx, work, "terminal_issue")
	}
	if !issue.ParentIssueID.Valid {
		return recordExternalPRWork(ctx, tx, work, "non_child_issue")
	}
	var hasChildren bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE parent_issue_id=$1)`, issue.ID).Scan(&hasChildren); err != nil {
		return err
	}
	if hasChildren {
		return recordExternalPRWork(ctx, tx, work, "non_leaf_issue")
	}
	var hasOpenPR bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM github_pull_request pr
    JOIN issue_pull_request link ON link.pull_request_id=pr.id
    WHERE link.issue_id=$1 AND NOT link.reference_only AND pr.state IN ('open','draft')
    UNION ALL
    SELECT 1 FROM vcs_pull_request pr
    JOIN issue_vcs_pull_request link ON link.pull_request_id=pr.id
    WHERE link.issue_id=$1 AND NOT link.reference_only AND pr.state IN ('open','draft')
    UNION ALL
    SELECT 1 FROM external_pull_request_link pr
    WHERE pr.workspace_id=$2 AND pr.issue_id=$1 AND pr.state IN ('open','draft')
)`, issue.ID, issue.WorkspaceID).Scan(&hasOpenPR); err != nil {
		return err
	}
	if hasOpenPR {
		return recordExternalPRWork(ctx, tx, work, "open_pull_request")
	}

	updated, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID: issue.ID, Status: "done", WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]string{
		"from": issue.Status, "to": updated.Status, "source": "external_pr_merged",
	})
	activity, err := qtx.CreateActivity(ctx, db.CreateActivityParams{
		ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, IssueID: issue.ID,
		ActorType: strToText("system"), Action: "status_changed", Details: details,
	})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET previous_status=$3, status_activity_id=$4, intended_parent_id=$5, updated_at=now()
WHERE id=$1 AND lease_token=$2 AND state='claimed'`, work.ID, work.LeaseToken,
		issue.Status, activity.ID, issue.ParentIssueID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("external PR reconcile lease lost")
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	work.PreviousStatus = pgtype.Text{String: issue.Status, Valid: true}
	work.StatusActivityID = activity.ID
	work.IntendedParentID = issue.ParentIssueID
	return h.finalizeExternalPRWork(ctx, work)
}

func externalPRCompletionPolicy(metadata []byte) (bool, string) {
	if len(metadata) == 0 {
		return true, ""
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &values); err != nil {
		return false, "unsupported_completion_policy"
	}
	raw, ok := values["external_pr_completion_policy"]
	if !ok {
		return true, ""
	}
	var policy string
	if err := json.Unmarshal(raw, &policy); err != nil {
		return false, "unsupported_completion_policy"
	}
	switch policy {
	case "", "leaf_child_only":
		return true, ""
	case "record_only":
		return false, "record_only"
	default:
		return false, "unsupported_completion_policy"
	}
}

func recordExternalPRWork(ctx context.Context, tx pgx.Tx, work externalPRWork, reason string) error {
	tag, err := tx.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET state='recorded', last_error_code=$3, lease_owner=NULL, lease_token=NULL,
    lease_expires_at=NULL, completed_at=now(), updated_at=now()
WHERE id=$1 AND lease_token=$2 AND state='claimed'`, work.ID, work.LeaseToken, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("external PR reconcile lease lost")
	}
	return tx.Commit(ctx)
}

func (h *Handler) finalizeExternalPRWork(ctx context.Context, work externalPRWork) error {
	activity, err := h.Queries.GetActivity(ctx, work.StatusActivityID)
	if err != nil {
		return err
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: work.IssueID, WorkspaceID: work.WorkspaceID})
	if err != nil {
		return err
	}
	if !work.ActivityPublished {
		h.publish(protocol.EventActivityCreated, uuidToString(work.WorkspaceID), "system", "", map[string]any{
			"issue_id": uuidToString(work.IssueID),
			"entry": map[string]any{
				"type": "activity", "id": uuidToString(activity.ID), "actor_type": "system",
				"action": activity.Action, "details": json.RawMessage(activity.Details),
				"created_at": timestampToString(activity.CreatedAt),
			},
		})
		if err := setExternalPRWorkFlag(ctx, h.DB, work, "activity_published"); err != nil {
			return err
		}
		work.ActivityPublished = true
	}
	if !work.IssuePublished {
		response := issueToResponse(issue, h.getIssuePrefix(ctx, issue.WorkspaceID))
		h.fillStatusCategory(ctx, issue.WorkspaceID, &response)
		h.publish(protocol.EventIssueUpdated, uuidToString(work.WorkspaceID), "system", "", map[string]any{
			"issue": response, "status_changed": true, "prev_status": work.PreviousStatus.String,
			"source": "external_pr_merged",
		})
		if err := setExternalPRWorkFlag(ctx, h.DB, work, "issue_published"); err != nil {
			return err
		}
		work.IssuePublished = true
	}
	if !work.ParentWakeDone {
		commentID, err := h.finalizeExternalPRParent(ctx, work, issue)
		if err != nil {
			return err
		}
		tag, err := h.DB.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET parent_comment_id=$3, parent_wake_done=TRUE, updated_at=now()
WHERE id=$1 AND lease_token=$2 AND state='claimed'`, work.ID, work.LeaseToken, commentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("external PR reconcile lease lost")
		}
		work.ParentCommentID = commentID
		work.ParentWakeDone = true
	}
	tag, err := h.DB.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET state='succeeded', lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL,
    completed_at=now(), updated_at=now()
WHERE id=$1 AND lease_token=$2 AND state='claimed'`, work.ID, work.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("external PR reconcile lease lost")
	}
	return nil
}

func (h *Handler) finalizeExternalPRParent(ctx context.Context, work externalPRWork, issue db.Issue) (pgtype.UUID, error) {
	if !issue.ParentIssueID.Valid || issue.ParentIssueID != work.IntendedParentID {
		return pgtype.UUID{}, nil
	}
	previous := issue
	previous.Status = work.PreviousStatus.String
	if isTerminalChildStatus(issuestatus.Effective(ctx, h.Queries, previous.WorkspaceID, previous.Status)) ||
		!isTerminalChildStatus(issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status)) {
		return pgtype.UUID{}, nil
	}
	parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	parentStatus := issuestatus.Effective(ctx, h.Queries, parent.WorkspaceID, parent.Status)
	if parentStatus == "done" || parentStatus == "cancelled" || parentStatus == "backlog" ||
		(parent.AssigneeType.Valid && parent.AssigneeType.String == "member") {
		return pgtype.UUID{}, nil
	}
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if !stageBarrierClosed(children, issue, h.terminalChildPredicate(ctx)) {
		return pgtype.UUID{}, nil
	}
	staged := siblingsAreStaged(children)
	var closedStage int32
	if staged {
		closedStage = issue.Stage.Int32
	}
	comment, err := h.postChildDoneCommentWithID(ctx, parent, issue, children, staged, closedStage, false, work.ID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return comment.ID, nil
}

func setExternalPRWorkFlag(ctx context.Context, dbexec dbExecutor, work externalPRWork, column string) error {
	switch column {
	case "activity_published", "issue_published":
	default:
		return fmt.Errorf("unsupported external PR work flag %q", column)
	}
	tag, err := dbexec.Exec(ctx, `UPDATE external_pr_reconcile_work SET `+column+`=TRUE, updated_at=now() WHERE id=$1 AND lease_token=$2 AND state='claimed'`, work.ID, work.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("external PR reconcile lease lost")
	}
	return nil
}

func failExternalPRWork(ctx context.Context, pool *pgxpool.Pool, work externalPRWork, code string) error {
	delay := time.Minute
	if work.Attempt == 2 {
		delay = 5 * time.Minute
	} else if work.Attempt >= 3 {
		delay = 15 * time.Minute
	}
	tag, err := pool.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET state=CASE WHEN attempt>=max_attempts THEN 'dead' ELSE 'retry_wait' END,
    next_attempt_at=CASE WHEN attempt>=max_attempts THEN next_attempt_at ELSE now()+make_interval(secs => $3) END,
    last_error_code=$4, lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL,
    completed_at=CASE WHEN attempt>=max_attempts THEN now() ELSE NULL END, updated_at=now()
WHERE id=$1 AND lease_token=$2 AND state='claimed'`, work.ID, work.LeaseToken, delay.Seconds(), code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("external PR reconcile lease lost")
	}
	return nil
}

func externalPRReconcileErrorCode(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(message, "lease lost"):
		return "lease_lost"
	default:
		return "reconcile_error"
	}
}
