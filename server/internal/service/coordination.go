package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	CoordinationAdvisoryNamespace int32 = 1464030001

	CoordinationActorMember = "member"
	CoordinationActorAgent  = "agent"

	CoordinationOperationEnsureScope       = "ensure_scope"
	CoordinationOperationAddDependency     = "add_dependency"
	CoordinationOperationResolveDependency = "resolve_dependency"
	CoordinationOperationAppendBlocker     = "append_blocker"
	CoordinationOperationResolveBlocker    = "resolve_blocker"
	CoordinationResourceScope              = "scope"
	CoordinationResourceDependency         = "dependency"
	CoordinationResourceBlocker            = "blocker"

	CoordinationOutcomeCreated  = "created"
	CoordinationOutcomeResolved = "resolved"
	CoordinationOutcomeNoop     = "noop"
	CoordinationOutcomeReplay   = "replay"
)

var coordinationProfileRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type CoordinationErrorCode string

const (
	CoordinationNotFound                CoordinationErrorCode = "coordination_not_found"
	CoordinationCrossWorkspace          CoordinationErrorCode = "coordination_cross_workspace"
	CoordinationForbidden               CoordinationErrorCode = "coordination_forbidden"
	CoordinationInvalidPayload          CoordinationErrorCode = "coordination_invalid_payload"
	CoordinationCapacityExceeded        CoordinationErrorCode = "coordination_capacity_exceeded"
	CoordinationSelfDependency          CoordinationErrorCode = "coordination_self_dependency"
	CoordinationCycle                   CoordinationErrorCode = "coordination_cycle"
	CoordinationRevisionConflict        CoordinationErrorCode = "coordination_revision_conflict"
	CoordinationIdempotencyConflict     CoordinationErrorCode = "coordination_idempotency_conflict"
	CoordinationDependencyScopeConflict CoordinationErrorCode = "coordination_dependency_scope_conflict"
	CoordinationDeleteBlocked           CoordinationErrorCode = "coordination_delete_blocked"
	CoordinationInternal                CoordinationErrorCode = "coordination_internal"
)

type CoordinationError struct {
	Code CoordinationErrorCode
	Msg  string
	Err  error
}

func (e *CoordinationError) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + ": " + e.Msg
}

func (e *CoordinationError) Unwrap() error { return e.Err }

func coordinationErr(code CoordinationErrorCode, msg string, err error) error {
	return &CoordinationError{Code: code, Msg: msg, Err: err}
}

type CoordinationService struct {
	Queries *db.Queries
	Tx      TxStarter
	Pool    *pgxpool.Pool
}

func NewCoordinationService(q *db.Queries, tx TxStarter) *CoordinationService {
	s := &CoordinationService{Queries: q, Tx: tx}
	if pool, ok := tx.(*pgxpool.Pool); ok {
		s.Pool = pool
	}
	return s
}

type CoordinationActor struct {
	WorkspaceID       pgtype.UUID
	ActorType         string
	ActorID           pgtype.UUID
	TaskID            pgtype.UUID
	TaskCredentialRef pgtype.UUID
}

type EnsureScopeInput struct {
	RootIssueID        pgtype.UUID
	WorkflowProfileKey string
	IdempotencyKey     string
}

