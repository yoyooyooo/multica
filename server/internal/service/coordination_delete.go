package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const coordinationDeleteCleanupTimeout = 5 * time.Second

type deleteHandleState uint8

const (
	deleteStateAcquiring deleteHandleState = iota
	deleteStateReady
	deleteStateFinishing
	deleteStateReleased
	deleteStateDiscarded
)

type IssueDeletionMode string

const (
	IssueDeletionSingle IssueDeletionMode = "single"
	IssueDeletionBatch  IssueDeletionMode = "batch"
)

type IssueDeletionOutcome string

const (
	IssueDeletionDeleted            IssueDeletionOutcome = "deleted"
	IssueDeletionSkippedRecoverable IssueDeletionOutcome = "skipped_recoverable"
)

type IssueDeletionPhase string

const (
	IssueDeletionPhaseTaskCancel        IssueDeletionPhase = "task_cancel"
	IssueDeletionPhaseTaskTokenCleanup  IssueDeletionPhase = "task_token_cleanup"
	IssueDeletionPhaseAutopilotFail     IssueDeletionPhase = "autopilot_fail"
	IssueDeletionPhaseAttachmentCensus  IssueDeletionPhase = "attachment_census"
	IssueDeletionPhaseEntityDelete      IssueDeletionPhase = "entity_delete"
	IssueDeletionPhaseSavepointCreate   IssueDeletionPhase = "savepoint_create"
	IssueDeletionPhaseSavepointRollback IssueDeletionPhase = "savepoint_rollback"
	IssueDeletionPhaseSavepointRelease  IssueDeletionPhase = "savepoint_release"
)

type IssueDeletionFailureClass string

const (
	IssueDeletionFailureStatement     IssueDeletionFailureClass = "statement"
	IssueDeletionFailureSerialization IssueDeletionFailureClass = "serialization"
	IssueDeletionFailureDeadlock      IssueDeletionFailureClass = "deadlock"
	IssueDeletionFailureContext       IssueDeletionFailureClass = "context"
	IssueDeletionFailureConnection    IssueDeletionFailureClass = "connection"
	IssueDeletionFailureSavepoint     IssueDeletionFailureClass = "savepoint"
	IssueDeletionFailureRowCount      IssueDeletionFailureClass = "row_count"
	IssueDeletionFailureUnknown       IssueDeletionFailureClass = "unknown"
)

// IssueDeletionFatalError is a sealed failure classification. Its wrapped
// CoordinationError gives handlers one safe coordination_internal envelope.
type IssueDeletionFatalError struct {
	Phase IssueDeletionPhase
	Class IssueDeletionFailureClass
	Err   error
}

func (e *IssueDeletionFatalError) Error() string {
	return fmt.Sprintf("coordination issue deletion failed at %s (%s)", e.Phase, e.Class)
}

func (e *IssueDeletionFatalError) Unwrap() error { return e.Err }

// WorkspaceDeletionFatalError keeps workspace deletion failures typed without
// permitting the batch-only recoverable outcome.
type WorkspaceDeletionFatalError struct {
	Phase IssueDeletionPhase
	Class IssueDeletionFailureClass
	Err   error
}

func (e *WorkspaceDeletionFatalError) Error() string {
	return fmt.Sprintf("coordination workspace deletion failed at %s (%s)", e.Phase, e.Class)
}

func (e *WorkspaceDeletionFatalError) Unwrap() error { return e.Err }

// CancelledTaskEffect is the compact, typed subset required by the existing
// post-commit metrics, reconciliation, runtime-hint, and event seams.
type CancelledTaskEffect struct {
	ID             pgtype.UUID
	WorkspaceID    pgtype.UUID
	AgentID        pgtype.UUID
	IssueID        pgtype.UUID
	RuntimeID      pgtype.UUID
	ChatSessionID  pgtype.UUID
	AutopilotRunID pgtype.UUID
	Status         string
	MetricsSource  string
	RuntimeMode    string
	Provider       string
	DispatchedAt   pgtype.Timestamptz
	StartedAt      pgtype.Timestamptz
	CompletedAt    pgtype.Timestamptz
	CreatedAt      pgtype.Timestamptz
	Attempt        int32
}

