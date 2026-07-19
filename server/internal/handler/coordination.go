package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

type coordinationEnsureRequest struct {
	RootIssueID        string `json:"root_issue_id"`
	WorkflowProfileKey string `json:"workflow_profile_key"`
}

type coordinationCreatedByDTO struct {
	ActorType string  `json:"actor_type"`
	ActorID   string  `json:"actor_id"`
	TaskID    *string `json:"task_id"`
}

type coordinationScopeDTO struct {
	ID                 string                   `json:"id"`
	WorkspaceID        string                   `json:"workspace_id"`
	ScopeKind          string                   `json:"scope_kind"`
	State              string                   `json:"state"`
	RootIssueID        string                   `json:"root_issue_id"`
	WorkflowProfileKey string                   `json:"workflow_profile_key"`
	Revision           int64                    `json:"revision"`
	CreatedBy          coordinationCreatedByDTO `json:"created_by"`
	CreatedAt          string                   `json:"created_at"`
	UpdatedAt          string                   `json:"updated_at"`
}

type coordinationReceiptDTO struct {
	ID             string `json:"id"`
	ReceiptOrdinal int64  `json:"receipt_ordinal"`
	Operation      string `json:"operation"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id"`
	RevisionBefore int64  `json:"revision_before"`
	RevisionAfter  int64  `json:"revision_after"`
	CreatedAt      string `json:"created_at"`
}

type coordinationErrorBody struct {
	Error coordinationErrorEnvelope `json:"error"`
}

type coordinationErrorEnvelope struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (h *Handler) EnsureCoordinationScope(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	var req coordinationEnsureRequest
	if err := decodeCoordinationJSON(r.Body, &req); err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "invalid coordination request")
		return
	}
	rootID, err := util.ParseUUID(req.RootIssueID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "root_issue_id must be a UUID")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	result, err := h.CoordinationService.EnsureScope(r.Context(), actor, service.EnsureScopeInput{
		RootIssueID: rootID, WorkflowProfileKey: req.WorkflowProfileKey, IdempotencyKey: key,
	})
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	status := http.StatusOK
	if result.Outcome == service.CoordinationOutcomeCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"scope": coordinationScopeResponse(result.Scope), "receipt": coordinationReceiptResponse(result.Receipt)})
}

func (h *Handler) GetCoordinationScope(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	scopeID, err := util.ParseUUID(chi.URLParam(r, "scopeId"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "scope id must be a UUID")
		return
	}
	scope, err := h.CoordinationService.GetScope(r.Context(), actor, scopeID)
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": coordinationScopeResponse(scope)})
}

func (h *Handler) GetCoordinationScopeByRoot(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coordinationActor(w, r)
	if !ok {
		return
	}
	rootID, err := util.ParseUUID(r.URL.Query().Get("root_issue_id"))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "root_issue_id must be a UUID")
		return
	}
	scope, err := h.CoordinationService.GetScopeByRoot(r.Context(), actor, rootID, r.URL.Query().Get("workflow_profile_key"))
	if err != nil {
		h.writeCoordinationServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": coordinationScopeResponse(scope)})
}

func (h *Handler) coordinationActor(w http.ResponseWriter, r *http.Request) (service.CoordinationActor, bool) {
	workspaceID, err := util.ParseUUID(strings.TrimSpace(h.resolveWorkspaceID(r)))
	if err != nil {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "workspace is required")
		return service.CoordinationActor{}, false
	}
	return h.coordinationActorForWorkspace(w, r, workspaceID)
}

func (h *Handler) coordinationActorForWorkspace(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID) (service.CoordinationActor, bool) {
	if h.CoordinationService == nil {
		writeCoordinationError(w, service.CoordinationInternal, "coordination service is unavailable")
		return service.CoordinationActor{}, false
	}
	if !workspaceID.Valid {
		writeCoordinationError(w, service.CoordinationInvalidPayload, "workspace is required")
		return service.CoordinationActor{}, false
	}
	if credentialRef, ok := middleware.TaskTokenCredentialRefFromContext(r.Context()); ok {
		agentID, agentErr := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Agent-ID")))
		taskID, taskErr := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Task-ID")))
		if agentErr != nil || taskErr != nil {
			writeCoordinationError(w, service.CoordinationForbidden, "task identity is invalid")
			return service.CoordinationActor{}, false
		}
		return service.CoordinationActor{WorkspaceID: workspaceID, ActorType: service.CoordinationActorAgent, ActorID: agentID, TaskID: taskID, TaskCredentialRef: credentialRef}, true
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return service.CoordinationActor{}, false
	}
	actorID, err := util.ParseUUID(userID)
	if err != nil {
		writeCoordinationError(w, service.CoordinationForbidden, "member identity is invalid")
		return service.CoordinationActor{}, false
	}
	return service.CoordinationActor{WorkspaceID: workspaceID, ActorType: service.CoordinationActorMember, ActorID: actorID}, true
}