type Scope struct {
	ID                 pgtype.UUID
	WorkspaceID        pgtype.UUID
	ScopeKind          string
	State              string
	RootIssueID        pgtype.UUID
	WorkflowProfileKey string
	Revision           int64
	CreatedByType      string
	CreatedByID        pgtype.UUID
	CreatedTaskID      pgtype.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Receipt struct {
	ID             pgtype.UUID
	ReceiptOrdinal int64
	Operation      string
	ResourceType   string
	ResourceID     pgtype.UUID
	RevisionBefore int64
	RevisionAfter  int64
	CreatedAt      time.Time
}

type EnsureScopeResult struct {
	Scope   Scope
	Receipt Receipt
	Outcome string
}

func CoordinationWorkspaceAdvisoryKey(workspaceID pgtype.UUID) (int32, error) {
	if !workspaceID.Valid {
		return 0, errors.New("workspace id is required")
	}
	sum := sha256.Sum256(workspaceID.Bytes[:])
	return int32(binary.BigEndian.Uint32(sum[0:4])), nil
}

func coordinationInLockedTx[T any](ctx context.Context, s *CoordinationService, workspaceID pgtype.UUID, fn func(pgx.Tx, *db.Queries) (T, error)) (T, error) {
	var zero T
	if s == nil || s.Queries == nil || s.Tx == nil {
		return zero, coordinationErr(CoordinationInternal, "coordination service is not configured", nil)
	}
	tx, err := s.Tx.Begin(ctx)
	if err != nil {
		return zero, coordinationErr(CoordinationInternal, "could not start coordination transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(cleanupCtx)
		}
	}()
	qtx := s.Queries.WithTx(tx)
	if err := lockWorkspace(ctx, qtx, workspaceID); err != nil {
		return zero, err
	}
	result, err := fn(tx, qtx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, coordinationErr(CoordinationInternal, "could not commit coordination transaction", err)
	}
	committed = true
	return result, nil
}

func (s *CoordinationService) EnsureScope(ctx context.Context, actor CoordinationActor, input EnsureScopeInput) (EnsureScopeResult, error) {
	if err := validateActor(actor); err != nil {
		return EnsureScopeResult{}, err
	}
	if err := validateEnsureInput(input); err != nil {
		return EnsureScopeResult{}, err
	}
	reqHash, err := EnsureScopeRequestHash(actor, input.RootIssueID, input.WorkflowProfileKey)
	if err != nil {
		return EnsureScopeResult{}, coordinationErr(CoordinationInvalidPayload, "invalid request hash input", err)
	}

	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(tx pgx.Tx, qtx *db.Queries) (EnsureScopeResult, error) {
		if err := s.revalidateActor(ctx, qtx, actor, input.RootIssueID); err != nil {
			return EnsureScopeResult{}, err
		}
		if err := s.revalidateWorkspaceAndRoot(ctx, qtx, actor.WorkspaceID, input.RootIssueID); err != nil {
			return EnsureScopeResult{}, err
		}

		existingReceipt, err := qtx.GetCoordinationReceiptByIdempotencyKey(ctx, db.GetCoordinationReceiptByIdempotencyKeyParams{
			WorkspaceID: actor.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		})
		if err == nil {
			if !receiptMatches(existingReceipt, actor, reqHash) {
				return EnsureScopeResult{}, coordinationErr(CoordinationIdempotencyConflict, "idempotency key was already used for a different coordination request", nil)
			}
			if !uuidEqual(existingReceipt.CoordinationScopeID, existingReceipt.ResourceID) {
				return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "saved coordination receipt is inconsistent", nil)
			}
			current, err := qtx.GetCoordinationScopeByID(ctx, db.GetCoordinationScopeByIDParams{WorkspaceID: actor.WorkspaceID, ID: existingReceipt.ResourceID})
			if err != nil {
				return EnsureScopeResult{}, coordinationErr(CoordinationNotFound, "saved coordination scope no longer exists", err)
			}
			saved, err := scopeFromSnapshot(existingReceipt.ResultSnapshot)
			if err != nil || !uuidEqual(saved.ID, current.ID) || !uuidEqual(saved.WorkspaceID, current.WorkspaceID) || !uuidEqual(saved.RootIssueID, current.RootIssueID) || saved.WorkflowProfileKey != input.WorkflowProfileKey || current.WorkflowProfileKey != input.WorkflowProfileKey || current.ScopeKind != "root" || current.State != "active" {
				return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "saved coordination result is invalid", err)
			}
			return EnsureScopeResult{Scope: saved, Receipt: receiptFromRow(existingReceipt), Outcome: CoordinationOutcomeReplay}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "could not load coordination receipt", err)
		}

		scopeRow, outcome, err := s.ensureScopeRow(ctx, tx, qtx, actor, input)
		if err != nil {
			return EnsureScopeResult{}, err
		}
		lockedScope, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: actor.WorkspaceID, ID: scopeRow.ID})
		if err != nil {
			return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "could not lock coordination scope", err)
		}
		ordinal, err := qtx.AllocateCoordinationReceiptOrdinal(ctx, db.AllocateCoordinationReceiptOrdinalParams{WorkspaceID: actor.WorkspaceID, ID: lockedScope.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return EnsureScopeResult{}, coordinationErr(CoordinationCapacityExceeded, "coordination receipt capacity was exhausted", nil)
			}
			return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "could not allocate coordination receipt ordinal", err)
		}
		scope := scopeFromRow(lockedScope)
		snapshot, err := json.Marshal(scopeSnapshot(scope))
		if err != nil {
			return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "could not encode coordination result", err)
		}
		receiptID, err := newPgUUID()
		if err != nil {
			return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "could not generate coordination receipt id", err)
		}
		receiptRow, err := qtx.InsertCoordinationReceipt(ctx, db.InsertCoordinationReceiptParams{
			ID: receiptID, WorkspaceID: actor.WorkspaceID, CoordinationScopeID: lockedScope.ID,
			ReceiptOrdinal: ordinal, Operation: CoordinationOperationEnsureScope,
			IdempotencyKey: input.IdempotencyKey, RequestHash: reqHash,
			ResourceType: CoordinationResourceScope, ResourceID: lockedScope.ID,
			RevisionBefore: lockedScope.Revision, RevisionAfter: lockedScope.Revision,
			ResultSnapshot: snapshot, ActorType: actor.ActorType, ActorID: actor.ActorID, ActorTaskID: actor.TaskID,
		})
		if err != nil {
			return EnsureScopeResult{}, coordinationErr(CoordinationInternal, "could not save coordination receipt", err)
		}
		return EnsureScopeResult{Scope: scope, Receipt: receiptFromRow(receiptRow), Outcome: outcome}, nil
	})
}