// IssueDeletionEffects contains only immutable, compact values needed after a
// successful Finish(true). It deliberately excludes full database rows and
// arbitrary JSON.
type IssueDeletionEffects struct {
	IssueID          pgtype.UUID
	WorkspaceID      pgtype.UUID
	CancelledTasks   []CancelledTaskEffect
	AffectedAgentIDs []pgtype.UUID
	AttachmentURLs   []string
}

// WorkspaceDeletionEffects contains the post-commit refs for one workspace.
type WorkspaceDeletionEffects struct {
	WorkspaceID      pgtype.UUID
	AffectedUserIDs  []pgtype.UUID
	CancelledTasks   []CancelledTaskEffect
	AffectedAgentIDs []pgtype.UUID
}

type IssueDeletionResult struct {
	Outcome  IssueDeletionOutcome
	Phase    IssueDeletionPhase
	SafeCode string
	Effects  IssueDeletionEffects
}

type coordinationDeleteLifecycle struct {
	conn     *pgxpool.Conn
	tx       pgx.Tx
	qtx      *db.Queries
	lockKey  int32
	lockHeld bool
	state    deleteHandleState

	// Per-handle seams keep terminal-state tests deterministic without exposing
	// the pinned connection or qtx outside this package.
	sessionLock   func(context.Context, int32) error
	sessionUnlock func(context.Context, int32) (bool, error)
	releaseConn   func()
	discardConn   func()
}

// IssueDeletionHandle owns one pinned connection and one transaction. Batch
// targets are deleted one at a time through savepoints inside this handle.
type IssueDeletionHandle struct {
	mu            sync.Mutex
	lifecycle     coordinationDeleteLifecycle
	mode          IssueDeletionMode
	issues        map[[16]byte]db.Issue
	targetIDs     []pgtype.UUID
	attempted     map[[16]byte]struct{}
	deleted       int
	failed        bool
	savepointExec func(context.Context, string, ...any) (pgconn.CommandTag, error)
	phaseFault    func(context.Context, IssueDeletionPhase, db.Issue) error
}

// WorkspaceDeletionHandle owns the pinned connection and transaction for one
// workspace deletion.
type WorkspaceDeletionHandle struct {
	mu        sync.Mutex
	lifecycle coordinationDeleteLifecycle
	workspace db.Workspace
	members   []db.Member
	deleted   bool
	failed    bool
}

func (s *CoordinationService) AcquireIssueDeletion(ctx context.Context, actor CoordinationActor, workspaceID pgtype.UUID, parsedIssueIDs []pgtype.UUID, mode IssueDeletionMode) (_ *IssueDeletionHandle, err error) {
	if s == nil || s.Pool == nil || s.Queries == nil {
		return nil, coordinationErr(CoordinationInternal, "coordination deletion service is not configured", nil)
	}
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if !uuidEqual(actor.WorkspaceID, workspaceID) {
		return nil, coordinationErr(CoordinationCrossWorkspace, "workspace does not match the actor", nil)
	}
	if mode != IssueDeletionSingle && mode != IssueDeletionBatch {
		return nil, coordinationErr(CoordinationInvalidPayload, "invalid issue deletion mode", nil)
	}
	issueIDs, err := canonicalIssueIDs(parsedIssueIDs)
	if err != nil {
		return nil, err
	}
	if mode == IssueDeletionSingle && len(issueIDs) != 1 {
		return nil, coordinationErr(CoordinationInvalidPayload, "single issue deletion requires one issue", nil)
	}

	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not acquire deletion connection", err)
	}
	h := &IssueDeletionHandle{
		lifecycle: coordinationDeleteLifecycle{conn: conn, state: deleteStateAcquiring},
		mode:      mode,
		issues:    make(map[[16]byte]db.Issue),
		attempted: make(map[[16]byte]struct{}),
	}
	ready := false
	defer func() {
		if !ready {
			h.lifecycle.cleanupAcquire()
		}
	}()

	if err := h.lifecycle.acquireSessionLock(ctx, workspaceID); err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not start deletion transaction", err)
	}
	h.lifecycle.tx = tx
	h.lifecycle.qtx = db.New(tx)

	if err := s.revalidateActor(ctx, h.lifecycle.qtx, actor, pgtype.UUID{}); err != nil {
		return nil, err
	}
	issues, err := h.lifecycle.qtx.LockIssuesForCoordinationDelete(ctx, db.LockIssuesForCoordinationDeleteParams{
		WorkspaceID: workspaceID,
		IssueIds:    issueIDs,
	})
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not lock issues for deletion", err)
	}
	if mode == IssueDeletionSingle && len(issues) != 1 {
		return nil, coordinationErr(CoordinationNotFound, "issue not found", nil)
	}
	for _, issue := range issues {
		if actor.ActorType == CoordinationActorAgent {
			actual, rootErr := h.lifecycle.qtx.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: workspaceID, IssueID: issue.ID})
			if rootErr != nil {
				return nil, coordinationErr(CoordinationInternal, "could not validate issue root for deletion", rootErr)
			}
			if actual.Status != "ok" || !actual.RootIssueID.Valid {
				return nil, coordinationErr(CoordinationForbidden, "issue authority is not current", nil)
			}
			if err := s.revalidateActor(ctx, h.lifecycle.qtx, actor, actual.RootIssueID); err != nil {
				return nil, err
			}
		}
		h.issues[issue.ID.Bytes] = issue
		h.targetIDs = append(h.targetIDs, issue.ID)
	}
	if len(h.targetIDs) > 0 {
		count, err := h.lifecycle.qtx.CountCoordinationScopesByRootIssues(ctx, db.CountCoordinationScopesByRootIssuesParams{WorkspaceID: workspaceID, IssueIds: h.targetIDs})
		if err != nil {
			return nil, coordinationErr(CoordinationInternal, "could not check coordination delete guard", err)
		}
		if count > 0 {
			return nil, coordinationErr(CoordinationDeleteBlocked, "issue deletion is blocked by an active coordination scope", nil)
		}
	}
	if s.deletionReadyBoundary != nil {
		s.deletionReadyBoundary()
	}
	h.lifecycle.state = deleteStateReady
	ready = true
	return h, nil
}

