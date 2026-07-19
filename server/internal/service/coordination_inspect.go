package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	CoordinationReceiptPageSize      = 100
	coordinationReceiptCursorV1      = 1
	coordinationReceiptCollectionKey = "receipt"
)

type ReceiptRef struct {
	ID             pgtype.UUID
	ReceiptOrdinal int64
	Operation      string
	ResourceType   string
	ResourceID     pgtype.UUID
	RevisionBefore int64
	RevisionAfter  int64
	ActorType      string
	CreatedAt      time.Time
}

type ScopeInspection struct {
	Scope              Scope
	ScopeRevision      int64
	ActiveDependencies []Dependency
	OpenBlockers       []Blocker
	ReceiptRefs        []ReceiptRef
	NextReceiptCursor  string
}

func (s *CoordinationService) InspectScope(ctx context.Context, actor CoordinationActor, scopeID pgtype.UUID, receiptCursor string) (ScopeInspection, error) {
	if err := validateActor(actor); err != nil {
		return ScopeInspection{}, err
	}
	if !scopeID.Valid {
		return ScopeInspection{}, coordinationErr(CoordinationInvalidPayload, "scope id is required", nil)
	}
	decoded, err := decodeCoordinationReceiptCursor(receiptCursor)
	if err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInvalidPayload, "invalid coordination receipt cursor", err)
	}
	if decoded != nil && (!uuidEqual(decoded.WorkspaceID, actor.WorkspaceID) || !uuidEqual(decoded.ScopeID, scopeID)) {
		return ScopeInspection{}, coordinationErr(CoordinationInvalidPayload, "coordination receipt cursor does not match this scope", nil)
	}
	if s == nil || s.Queries == nil || s.Pool == nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "coordination service is not configured for consistent inspection", nil)
	}

	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not start coordination inspection transaction", err)
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

	scopeRow, err := s.loadDependencyScope(ctx, qtx, actor, scopeID)
	if err != nil {
		return ScopeInspection{}, err
	}
	if decoded != nil && decoded.ScopeRevision != scopeRow.Revision {
		return ScopeInspection{}, coordinationErr(CoordinationRevisionConflict, "coordination scope revision changed", nil)
	}

	dependencyRows, err := qtx.ListActiveCoordinationDependenciesByScope(ctx, db.ListActiveCoordinationDependenciesByScopeParams{
		WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID, LimitRows: CoordinationDependencyCapacity + 1,
	})
	if err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not inspect coordination dependencies", err)
	}
	if len(dependencyRows) > CoordinationDependencyCapacity {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "coordination dependency capacity invariant was violated", nil)
	}
	dependencies := make([]Dependency, 0, len(dependencyRows))
	for _, row := range dependencyRows {
		dependencies = append(dependencies, dependencyFromRow(row))
	}

	blockerRows, err := qtx.ListCoordinationRecordsByScopeStatus(ctx, db.ListCoordinationRecordsByScopeStatusParams{
		WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID, StatusFilter: "open", LimitRows: CoordinationBlockerCapacity + 1,
	})
	if err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not inspect coordination blockers", err)
	}
	if len(blockerRows) > CoordinationBlockerCapacity {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "coordination blocker capacity invariant was violated", nil)
	}
	evidenceByRecord, err := loadBlockerEvidenceRefsForRows(ctx, qtx, actor.WorkspaceID, scopeID, blockerRows)
	if err != nil {
		return ScopeInspection{}, err
	}
	blockers := make([]Blocker, 0, len(blockerRows))
	for _, row := range blockerRows {
		evidence := evidenceByRecord[row.ID.Bytes]
		blockers = append(blockers, blockerFromRow(row, evidence.Create, evidence.Resolution))
	}

	maxOrdinal, err := qtx.GetMaxCoordinationReceiptOrdinalByScope(ctx, db.GetMaxCoordinationReceiptOrdinalByScopeParams{
		WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID,
	})
	if err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not inspect coordination receipt window", err)
	}
	upperOrdinal := maxOrdinal
	beforeOrdinal := pgtype.Int8{}
	if decoded != nil {
		if decoded.UpperOrdinal > maxOrdinal {
			return ScopeInspection{}, coordinationErr(CoordinationInvalidPayload, "coordination receipt cursor is outside the committed window", nil)
		}
		upperOrdinal = decoded.UpperOrdinal
		beforeOrdinal = pgtype.Int8{Int64: decoded.LastOrdinal, Valid: true}
	}
	receiptRows, err := qtx.ListCoordinationReceiptWindow(ctx, db.ListCoordinationReceiptWindowParams{
		WorkspaceID: actor.WorkspaceID, CoordinationScopeID: scopeID, UpperOrdinal: upperOrdinal,
		BeforeOrdinal: beforeOrdinal, LimitRows: CoordinationReceiptPageSize + 1,
	})
	if err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not inspect coordination receipts", err)
	}
	hasMore := len(receiptRows) > CoordinationReceiptPageSize
	if hasMore {
		receiptRows = receiptRows[:CoordinationReceiptPageSize]
	}
	receipts := make([]ReceiptRef, 0, len(receiptRows))
	var previousOrdinal int64
	for index, row := range receiptRows {
		ref, err := coordinationReceiptRefFromRow(row, scopeRow.Revision, upperOrdinal)
		if err != nil {
			return ScopeInspection{}, err
		}
		if index > 0 && ref.ReceiptOrdinal >= previousOrdinal {
			return ScopeInspection{}, coordinationErr(CoordinationInternal, "coordination receipt order is inconsistent", nil)
		}
		previousOrdinal = ref.ReceiptOrdinal
		receipts = append(receipts, ref)
	}
	nextCursor := ""
	if hasMore {
		last := receipts[len(receipts)-1]
		nextCursor, err = encodeCoordinationReceiptCursor(coordinationReceiptCursor{
			WorkspaceID: actor.WorkspaceID, ScopeID: scopeID, ScopeRevision: scopeRow.Revision,
			UpperOrdinal: upperOrdinal, LastOrdinal: last.ReceiptOrdinal,
		})
		if err != nil {
			return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not encode coordination receipt cursor", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ScopeInspection{}, coordinationErr(CoordinationInternal, "could not commit coordination inspection transaction", err)
	}
	committed = true
	scope := scopeFromRow(scopeRow)
	return ScopeInspection{
		Scope: scope, ScopeRevision: scope.Revision, ActiveDependencies: dependencies, OpenBlockers: blockers,
		ReceiptRefs: receipts, NextReceiptCursor: nextCursor,
	}, nil
}