func (s *CoordinationService) GetScope(ctx context.Context, actor CoordinationActor, scopeID pgtype.UUID) (Scope, error) {
	if err := validateActor(actor); err != nil {
		return Scope{}, err
	}
	if !scopeID.Valid {
		return Scope{}, coordinationErr(CoordinationInvalidPayload, "scope id is required", nil)
	}
	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (Scope, error) {
		row, err := qtx.GetCoordinationScopeByID(ctx, db.GetCoordinationScopeByIDParams{WorkspaceID: actor.WorkspaceID, ID: scopeID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Scope{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
			}
			return Scope{}, coordinationErr(CoordinationInternal, "could not load coordination scope", err)
		}
		if err := s.revalidateActor(ctx, qtx, actor, row.RootIssueID); err != nil {
			return Scope{}, err
		}
		if err := s.revalidateWorkspaceAndRoot(ctx, qtx, actor.WorkspaceID, row.RootIssueID); err != nil {
			return Scope{}, err
		}
		return scopeFromRow(row), nil
	})
}

func (s *CoordinationService) GetScopeByRoot(ctx context.Context, actor CoordinationActor, rootIssueID pgtype.UUID, workflowProfileKey string) (Scope, error) {
	if err := validateActor(actor); err != nil {
		return Scope{}, err
	}
	if !rootIssueID.Valid || !coordinationProfileRE.MatchString(workflowProfileKey) {
		return Scope{}, coordinationErr(CoordinationInvalidPayload, "invalid root or workflow profile key", nil)
	}
	return coordinationInLockedTx(ctx, s, actor.WorkspaceID, func(_ pgx.Tx, qtx *db.Queries) (Scope, error) {
		if err := s.revalidateActor(ctx, qtx, actor, rootIssueID); err != nil {
			return Scope{}, err
		}
		if err := s.revalidateWorkspaceAndRoot(ctx, qtx, actor.WorkspaceID, rootIssueID); err != nil {
			return Scope{}, err
		}
		row, err := qtx.GetActiveCoordinationScopeByRoot(ctx, db.GetActiveCoordinationScopeByRootParams{WorkspaceID: actor.WorkspaceID, RootIssueID: rootIssueID, WorkflowProfileKey: workflowProfileKey})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Scope{}, coordinationErr(CoordinationNotFound, "coordination scope not found", err)
			}
			return Scope{}, coordinationErr(CoordinationInternal, "could not load coordination scope", err)
		}
		return scopeFromRow(row), nil
	})
}

func (s *CoordinationService) ensureScopeRow(ctx context.Context, tx pgx.Tx, qtx *db.Queries, actor CoordinationActor, input EnsureScopeInput) (db.CoordinationScope, string, error) {
	params := db.GetActiveCoordinationScopeByRootParams{WorkspaceID: actor.WorkspaceID, RootIssueID: input.RootIssueID, WorkflowProfileKey: input.WorkflowProfileKey}
	row, err := qtx.GetActiveCoordinationScopeByRoot(ctx, params)
	if err == nil {
		return row, CoordinationOutcomeNoop, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not load coordination scope", err)
	}
	return s.createScopeRowAfterMiss(ctx, tx, qtx, actor, input, params)
}