func (s *CoordinationService) AcquireWorkspaceDeletion(ctx context.Context, actor CoordinationActor, workspaceID pgtype.UUID) (_ *WorkspaceDeletionHandle, err error) {
	if s == nil || s.Pool == nil || s.Queries == nil {
		return nil, coordinationErr(CoordinationInternal, "coordination deletion service is not configured", nil)
	}
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if !uuidEqual(actor.WorkspaceID, workspaceID) {
		return nil, coordinationErr(CoordinationCrossWorkspace, "workspace does not match the actor", nil)
	}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not acquire deletion connection", err)
	}
	h := &WorkspaceDeletionHandle{lifecycle: coordinationDeleteLifecycle{conn: conn, state: deleteStateAcquiring}}
	ready := false
	defer func() {
		if !ready {
			h.lifecycle.cleanupAcquire()
		}
	}()
	if err := h.lifecycle.acquireSessionLock(ctx, workspaceID); err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not start deletion transaction", err)
	}
	h.lifecycle.tx = tx
	h.lifecycle.qtx = db.New(tx)
	workspace, err := h.lifecycle.qtx.LockWorkspaceForCoordinationDelete(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, coordinationErr(CoordinationNotFound, "workspace not found", err)
		}
		return nil, coordinationErr(CoordinationInternal, "could not lock workspace for deletion", err)
	}
	if _, err := h.lifecycle.qtx.LockChatSessionsByWorkspace(ctx, workspaceID); err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not lock workspace chat sessions", err)
	}
	if actor.ActorType != CoordinationActorMember {
		return nil, coordinationErr(CoordinationForbidden, "workspace deletion requires a member owner", nil)
	}
	member, err := h.lifecycle.qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: actor.ActorID, WorkspaceID: workspaceID})
	if err != nil || member.Role != "owner" {
		return nil, coordinationErr(CoordinationForbidden, "workspace owner authority is not current", err)
	}
	count, err := h.lifecycle.qtx.CountCoordinationScopesByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not check workspace coordination guard", err)
	}
	if count > 0 {
		return nil, coordinationErr(CoordinationDeleteBlocked, "workspace deletion is blocked by an active coordination scope", nil)
	}
	members, err := h.lifecycle.qtx.ListMembers(ctx, workspaceID)
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not capture workspace members", err)
	}
	h.workspace = workspace
	h.members = append([]db.Member(nil), members...)
	if s.deletionReadyBoundary != nil {
		s.deletionReadyBoundary()
	}
	h.lifecycle.state = deleteStateReady
	ready = true
	return h, nil
}

