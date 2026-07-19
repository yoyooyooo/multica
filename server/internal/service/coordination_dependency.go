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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	CoordinationDependencyPageLimit = 100
	CoordinationDependencyCapacity  = 1000
	coordinationDependencyCursorV1  = 1
)

type AddDependencyInput struct {
	ScopeID           pgtype.UUID
	ExpectedRevision  int64
	DownstreamIssueID pgtype.UUID
	UpstreamIssueID   pgtype.UUID
	IdempotencyKey    string
}

type ResolveDependencyInput struct {
	ScopeID          pgtype.UUID
	DependencyID     pgtype.UUID
	ExpectedRevision int64
	IdempotencyKey   string
}

type Dependency struct {
	ID                  pgtype.UUID
	WorkspaceID         pgtype.UUID
	CoordinationScopeID pgtype.UUID
	DownstreamIssueID   pgtype.UUID
	UpstreamIssueID     pgtype.UUID
	CreatedByType       string
	CreatedByID         pgtype.UUID
	CreatedTaskID       pgtype.UUID
	CreatedAt           time.Time
	ResolvedByType      string
	ResolvedByID        pgtype.UUID
	ResolvedTaskID      pgtype.UUID
	ResolvedAt          time.Time
	Resolved            bool
}

type DependencyMutationResult struct {
	Dependency    Dependency
	ScopeRevision int64
	Receipt       Receipt
	Outcome       string
}

type DependencyPage struct {
	Dependencies  []Dependency
	ScopeRevision int64
	NextCursor    string
}

func (s *CoordinationService) AddDependency(ctx context.Context, actor CoordinationActor, input AddDependencyInput) (DependencyMutationResult, error) {
	if err := validateActor(actor); err != nil {
		return DependencyMutationResult{}, err
	}
	if err := validateAddDependencyInput(input); err != nil {
		return DependencyMutationResult{}, err
	}
	requestHash, err := AddDependencyRequestHash(actor, input)
	if err != nil {
		return DependencyMutationResult{}, coordinationErr(CoordinationInvalidPayload, "invalid dependency request hash input", err)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (DependencyMutationResult, error) {
		scope, err := s.loadDependencyScope(ctx, qtx, actor, input.ScopeID)
		if err != nil {
			return DependencyMutationResult{}, err
		}
		if err := s.revalidateDependencyEndpoints(ctx, qtx, actor, scope, input.DownstreamIssueID, input.UpstreamIssueID); err != nil {
			return DependencyMutationResult{}, err
		}

		existingReceipt, err := qtx.GetCoordinationReceiptByIdempotencyKey(ctx, db.GetCoordinationReceiptByIdempotencyKeyParams{
			WorkspaceID: actor.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		})
		if err == nil {
			if !dependencyReceiptMatches(existingReceipt, actor, requestHash, CoordinationOperationAddDependency) || !uuidEqual(existingReceipt.CoordinationScopeID, input.ScopeID) {
				return DependencyMutationResult{}, coordinationErr(CoordinationIdempotencyConflict, "idempotency key was already used for a different coordination request", nil)
			}
			saved, err := replayDependencyReceipt(ctx, qtx, existingReceipt, input.ScopeID)
			if err != nil {
				return DependencyMutationResult{}, err
			}
			if !uuidEqual(saved.DownstreamIssueID, input.DownstreamIssueID) || !uuidEqual(saved.UpstreamIssueID, input.UpstreamIssueID) {
				return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "saved dependency result is inconsistent", nil)
			}
			return DependencyMutationResult{Dependency: saved, ScopeRevision: existingReceipt.RevisionAfter, Receipt: receiptFromRow(existingReceipt), Outcome: CoordinationOutcomeReplay}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "could not load coordination receipt", err)
		}

		lockedScope, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: actor.WorkspaceID, ID: input.ScopeID})
		if err != nil {
			return DependencyMutationResult{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
		}
		if lockedScope.State != "active" || lockedScope.ScopeKind != "root" || lockedScope.Revision != input.ExpectedRevision {
			if lockedScope.Revision != input.ExpectedRevision {
				return DependencyMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
			}
			return DependencyMutationResult{}, coordinationErr(CoordinationNotFound, "coordination scope is not active", nil)
		}

		row, outcome, changed, err := s.addDependencyAfterLock(ctx, qtx, actor, lockedScope, input)
		if err != nil {
			return DependencyMutationResult{}, err
		}
		revisionAfter := lockedScope.Revision
		if changed {
			updated, err := qtx.IncrementCoordinationScopeRevisionCAS(ctx, db.IncrementCoordinationScopeRevisionCASParams{WorkspaceID: actor.WorkspaceID, ID: input.ScopeID, ExpectedRevision: input.ExpectedRevision})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return DependencyMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", err)
				}
				return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "could not advance coordination scope revision", err)
			}
			revisionAfter = updated.Revision
		}
		dependency := dependencyFromRow(row)
		receipt, err := saveDependencyReceipt(ctx, qtx, actor, input.ScopeID, input.IdempotencyKey, requestHash, CoordinationOperationAddDependency, dependency, input.ExpectedRevision, revisionAfter)
		if err != nil {
			return DependencyMutationResult{}, err
		}
		return DependencyMutationResult{Dependency: dependency, ScopeRevision: revisionAfter, Receipt: receipt, Outcome: outcome}, nil
	})
}

