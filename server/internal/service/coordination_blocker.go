package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	CoordinationBlockerPageLimit = 100
	CoordinationBlockerCapacity  = 1000
	CoordinationBlockerSchemaV1  = 1

	CoordinationBlockerReasonWaitingOnIssue       = "waiting_on_issue"
	CoordinationBlockerResolutionNoLongerBlocking = "no_longer_blocking"
	CoordinationBlockerResolutionSuperseded       = "superseded"

	coordinationBlockerCursorV1       = 1
	coordinationRecordPhaseCreate     = "create"
	coordinationRecordPhaseResolution = "resolution"
)

type CoordinationEvidenceRef struct {
	Kind string
	ID   pgtype.UUID
}

type AppendBlockerInput struct {
	ScopeID           pgtype.UUID
	ExpectedRevision  int64
	DownstreamIssueID pgtype.UUID
	UpstreamIssueID   pgtype.UUID
	DependencyID      pgtype.UUID
	SchemaVersion     int32
	ReasonCode        string
	EvidenceRefs      []CoordinationEvidenceRef
	IdempotencyKey    string
}

type ResolveBlockerInput struct {
	ScopeID          pgtype.UUID
	BlockerID        pgtype.UUID
	ExpectedRevision int64
	SchemaVersion    int32
	ResolutionCode   string
	EvidenceRefs     []CoordinationEvidenceRef
	IdempotencyKey   string
}

type Blocker struct {
	ID                     pgtype.UUID
	WorkspaceID            pgtype.UUID
	CoordinationScopeID    pgtype.UUID
	Kind                   string
	SchemaVersion          int32
	Status                 string
	RootIssueID            pgtype.UUID
	DownstreamIssueID      pgtype.UUID
	UpstreamIssueID        pgtype.UUID
	DependencyID           pgtype.UUID
	ReasonCode             string
	ResolutionCode         string
	CreateEvidenceRefs     []CoordinationEvidenceRef
	ResolutionEvidenceRefs []CoordinationEvidenceRef
	CreatedByType          string
	CreatedByID            pgtype.UUID
	CreatedTaskID          pgtype.UUID
	CreatedAt              time.Time
	ResolvedByType         string
	ResolvedByID           pgtype.UUID
	ResolvedTaskID         pgtype.UUID
	ResolvedAt             time.Time
}

type BlockerMutationResult struct {
	Blocker       Blocker
	ScopeRevision int64
	Receipt       Receipt
	Outcome       string
	Changed       bool
}

type BlockerPage struct {
	Blockers      []Blocker
	ScopeRevision int64
	StatusFilter  string
	NextCursor    string
}

func (s *CoordinationService) AppendBlocker(ctx context.Context, actor CoordinationActor, input AppendBlockerInput) (BlockerMutationResult, error) {
	if err := validateActor(actor); err != nil {
		return BlockerMutationResult{}, err
	}
	refs, err := validateAppendBlockerInput(input)
	if err != nil {
		return BlockerMutationResult{}, err
	}
	input.EvidenceRefs = refs
	requestHash, err := AppendBlockerRequestHash(actor, input)
	if err != nil {
		return BlockerMutationResult{}, coordinationErr(CoordinationInvalidPayload, "invalid blocker request hash input", err)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (BlockerMutationResult, error) {
		scope, err := s.loadDependencyScope(ctx, qtx, actor, input.ScopeID)
		if err != nil {
			return BlockerMutationResult{}, err
		}
		if err := s.revalidateDependencyEndpoints(ctx, qtx, actor, scope, input.DownstreamIssueID, input.UpstreamIssueID); err != nil {
			return BlockerMutationResult{}, err
		}
		if err := validateBlockerEvidenceIssues(ctx, qtx, actor.WorkspaceID, refs); err != nil {
			return BlockerMutationResult{}, err
		}
		if input.DependencyID.Valid {
			if _, err := loadBlockerDependency(ctx, qtx, actor.WorkspaceID, input, true); err != nil {
				return BlockerMutationResult{}, err
			}
		}

		existingReceipt, err := qtx.GetCoordinationReceiptByIdempotencyKey(ctx, db.GetCoordinationReceiptByIdempotencyKeyParams{
			WorkspaceID: actor.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		})
		if err == nil {
			if !blockerReceiptMatches(existingReceipt, actor, requestHash, CoordinationOperationAppendBlocker) ||
				!uuidEqual(existingReceipt.CoordinationScopeID, input.ScopeID) {
				return BlockerMutationResult{}, coordinationErr(CoordinationIdempotencyConflict, "idempotency key was already used for a different coordination request", nil)
			}
			saved, revision, changed, err := replayBlockerReceipt(ctx, qtx, existingReceipt, input.ScopeID)
			if err != nil {
				return BlockerMutationResult{}, err
			}
			if !uuidEqual(saved.DownstreamIssueID, input.DownstreamIssueID) || !uuidEqual(saved.UpstreamIssueID, input.UpstreamIssueID) {
				return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "saved blocker result is inconsistent", nil)
			}
			return BlockerMutationResult{Blocker: saved, ScopeRevision: revision, Receipt: receiptFromRow(existingReceipt), Outcome: CoordinationOutcomeReplay, Changed: changed}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not load coordination receipt", err)
		}

		lockedScope, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: actor.WorkspaceID, ID: input.ScopeID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return BlockerMutationResult{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
			}
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not lock coordination scope", err)
		}
		if lockedScope.State != "active" || lockedScope.ScopeKind != "root" {
			return BlockerMutationResult{}, coordinationErr(CoordinationNotFound, "coordination scope is not active", nil)
		}
		if lockedScope.Revision != input.ExpectedRevision {
			return BlockerMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
		}
		if input.DependencyID.Valid {
			if _, err := loadBlockerDependency(ctx, qtx, actor.WorkspaceID, input, true); err != nil {
				return BlockerMutationResult{}, err
			}
		}
		count, err := qtx.CountOpenCoordinationRecordsByScope(ctx, db.CountOpenCoordinationRecordsByScopeParams{
			WorkspaceID: actor.WorkspaceID, CoordinationScopeID: input.ScopeID,
		})
		if err != nil {
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not count open coordination blockers", err)
		}
		if count >= CoordinationBlockerCapacity {
			return BlockerMutationResult{}, coordinationErr(CoordinationCapacityExceeded, "coordination blocker capacity was reached", nil)
		}

		recordID, err := newPgUUID()
		if err != nil {
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not generate coordination blocker id", err)
		}
		row, err := qtx.CreateCoordinationRecord(ctx, db.CreateCoordinationRecordParams{
			ID: recordID, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: input.ScopeID,
			RootIssueID: scope.RootIssueID, DownstreamIssueID: input.DownstreamIssueID, UpstreamIssueID: input.UpstreamIssueID,
			DependencyID: input.DependencyID, CreatedByType: actor.ActorType, CreatedByID: actor.ActorID, CreatedTaskID: actor.TaskID,
		})
		if err != nil {
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not create coordination blocker", err)
		}
		if err := insertBlockerEvidenceRefs(ctx, qtx, actor.WorkspaceID, input.ScopeID, recordID, coordinationRecordPhaseCreate, refs); err != nil {
			return BlockerMutationResult{}, err
		}
		updated, err := qtx.IncrementCoordinationScopeRevisionCAS(ctx, db.IncrementCoordinationScopeRevisionCASParams{
			WorkspaceID: actor.WorkspaceID, ID: input.ScopeID, ExpectedRevision: input.ExpectedRevision,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return BlockerMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", err)
			}
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not advance coordination scope revision", err)
		}
		blocker := blockerFromRow(row, refs, nil)
		receipt, err := saveBlockerReceipt(ctx, qtx, actor, input.ScopeID, input.IdempotencyKey, requestHash,
			CoordinationOperationAppendBlocker, blocker, input.ExpectedRevision, updated.Revision, true)
		if err != nil {
			return BlockerMutationResult{}, err
		}
		return BlockerMutationResult{Blocker: blocker, ScopeRevision: updated.Revision, Receipt: receipt, Outcome: CoordinationOutcomeCreated, Changed: true}, nil
	})
}