func (s *CoordinationService) createScopeRowAfterMiss(ctx context.Context, tx pgx.Tx, qtx *db.Queries, actor CoordinationActor, input EnsureScopeInput, params db.GetActiveCoordinationScopeByRootParams) (db.CoordinationScope, string, error) {
	scopeID, err := newPgUUID()
	if err != nil {
		return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not generate coordination scope id", err)
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not create coordination scope savepoint", err)
	}
	row, createErr := s.Queries.WithTx(savepoint).CreateCoordinationScope(ctx, db.CreateCoordinationScopeParams{
		ID: scopeID, WorkspaceID: actor.WorkspaceID, RootIssueID: input.RootIssueID,
		WorkflowProfileKey: input.WorkflowProfileKey, CreatedByType: actor.ActorType,
		CreatedByID: actor.ActorID, CreatedTaskID: actor.TaskID,
	})
	if createErr == nil {
		if err := savepoint.Commit(ctx); err != nil {
			return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not release coordination scope savepoint", err)
		}
		return row, CoordinationOutcomeCreated, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	rollbackErr := savepoint.Rollback(cleanupCtx)
	cancel()
	if rollbackErr != nil {
		return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not recover coordination scope transaction", rollbackErr)
	}
	var pgErr *pgconn.PgError
	if !errors.As(createErr, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "coordination_scope_active_natural_idx" {
		return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not create coordination scope", createErr)
	}
	row, reloadErr := qtx.GetActiveCoordinationScopeByRoot(ctx, params)
	if reloadErr == nil {
		return row, CoordinationOutcomeNoop, nil
	}
	return db.CoordinationScope{}, "", coordinationErr(CoordinationInternal, "could not reload coordination scope", reloadErr)
}

func (s *CoordinationService) revalidateActor(ctx context.Context, q *db.Queries, actor CoordinationActor, requiredRoot pgtype.UUID) error {
	switch actor.ActorType {
	case CoordinationActorMember:
		if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: actor.ActorID, WorkspaceID: actor.WorkspaceID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return coordinationErr(CoordinationForbidden, "workspace membership is not current", err)
			}
			return coordinationErr(CoordinationInternal, "could not revalidate workspace membership", err)
		}
		return nil
	case CoordinationActorAgent:
		if !actor.TaskID.Valid || !actor.TaskCredentialRef.Valid {
			return coordinationErr(CoordinationForbidden, "task credential is required", nil)
		}
		token, err := q.GetTaskTokenByIDForCoordination(ctx, db.GetTaskTokenByIDForCoordinationParams{ID: actor.TaskCredentialRef, WorkspaceID: actor.WorkspaceID, AgentID: actor.ActorID, TaskID: actor.TaskID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return coordinationErr(CoordinationForbidden, "task credential is not current", err)
			}
			return coordinationErr(CoordinationInternal, "could not revalidate task credential", err)
		}
		if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: token.UserID, WorkspaceID: actor.WorkspaceID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return coordinationErr(CoordinationForbidden, "task delegator membership is not current", err)
			}
			return coordinationErr(CoordinationInternal, "could not revalidate task delegator membership", err)
		}
		task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: actor.TaskID, WorkspaceID: actor.WorkspaceID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return coordinationErr(CoordinationForbidden, "task is not current", err)
			}
			return coordinationErr(CoordinationInternal, "could not revalidate task", err)
		}
		if !uuidEqual(task.AgentID, actor.ActorID) || !task.IssueID.Valid {
			return coordinationErr(CoordinationForbidden, "task binding is not current", nil)
		}
		if requiredRoot.Valid {
			actual, err := q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: actor.WorkspaceID, IssueID: task.IssueID})
			if err != nil {
				return coordinationErr(CoordinationInternal, "could not revalidate task root", err)
			}
			if actual.Status != "ok" || !uuidEqual(actual.RootIssueID, requiredRoot) {
				return coordinationErr(CoordinationForbidden, "task is not bound to the requested root", nil)
			}
		}
		return nil
	default:
		return coordinationErr(CoordinationInvalidPayload, "invalid coordination actor type", nil)
	}
}