// TargetIssueIDs returns the immutable, workspace-scoped actual targets loaded
// and locked by Acquire. Missing, inaccessible, and foreign IDs are absent.
func (h *IssueDeletionHandle) TargetIssueIDs() []pgtype.UUID {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pgtype.UUID(nil), h.targetIDs...)
}

// Delete advances exactly one locked target. Batch mode wraps the sealed target
// phases in a savepoint so only entity_delete/23503 can be skipped safely.
func (h *IssueDeletionHandle) Delete(ctx context.Context, issueID pgtype.UUID) (IssueDeletionResult, error) {
	if h == nil {
		return IssueDeletionResult{}, coordinationErr(CoordinationInternal, "issue deletion handle is nil", nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lifecycle.state != deleteStateReady || h.failed {
		return IssueDeletionResult{}, coordinationErr(CoordinationInternal, "issue deletion handle is not ready", nil)
	}
	issue, ok := h.issues[issueID.Bytes]
	if !issueID.Valid || !ok {
		return h.failIssueDeletion(coordinationErr(CoordinationInternal, "issue is not in the locked deletion set", nil))
	}
	if _, duplicate := h.attempted[issueID.Bytes]; duplicate {
		return h.failIssueDeletion(coordinationErr(CoordinationInternal, "issue deletion was already attempted", nil))
	}
	h.attempted[issueID.Bytes] = struct{}{}

	if h.mode == IssueDeletionBatch {
		if _, err := h.execSavepoint(ctx, "SAVEPOINT coordination_issue_delete_target"); err != nil {
			return h.failIssueDeletion(newIssueDeletionFatal(IssueDeletionPhaseSavepointCreate, classifyIssueDeletionFailure(err, true), err))
		}
	}

	effects, phase, err := h.deleteIssuePhases(ctx, issue)
	if err != nil {
		if h.mode == IssueDeletionBatch && phase == IssueDeletionPhaseEntityDelete && sqlState(err) == "23503" {
			if _, rollbackErr := h.execSavepoint(ctx, "ROLLBACK TO SAVEPOINT coordination_issue_delete_target"); rollbackErr != nil {
				return h.failIssueDeletion(newIssueDeletionFatal(IssueDeletionPhaseSavepointRollback, classifyIssueDeletionFailure(rollbackErr, true), rollbackErr))
			}
			if _, releaseErr := h.execSavepoint(ctx, "RELEASE SAVEPOINT coordination_issue_delete_target"); releaseErr != nil {
				return h.failIssueDeletion(newIssueDeletionFatal(IssueDeletionPhaseSavepointRelease, classifyIssueDeletionFailure(releaseErr, true), releaseErr))
			}
			return IssueDeletionResult{Outcome: IssueDeletionSkippedRecoverable, Phase: IssueDeletionPhaseEntityDelete, SafeCode: "target_restricted"}, nil
		}
		return h.failIssueDeletion(newIssueDeletionFatal(phase, classifyIssueDeletionFailure(err, false), err))
	}
	if h.mode == IssueDeletionBatch {
		if _, err := h.execSavepoint(ctx, "RELEASE SAVEPOINT coordination_issue_delete_target"); err != nil {
			return h.failIssueDeletion(newIssueDeletionFatal(IssueDeletionPhaseSavepointRelease, classifyIssueDeletionFailure(err, true), err))
		}
	}
	h.deleted++
	return IssueDeletionResult{Outcome: IssueDeletionDeleted, Effects: effects}, nil
}

func (h *IssueDeletionHandle) failIssueDeletion(err error) (IssueDeletionResult, error) {
	h.failed = true
	return IssueDeletionResult{}, err
}

func (h *IssueDeletionHandle) execSavepoint(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if h.savepointExec != nil {
		return h.savepointExec(ctx, sql, arguments...)
	}
	return h.lifecycle.tx.Exec(ctx, sql, arguments...)
}

func (h *IssueDeletionHandle) deleteIssuePhases(ctx context.Context, issue db.Issue) (IssueDeletionEffects, IssueDeletionPhase, error) {
	cancelled, err := h.lifecycle.qtx.CancelAgentTasksByIssue(ctx, issue.ID)
	if err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseTaskCancel, err
	}
	if err := h.issuePhaseFault(ctx, IssueDeletionPhaseTaskCancel, issue); err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseTaskCancel, err
	}
	taskEffects, agentIDs, err := captureCancelledTaskEffects(ctx, h.lifecycle.qtx, issue.WorkspaceID, cancelled)
	if err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseTaskCancel, err
	}
	for _, task := range cancelled {
		if err := h.lifecycle.qtx.DeleteTaskTokensByTask(ctx, task.ID); err != nil {
			return IssueDeletionEffects{}, IssueDeletionPhaseTaskTokenCleanup, err
		}
		if err := h.issuePhaseFault(ctx, IssueDeletionPhaseTaskTokenCleanup, issue); err != nil {
			return IssueDeletionEffects{}, IssueDeletionPhaseTaskTokenCleanup, err
		}
	}
	if err := h.lifecycle.qtx.FailAutopilotRunsByIssue(ctx, issue.ID); err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseAutopilotFail, err
	}
	if err := h.issuePhaseFault(ctx, IssueDeletionPhaseAutopilotFail, issue); err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseAutopilotFail, err
	}
	attachmentURLs, err := h.lifecycle.qtx.ListAttachmentURLsByIssueOrComments(ctx, issue.ID)
	if err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseAttachmentCensus, err
	}
	if err := h.issuePhaseFault(ctx, IssueDeletionPhaseAttachmentCensus, issue); err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseAttachmentCensus, err
	}
	rows, err := h.lifecycle.qtx.DeleteIssue(ctx, db.DeleteIssueParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseEntityDelete, err
	}
	if rows != 1 {
		return IssueDeletionEffects{}, IssueDeletionPhaseEntityDelete, errDeletionRowCount
	}
	if err := h.issuePhaseFault(ctx, IssueDeletionPhaseEntityDelete, issue); err != nil {
		return IssueDeletionEffects{}, IssueDeletionPhaseEntityDelete, err
	}
	return IssueDeletionEffects{
		IssueID:          issue.ID,
		WorkspaceID:      issue.WorkspaceID,
		CancelledTasks:   taskEffects,
		AffectedAgentIDs: agentIDs,
		AttachmentURLs:   append([]string(nil), attachmentURLs...),
	}, "", nil
}