func (s *CoordinationService) ResolveBlocker(ctx context.Context, actor CoordinationActor, input ResolveBlockerInput) (BlockerMutationResult, error) {
	if err := validateActor(actor); err != nil {
		return BlockerMutationResult{}, err
	}
	refs, err := validateResolveBlockerInput(input)
	if err != nil {
		return BlockerMutationResult{}, err
	}
	input.EvidenceRefs = refs
	requestHash, err := ResolveBlockerRequestHash(actor, input)
	if err != nil {
		return BlockerMutationResult{}, coordinationErr(CoordinationInvalidPayload, "invalid blocker resolution request hash input", err)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (BlockerMutationResult, error) {
		scope, err := s.loadDependencyScope(ctx, qtx, actor, input.ScopeID)
		if err != nil {
			return BlockerMutationResult{}, err
		}
		current, err := qtx.GetCoordinationRecordByID(ctx, db.GetCoordinationRecordByIDParams{WorkspaceID: actor.WorkspaceID, ID: input.BlockerID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return BlockerMutationResult{}, coordinationErr(CoordinationNotFound, "coordination blocker not found", err)
			}
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not load coordination blocker", err)
		}
		if !uuidEqual(current.CoordinationScopeID, input.ScopeID) {
			return BlockerMutationResult{}, coordinationErr(CoordinationNotFound, "coordination blocker not found", nil)
		}
		if err := s.revalidateDependencyEndpoints(ctx, qtx, actor, scope, current.DownstreamIssueID, current.UpstreamIssueID); err != nil {
			return BlockerMutationResult{}, err
		}
		if current.DependencyID.Valid {
			if _, err := loadBlockerDependency(ctx, qtx, actor.WorkspaceID, AppendBlockerInput{
				ScopeID: input.ScopeID, DownstreamIssueID: current.DownstreamIssueID,
				UpstreamIssueID: current.UpstreamIssueID, DependencyID: current.DependencyID,
			}, false); err != nil {
				return BlockerMutationResult{}, err
			}
		}
		if err := validateBlockerEvidenceIssues(ctx, qtx, actor.WorkspaceID, refs); err != nil {
			return BlockerMutationResult{}, err
		}

		existingReceipt, err := qtx.GetCoordinationReceiptByIdempotencyKey(ctx, db.GetCoordinationReceiptByIdempotencyKeyParams{
			WorkspaceID: actor.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		})
		if err == nil {
			if !blockerReceiptMatches(existingReceipt, actor, requestHash, CoordinationOperationResolveBlocker) ||
				!uuidEqual(existingReceipt.CoordinationScopeID, input.ScopeID) || !uuidEqual(existingReceipt.ResourceID, input.BlockerID) {
				return BlockerMutationResult{}, coordinationErr(CoordinationIdempotencyConflict, "idempotency key was already used for a different coordination request", nil)
			}
			saved, revision, changed, err := replayBlockerReceipt(ctx, qtx, existingReceipt, input.ScopeID)
			if err != nil {
				return BlockerMutationResult{}, err
			}
			return BlockerMutationResult{Blocker: saved, ScopeRevision: revision, Receipt: receiptFromRow(existingReceipt), Outcome: CoordinationOutcomeReplay, Changed: changed}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not load coordination receipt", err)
		}

		lockedScope, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: actor.WorkspaceID, ID: input.ScopeID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return BlockerMutationResult{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
			}
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not lock coordination scope", err)
		}
		if lockedScope.Revision != input.ExpectedRevision {
			return BlockerMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
		}
		locked, err := qtx.LockCoordinationRecord(ctx, db.LockCoordinationRecordParams{
			WorkspaceID: actor.WorkspaceID, CoordinationScopeID: input.ScopeID, ID: input.BlockerID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return BlockerMutationResult{}, coordinationErr(CoordinationNotFound, "coordination blocker not found", err)
			}
			return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not lock coordination blocker", err)
		}
		createRefs, err := loadBlockerEvidenceRefs(ctx, qtx, actor.WorkspaceID, input.BlockerID, coordinationRecordPhaseCreate)
		if err != nil {
			return BlockerMutationResult{}, err
		}
		resolutionRefs, err := loadBlockerEvidenceRefs(ctx, qtx, actor.WorkspaceID, input.BlockerID, coordinationRecordPhaseResolution)
		if err != nil {
			return BlockerMutationResult{}, err
		}

		row := locked
		changed := false
		outcome := CoordinationOutcomeNoop
		revisionAfter := lockedScope.Revision
		if locked.Status == "open" {
			row, err = qtx.ResolveCoordinationRecord(ctx, db.ResolveCoordinationRecordParams{
				ResolutionCode: pgtype.Text{String: input.ResolutionCode, Valid: true},
				ResolvedByType: pgtype.Text{String: actor.ActorType, Valid: true}, ResolvedByID: actor.ActorID,
				ResolvedTaskID: actor.TaskID, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: input.ScopeID, ID: input.BlockerID,
			})
			if err != nil {
				return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not resolve coordination blocker", err)
			}
			if err := insertBlockerEvidenceRefs(ctx, qtx, actor.WorkspaceID, input.ScopeID, input.BlockerID, coordinationRecordPhaseResolution, refs); err != nil {
				return BlockerMutationResult{}, err
			}
			resolutionRefs = refs
			updated, err := qtx.IncrementCoordinationScopeRevisionCAS(ctx, db.IncrementCoordinationScopeRevisionCASParams{
				WorkspaceID: actor.WorkspaceID, ID: input.ScopeID, ExpectedRevision: input.ExpectedRevision,
			})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return BlockerMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", err)
				}
				return BlockerMutationResult{}, coordinationErr(CoordinationInternal, "could not advance coordination scope revision", err)
			}
			revisionAfter = updated.Revision
			changed = true
			outcome = CoordinationOutcomeResolved
		}
		blocker := blockerFromRow(row, createRefs, resolutionRefs)
		receipt, err := saveBlockerReceipt(ctx, qtx, actor, input.ScopeID, input.IdempotencyKey, requestHash,
			CoordinationOperationResolveBlocker, blocker, input.ExpectedRevision, revisionAfter, changed)
		if err != nil {
			return BlockerMutationResult{}, err
		}
		return BlockerMutationResult{Blocker: blocker, ScopeRevision: revisionAfter, Receipt: receipt, Outcome: outcome, Changed: changed}, nil
	})
}