func (s *CoordinationService) revalidateWorkspaceAndRoot(ctx context.Context, q *db.Queries, workspaceID, rootIssueID pgtype.UUID) error {
	if _, err := q.GetWorkspace(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coordinationErr(CoordinationNotFound, "workspace not found", err)
		}
		return coordinationErr(CoordinationInternal, "could not load workspace", err)
	}
	actual, err := q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: workspaceID, IssueID: rootIssueID})
	if err != nil {
		return coordinationErr(CoordinationInternal, "could not validate root issue", err)
	}
	switch actual.Status {
	case "ok":
		if !uuidEqual(actual.RootIssueID, rootIssueID) {
			return coordinationErr(CoordinationInvalidPayload, "root issue must be an actual root", nil)
		}
		return nil
	case "cross_workspace":
		return coordinationErr(CoordinationCrossWorkspace, "root issue is outside the workspace", nil)
	case "foreign_parent":
		return coordinationErr(CoordinationNotFound, "root issue parent was not found", nil)
	case "cycle", "depth_exceeded":
		return coordinationErr(CoordinationInvalidPayload, "root issue parent chain is invalid", nil)
	default:
		return coordinationErr(CoordinationNotFound, "root issue not found", nil)
	}
}

func validateActor(actor CoordinationActor) error {
	if !actor.WorkspaceID.Valid || !actor.ActorID.Valid {
		return coordinationErr(CoordinationInvalidPayload, "workspace and actor are required", nil)
	}
	switch actor.ActorType {
	case CoordinationActorMember:
		if actor.TaskID.Valid || actor.TaskCredentialRef.Valid {
			return coordinationErr(CoordinationInvalidPayload, "member actor cannot include task identity", nil)
		}
	case CoordinationActorAgent:
		if !actor.TaskID.Valid || !actor.TaskCredentialRef.Valid {
			return coordinationErr(CoordinationForbidden, "agent actor requires exact task credential", nil)
		}
	default:
		return coordinationErr(CoordinationInvalidPayload, "invalid coordination actor type", nil)
	}
	return nil
}

func validateEnsureInput(input EnsureScopeInput) error {
	if !input.RootIssueID.Valid {
		return coordinationErr(CoordinationInvalidPayload, "root issue id is required", nil)
	}
	if !coordinationProfileRE.MatchString(input.WorkflowProfileKey) {
		return coordinationErr(CoordinationInvalidPayload, "invalid workflow profile key", nil)
	}
	return validateCoordinationIdempotencyKey(input.IdempotencyKey)
}

func lockWorkspace(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) error {
	key, err := CoordinationWorkspaceAdvisoryKey(workspaceID)
	if err != nil {
		return coordinationErr(CoordinationInvalidPayload, "invalid workspace id", err)
	}
	if err := q.CoordinationAdvisoryXactLock(ctx, db.CoordinationAdvisoryXactLockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: key}); err != nil {
		return coordinationErr(CoordinationInternal, "could not acquire workspace coordination lock", err)
	}
	return nil
}

func EnsureScopeCanonicalJSON(actor CoordinationActor, rootIssueID pgtype.UUID, workflowProfileKey string) ([]byte, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if !rootIssueID.Valid || !coordinationProfileRE.MatchString(workflowProfileKey) {
		return nil, coordinationErr(CoordinationInvalidPayload, "invalid ensure scope canonical input", nil)
	}
	workspaceID := strings.ToLower(util.UUIDToString(actor.WorkspaceID))
	actorID := strings.ToLower(util.UUIDToString(actor.ActorID))
	rootID := strings.ToLower(util.UUIDToString(rootIssueID))
	taskJSON := "null"
	if actor.TaskID.Valid {
		taskJSON = fmt.Sprintf("%q", strings.ToLower(util.UUIDToString(actor.TaskID)))
	}
	return []byte(fmt.Sprintf(`{"actor":{"id":%q,"task_id":%s,"type":%q},"hash_version":1,"operation":"ensure_scope","request":{"root_issue_id":%q,"workflow_profile_key":%q},"workspace_id":%q}`,
		actorID, taskJSON, actor.ActorType, rootID, workflowProfileKey, workspaceID)), nil
}