func (h *IssueDeletionHandle) issuePhaseFault(ctx context.Context, phase IssueDeletionPhase, issue db.Issue) error {
	if h.phaseFault == nil {
		return nil
	}
	return h.phaseFault(ctx, phase, issue)
}

func (h *WorkspaceDeletionHandle) Delete(ctx context.Context) (WorkspaceDeletionEffects, error) {
	if h == nil {
		return WorkspaceDeletionEffects{}, coordinationErr(CoordinationInternal, "workspace deletion handle is nil", nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lifecycle.state != deleteStateReady || h.deleted || h.failed {
		return WorkspaceDeletionEffects{}, coordinationErr(CoordinationInternal, "workspace deletion handle is not ready", nil)
	}
	h.deleted = true
	cancelled, err := h.lifecycle.qtx.CancelAgentTasksByWorkspaceForCoordination(ctx, h.workspace.ID)
	if err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseTaskCancel, classifyIssueDeletionFailure(err, false), err))
	}
	taskEffects, agentIDs, err := captureCancelledTaskEffects(ctx, h.lifecycle.qtx, h.workspace.ID, cancelled)
	if err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseTaskCancel, classifyIssueDeletionFailure(err, false), err))
	}
	if err := h.lifecycle.qtx.DeleteTaskTokensByWorkspaceForCoordination(ctx, h.workspace.ID); err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseTaskTokenCleanup, classifyIssueDeletionFailure(err, false), err))
	}
	if err := h.lifecycle.qtx.FailAutopilotRunsByWorkspaceForCoordination(ctx, h.workspace.ID); err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseAutopilotFail, classifyIssueDeletionFailure(err, false), err))
	}
	if err := h.lifecycle.qtx.DeleteChatPinnedAgentsByWorkspace(ctx, h.workspace.ID); err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseEntityDelete, classifyIssueDeletionFailure(err, false), err))
	}
	memberRows, err := h.lifecycle.qtx.DeleteMembersByWorkspaceForCoordination(ctx, h.workspace.ID)
	if err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseEntityDelete, classifyIssueDeletionFailure(err, false), err))
	}
	if memberRows != int64(len(h.members)) {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseEntityDelete, IssueDeletionFailureRowCount, errDeletionRowCount))
	}
	rows, err := h.lifecycle.qtx.DeleteWorkspace(ctx, h.workspace.ID)
	if err != nil {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseEntityDelete, classifyIssueDeletionFailure(err, false), err))
	}
	if rows != 1 {
		return h.failWorkspaceDeletion(newWorkspaceDeletionFatal(IssueDeletionPhaseEntityDelete, IssueDeletionFailureRowCount, errDeletionRowCount))
	}
	userIDs := make([]pgtype.UUID, 0, len(h.members))
	for _, member := range h.members {
		userIDs = append(userIDs, member.UserID)
	}
	return WorkspaceDeletionEffects{
		WorkspaceID:      h.workspace.ID,
		AffectedUserIDs:  userIDs,
		CancelledTasks:   taskEffects,
		AffectedAgentIDs: agentIDs,
	}, nil
}