func (s *CoordinationService) addDependencyAfterLock(ctx context.Context, qtx *db.Queries, actor CoordinationActor, scope db.CoordinationScope, input AddDependencyInput) (db.CoordinationDependency, string, bool, error) {
	existing, err := qtx.GetActiveCoordinationDependencyByPair(ctx, db.GetActiveCoordinationDependencyByPairParams{
		WorkspaceID: actor.WorkspaceID, DownstreamIssueID: input.DownstreamIssueID, UpstreamIssueID: input.UpstreamIssueID,
	})
	if err == nil {
		if !uuidEqual(existing.CoordinationScopeID, input.ScopeID) {
			return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationDependencyScopeConflict, "active dependency belongs to another coordination scope", nil)
		}
		return existing, CoordinationOutcomeNoop, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationInternal, "could not load active coordination dependency", err)
	}
	count, err := qtx.CountActiveCoordinationDependenciesByScope(ctx, db.CountActiveCoordinationDependenciesByScopeParams{WorkspaceID: actor.WorkspaceID, CoordinationScopeID: input.ScopeID})
	if err != nil {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationInternal, "could not count active coordination dependencies", err)
	}
	if count >= CoordinationDependencyCapacity {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationCapacityExceeded, "coordination dependency capacity was reached", nil)
	}
	cycle, err := qtx.CoordinationDependencyPathExists(ctx, db.CoordinationDependencyPathExistsParams{
		WorkspaceID: actor.WorkspaceID, StartIssueID: input.UpstreamIssueID, TargetIssueID: input.DownstreamIssueID,
	})
	if err != nil {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationInternal, "could not validate coordination dependency graph", err)
	}
	if cycle {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationCycle, "coordination dependency would create a cycle", nil)
	}
	id, err := newPgUUID()
	if err != nil {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationInternal, "could not generate coordination dependency id", err)
	}
	row, err := qtx.CreateCoordinationDependency(ctx, db.CreateCoordinationDependencyParams{
		ID: id, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scope.ID,
		DownstreamIssueID: input.DownstreamIssueID, UpstreamIssueID: input.UpstreamIssueID,
		CreatedByType: actor.ActorType, CreatedByID: actor.ActorID, CreatedTaskID: actor.TaskID,
	})
	if err != nil {
		return db.CoordinationDependency{}, "", false, coordinationErr(CoordinationInternal, "could not create coordination dependency", err)
	}
	return row, CoordinationOutcomeCreated, true, nil
}

