package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

type coordinationEvidenceRefRequest struct {
	Kind *string `json:"kind"`
	ID   *string `json:"id"`
}

type coordinationBlockerPayloadRequest struct {
	ReasonCode   *string                           `json:"reason_code"`
	EvidenceRefs *[]coordinationEvidenceRefRequest `json:"evidence_refs"`
}

type coordinationAppendBlockerRequest struct {
	ExpectedRevision  *int64                             `json:"expected_revision"`
	DownstreamIssueID *string                            `json:"downstream_issue_id"`
	UpstreamIssueID   *string                            `json:"upstream_issue_id"`
	DependencyID      *string                            `json:"dependency_id"`
	SchemaVersion     *int32                             `json:"schema_version"`
	Payload           *coordinationBlockerPayloadRequest `json:"payload"`
}

type coordinationBlockerResolutionRequest struct {
	ResolutionCode *string                           `json:"resolution_code"`
	EvidenceRefs   *[]coordinationEvidenceRefRequest `json:"evidence_refs"`
}

type coordinationResolveBlockerRequest struct {
	ExpectedRevision *int64                                `json:"expected_revision"`
	SchemaVersion    *int32                                `json:"schema_version"`
	Resolution       *coordinationBlockerResolutionRequest `json:"resolution"`
}

type coordinationBlockerActorDTO struct {
	Type   string  `json:"type"`
	ID     string  `json:"id"`
	TaskID *string `json:"task_id"`
}

type coordinationEvidenceRefDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type coordinationBlockerDTO struct {
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
	CreateEvidenceRefs     []coordinationEvidenceRefDTO `json:"create_evidence_refs"`
	ResolutionEvidenceRefs []coordinationEvidenceRefDTO `json:"resolution_evidence_refs"`
	CreatedBy              coordinationBlockerActorDTO  `json:"created_by"`
	ResolvedBy             *coordinationBlockerActorDTO `json:"resolved_by"`
	CreatedAt              string                       `json:"created_at"`
	ResolvedAt             *string                      `json:"resolved_at"`
}

type coordinationBlockerMutationResponse struct {
	Receipt       coordinationReceiptDTO `json:"receipt"`
	Resource      coordinationBlockerDTO `json:"resource"`
	ScopeRevision int64                  `json:"scope_revision"`
	Changed       bool                   `json:"changed"`
	Replayed      bool                   `json:"replayed"`
}

type coordinationBlockerPageResponse struct {
	ScopeID       string                   `json:"scope_id"`
	ScopeRevision int64                    `json:"scope_revision"`
	StatusFilter  string                   `json:"status_filter"`
	Items         []coordinationBlockerDTO `json:"items"`
	NextCursor    *string                  `json:"next_cursor"`
}

func (h *Handler) AppendCoordinationBlocker(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scopeId must be a UUID")
		return
	}
	var request coordinationAppendBlockerRequest
	if err := decodeCoordinationJSON(r.Body, &request); err != nil || request.ExpectedRevision == nil ||
		request.DownstreamIssueID == nil || request.UpstreamIssueID == nil || request.SchemaVersion == nil || request.Payload == nil ||
		request.Payload.ReasonCode == nil || request.Payload.EvidenceRefs == nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid blocker append request")
		return
	}
	downstreamID, err := util.ParseUUID(*request.DownstreamIssueID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "downstream_issue_id must be a UUID")
		return
	}
	upstreamID, err := util.ParseUUID(*request.UpstreamIssueID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "upstream_issue_id must be a UUID")
		return
	}
	var dependencyID string
	if request.DependencyID != nil {
		if *request.DependencyID == "" {
			writeCoordinationError(w, service.CoordinationInvalidPayload, "dependency_id must be null or a UUID")
			return
		}
		dependencyID = *request.DependencyID
	}
	parsedDependencyID, err := parseOptionalCoordinationUUID(dependencyID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "dependency_id must be null or a UUID")
		return
	}
	refs, err := parseCoordinationEvidenceRefs(*request.Payload.EvidenceRefs)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid blocker evidence references")
		return
	}
	result, err := h.CoordinationService.AppendBlocker(r.Context(), actor, service.AppendBlockerInput{
		ScopeID: scopeID, ExpectedRevision: *request.ExpectedRevision, DownstreamIssueID: downstreamID, UpstreamIssueID: upstreamID,
		DependencyID: parsedDependencyID, SchemaVersion: *request.SchemaVersion, ReasonCode: *request.Payload.ReasonCode,
		EvidenceRefs: refs, IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	status := http.StatusOK
	if result.Outcome == service.CoordinationOutcomeCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, coordinationBlockerMutationResponse{
		Receipt: coordinationReceiptResponse(result.Receipt), Resource: blockerResponse(result.Blocker), ScopeRevision: result.ScopeRevision,
		Changed: result.Changed, Replayed: result.Outcome == service.CoordinationOutcomeReplay,
	})
}