func (h *WorkspaceDeletionHandle) failWorkspaceDeletion(err error) (WorkspaceDeletionEffects, error) {
	h.failed = true
	return WorkspaceDeletionEffects{}, err
}

func (h *IssueDeletionHandle) Finish(commit bool) error {
	if h == nil {
		return coordinationErr(CoordinationInternal, "issue deletion handle is nil", nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if commit && (h.failed || (h.mode == IssueDeletionSingle && h.deleted != 1)) {
		finishErr := h.lifecycle.finish(false)
		if finishErr != nil {
			return finishErr
		}
		return coordinationErr(CoordinationInternal, "issue deletion cannot commit after an incomplete or failed target", nil)
	}
	return h.lifecycle.finish(commit)
}

func (h *WorkspaceDeletionHandle) Finish(commit bool) error {
	if h == nil {
		return coordinationErr(CoordinationInternal, "workspace deletion handle is nil", nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if commit && (h.failed || !h.deleted) {
		finishErr := h.lifecycle.finish(false)
		if finishErr != nil {
			return finishErr
		}
		return coordinationErr(CoordinationInternal, "workspace deletion cannot commit after an incomplete or failed delete", nil)
	}
	return h.lifecycle.finish(commit)
}

func (l *coordinationDeleteLifecycle) acquireSessionLock(ctx context.Context, workspaceID pgtype.UUID) error {
	key, err := CoordinationWorkspaceAdvisoryKey(workspaceID)
	if err != nil {
		l.discard()
		return coordinationErr(CoordinationInvalidPayload, "invalid workspace id", err)
	}
	l.lockKey = key
	var lockErr error
	if l.sessionLock != nil {
		lockErr = l.sessionLock(ctx, key)
	} else {
		lockErr = db.New(l.conn).CoordinationAdvisorySessionLock(ctx, db.CoordinationAdvisorySessionLockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: key})
	}
	if lockErr != nil {
		// The server may have acquired the lock before the transport error became
		// visible. Never return this connection to the pool.
		l.discard()
		return coordinationErr(CoordinationInternal, "could not acquire workspace deletion lock", lockErr)
	}
	l.lockHeld = true
	return nil
}

// finish is the one terminal boundary. It never inherits request cancellation,
// never propagates a panic, and always leaves the connection released or
// discarded before returning.
func (l *coordinationDeleteLifecycle) finish(commit bool) (err error) {
	if l.state != deleteStateReady || l.tx == nil {
		return coordinationErr(CoordinationInternal, "deletion handle cannot finish in its current state", nil)
	}
	l.state = deleteStateFinishing
	defer func() {
		if recovered := recover(); recovered != nil {
			l.discard()
			err = coordinationErr(CoordinationInternal, "deletion finalization failed", fmt.Errorf("finish panic: %v", recovered))
		}
	}()

	cleanupCtx, cancel := context.WithTimeout(context.Background(), coordinationDeleteCleanupTimeout)
	defer cancel()
	if commit {
		commitErr := l.tx.Commit(cleanupCtx)
		if commitErr != nil {
			if !definiteCommitFailure(commitErr) {
				l.discard()
				return coordinationErr(CoordinationInternal, "database deletion outcome is unknown", commitErr)
			}
			rollbackErr := l.tx.Rollback(cleanupCtx)
			if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				l.discard()
				return coordinationErr(CoordinationInternal, "could not verify failed deletion commit", rollbackErr)
			}
			if unlockErr := l.finishSessionLock(cleanupCtx); unlockErr != nil {
				return coordinationErr(CoordinationInternal, "could not verify deletion unlock after failed commit", unlockErr)
			}
			return coordinationErr(CoordinationInternal, "database deletion commit failed", commitErr)
		}
	} else {
		if rollbackErr := l.tx.Rollback(cleanupCtx); rollbackErr != nil {
			l.discard()
			return coordinationErr(CoordinationInternal, "could not verify deletion rollback", rollbackErr)
		}
	}
	if unlockErr := l.finishSessionLock(cleanupCtx); unlockErr != nil {
		return coordinationErr(CoordinationInternal, "could not verify deletion unlock", unlockErr)
	}
	return nil
}

func (l *coordinationDeleteLifecycle) finishSessionLock(ctx context.Context) error {
	if l.conn == nil && l.sessionUnlock == nil && l.releaseConn == nil {
		if l.state == deleteStateDiscarded {
			return errors.New("deletion connection was discarded")
		}
		l.state = deleteStateDiscarded
		return errors.New("deletion connection is unavailable")
	}
	if l.lockHeld {
		var unlocked bool
		var err error
		if l.sessionUnlock != nil {
			unlocked, err = l.sessionUnlock(ctx, l.lockKey)
		} else {
			unlocked, err = db.New(l.conn).CoordinationAdvisorySessionUnlock(ctx, db.CoordinationAdvisorySessionUnlockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: l.lockKey})
		}
		if err != nil || !unlocked {
			l.discard()
			if err != nil {
				return err
			}
			return errors.New("workspace deletion lock was not held")
		}
		l.lockHeld = false
	}
	if l.releaseConn != nil {
		l.releaseConn()
	} else {
		l.conn.Release()
	}
	l.conn = nil
	l.state = deleteStateReleased
	return nil
}

func (l *coordinationDeleteLifecycle) cleanupAcquire() {
	defer func() {
		if recover() != nil {
			l.discard()
		}
	}()
	if l.conn == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), coordinationDeleteCleanupTimeout)
	defer cancel()
	if l.tx != nil {
		if err := l.tx.Rollback(cleanupCtx); err != nil {
			l.discard()
			return
		}
	}
	if err := l.finishSessionLock(cleanupCtx); err != nil {
		l.discard()
	}
}

