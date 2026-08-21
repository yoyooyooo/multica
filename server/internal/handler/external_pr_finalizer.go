package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const externalPRFinalizationLeaseSeconds = 90

var errExternalPRFinalizationLeaseLost = errors.New("external PR finalization lease lost")
var errExternalPRFinalizationDead = errors.New("external PR finalization is dead")

// externalPRFinalizationDeadError is returned when a retry observes a
// terminal finalization failure. It is deliberately typed so the reconcile
// worker can mark its linked work dead instead of falsely completing it.
type externalPRFinalizationDeadError struct {
	IntentID pgtype.UUID
	Code     string
}

func (e *externalPRFinalizationDeadError) Error() string {
	return fmt.Sprintf("external PR finalization %s is dead (%s)", uuidToString(e.IntentID), e.Code)
}

func (e *externalPRFinalizationDeadError) Unwrap() error { return errExternalPRFinalizationDead }

func externalPRFinalizationDeliveryKey(intentID pgtype.UUID, step string) string {
	return "external-pr-finalization:v1:" + uuidToString(intentID) + ":" + step
}

// finalizePullRequestCompletionIntent claims a durable intent only after the
// canonical provider-workspace -> sorted Issue advisory -> locked reread
// protocol is held. This keeps finalization and every deletion path in one lock order.
func (h *Handler) finalizePullRequestCompletionIntent(ctx context.Context, intentID pgtype.UUID) error {
	_, err := h.finalizePullRequestCompletionIntentWithOutcome(ctx, intentID)
	return err
}

func (h *Handler) finalizePullRequestCompletionIntentWithOutcome(ctx context.Context, intentID pgtype.UUID) (bool, error) {
	if !intentID.Valid {
		return false, fmt.Errorf("external PR finalization intent is invalid")
	}
	intent, err := h.claimExternalPRFinalizationWithCanonicalLocks(ctx, intentID, "external-pr-finalizer-"+randomID())
	if errors.Is(err, pgx.ErrNoRows) {
		current, readErr := h.Queries.GetExternalPRFinalization(ctx, intentID)
		if errors.Is(readErr, pgx.ErrNoRows) {
			// The owning Issue/workspace was deleted and its application-owned
			// continuation row was cleaned up. There is no side effect to replay.
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
		if current.State == "dead" {
			return false, &externalPRFinalizationDeadError{IntentID: intentID, Code: current.LastErrorCode.String}
		}
		// Another worker owns the lease, or a prior attempt is terminal.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, h.finalizeClaimedPullRequestCompletionIntent(ctx, intent)
}

// claimExternalPRFinalizationWithCanonicalLocks performs the claim after the
// same workspace and Issue locks used by provider fact writers and deletes.
// The initial reads only discover the lock scope; all authorization/effect
// reads happen again after the locks are held.
func (h *Handler) claimExternalPRFinalizationWithCanonicalLocks(ctx context.Context, intentID pgtype.UUID, owner string) (db.ExternalPrReconcileFinalization, error) {
	var empty db.ExternalPrReconcileFinalization
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	intent, err := qtx.GetExternalPRFinalization(ctx, intentID)
	if err != nil {
		return empty, err
	}
	if !intent.WorkspaceID.Valid || !intent.IssueID.Valid {
		return empty, fmt.Errorf("external PR finalization has invalid lock scope")
	}
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{intent.WorkspaceID}); err != nil {
		return empty, fmt.Errorf("lock finalization provider workspace: %w", err)
	}
	if _, _, err := lockExternalPRFinalizationIssueScope(ctx, qtx, intent.WorkspaceID, intent.IssueID); err != nil {
		return empty, err
	}
	claimed, err := qtx.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentID, LeaseOwner: pgtype.Text{String: owner, Valid: true}, Secs: externalPRFinalizationLeaseSeconds,
	})
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}
	return claimed, nil
}