func (s *CoordinationService) ResolveDependency(ctx context.Context, actor CoordinationActor, input ResolveDependencyInput) (DependencyMutationResult, error) {
	if err := validateActor(actor); err != nil {
		return DependencyMutationResult{}, err
	}
	if err := validateResolveDependencyInput(input); err != nil {
		return DependencyMutationResult{}, err
	}
	requestHash, err := ResolveDependencyRequestHash(actor, input)
	if err != nil {
		return DependencyMutationResult{}, coordinationErr(CoordinationInvalidPayload, "invalid dependency request hash input", err)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (DependencyMutationResult, error) {
		scope, err := s.loadDependencyScope(ctx, qtx, actor, input.ScopeID)
		if err != nil {
			return DependencyMutationResult{}, err
		}
		current, err := qtx.GetCoordinationDependencyByID(ctx, db.GetCoordinationDependencyByIDParams{WorkspaceID: actor.WorkspaceID, ID: input.DependencyID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return DependencyMutationResult{}, coordinationErr(CoordinationNotFound, "coordination dependency not found", err)
			}
			return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "could not load coordination dependency", err)
		}
		if !uuidEqual(current.CoordinationScopeID, input.ScopeID) {
			return DependencyMutationResult{}, coordinationErr(CoordinationDependencyScopeConflict, "coordination dependency belongs to another scope", nil)
		}
		if err := s.revalidateDependencyEndpoints(ctx, qtx, actor, scope, current.DownstreamIssueID, current.UpstreamIssueID); err != nil {
			return DependencyMutationResult{}, err
		}

		existingReceipt, err := qtx.GetCoordinationReceiptByIdempotencyKey(ctx, db.GetCoordinationReceiptByIdempotencyKeyParams{
			WorkspaceID: actor.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		})
		if err == nil {
			if !dependencyReceiptMatches(existingReceipt, actor, requestHash, CoordinationOperationResolveDependency) || !uuidEqual(existingReceipt.CoordinationScopeID, input.ScopeID) || !uuidEqual(existingReceipt.ResourceID, input.DependencyID) {
				return DependencyMutationResult{}, coordinationErr(CoordinationIdempotencyConflict, "idempotency key was already used for a different coordination request", nil)
			}
			saved, err := replayDependencyReceipt(ctx, qtx, existingReceipt, input.ScopeID)
			if err != nil {
				return DependencyMutationResult{}, err
			}
			return DependencyMutationResult{Dependency: saved, ScopeRevision: existingReceipt.RevisionAfter, Receipt: receiptFromRow(existingReceipt), Outcome: CoordinationOutcomeReplay}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "could not load coordination receipt", err)
		}

		lockedScope, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: actor.WorkspaceID, ID: input.ScopeID})
		if err != nil {
			return DependencyMutationResult{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
		}
		if lockedScope.Revision != input.ExpectedRevision {
			return DependencyMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
		}
		lockedDependency, err := qtx.LockCoordinationDependency(ctx, db.LockCoordinationDependencyParams{WorkspaceID: actor.WorkspaceID, ID: input.DependencyID})
		if err != nil {
			return DependencyMutationResult{}, coordinationErr(CoordinationNotFound, "coordination dependency not found", err)
		}
		if !uuidEqual(lockedDependency.CoordinationScopeID, input.ScopeID) {
			return DependencyMutationResult{}, coordinationErr(CoordinationDependencyScopeConflict, "coordination dependency belongs to another scope", nil)
		}

		outcome := CoordinationOutcomeNoop
		revisionAfter := lockedScope.Revision
		row := lockedDependency
		if !lockedDependency.ResolvedAt.Valid {
			row, err = qtx.ResolveCoordinationDependency(ctx, db.ResolveCoordinationDependencyParams{
				ResolvedByType: pgtype.Text{String: actor.ActorType, Valid: true}, ResolvedByID: actor.ActorID,
				ResolvedTaskID: actor.TaskID, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: input.ScopeID, ID: input.DependencyID,
			})
			if err != nil {
				return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "could not resolve coordination dependency", err)
			}
			updated, err := qtx.IncrementCoordinationScopeRevisionCAS(ctx, db.IncrementCoordinationScopeRevisionCASParams{WorkspaceID: actor.WorkspaceID, ID: input.ScopeID, ExpectedRevision: input.ExpectedRevision})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return DependencyMutationResult{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", err)
				}
				return DependencyMutationResult{}, coordinationErr(CoordinationInternal, "could not advance coordination scope revision", err)
			}
			revisionAfter = updated.Revision
			outcome = CoordinationOutcomeResolved
		}
		dependency := dependencyFromRow(row)
		receipt, err := saveDependencyReceipt(ctx, qtx, actor, input.ScopeID, input.IdempotencyKey, requestHash, CoordinationOperationResolveDependency, dependency, input.ExpectedRevision, revisionAfter)
		if err != nil {
			return DependencyMutationResult{}, err
		}
		return DependencyMutationResult{Dependency: dependency, ScopeRevision: revisionAfter, Receipt: receipt, Outcome: outcome}, nil
	})
}