func (l *coordinationDeleteLifecycle) discard() {
	conn := l.conn
	l.conn = nil
	l.lockHeld = false
	l.state = deleteStateDiscarded
	if l.discardConn != nil {
		l.discardConn()
		return
	}
	if conn == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		raw := conn.Hijack()
		ctx, cancel := context.WithTimeout(context.Background(), coordinationDeleteCleanupTimeout)
		defer cancel()
		_ = raw.Close(ctx)
	}()
}

var errDeletionRowCount = errors.New("deletion row-count invariant failed")

func newIssueDeletionFatal(phase IssueDeletionPhase, class IssueDeletionFailureClass, cause error) error {
	return &IssueDeletionFatalError{
		Phase: phase,
		Class: class,
		Err:   coordinationErr(CoordinationInternal, "coordination deletion failed", cause),
	}
}

func newWorkspaceDeletionFatal(phase IssueDeletionPhase, class IssueDeletionFailureClass, cause error) error {
	return &WorkspaceDeletionFatalError{
		Phase: phase,
		Class: class,
		Err:   coordinationErr(CoordinationInternal, "coordination deletion failed", cause),
	}
}

func classifyIssueDeletionFailure(err error, savepoint bool) IssueDeletionFailureClass {
	if errors.Is(err, errDeletionRowCount) || errors.Is(err, pgx.ErrNoRows) {
		return IssueDeletionFailureRowCount
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return IssueDeletionFailureContext
	}
	switch sqlState(err) {
	case "40001":
		return IssueDeletionFailureSerialization
	case "40P01":
		return IssueDeletionFailureDeadlock
	case "":
		if pgconn.SafeToRetry(err) {
			return IssueDeletionFailureConnection
		}
		if savepoint {
			return IssueDeletionFailureSavepoint
		}
		return IssueDeletionFailureUnknown
	default:
		return IssueDeletionFailureStatement
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func definiteCommitFailure(err error) bool {
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr)
}

func captureCancelledTaskEffects(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, tasks []db.AgentTaskQueue) ([]CancelledTaskEffect, []pgtype.UUID, error) {
	out := make([]CancelledTaskEffect, 0, len(tasks))
	for _, task := range tasks {
		runtimeMode := ""
		provider := ""
		if task.AgentID.Valid {
			agent, err := queries.GetAgent(ctx, task.AgentID)
			switch {
			case err == nil:
				runtimeMode = agent.RuntimeMode
			case !errors.Is(err, pgx.ErrNoRows):
				return nil, nil, err
			}
		}
		if task.RuntimeID.Valid {
			runtime, runtimeErr := queries.GetAgentRuntime(ctx, task.RuntimeID)
			switch {
			case runtimeErr == nil:
				runtimeMode = runtime.RuntimeMode
				provider = runtime.Provider
			case !errors.Is(runtimeErr, pgx.ErrNoRows):
				return nil, nil, runtimeErr
			}
		}
		out = append(out, CancelledTaskEffect{
			ID:             task.ID,
			WorkspaceID:    workspaceID,
			AgentID:        task.AgentID,
			IssueID:        task.IssueID,
			RuntimeID:      task.RuntimeID,
			ChatSessionID:  task.ChatSessionID,
			AutopilotRunID: task.AutopilotRunID,
			Status:         task.Status,
			MetricsSource:  coordinationTaskMetricsSource(task),
			RuntimeMode:    runtimeMode,
			Provider:       provider,
			DispatchedAt:   task.DispatchedAt,
			StartedAt:      task.StartedAt,
			CompletedAt:    task.CompletedAt,
			CreatedAt:      task.CreatedAt,
			Attempt:        task.Attempt,
		})
	}
	return out, affectedAgentIDs(tasks), nil
}

func coordinationTaskMetricsSource(task db.AgentTaskQueue) string {
	switch {
	case task.ChatSessionID.Valid:
		return "chat"
	case task.IssueID.Valid && task.AutopilotRunID.Valid:
		return "autopilot_issue"
	case task.IssueID.Valid:
		return "issue"
	case task.AutopilotRunID.Valid:
		return "autopilot"
	default:
		if _, ok := parseQuickCreateTaskContext(task); ok {
			return "quick_create"
		}
		return "other"
	}
}

func affectedAgentIDs(tasks []db.AgentTaskQueue) []pgtype.UUID {
	seen := make(map[[16]byte]struct{}, len(tasks))
	out := make([]pgtype.UUID, 0, len(tasks))
	for _, task := range tasks {
		if !task.AgentID.Valid {
			continue
		}
		if _, ok := seen[task.AgentID.Bytes]; ok {
			continue
		}
		seen[task.AgentID.Bytes] = struct{}{}
		out = append(out, task.AgentID)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Bytes[:], out[j].Bytes[:]) < 0 })
	return out
}