// beginExternalPRFinalizationStep obtains the same locks before locking and
// rereading the intent lease. If a delete won the fence, the post-lock Issue
// reread reports a missing scope and the caller records the intent without
// creating a stale parent effect.
func (h *Handler) beginExternalPRFinalizationStep(ctx context.Context, intentID, leaseToken pgtype.UUID) (pgx.Tx, *db.Queries, db.ExternalPrReconcileFinalization, db.Issue, db.Issue, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, nil, db.ExternalPrReconcileFinalization{}, db.Issue{}, db.Issue{}, err
	}
	qtx := h.Queries.WithTx(tx)
	rollback := func(err error) (pgx.Tx, *db.Queries, db.ExternalPrReconcileFinalization, db.Issue, db.Issue, error) {
		_ = tx.Rollback(ctx)
		return nil, nil, db.ExternalPrReconcileFinalization{}, db.Issue{}, db.Issue{}, err
	}
	initial, err := qtx.GetExternalPRFinalization(ctx, intentID)
	if err != nil {
		return rollback(err)
	}
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{initial.WorkspaceID}); err != nil {
		return rollback(fmt.Errorf("lock finalization provider workspace: %w", err))
	}
	child, parent, err := lockExternalPRFinalizationIssueScope(ctx, qtx, initial.WorkspaceID, initial.IssueID)
	if err != nil {
		return rollback(err)
	}
	intent, err := loadClaimedExternalPRFinalization(ctx, qtx, intentID, leaseToken)
	if err != nil {
		return rollback(err)
	}
	return tx, qtx, intent, child, parent, nil
}

// lockExternalPRFinalizationIssueScope locks the child and current parent in
// UUID order, then rereads their rows. The provider-workspace fence makes this
// advisory scope mutually exclusive with deletion; unlike deletion, the
// finalizer does not take a parent FOR UPDATE row lock because parent task
// enqueue uses a separate transaction and must not deadlock on that row.
func lockExternalPRFinalizationIssueScope(ctx context.Context, qtx *db.Queries, workspaceID, issueID pgtype.UUID) (db.Issue, db.Issue, error) {
	child, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := lockCompletionIssues(ctx, qtx, []pgtype.UUID{issueID}); err != nil {
			return db.Issue{}, db.Issue{}, err
		}
		return db.Issue{}, db.Issue{}, nil
	}
	if err != nil {
		return db.Issue{}, db.Issue{}, err
	}
	initialParentID := child.ParentIssueID
	issueIDs := []pgtype.UUID{issueID}
	if initialParentID.Valid {
		issueIDs = append(issueIDs, initialParentID)
	}
	if err := lockCompletionIssues(ctx, qtx, issueIDs); err != nil {
		return db.Issue{}, db.Issue{}, err
	}
	child, err = qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, db.Issue{}, nil
	}
	if err != nil {
		return db.Issue{}, db.Issue{}, err
	}
	if child.ParentIssueID != initialParentID {
		return db.Issue{}, db.Issue{}, fmt.Errorf("external PR finalization issue scope changed during lock acquisition")
	}
	if !child.ParentIssueID.Valid {
		return child, db.Issue{}, nil
	}
	parent, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: child.ParentIssueID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return child, db.Issue{}, nil
	}
	if err != nil {
		return db.Issue{}, db.Issue{}, err
	}
	return child, parent, nil
}