func EnsureScopeRequestHash(actor CoordinationActor, rootIssueID pgtype.UUID, workflowProfileKey string) ([]byte, error) {
	b, err := EnsureScopeCanonicalJSON(actor, rootIssueID, workflowProfileKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

func receiptMatches(row db.CoordinationReceipt, actor CoordinationActor, requestHash []byte) bool {
	if row.Operation != CoordinationOperationEnsureScope || row.ResourceType != CoordinationResourceScope {
		return false
	}
	if row.ActorType != actor.ActorType || !uuidEqual(row.ActorID, actor.ActorID) {
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

func scopeFromRow(row db.CoordinationScope) Scope {
	return Scope{ID: row.ID, WorkspaceID: row.WorkspaceID, ScopeKind: row.ScopeKind, State: row.State,
		RootIssueID: row.RootIssueID, WorkflowProfileKey: row.WorkflowProfileKey, Revision: row.Revision,
		CreatedByType: row.CreatedByType, CreatedByID: row.CreatedByID, CreatedTaskID: row.CreatedTaskID,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

func receiptFromRow(row db.CoordinationReceipt) Receipt {
	return Receipt{ID: row.ID, ReceiptOrdinal: row.ReceiptOrdinal, Operation: row.Operation,
		ResourceType: row.ResourceType, ResourceID: row.ResourceID, RevisionBefore: row.RevisionBefore,
		RevisionAfter: row.RevisionAfter, CreatedAt: row.CreatedAt.Time}
}

type scopeSnapshotDTO struct {
	ID                 string            `json:"id"`
	WorkspaceID        string            `json:"workspace_id"`
	ScopeKind          string            `json:"scope_kind"`
	State              string            `json:"state"`
	RootIssueID        string            `json:"root_issue_id"`
	WorkflowProfileKey string            `json:"workflow_profile_key"`
	Revision           int64             `json:"revision"`
	CreatedBy          scopeCreatedByDTO `json:"created_by"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
}

type scopeCreatedByDTO struct {
	ActorType string  `json:"actor_type"`
	ActorID   string  `json:"actor_id"`
	TaskID    *string `json:"task_id"`
}

func scopeSnapshot(scope Scope) scopeSnapshotDTO {
	var taskID *string
	if scope.CreatedTaskID.Valid {
		s := util.UUIDToString(scope.CreatedTaskID)
		taskID = &s
	}
	return scopeSnapshotDTO{ID: util.UUIDToString(scope.ID), WorkspaceID: util.UUIDToString(scope.WorkspaceID),
		ScopeKind: scope.ScopeKind, State: scope.State, RootIssueID: util.UUIDToString(scope.RootIssueID),
		WorkflowProfileKey: scope.WorkflowProfileKey, Revision: scope.Revision,
		CreatedBy: scopeCreatedByDTO{ActorType: scope.CreatedByType, ActorID: util.UUIDToString(scope.CreatedByID), TaskID: taskID},
		CreatedAt: scope.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: scope.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func scopeFromSnapshot(raw []byte) (Scope, error) {
	var dto scopeSnapshotDTO
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return Scope{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Scope{}, errors.New("invalid trailing scope snapshot data")
	}
	id, err := util.ParseUUID(dto.ID)
	if err != nil {
		return Scope{}, err
	}
	workspaceID, err := util.ParseUUID(dto.WorkspaceID)
	if err != nil {
		return Scope{}, err
	}
	rootID, err := util.ParseUUID(dto.RootIssueID)
	if err != nil {
		return Scope{}, err
	}
	actorID, err := util.ParseUUID(dto.CreatedBy.ActorID)
	if err != nil {
		return Scope{}, err
	}
	var taskID pgtype.UUID
	if dto.CreatedBy.TaskID != nil {
		taskID, err = util.ParseUUID(*dto.CreatedBy.TaskID)
		if err != nil {
			return Scope{}, err
		}
	}
	createdAt, err := time.Parse(time.RFC3339Nano, dto.CreatedAt)
	if err != nil {
		return Scope{}, err
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, dto.UpdatedAt)
	if err != nil {
		return Scope{}, err
	}
	validCreator := (dto.CreatedBy.ActorType == CoordinationActorMember && !taskID.Valid) || (dto.CreatedBy.ActorType == CoordinationActorAgent && taskID.Valid)
	if dto.ScopeKind != "root" || dto.State != "active" || dto.Revision < 0 || !coordinationProfileRE.MatchString(dto.WorkflowProfileKey) || !validCreator {
		return Scope{}, errors.New("invalid scope snapshot")
	}
	return Scope{ID: id, WorkspaceID: workspaceID, ScopeKind: dto.ScopeKind, State: dto.State,
		RootIssueID: rootID, WorkflowProfileKey: dto.WorkflowProfileKey, Revision: dto.Revision,
		CreatedByType: dto.CreatedBy.ActorType, CreatedByID: actorID, CreatedTaskID: taskID,
		CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func uuidEqual(a, b pgtype.UUID) bool { return a.Valid && b.Valid && a.Bytes == b.Bytes }

func newPgUUID() (pgtype.UUID, error) { return util.ParseUUID(uuid.NewString()) }

var _ TxStarter = (*pgxpool.Pool)(nil)