func coordinationReceiptRefFromRow(row db.CoordinationReceipt, scopeRevision, upperOrdinal int64) (ReceiptRef, error) {
	if !row.ID.Valid || !row.ResourceID.Valid || row.ReceiptOrdinal <= 0 || row.ReceiptOrdinal > upperOrdinal ||
		row.RevisionBefore < 0 || row.RevisionAfter < row.RevisionBefore || row.RevisionAfter > scopeRevision || !row.CreatedAt.Valid {
		return ReceiptRef{}, coordinationErr(CoordinationInternal, "coordination receipt reference is inconsistent", nil)
	}
	if row.ActorType != CoordinationActorMember && row.ActorType != CoordinationActorAgent {
		return ReceiptRef{}, coordinationErr(CoordinationInternal, "coordination receipt actor type is invalid", nil)
	}
	validPair := false
	switch row.Operation {
	case CoordinationOperationEnsureScope:
		validPair = row.ResourceType == CoordinationResourceScope
	case CoordinationOperationAddDependency, CoordinationOperationResolveDependency:
		validPair = row.ResourceType == CoordinationResourceDependency
	case CoordinationOperationAppendBlocker, CoordinationOperationResolveBlocker:
		validPair = row.ResourceType == CoordinationResourceBlocker
	}
	if !validPair {
		return ReceiptRef{}, coordinationErr(CoordinationInternal, "coordination receipt operation is not allowlisted", nil)
	}
	return ReceiptRef{
		ID: row.ID, ReceiptOrdinal: row.ReceiptOrdinal, Operation: row.Operation, ResourceType: row.ResourceType,
		ResourceID: row.ResourceID, RevisionBefore: row.RevisionBefore, RevisionAfter: row.RevisionAfter,
		ActorType: row.ActorType, CreatedAt: row.CreatedAt.Time,
	}, nil
}