func (s *CoordinationService) ListBlockers(ctx context.Context, actor CoordinationActor, scopeID pgtype.UUID, status, cursor string, limit int) (BlockerPage, error) {
	if err := validateActor(actor); err != nil {
		return BlockerPage{}, err
	}
	if !scopeID.Valid {
		return BlockerPage{}, coordinationErr(CoordinationInvalidPayload, "scope id is required", nil)
	}
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" && status != "all" {
		return BlockerPage{}, coordinationErr(CoordinationInvalidPayload, "blocker status must be open, resolved, or all", nil)
	}
	if limit == 0 {
		limit = CoordinationBlockerPageLimit
	}
	if limit < 1 || limit > CoordinationBlockerPageLimit {
		return BlockerPage{}, coordinationErr(CoordinationInvalidPayload, "blocker page limit must be between 1 and 100", nil)
	}
	decoded, err := decodeBlockerCursor(cursor)
	if err != nil {
		return BlockerPage{}, coordinationErr(CoordinationInvalidPayload, "invalid blocker cursor", err)
	}
	if decoded != nil && (!uuidEqual(decoded.WorkspaceID, actor.WorkspaceID) || !uuidEqual(decoded.ScopeID, scopeID) || decoded.Status != status) {
		return BlockerPage{}, coordinationErr(CoordinationInvalidPayload, "blocker cursor does not match the request", nil)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (BlockerPage, error) {
		scope, err := s.loadDependencyScope(ctx, qtx, actor, scopeID)
		if err != nil {
			return BlockerPage{}, err
		}
		var visibleEndpointIssueID pgtype.UUID
		if actor.ActorType == CoordinationActorAgent {
			task, err := qtx.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: actor.TaskID, WorkspaceID: actor.WorkspaceID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return BlockerPage{}, coordinationErr(CoordinationForbidden, "task is not current", err)
				}
				return BlockerPage{}, coordinationErr(CoordinationInternal, "could not load task endpoint authority", err)
			}
			if !uuidEqual(task.AgentID, actor.ActorID) || !task.IssueID.Valid {
				return BlockerPage{}, coordinationErr(CoordinationForbidden, "task endpoint authority is not current", nil)
			}
			visibleEndpointIssueID = task.IssueID
		}
		if decoded != nil && decoded.ScopeRevision != scope.Revision {
			return BlockerPage{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
		}
		params := db.ListCoordinationRecordsByScopeParams{
			WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID, StatusFilter: status,
			VisibleEndpointIssueID: visibleEndpointIssueID, LimitRows: int32(limit + 1),
		}
		if decoded != nil {
			params.CursorCreatedAt = pgtype.Timestamptz{Time: decoded.CreatedAt, Valid: true}
			params.CursorID = decoded.ID
		}
		rows, err := qtx.ListCoordinationRecordsByScope(ctx, params)
		if err != nil {
			return BlockerPage{}, coordinationErr(CoordinationInternal, "could not list coordination blockers", err)
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		items := make([]Blocker, 0, len(rows))
		for _, row := range rows {
			createRefs, err := loadBlockerEvidenceRefs(ctx, qtx, actor.WorkspaceID, row.ID, coordinationRecordPhaseCreate)
			if err != nil {
				return BlockerPage{}, err
			}
			resolutionRefs, err := loadBlockerEvidenceRefs(ctx, qtx, actor.WorkspaceID, row.ID, coordinationRecordPhaseResolution)
			if err != nil {
				return BlockerPage{}, err
			}
			items = append(items, blockerFromRow(row, createRefs, resolutionRefs))
		}
		next := ""
		if hasMore {
			last := items[len(items)-1]
			next, err = encodeBlockerCursor(blockerCursor{
				WorkspaceID: actor.WorkspaceID, ScopeID: scopeID, ScopeRevision: scope.Revision,
				Status: status, CreatedAt: last.CreatedAt, ID: last.ID,
			})
			if err != nil {
				return BlockerPage{}, coordinationErr(CoordinationInternal, "could not encode blocker cursor", err)
			}
		}
		return BlockerPage{Blockers: items, ScopeRevision: scope.Revision, StatusFilter: status, NextCursor: next}, nil
	})
}