func (s *CoordinationService) ListDependencies(ctx context.Context, actor CoordinationActor, scopeID pgtype.UUID, cursor string, limit int) (DependencyPage, error) {
	if err := validateActor(actor); err != nil {
		return DependencyPage{}, err
	}
	if !scopeID.Valid {
		return DependencyPage{}, coordinationErr(CoordinationInvalidPayload, "scope id is required", nil)
	}
	if limit == 0 {
		limit = CoordinationDependencyPageLimit
	}
	if limit < 1 || limit > CoordinationDependencyPageLimit {
		return DependencyPage{}, coordinationErr(CoordinationInvalidPayload, "dependency page limit must be between 1 and 100", nil)
	}
	decoded, err := decodeDependencyCursor(cursor)
	if err != nil {
		return DependencyPage{}, coordinationErr(CoordinationInvalidPayload, "invalid dependency cursor", err)
	}
	if decoded != nil && (!uuidEqual(decoded.WorkspaceID, actor.WorkspaceID) || !uuidEqual(decoded.ScopeID, scopeID)) {
		return DependencyPage{}, coordinationErr(CoordinationInvalidPayload, "dependency cursor does not match the request", nil)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (DependencyPage, error) {
		scope, err := s.loadDependencyScope(ctx, qtx, actor, scopeID)
		if err != nil {
			return DependencyPage{}, err
		}
		var visibleEndpointIssueID pgtype.UUID
		if actor.ActorType == CoordinationActorAgent {
			task, err := qtx.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: actor.TaskID, WorkspaceID: actor.WorkspaceID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return DependencyPage{}, coordinationErr(CoordinationForbidden, "task is not current", err)
				}
				return DependencyPage{}, coordinationErr(CoordinationInternal, "could not load task endpoint authority", err)
			}
			if !uuidEqual(task.AgentID, actor.ActorID) || !task.IssueID.Valid {
				return DependencyPage{}, coordinationErr(CoordinationForbidden, "task endpoint authority is not current", nil)
			}
			visibleEndpointIssueID = task.IssueID
		}
		if decoded != nil && decoded.ScopeRevision != scope.Revision {
			return DependencyPage{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
		}
		params := db.ListActiveCoordinationDependenciesByScopeParams{
			WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID,
			VisibleEndpointIssueID: visibleEndpointIssueID, LimitRows: int32(limit + 1),
		}
		if decoded != nil {
			params.CursorCreatedAt = pgtype.Timestamptz{Time: decoded.CreatedAt, Valid: true}
			params.CursorID = decoded.ID
		}
		rows, err := qtx.ListActiveCoordinationDependenciesByScope(ctx, params)
		if err != nil {
			return DependencyPage{}, coordinationErr(CoordinationInternal, "could not list coordination dependencies", err)
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		items := make([]Dependency, 0, len(rows))
		for _, row := range rows {
			items = append(items, dependencyFromRow(row))
		}
		next := ""
		if hasMore {
			last := items[len(items)-1]
			next, err = encodeDependencyCursor(dependencyCursor{
				WorkspaceID: actor.WorkspaceID, ScopeID: scopeID, ScopeRevision: scope.Revision,
				CreatedAt: last.CreatedAt, ID: last.ID,
			})
			if err != nil {
				return DependencyPage{}, coordinationErr(CoordinationInternal, "could not encode dependency cursor", err)
			}
		}
		return DependencyPage{Dependencies: items, ScopeRevision: scope.Revision, NextCursor: next}, nil
	})
}