func (h *Handler) finalizeClaimedPullRequestCompletionIntent(ctx context.Context, intent db.ExternalPrReconcileFinalization) error {
	if !intent.LeaseToken.Valid {
		return errExternalPRFinalizationLeaseLost
	}
	steps := []struct {
		code string
		fn   func(context.Context, pgtype.UUID, pgtype.UUID) error
	}{
		{"activity_finalize_error", h.finalizeCompletionActivities},
		{"issue_finalize_error", h.finalizeCompletionIssueEvent},
		{"parent_comment_finalize_error", h.finalizeCompletionParentComment},
		{"comment_finalize_error", h.finalizeCompletionCommentEvent},
		{"parent_wake_finalize_error", h.finalizeCompletionParentWake},
	}
	for i, step := range steps {
		if i > 0 {
			current, readErr := h.Queries.GetExternalPRFinalization(ctx, intent.ID)
			if errors.Is(readErr, pgx.ErrNoRows) {
				// Issue/workspace cleanup owns the continuation row. The
				// finalizer has no remaining effect to replay.
				return nil
			}
			if readErr != nil {
				return readErr
			}
			switch current.State {
			case "succeeded", "recorded":
				// A prior step may have consumed the intent as recorded
				// (for example, after an Issue was reopened). Do not try
				// later steps with the lease that terminalization cleared.
				return nil
			case "dead":
				return &externalPRFinalizationDeadError{IntentID: current.ID, Code: current.LastErrorCode.String}
			}
		}
		if h.ExternalPRFinalizationStepHook != nil {
			if hookErr := h.ExternalPRFinalizationStepHook(step.code); hookErr != nil {
				if recordErr := h.recordFinalizationError(ctx, intent.ID, intent.LeaseToken, step.code); recordErr != nil {
					return errors.Join(hookErr, recordErr)
				}
				return hookErr
			}
		}
		if err := step.fn(ctx, intent.ID, intent.LeaseToken); err != nil {
			if recordErr := h.recordFinalizationError(ctx, intent.ID, intent.LeaseToken, step.code); recordErr != nil {
				return errors.Join(err, recordErr)
			}
			return err
		}
	}
	return nil
}

func loadClaimedExternalPRFinalization(ctx context.Context, qtx *db.Queries, intentID, leaseToken pgtype.UUID) (db.ExternalPrReconcileFinalization, error) {
	intent, err := qtx.GetExternalPRFinalizationForUpdate(ctx, intentID)
	if err != nil {
		return intent, err
	}
	if !leaseToken.Valid || !intent.LeaseToken.Valid || intent.LeaseToken != leaseToken {
		return intent, errExternalPRFinalizationLeaseLost
	}
	return intent, nil
}