func decodeCoordinationJSON(body io.Reader, dst any) error {
	data, err := io.ReadAll(io.LimitReader(body, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return errors.New("invalid body")
	}
	probe := json.NewDecoder(bytes.NewReader(data))
	first, err := probe.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("body must be an object")
	}
	if err := consumeCoordinationJSONValue(probe, first); err != nil {
		return err
	}
	if _, err := probe.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	if err := validateExactCoordinationJSONFields(data, reflect.TypeOf(dst)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func validateExactCoordinationJSONFields(data []byte, target reflect.Type) error {
	if target == nil || target.Kind() != reflect.Pointer {
		return errors.New("coordination JSON target must be a pointer")
	}
	return validateExactCoordinationJSONValue(data, target.Elem())
}

func validateExactCoordinationJSONValue(data []byte, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	jsonUnmarshaler := reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	if target.Implements(jsonUnmarshaler) || reflect.PointerTo(target).Implements(jsonUnmarshaler) {
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		fields := make(map[string]reflect.Type, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for name, raw := range object {
			fieldType, ok := fields[name]
			if !ok {
				return errors.New("unknown coordination JSON field")
			}
			if err := validateExactCoordinationJSONValue(raw, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		for _, raw := range items {
			if err := validateExactCoordinationJSONValue(raw, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func consumeCoordinationJSONValue(decoder *json.Decoder, token json.Token) error {
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeCoordinationJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeCoordinationJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func (h *Handler) writeCoordinationServiceError(w http.ResponseWriter, err error) {
	var coordinationErr *service.CoordinationError
	if !errors.As(err, &coordinationErr) {
		writeCoordinationError(w, service.CoordinationInternal, "coordination request failed")
		return
	}
	writeCoordinationError(w, coordinationErr.Code, coordinationErr.Msg)
}

func writeCoordinationError(w http.ResponseWriter, code service.CoordinationErrorCode, message string) {
	status := http.StatusInternalServerError
	switch code {
	case service.CoordinationNotFound:
		status = http.StatusNotFound
	case service.CoordinationCrossWorkspace, service.CoordinationForbidden:
		status = http.StatusForbidden
	case service.CoordinationInvalidPayload:
		status = http.StatusBadRequest
	case service.CoordinationSelfDependency, service.CoordinationCycle:
		status = http.StatusUnprocessableEntity
	case service.CoordinationCapacityExceeded, service.CoordinationRevisionConflict, service.CoordinationIdempotencyConflict, service.CoordinationDependencyScopeConflict, service.CoordinationDeleteBlocked:
		status = http.StatusConflict
	}
	writeJSON(w, status, coordinationErrorBody{Error: coordinationErrorEnvelope{Code: string(code), Message: message}})
}

func coordinationScopeResponse(scope service.Scope) coordinationScopeDTO {
	var taskID *string
	if scope.CreatedTaskID.Valid {
		value := util.UUIDToString(scope.CreatedTaskID)
		taskID = &value
	}
	return coordinationScopeDTO{
		ID: util.UUIDToString(scope.ID), WorkspaceID: util.UUIDToString(scope.WorkspaceID), ScopeKind: scope.ScopeKind,
		State: scope.State, RootIssueID: util.UUIDToString(scope.RootIssueID), WorkflowProfileKey: scope.WorkflowProfileKey,
		Revision: scope.Revision, CreatedBy: coordinationCreatedByDTO{ActorType: scope.CreatedByType, ActorID: util.UUIDToString(scope.CreatedByID), TaskID: taskID},
		CreatedAt: scope.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: scope.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func coordinationReceiptResponse(receipt service.Receipt) coordinationReceiptDTO {
	return coordinationReceiptDTO{ID: util.UUIDToString(receipt.ID), ReceiptOrdinal: receipt.ReceiptOrdinal,
		Operation: receipt.Operation, ResourceType: receipt.ResourceType, ResourceID: util.UUIDToString(receipt.ResourceID),
		RevisionBefore: receipt.RevisionBefore, RevisionAfter: receipt.RevisionAfter, CreatedAt: receipt.CreatedAt.UTC().Format(time.RFC3339Nano)}
}