func validateAppendBlockerInput(input AppendBlockerInput) ([]CoordinationEvidenceRef, error) {
	if !input.ScopeID.Valid || !input.DownstreamIssueID.Valid || !input.UpstreamIssueID.Valid || input.ExpectedRevision < 0 ||
		input.SchemaVersion != CoordinationBlockerSchemaV1 || input.ReasonCode != CoordinationBlockerReasonWaitingOnIssue {
		return nil, coordinationErr(CoordinationInvalidPayload, "invalid append blocker input", nil)
	}
	if uuidEqual(input.DownstreamIssueID, input.UpstreamIssueID) {
		return nil, coordinationErr(CoordinationInvalidPayload, "blocker endpoints must be different", nil)
	}
	if err := validateCoordinationIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	return normalizeEvidenceRefs(input.EvidenceRefs)
}

func validateResolveBlockerInput(input ResolveBlockerInput) ([]CoordinationEvidenceRef, error) {
	if !input.ScopeID.Valid || !input.BlockerID.Valid || input.ExpectedRevision < 0 || input.SchemaVersion != CoordinationBlockerSchemaV1 ||
		(input.ResolutionCode != CoordinationBlockerResolutionNoLongerBlocking && input.ResolutionCode != CoordinationBlockerResolutionSuperseded) {
		return nil, coordinationErr(CoordinationInvalidPayload, "invalid resolve blocker input", nil)
	}
	if err := validateCoordinationIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	return normalizeEvidenceRefs(input.EvidenceRefs)
}

func normalizeEvidenceRefs(refs []CoordinationEvidenceRef) ([]CoordinationEvidenceRef, error) {
	if len(refs) > 32 {
		return nil, coordinationErr(CoordinationInvalidPayload, "blocker evidence references exceed the limit", nil)
	}
	normalized := append([]CoordinationEvidenceRef(nil), refs...)
	for _, ref := range normalized {
		if ref.Kind != "issue" || !ref.ID.Valid {
			return nil, coordinationErr(CoordinationInvalidPayload, "invalid blocker evidence reference", nil)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		return bytes.Compare(normalized[i].ID.Bytes[:], normalized[j].ID.Bytes[:]) < 0
	})
	for i := 1; i < len(normalized); i++ {
		if normalized[i].Kind == normalized[i-1].Kind && uuidEqual(normalized[i].ID, normalized[i-1].ID) {
			return nil, coordinationErr(CoordinationInvalidPayload, "duplicate blocker evidence reference", nil)
		}
	}
	return normalized, nil
}

func validateBlockerEvidenceIssues(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, refs []CoordinationEvidenceRef) error {
	for _, ref := range refs {
		issue, err := q.GetIssue(ctx, ref.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return coordinationErr(CoordinationNotFound, "blocker evidence issue not found", err)
			}
			return coordinationErr(CoordinationInternal, "could not validate blocker evidence issue", err)
		}
		if !uuidEqual(issue.WorkspaceID, workspaceID) {
			return coordinationErr(CoordinationCrossWorkspace, "blocker evidence issue is outside the workspace", nil)
		}
	}
	return nil
}

func loadBlockerDependency(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, input AppendBlockerInput, requireActive bool) (db.CoordinationDependency, error) {
	row, err := q.GetCoordinationDependencyByID(ctx, db.GetCoordinationDependencyByIDParams{WorkspaceID: workspaceID, ID: input.DependencyID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.CoordinationDependency{}, coordinationErr(CoordinationNotFound, "coordination dependency not found", err)
		}
		return db.CoordinationDependency{}, coordinationErr(CoordinationInternal, "could not load coordination dependency", err)
	}
	if !uuidEqual(row.CoordinationScopeID, input.ScopeID) {
		return db.CoordinationDependency{}, coordinationErr(CoordinationDependencyScopeConflict, "coordination dependency belongs to another scope", nil)
	}
	if !uuidEqual(row.DownstreamIssueID, input.DownstreamIssueID) || !uuidEqual(row.UpstreamIssueID, input.UpstreamIssueID) {
		return db.CoordinationDependency{}, coordinationErr(CoordinationInvalidPayload, "coordination dependency endpoints do not match the blocker", nil)
	}
	if requireActive && row.ResolvedAt.Valid {
		return db.CoordinationDependency{}, coordinationErr(CoordinationInvalidPayload, "coordination dependency is resolved", nil)
	}
	return row, nil
}

func insertBlockerEvidenceRefs(ctx context.Context, q *db.Queries, workspaceID, scopeID, recordID pgtype.UUID, phase string, refs []CoordinationEvidenceRef) error {
	for index, ref := range refs {
		id, err := newPgUUID()
		if err != nil {
			return coordinationErr(CoordinationInternal, "could not generate blocker evidence reference id", err)
		}
		_, err = q.InsertCoordinationRecordIssueRef(ctx, db.InsertCoordinationRecordIssueRefParams{
			ID: id, WorkspaceID: workspaceID, CoordinationScopeID: scopeID, RecordID: recordID,
			Phase: phase, IssueID: ref.ID, Position: int32(index),
		})
		if err != nil {
			return coordinationErr(CoordinationInternal, "could not save blocker evidence reference", err)
		}
	}
	return nil
}

