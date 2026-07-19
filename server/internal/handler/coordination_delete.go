package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type issueDeletionHandle interface {
	TargetIssueIDs() []pgtype.UUID
	Delete(context.Context, pgtype.UUID) (service.IssueDeletionResult, error)
	Finish(bool) error
}

type workspaceDeletionHandle interface {
	Delete(context.Context) (service.WorkspaceDeletionEffects, error)
	Finish(bool) error
}

func (h *Handler) performIssueDeletion(ctx context.Context, actor service.CoordinationActor, workspaceID pgtype.UUID, issueIDs []pgtype.UUID, mode service.IssueDeletionMode) (deleted int, effects []service.IssueDeletionEffects, err error) {
	var handle issueDeletionHandle
	if h.acquireIssueDeletion != nil {
		handle, err = h.acquireIssueDeletion(ctx, actor, workspaceID, issueIDs, mode)
	} else {
		handle, err = h.CoordinationService.AcquireIssueDeletion(ctx, actor, workspaceID, issueIDs, mode)
	}
	if err != nil {
		return 0, nil, err
	}
	return runIssueDeletionHandle(ctx, handle, nil)
}

// runIssueDeletionHandle owns the caller-side at-most-once guard. afterDelete
// is an unexported pure test seam for a panic after Delete has returned but
// before explicit Finish starts.
func runIssueDeletionHandle(ctx context.Context, handle issueDeletionHandle, afterDelete func()) (deleted int, effects []service.IssueDeletionEffects, err error) {
	finishStarted := false
	defer func() {
		if !finishStarted {
			_ = handle.Finish(false)
		}
	}()

	abort := func(primary error) error {
		finishStarted = true
		if finishErr := handle.Finish(false); finishErr != nil {
			return finishErr
		}
		return primary
	}
	for _, issueID := range handle.TargetIssueIDs() {
		result, deleteErr := handle.Delete(ctx, issueID)
		if deleteErr != nil {
			return 0, nil, abort(deleteErr)
		}
		if afterDelete != nil {
			afterDelete()
		}
		switch result.Outcome {
		case service.IssueDeletionDeleted:
			deleted++
			effects = append(effects, result.Effects)
		case service.IssueDeletionSkippedRecoverable:
			// The handle has already rolled this target back to and released its
			// savepoint. It deliberately returns no effects for skipped targets.
		default:
			return 0, nil, abort(errors.New("invalid issue deletion outcome"))
		}
	}
	finishStarted = true
	if finishErr := handle.Finish(true); finishErr != nil {
		return 0, nil, finishErr
	}
	return deleted, effects, nil
}

func (h *Handler) performWorkspaceDeletion(ctx context.Context, actor service.CoordinationActor, workspaceID pgtype.UUID) (effects service.WorkspaceDeletionEffects, err error) {
	var handle workspaceDeletionHandle
	if h.acquireWorkspaceDelete != nil {
		handle, err = h.acquireWorkspaceDelete(ctx, actor, workspaceID)
	} else {
		handle, err = h.CoordinationService.AcquireWorkspaceDeletion(ctx, actor, workspaceID)
	}
	if err != nil {
		return service.WorkspaceDeletionEffects{}, err
	}
	finishStarted := false
	defer func() {
		if !finishStarted {
			_ = handle.Finish(false)
		}
	}()

	effects, err = handle.Delete(ctx)
	if err != nil {
		finishStarted = true
		if finishErr := handle.Finish(false); finishErr != nil {
			return service.WorkspaceDeletionEffects{}, finishErr
		}
		return service.WorkspaceDeletionEffects{}, err
	}
	finishStarted = true
	if err := handle.Finish(true); err != nil {
		return service.WorkspaceDeletionEffects{}, err
	}
	return effects, nil
}

// applyIssueDeletionEffects runs only after Finish(true) has committed and made
// the pinned connection terminal. Current adapters are best effort and do not
// expose a uniform error result; V1 records attempts, not delivery.
func (h *Handler) applyIssueDeletionEffects(ctx context.Context, actor service.CoordinationActor, effects []service.IssueDeletionEffects) {
	for _, effect := range effects {
		h.TaskService.BroadcastCoordinationCancelledTasks(ctx, effect.CancelledTasks, effect.AffectedAgentIDs)
		h.deleteS3Objects(ctx, effect.AttachmentURLs)
		workspaceID := uuidToString(effect.WorkspaceID)
		h.publish(protocol.EventIssueDeleted, workspaceID, actor.ActorType, uuidToString(actor.ActorID), map[string]any{"issue_id": uuidToString(effect.IssueID)})
	}
}

func (h *Handler) applyWorkspaceDeletionEffects(ctx context.Context, actor service.CoordinationActor, effects service.WorkspaceDeletionEffects) {
	workspaceID := uuidToString(effects.WorkspaceID)
	h.TaskService.BroadcastCoordinationCancelledTasks(ctx, effects.CancelledTasks, effects.AffectedAgentIDs)
	userIDs := make([]string, 0, len(effects.AffectedUserIDs))
	for _, userID := range effects.AffectedUserIDs {
		value := uuidToString(userID)
		h.MembershipCache.Invalidate(ctx, value, workspaceID)
		userIDs = append(userIDs, value)
	}
	h.publish(protocol.EventWorkspaceDeleted, workspaceID, actor.ActorType, uuidToString(actor.ActorID), map[string]any{"workspace_id": workspaceID})
	h.notifyDaemonWorkspacesChanged(userIDs...)
}