func (s *CoordinationService) loadDependencyScope(ctx context.Context, q *db.Queries, actor CoordinationActor, scopeID pgtype.UUID) (db.CoordinationScope, error) {
	row, err := q.GetCoordinationScopeByID(ctx, db.GetCoordinationScopeByIDParams{WorkspaceID: actor.WorkspaceID, ID: scopeID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.CoordinationScope{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
		}
		return db.CoordinationScope{}, coordinationErr(CoordinationInternal, "could not load coordination scope", err)
	}
	if row.ScopeKind != "root" || row.State != "active" {
		return db.CoordinationScope{}, coordinationErr(CoordinationNotFound, "coordination scope is not active", nil)
	}
	if err := s.revalidateActor(ctx, q, actor, row.RootIssueID); err != nil {
		return db.CoordinationScope{}, err
	}
	if err := s.revalidateWorkspaceAndRoot(ctx, q, actor.WorkspaceID, row.RootIssueID); err != nil {
		return db.CoordinationScope{}, err
	}
	return row, nil
}

func (s *CoordinationService) revalidateDependencyEndpoints(ctx context.Context, q *db.Queries, actor CoordinationActor, scope db.CoordinationScope, downstreamID, upstreamID pgtype.UUID) error {
	for _, issueID := range []pgtype.UUID{downstreamID, upstreamID} {
		actual, err := q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: actor.WorkspaceID, IssueID: issueID})
		if err != nil {
			return coordinationErr(CoordinationInternal, "could not validate dependency endpoint", err)
		}
		switch actual.Status {
		case "ok":
			if !uuidEqual(actual.RootIssueID, scope.RootIssueID) {
				return coordinationErr(CoordinationForbidden, "dependency endpoint is outside the coordination scope", nil)
			}
		case "cross_workspace":
			return coordinationErr(CoordinationCrossWorkspace, "dependency endpoint is outside the workspace", nil)
		case "cycle", "depth_exceeded":
			return coordinationErr(CoordinationInvalidPayload, "dependency endpoint parent chain is invalid", nil)
		default:
			return coordinationErr(CoordinationNotFound, "dependency endpoint not found", nil)
		}
	}
	if actor.ActorType == CoordinationActorAgent {
		task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: actor.TaskID, WorkspaceID: actor.WorkspaceID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return coordinationErr(CoordinationForbidden, "task is not current", err)
			}
			return coordinationErr(CoordinationInternal, "could not load task endpoint authority", err)
		}
		if !uuidEqual(task.AgentID, actor.ActorID) || !task.IssueID.Valid || (!uuidEqual(task.IssueID, downstreamID) && !uuidEqual(task.IssueID, upstreamID)) {
			return coordinationErr(CoordinationForbidden, "task endpoint authority is not current", nil)
		}
	}
	return nil
}

func validateAddDependencyInput(input AddDependencyInput) error {
	if !input.ScopeID.Valid || !input.DownstreamIssueID.Valid || !input.UpstreamIssueID.Valid || input.ExpectedRevision < 0 {
		return coordinationErr(CoordinationInvalidPayload, "invalid add dependency input", nil)
	}
	if uuidEqual(input.DownstreamIssueID, input.UpstreamIssueID) {
		return coordinationErr(CoordinationSelfDependency, "dependency endpoints must be different", nil)
	}
	return validateCoordinationIdempotencyKey(input.IdempotencyKey)
}

func validateResolveDependencyInput(input ResolveDependencyInput) error {
	if !input.ScopeID.Valid || !input.DependencyID.Valid || input.ExpectedRevision < 0 {
		return coordinationErr(CoordinationInvalidPayload, "invalid resolve dependency input", nil)
	}
	return validateCoordinationIdempotencyKey(input.IdempotencyKey)
}

func validateCoordinationIdempotencyKey(value string) error {
	if len(value) < 1 || len(value) > 200 || strings.TrimSpace(value) != value {
		return coordinationErr(CoordinationInvalidPayload, "invalid idempotency key", nil)
	}
	return nil
}

func AddDependencyCanonicalJSON(actor CoordinationActor, input AddDependencyInput) ([]byte, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if err := validateAddDependencyInput(input); err != nil {
		return nil, err
	}
	return dependencyCanonicalJSON(actor, CoordinationOperationAddDependency, fmt.Sprintf(
		`{"downstream_issue_id":%q,"expected_revision":%q,"scope_id":%q,"upstream_issue_id":%q}`,
		strings.ToLower(util.UUIDToString(input.DownstreamIssueID)), fmt.Sprintf("%d", input.ExpectedRevision),
		strings.ToLower(util.UUIDToString(input.ScopeID)), strings.ToLower(util.UUIDToString(input.UpstreamIssueID)),
	)), nil
}