func loadBlockerEvidenceRefs(ctx context.Context, q *db.Queries, workspaceID, recordID pgtype.UUID, phase string) ([]CoordinationEvidenceRef, error) {
	rows, err := q.ListCoordinationRecordIssueRefs(ctx, db.ListCoordinationRecordIssueRefsParams{WorkspaceID: workspaceID, RecordID: recordID, Phase: phase})
	if err != nil {
		return nil, coordinationErr(CoordinationInternal, "could not load blocker evidence references", err)
	}
	refs := make([]CoordinationEvidenceRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, CoordinationEvidenceRef{Kind: "issue", ID: row.IssueID})
	}
	return refs, nil
}

func AppendBlockerCanonicalJSON(actor CoordinationActor, input AppendBlockerInput) ([]byte, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	refs, err := validateAppendBlockerInput(input)
	if err != nil {
		return nil, err
	}
	dependency := "null"
	if input.DependencyID.Valid {
		dependency = fmt.Sprintf("%q", strings.ToLower(util.UUIDToString(input.DependencyID)))
	}
	request := fmt.Sprintf(`{"dependency_id":%s,"downstream_issue_id":%q,"expected_revision":%q,"payload":{"evidence_refs":%s,"reason_code":%q},"schema_version":1,"scope_id":%q,"upstream_issue_id":%q}`,
		dependency, strings.ToLower(util.UUIDToString(input.DownstreamIssueID)), fmt.Sprintf("%d", input.ExpectedRevision),
		evidenceRefsCanonicalJSON(refs), input.ReasonCode, strings.ToLower(util.UUIDToString(input.ScopeID)), strings.ToLower(util.UUIDToString(input.UpstreamIssueID)))
	return dependencyCanonicalJSON(actor, CoordinationOperationAppendBlocker, request), nil
}

func ResolveBlockerCanonicalJSON(actor CoordinationActor, input ResolveBlockerInput) ([]byte, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	refs, err := validateResolveBlockerInput(input)
	if err != nil {
		return nil, err
	}
	request := fmt.Sprintf(`{"expected_revision":%q,"record_id":%q,"resolution":{"evidence_refs":%s,"resolution_code":%q},"schema_version":1,"scope_id":%q}`,
		fmt.Sprintf("%d", input.ExpectedRevision), strings.ToLower(util.UUIDToString(input.BlockerID)), evidenceRefsCanonicalJSON(refs),
		input.ResolutionCode, strings.ToLower(util.UUIDToString(input.ScopeID)))
	return dependencyCanonicalJSON(actor, CoordinationOperationResolveBlocker, request), nil
}

func evidenceRefsCanonicalJSON(refs []CoordinationEvidenceRef) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, ref := range refs {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(fmt.Sprintf(`{"id":%q,"kind":%q}`, strings.ToLower(util.UUIDToString(ref.ID)), ref.Kind))
	}
	builder.WriteByte(']')
	return builder.String()
}