func (h *Handler) ResolveCoordinationBlocker(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scopeId must be a UUID")
		return
	}
	blockerID, err := util.ParseUUID(chi.URLParam(r, "recordId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "recordId must be a UUID")
		return
	}
	var request coordinationResolveBlockerRequest
	if err := decodeCoordinationJSON(r.Body, &request); err != nil || request.ExpectedRevision == nil || request.SchemaVersion == nil ||
		request.Resolution == nil || request.Resolution.ResolutionCode == nil || request.Resolution.EvidenceRefs == nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid blocker resolve request")
		return
	}
	refs, err := parseCoordinationEvidenceRefs(*request.Resolution.EvidenceRefs)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid blocker evidence references")
		return
	}
	result, err := h.CoordinationService.ResolveBlocker(r.Context(), actor, service.ResolveBlockerInput{
		ScopeID: scopeID, BlockerID: blockerID, ExpectedRevision: *request.ExpectedRevision, SchemaVersion: *request.SchemaVersion,
		ResolutionCode: *request.Resolution.ResolutionCode, EvidenceRefs: refs, IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, coordinationBlockerMutationResponse{
		Receipt: coordinationReceiptResponse(result.Receipt), Resource: blockerResponse(result.Blocker), ScopeRevision: result.ScopeRevision,
		Changed: result.Changed, Replayed: result.Outcome == service.CoordinationOutcomeReplay,
	})
}

func (h *Handler) ListCoordinationBlockers(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scopeId must be a UUID")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid blocker list query")
		return
	}
	for key, values := range query {
		if (key != "status" && key != "cursor" && key != "limit") || len(values) != 1 {
			writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid blocker list query")
			return
		}
	}
	status := "open"
	if values, present := query["status"]; present {
		status = values[0]
	}
	limit := service.CoordinationBlockerPageLimit
	if values, present := query["limit"]; present {
		parsed, err := strconv.ParseInt(values[0], 10, 32)
		if err != nil || parsed < 1 || parsed > service.CoordinationBlockerPageLimit {
			writeCoordinationError(w, service.CoordinationInvalidPayload, "limit must be between 1 and 100")
			return
		}
		limit = int(parsed)
	}
	cursor := ""
	if values, present := query["cursor"]; present {
		cursor = values[0]
	}
	page, err := h.CoordinationService.ListBlockers(r.Context(), actor, scopeID, status, cursor, limit)
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	items := make([]coordinationBlockerDTO, 0, len(page.Blockers))
	for _, blocker := range page.Blockers {
		items = append(items, blockerResponse(blocker))
	}
	var nextCursor *string
	if page.NextCursor != "" {
		nextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, coordinationBlockerPageResponse{
		ScopeID: util.UUIDToString(scopeID), ScopeRevision: page.ScopeRevision, StatusFilter: page.StatusFilter,
		Items: items, NextCursor: nextCursor,
	})
}

func parseOptionalCoordinationUUID(raw string) (pgtype.UUID, error) {
	if raw == "" {
		return pgtype.UUID{}, nil
	}
	return util.ParseUUID(raw)
}

func parseCoordinationEvidenceRefs(items []coordinationEvidenceRefRequest) ([]service.CoordinationEvidenceRef, error) {
	refs := make([]service.CoordinationEvidenceRef, 0, len(items))
	for _, item := range items {
		if item.Kind == nil || item.ID == nil {
			return nil, strconv.ErrSyntax
		}
		id, err := util.ParseUUID(*item.ID)
		if err != nil {
			return nil, err
		}
		refs = append(refs, service.CoordinationEvidenceRef{Kind: *item.Kind, ID: id})
	}
	return refs, nil
}

func blockerResponse(blocker service.Blocker) coordinationBlockerDTO {
	created := blockerActorResponse(blocker.CreatedByType, blocker.CreatedByID, blocker.CreatedTaskID)
	result := coordinationBlockerDTO{
		ID: util.UUIDToString(blocker.ID), WorkspaceID: util.UUIDToString(blocker.WorkspaceID), ScopeID: util.UUIDToString(blocker.CoordinationScopeID),
		Kind: blocker.Kind, SchemaVersion: blocker.SchemaVersion, Status: blocker.Status, RootIssueID: util.UUIDToString(blocker.RootIssueID),
		DownstreamIssueID: util.UUIDToString(blocker.DownstreamIssueID), UpstreamIssueID: util.UUIDToString(blocker.UpstreamIssueID),
		ReasonCode: blocker.ReasonCode, CreateEvidenceRefs: blockerEvidenceResponse(blocker.CreateEvidenceRefs),
		ResolutionEvidenceRefs: blockerEvidenceResponse(blocker.ResolutionEvidenceRefs), CreatedBy: created,
		CreatedAt: blocker.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if blocker.DependencyID.Valid {
		value := util.UUIDToString(blocker.DependencyID)
		result.DependencyID = &value
	}
	if blocker.Status == "resolved" {
		code := blocker.ResolutionCode
		result.ResolutionCode = &code
		resolved := blockerActorResponse(blocker.ResolvedByType, blocker.ResolvedByID, blocker.ResolvedTaskID)
		result.ResolvedBy = &resolved
		resolvedAt := blocker.ResolvedAt.UTC().Format(time.RFC3339Nano)
		result.ResolvedAt = &resolvedAt
	}
	return result
}

func blockerActorResponse(actorType string, actorID, taskID pgtype.UUID) coordinationBlockerActorDTO {
	result := coordinationBlockerActorDTO{Type: actorType, ID: util.UUIDToString(actorID)}
	if taskID.Valid {
		value := util.UUIDToString(taskID)
		result.TaskID = &value
	}
	return result
}

func blockerEvidenceResponse(refs []service.CoordinationEvidenceRef) []coordinationEvidenceRefDTO {
	result := make([]coordinationEvidenceRefDTO, 0, len(refs))
	for _, ref := range refs {
		result = append(result, coordinationEvidenceRefDTO{Kind: ref.Kind, ID: util.UUIDToString(ref.ID)})
	}
	return result
}