type coordinationReceiptCursor struct {
	WorkspaceID   pgtype.UUID
	ScopeID       pgtype.UUID
	ScopeRevision int64
	UpperOrdinal  int64
	LastOrdinal   int64
}

type coordinationReceiptCursorDTO struct {
	Version       int    `json:"v"`
	Collection    string `json:"collection"`
	WorkspaceID   string `json:"workspace_id"`
	ScopeID       string `json:"scope_id"`
	ScopeRevision int64  `json:"scope_revision"`
	UpperOrdinal  int64  `json:"upper_ordinal"`
	LastOrdinal   int64  `json:"last_ordinal"`
}

func encodeCoordinationReceiptCursor(value coordinationReceiptCursor) (string, error) {
	if !value.WorkspaceID.Valid || !value.ScopeID.Valid || value.ScopeRevision < 0 || value.UpperOrdinal <= 0 ||
		value.LastOrdinal <= 0 || value.LastOrdinal > value.UpperOrdinal {
		return "", errors.New("invalid coordination receipt cursor state")
	}
	raw, err := json.Marshal(coordinationReceiptCursorDTO{
		Version: coordinationReceiptCursorV1, Collection: coordinationReceiptCollectionKey,
		WorkspaceID: util.UUIDToString(value.WorkspaceID), ScopeID: util.UUIDToString(value.ScopeID),
		ScopeRevision: value.ScopeRevision, UpperOrdinal: value.UpperOrdinal, LastOrdinal: value.LastOrdinal,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCoordinationReceiptCursor(raw string) (*coordinationReceiptCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 3000 {
		return nil, errors.New("coordination receipt cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > 2048 {
		return nil, errors.New("invalid coordination receipt cursor encoding")
	}
	if err := validateCoordinationReceiptCursorShape(decoded); err != nil {
		return nil, err
	}
	var dto coordinationReceiptCursorDTO
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid trailing coordination receipt cursor data")
	}
	if dto.Version != coordinationReceiptCursorV1 || dto.Collection != coordinationReceiptCollectionKey || dto.ScopeRevision < 0 ||
		dto.UpperOrdinal <= 0 || dto.LastOrdinal <= 0 || dto.LastOrdinal > dto.UpperOrdinal {
		return nil, errors.New("unsupported coordination receipt cursor")
	}
	workspaceID, err := util.ParseUUID(dto.WorkspaceID)
	if err != nil || util.UUIDToString(workspaceID) != dto.WorkspaceID {
		return nil, errors.New("invalid coordination receipt cursor workspace")
	}
	scopeID, err := util.ParseUUID(dto.ScopeID)
	if err != nil || util.UUIDToString(scopeID) != dto.ScopeID {
		return nil, errors.New("invalid coordination receipt cursor scope")
	}
	return &coordinationReceiptCursor{
		WorkspaceID: workspaceID, ScopeID: scopeID, ScopeRevision: dto.ScopeRevision,
		UpperOrdinal: dto.UpperOrdinal, LastOrdinal: dto.LastOrdinal,
	}, nil
}

func validateCoordinationReceiptCursorShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return errors.New("coordination receipt cursor must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid coordination receipt cursor key")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate coordination receipt cursor key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing coordination receipt cursor data")
		}
		return err
	}
	return nil
}