func AppendBlockerRequestHash(actor CoordinationActor, input AppendBlockerInput) ([]byte, error) {
	raw, err := AppendBlockerCanonicalJSON(actor, input)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func ResolveBlockerRequestHash(actor CoordinationActor, input ResolveBlockerInput) ([]byte, error) {
	raw, err := ResolveBlockerCanonicalJSON(actor, input)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func blockerReceiptMatches(row db.CoordinationReceipt, actor CoordinationActor, requestHash []byte, operation string) bool {
	if row.Operation != operation || row.ResourceType != CoordinationResourceBlocker || row.ActorType != actor.ActorType || !uuidEqual(row.ActorID, actor.ActorID) {
		return false
	}
	if actor.ActorType == CoordinationActorMember && row.ActorTaskID.Valid {
		return false
	}
	if actor.ActorType == CoordinationActorAgent && !uuidEqual(row.ActorTaskID, actor.TaskID) {
		return false
	}
	return bytes.Equal(row.RequestHash, requestHash)
}

func saveBlockerReceipt(ctx context.Context, q *db.Queries, actor CoordinationActor, scopeID pgtype.UUID, idempotencyKey string,
	requestHash []byte, operation string, blocker Blocker, revisionBefore, revisionAfter int64, changed bool) (Receipt, error) {
	ordinal, err := q.AllocateCoordinationReceiptOrdinal(ctx, db.AllocateCoordinationReceiptOrdinalParams{WorkspaceID: actor.WorkspaceID, ID: scopeID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Receipt{}, coordinationErr(CoordinationCapacityExceeded, "coordination receipt capacity was exhausted", nil)
		}
		return Receipt{}, coordinationErr(CoordinationInternal, "could not allocate coordination receipt ordinal", err)
	}
	snapshot, err := json.Marshal(blockerResultSnapshotDTO{Resource: blockerSnapshot(blocker), ScopeRevision: revisionAfter, Changed: changed})
	if err != nil {
		return Receipt{}, coordinationErr(CoordinationInternal, "could not encode coordination blocker result", err)
	}
	receiptID, err := newPgUUID()
	if err != nil {
		return Receipt{}, coordinationErr(CoordinationInternal, "could not generate coordination receipt id", err)
	}
	row, err := q.InsertCoordinationReceipt(ctx, db.InsertCoordinationReceiptParams{
		ID: receiptID, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID, ReceiptOrdinal: ordinal,
		Operation: operation, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		ResourceType: CoordinationResourceBlocker, ResourceID: blocker.ID, RevisionBefore: revisionBefore,
		RevisionAfter: revisionAfter, ResultSnapshot: snapshot, ActorType: actor.ActorType, ActorID: actor.ActorID, ActorTaskID: actor.TaskID,
	})
	if err != nil {
		return Receipt{}, coordinationErr(CoordinationInternal, "could not save coordination receipt", err)
	}
	return receiptFromRow(row), nil
}

func replayBlockerReceipt(ctx context.Context, q *db.Queries, receipt db.CoordinationReceipt, scopeID pgtype.UUID) (Blocker, int64, bool, error) {
	if !uuidEqual(receipt.CoordinationScopeID, scopeID) || !receipt.ResourceID.Valid {
		return Blocker{}, 0, false, coordinationErr(CoordinationInternal, "saved blocker receipt is inconsistent", nil)
	}
	currentRow, err := q.GetCoordinationRecordByID(ctx, db.GetCoordinationRecordByIDParams{WorkspaceID: receipt.WorkspaceID, ID: receipt.ResourceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Blocker{}, 0, false, coordinationErr(CoordinationNotFound, "saved coordination blocker no longer exists", err)
		}
		return Blocker{}, 0, false, coordinationErr(CoordinationInternal, "could not load saved coordination blocker", err)
	}
	if !uuidEqual(currentRow.CoordinationScopeID, scopeID) {
		return Blocker{}, 0, false, coordinationErr(CoordinationNotFound, "saved coordination blocker no longer exists", nil)
	}
	createRefs, err := loadBlockerEvidenceRefs(ctx, q, receipt.WorkspaceID, receipt.ResourceID, coordinationRecordPhaseCreate)
	if err != nil {
		return Blocker{}, 0, false, err
	}
	resolutionRefs, err := loadBlockerEvidenceRefs(ctx, q, receipt.WorkspaceID, receipt.ResourceID, coordinationRecordPhaseResolution)
	if err != nil {
		return Blocker{}, 0, false, err
	}
	current := blockerFromRow(currentRow, createRefs, resolutionRefs)
	var snapshot blockerResultSnapshotDTO
	decoder := json.NewDecoder(bytes.NewReader(receipt.ResultSnapshot))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Blocker{}, 0, false, coordinationErr(CoordinationInternal, "saved coordination blocker result is invalid", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Blocker{}, 0, false, coordinationErr(CoordinationInternal, "saved coordination blocker result has trailing data", nil)
	}
	saved, err := blockerFromSnapshot(snapshot.Resource)
	if err != nil || !sameBlockerIdentity(saved, current) || snapshot.ScopeRevision != receipt.RevisionAfter ||
		(receipt.Operation == CoordinationOperationResolveBlocker && !sameBlockerResolution(saved, current)) {
		return Blocker{}, 0, false, coordinationErr(CoordinationInternal, "saved coordination blocker result is inconsistent", err)
	}
	return saved, snapshot.ScopeRevision, snapshot.Changed, nil
}

func blockerFromRow(row db.CoordinationRecord, createRefs, resolutionRefs []CoordinationEvidenceRef) Blocker {
	value := Blocker{
		ID: row.ID, WorkspaceID: row.WorkspaceID, CoordinationScopeID: row.CoordinationScopeID,
		Kind: row.Kind, SchemaVersion: row.SchemaVersion, Status: row.Status, RootIssueID: row.RootIssueID,
		DownstreamIssueID: row.DownstreamIssueID, UpstreamIssueID: row.UpstreamIssueID, DependencyID: row.DependencyID,
		ReasonCode: row.ReasonCode, CreateEvidenceRefs: append([]CoordinationEvidenceRef(nil), createRefs...),
		ResolutionEvidenceRefs: append([]CoordinationEvidenceRef(nil), resolutionRefs...),
		CreatedByType:          row.CreatedByType, CreatedByID: row.CreatedByID, CreatedTaskID: row.CreatedTaskID, CreatedAt: row.CreatedAt.Time,
	}
	if row.Status == "resolved" {
		value.ResolutionCode = row.ResolutionCode.String
		value.ResolvedByType = row.ResolvedByType.String
		value.ResolvedByID = row.ResolvedByID
		value.ResolvedTaskID = row.ResolvedTaskID
		value.ResolvedAt = row.ResolvedAt.Time
	}
	return value
}

func sameBlockerIdentity(a, b Blocker) bool {
	if !uuidEqual(a.ID, b.ID) || !uuidEqual(a.WorkspaceID, b.WorkspaceID) || !uuidEqual(a.CoordinationScopeID, b.CoordinationScopeID) ||
		!uuidEqual(a.RootIssueID, b.RootIssueID) || !uuidEqual(a.DownstreamIssueID, b.DownstreamIssueID) || !uuidEqual(a.UpstreamIssueID, b.UpstreamIssueID) ||
		a.DependencyID.Valid != b.DependencyID.Valid || (a.DependencyID.Valid && !uuidEqual(a.DependencyID, b.DependencyID)) ||
		a.Kind != b.Kind || a.SchemaVersion != b.SchemaVersion || a.ReasonCode != b.ReasonCode ||
		a.CreatedByType != b.CreatedByType || !uuidEqual(a.CreatedByID, b.CreatedByID) ||
		a.CreatedTaskID.Valid != b.CreatedTaskID.Valid || (a.CreatedTaskID.Valid && !uuidEqual(a.CreatedTaskID, b.CreatedTaskID)) ||
		!a.CreatedAt.Equal(b.CreatedAt) || len(a.CreateEvidenceRefs) != len(b.CreateEvidenceRefs) {
		return false
	}
	for index := range a.CreateEvidenceRefs {
		if a.CreateEvidenceRefs[index].Kind != b.CreateEvidenceRefs[index].Kind || !uuidEqual(a.CreateEvidenceRefs[index].ID, b.CreateEvidenceRefs[index].ID) {
			return false
		}
	}
	return true
}

func sameBlockerResolution(a, b Blocker) bool {
	if a.Status != "resolved" || b.Status != "resolved" || a.ResolutionCode != b.ResolutionCode ||
		a.ResolvedByType != b.ResolvedByType || !uuidEqual(a.ResolvedByID, b.ResolvedByID) ||
		a.ResolvedTaskID.Valid != b.ResolvedTaskID.Valid || (a.ResolvedTaskID.Valid && !uuidEqual(a.ResolvedTaskID, b.ResolvedTaskID)) ||
		!a.ResolvedAt.Equal(b.ResolvedAt) || len(a.ResolutionEvidenceRefs) != len(b.ResolutionEvidenceRefs) {
		return false
	}
	for index := range a.ResolutionEvidenceRefs {
		if a.ResolutionEvidenceRefs[index].Kind != b.ResolutionEvidenceRefs[index].Kind ||
			!uuidEqual(a.ResolutionEvidenceRefs[index].ID, b.ResolutionEvidenceRefs[index].ID) {
			return false
		}
	}
	return true
}

type blockerActorSnapshotDTO struct {
	Type   string  `json:"type"`
	ID     string  `json:"id"`
	TaskID *string `json:"task_id"`
}

type blockerEvidenceSnapshotDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type blockerSnapshotDTO struct {
	ID                     string                       `json:"id"`
	WorkspaceID            string                       `json:"workspace_id"`
	ScopeID                string                       `json:"scope_id"`
	Kind                   string                       `json:"kind"`
	SchemaVersion          int32                        `json:"schema_version"`
	Status                 string                       `json:"status"`
	RootIssueID            string                       `json:"root_issue_id"`
	DownstreamIssueID      string                       `json:"downstream_issue_id"`
	UpstreamIssueID        string                       `json:"upstream_issue_id"`
	DependencyID           *string                      `json:"dependency_id"`
	ReasonCode             string                       `json:"reason_code"`
	ResolutionCode         *string                      `json:"resolution_code"`
	CreateEvidenceRefs     []blockerEvidenceSnapshotDTO `json:"create_evidence_refs"`
	ResolutionEvidenceRefs []blockerEvidenceSnapshotDTO `json:"resolution_evidence_refs"`
	CreatedBy              blockerActorSnapshotDTO      `json:"created_by"`
	ResolvedBy             *blockerActorSnapshotDTO     `json:"resolved_by"`
	CreatedAt              string                       `json:"created_at"`
	ResolvedAt             *string                      `json:"resolved_at"`
}

type blockerResultSnapshotDTO struct {
	Resource      blockerSnapshotDTO `json:"resource"`
	ScopeRevision int64              `json:"scope_revision"`
	Changed       bool               `json:"changed"`
}

func blockerSnapshot(value Blocker) blockerSnapshotDTO {
	dto := blockerSnapshotDTO{
		ID: util.UUIDToString(value.ID), WorkspaceID: util.UUIDToString(value.WorkspaceID), ScopeID: util.UUIDToString(value.CoordinationScopeID),
		Kind: value.Kind, SchemaVersion: value.SchemaVersion, Status: value.Status, RootIssueID: util.UUIDToString(value.RootIssueID),
		DownstreamIssueID: util.UUIDToString(value.DownstreamIssueID), UpstreamIssueID: util.UUIDToString(value.UpstreamIssueID),
		ReasonCode: value.ReasonCode, CreateEvidenceRefs: evidenceSnapshot(value.CreateEvidenceRefs),
		ResolutionEvidenceRefs: evidenceSnapshot(value.ResolutionEvidenceRefs), CreatedBy: blockerActorSnapshot(value.CreatedByType, value.CreatedByID, value.CreatedTaskID),
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if value.DependencyID.Valid {
		id := util.UUIDToString(value.DependencyID)
		dto.DependencyID = &id
	}
	if value.Status == "resolved" {
		code := value.ResolutionCode
		dto.ResolutionCode = &code
		actor := blockerActorSnapshot(value.ResolvedByType, value.ResolvedByID, value.ResolvedTaskID)
		dto.ResolvedBy = &actor
		resolvedAt := value.ResolvedAt.UTC().Format(time.RFC3339Nano)
		dto.ResolvedAt = &resolvedAt
	}
	return dto
}

func evidenceSnapshot(refs []CoordinationEvidenceRef) []blockerEvidenceSnapshotDTO {
	result := make([]blockerEvidenceSnapshotDTO, 0, len(refs))
	for _, ref := range refs {
		result = append(result, blockerEvidenceSnapshotDTO{Kind: ref.Kind, ID: util.UUIDToString(ref.ID)})
	}
	return result
}

func blockerActorSnapshot(actorType string, actorID, taskID pgtype.UUID) blockerActorSnapshotDTO {
	dto := blockerActorSnapshotDTO{Type: actorType, ID: util.UUIDToString(actorID)}
	if taskID.Valid {
		task := util.UUIDToString(taskID)
		dto.TaskID = &task
	}
	return dto
}

func blockerFromSnapshot(dto blockerSnapshotDTO) (Blocker, error) {
	parse := func(raw string) (pgtype.UUID, error) { return util.ParseUUID(raw) }
	id, err := parse(dto.ID)
	if err != nil {
		return Blocker{}, err
	}
	workspaceID, err := parse(dto.WorkspaceID)
	if err != nil {
		return Blocker{}, err
	}
	scopeID, err := parse(dto.ScopeID)
	if err != nil {
		return Blocker{}, err
	}
	rootID, err := parse(dto.RootIssueID)
	if err != nil {
		return Blocker{}, err
	}
	downstreamID, err := parse(dto.DownstreamIssueID)
	if err != nil {
		return Blocker{}, err
	}
	upstreamID, err := parse(dto.UpstreamIssueID)
	if err != nil || uuidEqual(downstreamID, upstreamID) {
		return Blocker{}, errors.New("invalid blocker endpoints")
	}
	var dependencyID pgtype.UUID
	if dto.DependencyID != nil {
		dependencyID, err = parse(*dto.DependencyID)
		if err != nil {
			return Blocker{}, err
		}
	}
	createdByID, createdTaskID, err := parseBlockerSnapshotActor(dto.CreatedBy)
	if err != nil {
		return Blocker{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, dto.CreatedAt)
	if err != nil {
		return Blocker{}, err
	}
	createRefs, err := parseEvidenceSnapshot(dto.CreateEvidenceRefs)
	if err != nil {
		return Blocker{}, err
	}
	resolutionRefs, err := parseEvidenceSnapshot(dto.ResolutionEvidenceRefs)
	if err != nil {
		return Blocker{}, err
	}
	value := Blocker{
		ID: id, WorkspaceID: workspaceID, CoordinationScopeID: scopeID, Kind: dto.Kind, SchemaVersion: dto.SchemaVersion,
		Status: dto.Status, RootIssueID: rootID, DownstreamIssueID: downstreamID, UpstreamIssueID: upstreamID,
		DependencyID: dependencyID, ReasonCode: dto.ReasonCode, CreateEvidenceRefs: createRefs,
		ResolutionEvidenceRefs: resolutionRefs, CreatedByType: dto.CreatedBy.Type, CreatedByID: createdByID,
		CreatedTaskID: createdTaskID, CreatedAt: createdAt,
	}
	if value.Kind != "blocker" || value.SchemaVersion != CoordinationBlockerSchemaV1 || value.ReasonCode != CoordinationBlockerReasonWaitingOnIssue {
		return Blocker{}, errors.New("invalid blocker snapshot kind")
	}
	switch dto.Status {
	case "open":
		if dto.ResolutionCode != nil || dto.ResolvedBy != nil || dto.ResolvedAt != nil || len(resolutionRefs) != 0 {
			return Blocker{}, errors.New("invalid open blocker snapshot")
		}
	case "resolved":
		if dto.ResolutionCode == nil || dto.ResolvedBy == nil || dto.ResolvedAt == nil {
			return Blocker{}, errors.New("invalid resolved blocker snapshot")
		}
		if *dto.ResolutionCode != CoordinationBlockerResolutionNoLongerBlocking && *dto.ResolutionCode != CoordinationBlockerResolutionSuperseded {
			return Blocker{}, errors.New("invalid blocker resolution code")
		}
		resolvedByID, resolvedTaskID, err := parseBlockerSnapshotActor(*dto.ResolvedBy)
		if err != nil {
			return Blocker{}, err
		}
		resolvedAt, err := time.Parse(time.RFC3339Nano, *dto.ResolvedAt)
		if err != nil {
			return Blocker{}, err
		}
		value.ResolutionCode = *dto.ResolutionCode
		value.ResolvedByType = dto.ResolvedBy.Type
		value.ResolvedByID = resolvedByID
		value.ResolvedTaskID = resolvedTaskID
		value.ResolvedAt = resolvedAt
	default:
		return Blocker{}, errors.New("invalid blocker snapshot status")
	}
	return value, nil
}

func parseBlockerSnapshotActor(dto blockerActorSnapshotDTO) (pgtype.UUID, pgtype.UUID, error) {
	actorID, err := util.ParseUUID(dto.ID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	var taskID pgtype.UUID
	if dto.TaskID != nil {
		taskID, err = util.ParseUUID(*dto.TaskID)
		if err != nil {
			return pgtype.UUID{}, pgtype.UUID{}, err
		}
	}
	if (dto.Type == CoordinationActorMember && !taskID.Valid) || (dto.Type == CoordinationActorAgent && taskID.Valid) {
		return actorID, taskID, nil
	}
	return pgtype.UUID{}, pgtype.UUID{}, errors.New("invalid blocker snapshot actor")
}

func parseEvidenceSnapshot(items []blockerEvidenceSnapshotDTO) ([]CoordinationEvidenceRef, error) {
	refs := make([]CoordinationEvidenceRef, 0, len(items))
	for _, item := range items {
		id, err := util.ParseUUID(item.ID)
		if err != nil {
			return nil, err
		}
		refs = append(refs, CoordinationEvidenceRef{Kind: item.Kind, ID: id})
	}
	return normalizeEvidenceRefs(refs)
}

type blockerCursor struct {
	WorkspaceID   pgtype.UUID
	ScopeID       pgtype.UUID
	ScopeRevision int64
	Status        string
	CreatedAt     time.Time
	ID            pgtype.UUID
}

type blockerCursorDTO struct {
	Version       int    `json:"v"`
	WorkspaceID   string `json:"workspace_id"`
	ScopeID       string `json:"scope_id"`
	ScopeRevision int64  `json:"scope_revision"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	ID            string `json:"id"`
}

func encodeBlockerCursor(value blockerCursor) (string, error) {
	if !value.WorkspaceID.Valid || !value.ScopeID.Valid || !value.ID.Valid || value.ScopeRevision < 0 || value.CreatedAt.IsZero() ||
		(value.Status != "open" && value.Status != "resolved" && value.Status != "all") {
		return "", errors.New("invalid blocker cursor state")
	}
	raw, err := json.Marshal(blockerCursorDTO{
		Version: coordinationBlockerCursorV1, WorkspaceID: util.UUIDToString(value.WorkspaceID), ScopeID: util.UUIDToString(value.ScopeID),
		ScopeRevision: value.ScopeRevision, Status: value.Status, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), ID: util.UUIDToString(value.ID),
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeBlockerCursor(raw string) (*blockerCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 3000 {
		return nil, errors.New("blocker cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > 2048 {
		return nil, errors.New("invalid blocker cursor encoding")
	}
	var dto blockerCursorDTO
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid trailing blocker cursor data")
	}
	if dto.Version != coordinationBlockerCursorV1 || dto.ScopeRevision < 0 ||
		(dto.Status != "open" && dto.Status != "resolved" && dto.Status != "all") {
		return nil, errors.New("unsupported blocker cursor")
	}
	workspaceID, err := util.ParseUUID(dto.WorkspaceID)
	if err != nil {
		return nil, err
	}
	scopeID, err := util.ParseUUID(dto.ScopeID)
	if err != nil {
		return nil, err
	}
	id, err := util.ParseUUID(dto.ID)
	if err != nil {
		return nil, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, dto.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &blockerCursor{WorkspaceID: workspaceID, ScopeID: scopeID, ScopeRevision: dto.ScopeRevision, Status: dto.Status, CreatedAt: createdAt, ID: id}, nil
}