// finalizationGenerationCurrent must be called while the canonical Issue lock
// is held. A status string alone is not a generation: an Issue can be reopened
// and later enter the same terminal status again with a new status activity.
func finalizationGenerationCurrent(ctx context.Context, qtx *db.Queries, intent db.ExternalPrReconcileFinalization, child db.Issue) (bool, error) {
	if !child.ID.Valid || !intent.StatusActivityID.Valid {
		return false, nil
	}
	currentID, err := qtx.GetCurrentExternalPRTerminalStatusActivityID(ctx, db.GetCurrentExternalPRTerminalStatusActivityIDParams{
		WorkspaceID: intent.WorkspaceID,
		IssueID:     child.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return currentID == intent.StatusActivityID, nil
}

func finalizationParentIdentityCurrent(intent db.ExternalPrReconcileFinalization, child, parent db.Issue) bool {
	if child.ParentIssueID.Valid != intent.IntendedParentID.Valid {
		return false
	}
	if child.ParentIssueID.Valid && child.ParentIssueID != intent.IntendedParentID {
		return false
	}
	if intent.IntendedParentID.Valid {
		return parent.ID.Valid && parent.ID == intent.IntendedParentID
	}
	return !parent.ID.Valid
}

func updateClaimedExternalPRFinalization(ctx context.Context, qtx *db.Queries, intent db.ExternalPrReconcileFinalization) error {
	rows, err := qtx.UpdateExternalPRFinalization(ctx, finalizationUpdateParams(intent))
	if err != nil {
		return err
	}
	if rows != 1 {
		return errExternalPRFinalizationLeaseLost
	}
	return nil
}

func (h *Handler) finalizeCompletionActivities(ctx context.Context, intentID, leaseToken pgtype.UUID) error {
	tx, qtx, intent, child, _, err := h.beginExternalPRFinalizationStep(ctx, intentID, leaseToken)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if intent.State == "succeeded" || intent.State == "recorded" || intent.ActivityPublished {
		return tx.Commit(ctx)
	}
	for _, activityID := range intent.ActivityIds {
		current, currentErr := finalizationGenerationCurrent(ctx, qtx, intent, child)
		if currentErr != nil {
			return currentErr
		}
		if !current {
			return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
		}
		activity, getErr := qtx.GetActivity(ctx, activityID)
		if errors.Is(getErr, pgx.ErrNoRows) {
			continue
		}
		if getErr != nil {
			return getErr
		}
		h.publishCommittedCompletionActivityWithDeliveryKey(uuidToString(intent.WorkspaceID), activity, externalPRFinalizationDeliveryKey(intent.ID, "activity"))
	}
	intent.ActivityPublished = true
	if err := updateClaimedExternalPRFinalization(ctx, qtx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) finalizeCompletionIssueEvent(ctx context.Context, intentID, leaseToken pgtype.UUID) error {
	tx, qtx, intent, child, _, err := h.beginExternalPRFinalizationStep(ctx, intentID, leaseToken)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if intent.State == "succeeded" || intent.State == "recorded" || intent.IssuePublished {
		return tx.Commit(ctx)
	}
	if !child.ID.Valid {
		intent.IssuePublished = true
		intent.State = "recorded"
		if updateErr := updateClaimedExternalPRFinalization(ctx, qtx, intent); updateErr != nil {
			return updateErr
		}
		return tx.Commit(ctx)
	}
	// The status transaction may have committed before this replay, while the
	// Issue was subsequently reopened and possibly entered the same terminal
	// status again. The activity ID is the generation fence; a status string is
	// not sufficient to authorize this old event.
	current, currentErr := finalizationGenerationCurrent(ctx, qtx, intent, child)
	if currentErr != nil {
		return currentErr
	}
	if !current {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	issue := child
	prefix := h.getIssuePrefix(ctx, intent.WorkspaceID)
	if h.Bus != nil {
		h.Bus.Publish(events.Event{
			Type:        protocol.EventIssueUpdated,
			WorkspaceID: uuidToString(intent.WorkspaceID),
			ActorType:   "system",
			Payload: map[string]any{
				"issue":                    issueToResponse(issue, prefix),
				"status_changed":           true,
				"status_activity_recorded": true,
				"prev_status":              intent.PreviousStatus,
				"creator_type":             issue.CreatorType,
				"creator_id":               uuidToString(issue.CreatorID),
				"source":                   intent.Source,
			},
			DeliveryKey: externalPRFinalizationDeliveryKey(intent.ID, "issue"),
		})
	}
	intent.IssuePublished = true
	if err := updateClaimedExternalPRFinalization(ctx, qtx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) finalizeCompletionParentComment(ctx context.Context, intentID, leaseToken pgtype.UUID) error {
	tx, qtx, intent, child, parent, err := h.beginExternalPRFinalizationStep(ctx, intentID, leaseToken)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if intent.State == "succeeded" || intent.State == "recorded" {
		return tx.Commit(ctx)
	}
	if !child.ID.Valid || !parent.ID.Valid || !finalizationParentIdentityCurrent(intent, child, parent) {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	current, currentErr := finalizationGenerationCurrent(ctx, qtx, intent, child)
	if currentErr != nil {
		return currentErr
	}
	if !current {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	if parent.Status == "done" || parent.Status == "cancelled" || parent.Status == "backlog" ||
		(parent.AssigneeType.Valid && parent.AssigneeType.String == "member") {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	children, err := qtx.ListChildIssues(ctx, parent.ID)
	if err != nil {
		return err
	}
	if !stageBarrierClosed(children, child, h.terminalChildPredicate(ctx)) {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}

	staged := siblingsAreStaged(children)
	closedStage := int32(0)
	if staged && child.Stage.Valid {
		closedStage = child.Stage.Int32
	}
	content := h.externalPRChildDoneCommentContent(ctx, parent, child, children, staged, closedStage)
	key := externalPRBarrierFinalizationKey(parent, children, staged, closedStage)
	// Keep the generation and intended-parent checks immediately adjacent to the
	// durable comment insert. The Issue lock makes this a step-local fence while
	// the next event/wake steps use their own fresh fence.
	current, currentErr = finalizationGenerationCurrent(ctx, qtx, intent, child)
	if currentErr != nil {
		return currentErr
	}
	if !current || !finalizationParentIdentityCurrent(intent, child, parent) {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	comment, createErr := qtx.CreateExternalPRFinalizationComment(ctx, db.CreateExternalPRFinalizationCommentParams{
		AuthorType:      "system",
		AuthorID:        pgtype.UUID{Valid: true},
		Content:         content,
		Type:            "system",
		ParentID:        pgtype.UUID{Valid: false},
		ID:              parent.ID,
		WorkspaceID:     parent.WorkspaceID,
		FinalizationKey: key,
	})
	if errors.Is(createErr, pgx.ErrNoRows) {
		existing, lookupErr := qtx.GetCommentByFinalizationKey(ctx, key)
		if lookupErr != nil {
			return lookupErr
		}
		intent.ParentCommentID = existing.ID
	} else if createErr != nil {
		return createErr
	} else {
		intent.ParentCommentID = comment.ID
	}
	if err := updateClaimedExternalPRFinalization(ctx, qtx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) finalizeCompletionCommentEvent(ctx context.Context, intentID, leaseToken pgtype.UUID) error {
	tx, qtx, intent, child, parent, err := h.beginExternalPRFinalizationStep(ctx, intentID, leaseToken)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if intent.State == "succeeded" || intent.State == "recorded" || intent.CommentPublished || !intent.ParentCommentID.Valid {
		return tx.Commit(ctx)
	}
	comment, err := qtx.GetComment(ctx, intent.ParentCommentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("finalization comment %s is missing", uuidToString(intent.ParentCommentID))
	}
	if err != nil {
		return err
	}
	if !child.ID.Valid || !parent.ID.Valid || comment.IssueID != parent.ID || !finalizationParentIdentityCurrent(intent, child, parent) {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	current, currentErr := finalizationGenerationCurrent(ctx, qtx, intent, child)
	if currentErr != nil {
		return currentErr
	}
	if !current {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	if h.Bus != nil {
		payload := map[string]any{
			"comment":             commentToResponse(comment, nil, nil),
			"issue_status":        parent.Status,
			"issue_title":         parent.Title,
			"issue_assignee_type": textToPtr(parent.AssigneeType),
			"issue_assignee_id":   uuidToPtr(parent.AssigneeID),
		}
		h.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: uuidToString(intent.WorkspaceID),
			ActorType:   "system",
			Payload:     payload,
			DeliveryKey: externalPRFinalizationDeliveryKey(intent.ID, "comment"),
		})
	}
	intent.CommentPublished = true
	if err := updateClaimedExternalPRFinalization(ctx, qtx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) finalizeCompletionParentWake(ctx context.Context, intentID, leaseToken pgtype.UUID) error {
	tx, qtx, intent, child, parent, err := h.beginExternalPRFinalizationStep(ctx, intentID, leaseToken)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if intent.State == "succeeded" || intent.State == "recorded" || intent.ParentWakeDone {
		return tx.Commit(ctx)
	}
	if !intent.ParentCommentID.Valid {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	comment, err := qtx.GetComment(ctx, intent.ParentCommentID)
	if err != nil {
		return err
	}
	if !parent.ID.Valid || comment.IssueID != parent.ID || !finalizationParentIdentityCurrent(intent, child, parent) {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	current, currentErr := finalizationGenerationCurrent(ctx, qtx, intent, child)
	if currentErr != nil {
		return currentErr
	}
	if !current {
		return h.recordFinalizationAs(ctx, tx, qtx, intent, "recorded")
	}
	if err := h.dispatchParentAssigneeTriggerDurable(ctx, parent, comment.ID); err != nil {
		return err
	}
	intent.ParentWakeDone = true
	intent.State = "succeeded"
	if err := updateClaimedExternalPRFinalization(ctx, qtx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) recordFinalizationAs(ctx context.Context, tx pgx.Tx, qtx *db.Queries, intent db.ExternalPrReconcileFinalization, state string) error {
	intent.State = state
	intent.ParentWakeDone = true
	if err := updateClaimedExternalPRFinalization(ctx, qtx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func finalizationUpdateParams(intent db.ExternalPrReconcileFinalization) db.UpdateExternalPRFinalizationParams {
	return db.UpdateExternalPRFinalizationParams{
		ID:                intent.ID,
		State:             intent.State,
		ParentCommentID:   intent.ParentCommentID,
		ActivityPublished: intent.ActivityPublished,
		IssuePublished:    intent.IssuePublished,
		CommentPublished:  intent.CommentPublished,
		ParentWakeDone:    intent.ParentWakeDone,
		Attempt:           intent.Attempt,
		LastErrorCode:     intent.LastErrorCode,
		LastRedactedError: intent.LastRedactedError,
		LeaseToken:        intent.LeaseToken,
	}
}

func (h *Handler) recordFinalizationError(ctx context.Context, intentID, leaseToken pgtype.UUID, code string) error {
	rows, err := h.Queries.RecordExternalPRFinalizationError(ctx, db.RecordExternalPRFinalizationErrorParams{
		ID:                intentID,
		LeaseToken:        leaseToken,
		LastErrorCode:     pgtype.Text{String: code, Valid: true},
		LastRedactedError: pgtype.Text{String: "external PR finalization failed", Valid: true},
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errExternalPRFinalizationLeaseLost
	}
	current, err := h.Queries.GetExternalPRFinalization(ctx, intentID)
	if err != nil {
		return err
	}
	if current.State != "dead" {
		return nil
	}
	// The reconcile worker owns the linked work lease and is the sole same-tick
	// owner allowed to CAS that work to dead. The finalizer only records its own
	// terminal state; the worker can therefore report typed dead instead of
	// losing its lease after a second writer cleared it.
	return &externalPRFinalizationDeadError{IntentID: intentID, Code: code}
}

// externalPRBarrierFinalizationKey is one durable generation key for the
// parent barrier, not one key per child intent. Any child added to the current
// relevant set changes the generation; adding a later staged child does not
// reopen an already-closed earlier frontier until that child is relevant.
func externalPRBarrierFinalizationKey(parent db.Issue, children []db.Issue, staged bool, closedStage int32) pgtype.Text {
	relevant := make([]string, 0, len(children))
	for _, child := range children {
		if staged && (!child.Stage.Valid || child.Stage.Int32 > closedStage) {
			continue
		}
		relevant = append(relevant, uuidToString(child.ID))
	}
	sort.Strings(relevant)
	mode := "unstaged"
	stage := "implicit"
	if staged {
		mode = "staged"
		stage = strconv.Itoa(int(closedStage))
	}
	seed := "external-pr-barrier:v1:" + uuidToString(parent.ID) + ":" + mode + ":" + stage + ":" + strings.Join(relevant, ",")
	digest := sha256.Sum256([]byte(seed))
	return pgtype.Text{String: "external-pr-barrier:v1:" + uuidToString(parent.ID) + ":" + mode + ":" + stage + ":" + hex.EncodeToString(digest[:]), Valid: true}
}

func (h *Handler) externalPRChildDoneCommentContent(ctx context.Context, parent, completed db.Issue, children []db.Issue, staged bool, closedStage int32) string {
	prefix := h.getIssuePrefix(ctx, completed.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(completed.Number))
	childID := uuidToString(completed.ID)
	title := sanitizeChildTitleForSystemComment(completed.Title)
	parentID := uuidToString(parent.ID)
	mentionPrefix := h.buildParentAssigneeMention(ctx, parent)
	if staged {
		summary, nextStage := stageProgressSummary(children, closedStage, h.terminalChildPredicate(ctx))
		advance := stageAdvanceInstruction(nextStage, parentID)
		return fmt.Sprintf(
			"%sStage %d of this issue is complete — its last sub-issue [%s](mention://issue/%s) — \"%s\" — just finished. Stage progress — %s.%s",
			mentionPrefix, closedStage, identifier, childID, title, summary, advance,
		)
	}
	return fmt.Sprintf(
		"%sAll sub-issues are complete — the last one, [%s](mention://issue/%s) — \"%s\", just finished. Continue the parent: synthesize the children's results and move it forward, or — if nothing remains — run `multica issue status %s in_review` to mark the parent ready for review.",
		mentionPrefix, identifier, childID, title, parentID,
	)
}

func (h *Handler) dispatchParentAssigneeTriggerDurable(ctx context.Context, parent db.Issue, triggerCommentID pgtype.UUID) error {
	hasTask, err := h.Queries.HasAgentTaskForTriggerComment(ctx, triggerCommentID)
	if err != nil {
		return err
	}
	if hasTask || !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return nil
	}
	switch parent.AssigneeType.String {
	case "agent":
		agent, getErr := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parent.AssigneeID, WorkspaceID: parent.WorkspaceID})
		if errors.Is(getErr, pgx.ErrNoRows) || (getErr == nil && (!agent.RuntimeID.Valid || agent.ArchivedAt.Valid)) {
			return nil
		}
		if getErr != nil {
			return getErr
		}
		hasPending, pendingErr := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: parent.ID,
			AgentID: parent.AssigneeID,
			HeadSha: h.TaskService.ResolveIssueReviewSHAParam(ctx, parent.ID),
		})
		if pendingErr != nil {
			return pendingErr
		}
		if hasPending {
			return nil
		}
		_, enqueueErr := h.TaskService.EnqueueTaskForMention(ctx, parent, parent.AssigneeID, triggerCommentID)
		if enqueueErr != nil {
			if exists, checkErr := h.Queries.HasAgentTaskForTriggerComment(ctx, triggerCommentID); checkErr == nil && exists {
				return nil
			}
			return enqueueErr
		}
	case "squad":
		squad, getErr := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: parent.AssigneeID, WorkspaceID: parent.WorkspaceID})
		if errors.Is(getErr, pgx.ErrNoRows) {
			return nil
		}
		if getErr != nil {
			return getErr
		}
		agent, getErr := h.Queries.GetAgent(ctx, squad.LeaderID)
		if errors.Is(getErr, pgx.ErrNoRows) || (getErr == nil && (!agent.RuntimeID.Valid || agent.ArchivedAt.Valid)) {
			return nil
		}
		if getErr != nil {
			return getErr
		}
		hasPending, pendingErr := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: parent.ID,
			AgentID: squad.LeaderID,
			HeadSha: h.TaskService.ResolveIssueReviewSHAParam(ctx, parent.ID),
		})
		if pendingErr != nil {
			return pendingErr
		}
		if hasPending {
			return nil
		}
		_, enqueueErr := h.TaskService.EnqueueTaskForSquadLeader(ctx, parent, squad.LeaderID, squad.ID, triggerCommentID)
		if enqueueErr != nil {
			if exists, checkErr := h.Queries.HasAgentTaskForTriggerComment(ctx, triggerCommentID); checkErr == nil && exists {
				return nil
			}
			return enqueueErr
		}
	}
	return nil
}
