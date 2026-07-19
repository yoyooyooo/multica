package handler

import (
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

type coordinationReceiptRefDTO struct {
	ID             string `json:"id"`
	ReceiptOrdinal int64  `json:"receipt_ordinal"`
	Operation      string `json:"operation"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id"`
	RevisionBefore int64  `json:"revision_before"`
	RevisionAfter  int64  `json:"revision_after"`
	ActorType      string `json:"actor_type"`
	CreatedAt      string `json:"created_at"`
}

type coordinationScopeInspectionResponse struct {
	Scope              coordinationScopeDTO        `json:"scope"`
	ScopeRevision      int64                       `json:"scope_revision"`
	ActiveDependencies []coordinationDependencyDTO `json:"active_dependencies"`
	OpenBlockers       []coordinationBlockerDTO    `json:"open_blockers"`
	ReceiptRefs        []coordinationReceiptRefDTO `json:"receipt_refs"`
	NextReceiptCursor  *string                     `json:"next_receipt_cursor"`
}

func (h *Handler) InspectCoordinationScope(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scopeId must be a UUID")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid coordination inspect query")
		return
	}
	for key := range values {
		if key != "receipt_cursor" {
			writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid coordination inspect query")
			return
		}
	}
	cursorValues := values["receipt_cursor"]
	if len(cursorValues) > 1 {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "receipt_cursor must appear at most once")
		return
	}
	receiptCursor := ""
	if len(cursorValues) == 1 {
		receiptCursor = cursorValues[0]
	}
	inspection, err := h.CoordinationService.InspectScope(r.Context(), actor, scopeID, receiptCursor)
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, coordinationInspectionResponse(inspection))
}

func coordinationInspectionResponse(inspection service.ScopeInspection) coordinationScopeInspectionResponse {
	dependencies := make([]coordinationDependencyDTO, 0, len(inspection.ActiveDependencies))
	for _, dependency := range inspection.ActiveDependencies {
		dependencies = append(dependencies, dependencyResponse(dependency))
	}
	blockers := make([]coordinationBlockerDTO, 0, len(inspection.OpenBlockers))
	for _, blocker := range inspection.OpenBlockers {
		blockers = append(blockers, blockerResponse(blocker))
	}
	receipts := make([]coordinationReceiptRefDTO, 0, len(inspection.ReceiptRefs))
	for _, receipt := range inspection.ReceiptRefs {
		receipts = append(receipts, coordinationReceiptRefDTO{
			ID: util.UUIDToString(receipt.ID), ReceiptOrdinal: receipt.ReceiptOrdinal, Operation: receipt.Operation,
			ResourceType: receipt.ResourceType, ResourceID: util.UUIDToString(receipt.ResourceID),
			RevisionBefore: receipt.RevisionBefore, RevisionAfter: receipt.RevisionAfter,
			ActorType: receipt.ActorType, CreatedAt: receipt.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	var nextCursor *string
	if inspection.NextReceiptCursor != "" {
		cursor := inspection.NextReceiptCursor
		nextCursor = &cursor
	}
	return coordinationScopeInspectionResponse{
		Scope: coordinationScopeResponse(inspection.Scope), ScopeRevision: inspection.ScopeRevision,
		ActiveDependencies: dependencies, OpenBlockers: blockers, ReceiptRefs: receipts, NextReceiptCursor: nextCursor,
	}
}