func cancelledTaskRows(effects []CancelledTaskEffect) []db.AgentTaskQueue {
	out := make([]db.AgentTaskQueue, 0, len(effects))
	for _, effect := range effects {
		out = append(out, db.AgentTaskQueue{
			ID:             effect.ID,
			AgentID:        effect.AgentID,
			IssueID:        effect.IssueID,
			RuntimeID:      effect.RuntimeID,
			ChatSessionID:  effect.ChatSessionID,
			AutopilotRunID: effect.AutopilotRunID,
			Status:         effect.Status,
			DispatchedAt:   effect.DispatchedAt,
			StartedAt:      effect.StartedAt,
			CompletedAt:    effect.CompletedAt,
			CreatedAt:      effect.CreatedAt,
			Attempt:        effect.Attempt,
		})
	}
	return out
}

func canonicalIssueIDs(ids []pgtype.UUID) ([]pgtype.UUID, error) {
	seen := make(map[[16]byte]struct{}, len(ids))
	out := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		if !id.Valid {
			return nil, coordinationErr(CoordinationInvalidPayload, "issue id must be a UUID", nil)
		}
		if _, ok := seen[id.Bytes]; ok {
			continue
		}
		seen[id.Bytes] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Bytes[:], out[j].Bytes[:]) < 0 })
	return out, nil
}

func (s deleteHandleState) String() string {
	switch s {
	case deleteStateAcquiring:
		return "acquiring"
	case deleteStateReady:
		return "ready"
	case deleteStateFinishing:
		return "finishing"
	case deleteStateReleased:
		return "released"
	case deleteStateDiscarded:
		return "discarded"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}
