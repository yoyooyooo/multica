package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

type coordinationAddDependencyRequest struct {
	ExpectedRevision  *int64 `json:"expected_revision"`
	DownstreamIssueID string `json:"downstream_issue_id"`
	UpstreamIssueID   string `json:"upstream_issue_id"`
}

type coordinationResolveDependencyRequest struct {
	ExpectedRevision *int64 `json:"expected_revision"`
}

type coordinationDependencyDTO struct {
	ID                  string                    `json:"id"`
	WorkspaceID         string                    `json:"workspace_id"`
	CoordinationScopeID string                    `json:"coordination_scope_id"`
	DownstreamIssueID   string                    `json:"downstream_issue_id"`
	UpstreamIssueID     string                    `json:"upstream_issue_id"`
	BlocksIssueID       string                    `json:"blocks_issue_id"`
	CreatedBy           coordinationCreatedByDTO  `json:"created_by"`
	CreatedAt           string                    `json:"created_at"`
	ResolvedBy          *coordinationCreatedByDTO `json:"resolved_by"`
	ResolvedAt          *string                   `json:"resolved_at"`
}

type coordinationDependencyMutationResponse struct {
	Dependency    coordinationDependencyDTO `json:"dependency"`
	ScopeRevision int64                     `json:"scope_revision"`
	Receipt       coordinationReceiptDTO    `json:"receipt"`
	Outcome       string                    `json:"outcome"`
}

type coordinationDependencyPageResponse struct {
	Dependencies  []coordinationDependencyDTO `json:"dependencies"`
	ScopeRevision int64                       `json:"scope_revision"`
	NextCursor    *string                     `json:"next_cursor"`
}

func (h *Handler) AddCoordinationDependency(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scopeId must be a UUID")
		return
	}
	var request coordinationAddDependencyRequest
	if err := decodeCoordinationJSON(r.Body, &request); err != nil || request.ExpectedRevision == nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid dependency request")
		return
	}
	downstreamID, err := util.ParseUUID(request.DownstreamIssueID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "downstream_issue_id must be a UUID")
		return
	}
	upstreamID, err := util.ParseUUID(request.UpstreamIssueID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "upstream_issue_id must be a UUID")
		return
	}
	result, err := h.CoordinationService.AddDependency(r.Context(), actor, service.AddDependencyInput{
		ScopeID: scopeID, ExpectedRevision: *request.ExpectedRevision,
		DownstreamIssueID: downstreamID, UpstreamIssueID: upstreamID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	status := http.StatusOK
	if result.Outcome == service.CoordinationOutcomeCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, coordinationDependencyMutationResponse{
		Dependency: dependencyResponse(result.Dependency), ScopeRevision: result.ScopeRevision,
		Receipt: coordinationReceiptResponse(result.Receipt), Outcome: result.Outcome,
	})
}

func (h *Handler) ResolveCoordinationDependency(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scopeId must be a UUID")
		return
	}
	dependencyID, err := util.ParseUUID(chi.URLParam(r, "dependencyId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "dependencyId must be a UUID")
		return
	}
	var request coordinationResolveDependencyRequest
	if err := decodeCoordinationJSON(r.Body, &request); err != nil || request.ExpectedRevision == nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid dependency resolve request")
		return
	}
	result, err := h.CoordinationService.ResolveDependency(r.Context(), actor, service.ResolveDependencyInput{
		ScopeID: scopeID, DependencyID: dependencyID, ExpectedRevision: *request.ExpectedRevision,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, coordinationDependencyMutationResponse{
		Dependency: dependencyResponse(result.Dependency), ScopeRevision: result.ScopeRevision,
		Receipt: coordinationReceiptResponse(result.Receipt), Outcome: result.Outcome,
	})
}

func (h *Handler) ListCoordinationDependencies(w http.ResponseWriter, r *http.Request) {
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
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid dependency list query")
		return
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid dependency list query")
			return
		}
	}
	limit := service.CoordinationDependencyPageLimit
	if values, present := query["limit"]; present {
		parsed, err := strconv.ParseInt(values[0], 10, 32)
		if err != nil || parsed < 1 || parsed > service.CoordinationDependencyPageLimit {
			writeCoordinationError(w, service.CoordinationInvalidPayload, "limit must be between 1 and 100")
			return
		}
		limit = int(parsed)
	}
	cursor := ""
	if values, present := query["cursor"]; present {
		cursor = values[0]
	}
	page, err := h.CoordinationService.ListDependencies(r.Context(), actor, scopeID, cursor, limit)
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	items := make([]coordinationDependencyDTO, 0, len(page.Dependencies))
	for _, dependency := range page.Dependencies {
		items = append(items, dependencyResponse(dependency))
	}
	var nextCursor *string
	if page.NextCursor != "" {
		nextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, coordinationDependencyPageResponse{Dependencies: items, ScopeRevision: page.ScopeRevision, NextCursor: nextCursor})
}

func dependencyResponse(dependency service.Dependency) coordinationDependencyDTO {
	created := coordinationCreatedByDTO{ActorType: dependency.CreatedByType, ActorID: util.UUIDToString(dependency.CreatedByID)}
	if dependency.CreatedTaskID.Valid {
		value := util.UUIDToString(dependency.CreatedTaskID)
		created.TaskID = &value
	}
	result := coordinationDependencyDTO{
		ID: util.UUIDToString(dependency.ID), WorkspaceID: util.UUIDToString(dependency.WorkspaceID),
		CoordinationScopeID: util.UUIDToString(dependency.CoordinationScopeID),
		DownstreamIssueID:   util.UUIDToString(dependency.DownstreamIssueID), UpstreamIssueID: util.UUIDToString(dependency.UpstreamIssueID),
		BlocksIssueID: util.UUIDToString(dependency.DownstreamIssueID), CreatedBy: created,
		CreatedAt: dependency.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if dependency.Resolved {
		resolved := coordinationCreatedByDTO{ActorType: dependency.ResolvedByType, ActorID: util.UUIDToString(dependency.ResolvedByID)}
		if dependency.ResolvedTaskID.Valid {
			value := util.UUIDToString(dependency.ResolvedTaskID)
			resolved.TaskID = &value
		}
		resolvedAt := dependency.ResolvedAt.UTC().Format(time.RFC3339Nano)
		result.ResolvedBy = &resolved
		result.ResolvedAt = &resolvedAt
	}
	return result
}