func ResolveDependencyCanonicalJSON(actor CoordinationActor, input ResolveDependencyInput) ([]byte, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if err := validateResolveDependencyInput(input); err != nil {
		return nil, err
	}
	return dependencyCanonicalJSON(actor, CoordinationOperationResolveDependency, fmt.Sprintf(
		`{"dependency_id":%q,"expected_revision":%q,"scope_id":%q}`,
		strings.ToLower(util.UUIDToString(input.DependencyID)), fmt.Sprintf("%d", input.ExpectedRevision), strings.ToLower(util.UUIDToString(input.ScopeID)),
	)), nil
}

func dependencyCanonicalJSON(actor CoordinationActor, operation, request string) []byte {
	taskJSON := "null"
	if actor.TaskID.Valid {
		taskJSON = fmt.Sprintf("%q", strings.ToLower(util.UUIDToString(actor.TaskID)))
	}
	return []byte(fmt.Sprintf(`{"actor":{"id":%q,"task_id":%s,"type":%q},"hash_version":1,"operation":%q,"request":%s,"workspace_id":%q}`,
		strings.ToLower(util.UUIDToString(actor.ActorID)), taskJSON, actor.ActorType, operation, request,
		strings.ToLower(util.UUIDToString(actor.WorkspaceID))))
}

func AddDependencyRequestHash(actor CoordinationActor, input AddDependencyInput) ([]byte, error) {
	raw, err := AddDependencyCanonicalJSON(actor, input)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func ResolveDependencyRequestHash(actor CoordinationActor, input ResolveDependencyInput) ([]byte, error) {
	raw, err := ResolveDependencyCanonicalJSON(actor, input)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func dependencyReceiptMatches(row db.CoordinationReceipt, actor CoordinationActor, requestHash []byte, operation string) bool {
	if row.Operation != operation || row.ResourceType != CoordinationResourceDependency || row.ActorType != actor.ActorType || !uuidEqual(row.ActorID, actor.ActorID) {
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

func saveDependencyReceipt(ctx context.Context, q *db.Queries, actor CoordinationActor, scopeID pgtype.UUID, idempotencyKey string, requestHash []byte, operation string, dependency Dependency, revisionBefore, revisionAfter int64) (Receipt, error) {
	ordinal, err := q.AllocateCoordinationReceiptOrdinal(ctx, db.AllocateCoordinationReceiptOrdinalParams{WorkspaceID: actor.WorkspaceID, ID: scopeID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Receipt{}, coordinationErr(CoordinationCapacityExceeded, "coordination receipt capacity was exhausted", nil)
		}
		return Receipt{}, coordinationErr(CoordinationInternal, "could not allocate coordination receipt ordinal", err)
	}
	snapshot, err := json.Marshal(dependencySnapshot(dependency))
	if err != nil {
		return Receipt{}, coordinationErr(CoordinationInternal, "could not encode coordination dependency result", err)
	}
	receiptID, err := newPgUUID()
	if err != nil {
		return Receipt{}, coordinationErr(CoordinationInternal, "could not generate coordination receipt id", err)
	}
	row, err := q.InsertCoordinationReceipt(ctx, db.InsertCoordinationReceiptParams{
		ID: receiptID, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID,
		ReceiptOrdinal: ordinal, Operation: operation, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, ResourceType: CoordinationResourceDependency, ResourceID: dependency.ID,
		RevisionBefore: revisionBefore, RevisionAfter: revisionAfter, ResultSnapshot: snapshot,
		ActorType: actor.ActorType, ActorID: actor.ActorID, ActorTaskID: actor.TaskID,
	})
	if err != nil {
		return Receipt{}, coordinationErr(CoordinationInternal, "could not save coordination receipt", err)
	}
	return receiptFromRow(row), nil
}

func replayDependencyReceipt(ctx context.Context, q *db.Queries, receipt db.CoordinationReceipt, scopeID pgtype.UUID) (Dependency, error) {
	if !uuidEqual(receipt.CoordinationScopeID, scopeID) || !receipt.ResourceID.Valid {
		return Dependency{}, coordinationErr(CoordinationInternal, "saved dependency receipt is inconsistent", nil)
	}
	current, err := q.GetCoordinationDependencyByID(ctx, db.GetCoordinationDependencyByIDParams{WorkspaceID: receipt.WorkspaceID, ID: receipt.ResourceID})
	if err != nil {
		return Dependency{}, coordinationErr(CoordinationNotFound, "saved coordination dependency no longer exists", err)
	}
	if !uuidEqual(current.CoordinationScopeID, scopeID) {
		return Dependency{}, coordinationErr(CoordinationDependencyScopeConflict, "saved coordination dependency belongs to another scope", nil)
	}
	saved, err := dependencyFromSnapshot(receipt.ResultSnapshot)
	if err != nil || !sameDependencyIdentity(saved, dependencyFromRow(current)) {
		return Dependency{}, coordinationErr(CoordinationInternal, "saved coordination dependency result is invalid", err)
	}
	return saved, nil
}

func dependencyFromRow(row db.CoordinationDependency) Dependency {
	value := Dependency{
		ID: row.ID, WorkspaceID: row.WorkspaceID, CoordinationScopeID: row.CoordinationScopeID,
		DownstreamIssueID: row.DownstreamIssueID, UpstreamIssueID: row.UpstreamIssueID,
		CreatedByType: row.CreatedByType, CreatedByID: row.CreatedByID, CreatedTaskID: row.CreatedTaskID,
		CreatedAt: row.CreatedAt.Time,
	}
	if row.ResolvedAt.Valid {
		value.Resolved = true
		value.ResolvedByType = row.ResolvedByType.String
		value.ResolvedByID = row.ResolvedByID
		value.ResolvedTaskID = row.ResolvedTaskID
		value.ResolvedAt = row.ResolvedAt.Time
	}
	return value
}

func sameDependencyIdentity(a, b Dependency) bool {
	return uuidEqual(a.ID, b.ID) && uuidEqual(a.WorkspaceID, b.WorkspaceID) && uuidEqual(a.CoordinationScopeID, b.CoordinationScopeID) &&
		uuidEqual(a.DownstreamIssueID, b.DownstreamIssueID) && uuidEqual(a.UpstreamIssueID, b.UpstreamIssueID)
}

type dependencyActorSnapshotDTO struct {
	ActorType string  `json:"actor_type"`
	ActorID   string  `json:"actor_id"`
	TaskID    *string `json:"task_id"`
}

type dependencySnapshotDTO struct {
	ID                  string                      `json:"id"`
	WorkspaceID         string                      `json:"workspace_id"`
	CoordinationScopeID string                      `json:"coordination_scope_id"`
	DownstreamIssueID   string                      `json:"downstream_issue_id"`
	UpstreamIssueID     string                      `json:"upstream_issue_id"`
	CreatedBy           dependencyActorSnapshotDTO  `json:"created_by"`
	CreatedAt           string                      `json:"created_at"`
	ResolvedBy          *dependencyActorSnapshotDTO `json:"resolved_by"`
	ResolvedAt          *string                     `json:"resolved_at"`
}

func dependencySnapshot(value Dependency) dependencySnapshotDTO {
	created := dependencyActorSnapshotDTO{ActorType: value.CreatedByType, ActorID: util.UUIDToString(value.CreatedByID)}
	if value.CreatedTaskID.Valid {
		task := util.UUIDToString(value.CreatedTaskID)
		created.TaskID = &task
	}
	dto := dependencySnapshotDTO{
		ID: util.UUIDToString(value.ID), WorkspaceID: util.UUIDToString(value.WorkspaceID),
		CoordinationScopeID: util.UUIDToString(value.CoordinationScopeID), DownstreamIssueID: util.UUIDToString(value.DownstreamIssueID),
		UpstreamIssueID: util.UUIDToString(value.UpstreamIssueID), CreatedBy: created,
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if value.Resolved {
		resolved := dependencyActorSnapshotDTO{ActorType: value.ResolvedByType, ActorID: util.UUIDToString(value.ResolvedByID)}
		if value.ResolvedTaskID.Valid {
			task := util.UUIDToString(value.ResolvedTaskID)
			resolved.TaskID = &task
		}
		resolvedAt := value.ResolvedAt.UTC().Format(time.RFC3339Nano)
		dto.ResolvedBy = &resolved
		dto.ResolvedAt = &resolvedAt
	}
	return dto
}

func dependencyFromSnapshot(raw []byte) (Dependency, error) {
	var dto dependencySnapshotDTO
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return Dependency{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Dependency{}, errors.New("invalid trailing dependency snapshot data")
	}
	id, err := util.ParseUUID(dto.ID)
	if err != nil {
		return Dependency{}, err
	}
	workspaceID, err := util.ParseUUID(dto.WorkspaceID)
	if err != nil {
		return Dependency{}, err
	}
	scopeID, err := util.ParseUUID(dto.CoordinationScopeID)
	if err != nil {
		return Dependency{}, err
	}
	downstreamID, err := util.ParseUUID(dto.DownstreamIssueID)
	if err != nil {
		return Dependency{}, err
	}
	upstreamID, err := util.ParseUUID(dto.UpstreamIssueID)
	if err != nil || uuidEqual(downstreamID, upstreamID) {
		return Dependency{}, errors.New("invalid dependency endpoints")
	}
	createdByID, createdTaskID, err := parseDependencySnapshotActor(dto.CreatedBy)
	if err != nil {
		return Dependency{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, dto.CreatedAt)
	if err != nil {
		return Dependency{}, err
	}
	value := Dependency{
		ID: id, WorkspaceID: workspaceID, CoordinationScopeID: scopeID,
		DownstreamIssueID: downstreamID, UpstreamIssueID: upstreamID,
		CreatedByType: dto.CreatedBy.ActorType, CreatedByID: createdByID, CreatedTaskID: createdTaskID, CreatedAt: createdAt,
	}
	if (dto.ResolvedBy == nil) != (dto.ResolvedAt == nil) {
		return Dependency{}, errors.New("invalid dependency resolution snapshot")
	}
	if dto.ResolvedBy != nil {
		resolvedByID, resolvedTaskID, err := parseDependencySnapshotActor(*dto.ResolvedBy)
		if err != nil {
			return Dependency{}, err
		}
		resolvedAt, err := time.Parse(time.RFC3339Nano, *dto.ResolvedAt)
		if err != nil {
			return Dependency{}, err
		}
		value.Resolved = true
		value.ResolvedByType = dto.ResolvedBy.ActorType
		value.ResolvedByID = resolvedByID
		value.ResolvedTaskID = resolvedTaskID
		value.ResolvedAt = resolvedAt
	}
	return value, nil
}

func parseDependencySnapshotActor(dto dependencyActorSnapshotDTO) (pgtype.UUID, pgtype.UUID, error) {
	actorID, err := util.ParseUUID(dto.ActorID)
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
	if (dto.ActorType == CoordinationActorMember && !taskID.Valid) || (dto.ActorType == CoordinationActorAgent && taskID.Valid) {
		return actorID, taskID, nil
	}
	return pgtype.UUID{}, pgtype.UUID{}, errors.New("invalid dependency snapshot actor")
}

type dependencyCursor struct {
	WorkspaceID   pgtype.UUID
	ScopeID       pgtype.UUID
	ScopeRevision int64
	CreatedAt     time.Time
	ID            pgtype.UUID
}

type dependencyCursorDTO struct {
	Version       int    `json:"v"`
	WorkspaceID   string `json:"workspace_id"`
	ScopeID       string `json:"scope_id"`
	ScopeRevision int64  `json:"scope_revision"`
	CreatedAt     string `json:"created_at"`
	ID            string `json:"id"`
}

func encodeDependencyCursor(value dependencyCursor) (string, error) {
	if !value.WorkspaceID.Valid || !value.ScopeID.Valid || !value.ID.Valid || value.ScopeRevision < 0 || value.CreatedAt.IsZero() {
		return "", errors.New("invalid dependency cursor state")
	}
	raw, err := json.Marshal(dependencyCursorDTO{
		Version: coordinationDependencyCursorV1, WorkspaceID: util.UUIDToString(value.WorkspaceID), ScopeID: util.UUIDToString(value.ScopeID),
		ScopeRevision: value.ScopeRevision, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), ID: util.UUIDToString(value.ID),
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeDependencyCursor(raw string) (*dependencyCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > 2048 {
		return nil, errors.New("invalid dependency cursor encoding")
	}
	var dto dependencyCursorDTO
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid trailing dependency cursor data")
	}
	if dto.Version != coordinationDependencyCursorV1 || dto.ScopeRevision < 0 {
		return nil, errors.New("unsupported dependency cursor")
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
	return &dependencyCursor{WorkspaceID: workspaceID, ScopeID: scopeID, ScopeRevision: dto.ScopeRevision, CreatedAt: createdAt, ID: id}, nil
}
