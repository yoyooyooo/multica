package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const currentExecutionContextSchema = "multica.current-execution-context.v1"

type currentExecutionContextResponse struct {
	Schema      string                    `json:"schema"`
	ObservedAt  string                    `json:"observed_at"`
	Workspace   currentExecutionWorkspace `json:"workspace"`
	Agent       currentExecutionAgent     `json:"agent"`
	Task        currentExecutionTask      `json:"task"`
	Run         currentExecutionRun       `json:"run"`
	Issue       *currentExecutionIssue    `json:"issue,omitempty"`
	Squad       *currentExecutionSquad    `json:"squad,omitempty"`
	Runtime     *currentExecutionRuntime  `json:"runtime,omitempty"`
	Trigger     *currentExecutionTrigger  `json:"trigger,omitempty"`
	Attribution *TaskAttribution          `json:"attribution"`
}

type currentExecutionWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type currentExecutionAgent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type currentExecutionTask struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Attempt      int32  `json:"attempt"`
	MaxAttempts  int32  `json:"max_attempts"`
	CreatedAt    string `json:"created_at,omitempty"`
	DispatchedAt string `json:"dispatched_at,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
}

type currentExecutionRun struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	Status       string `json:"status"`
	Attempt      int32  `json:"attempt"`
	MaxAttempts  int32  `json:"max_attempts"`
	CreatedAt    string `json:"created_at,omitempty"`
	DispatchedAt string `json:"dispatched_at,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

type currentExecutionIssue struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type currentExecutionSquad struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	DetailsAvailable bool   `json:"details_available"`
}

type currentExecutionRuntime struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	CustomName       string `json:"custom_name,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Status           string `json:"status,omitempty"`
	DetailsAvailable bool   `json:"details_available"`
}

type currentExecutionTrigger struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	CommentID      string `json:"comment_id,omitempty"`
	AutopilotRunID string `json:"autopilot_run_id,omitempty"`
}

// GetCurrentExecutionContext returns a task-token-bound, provider-neutral
// snapshot of the currently running execution. It does not mint credentials,
// choose a policy class, normalize an operation, or authorize an external
// effect. The running task/token lock is shared with assertion issuance so a
// terminal or revoked task cannot keep reading this port.
func (h *Handler) GetCurrentExecutionContext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "task token required")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read current execution context")
		return
	}
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	if !lockRunningTaskTokenForAssertion(w, r, queries) {
		return
	}
	if h.WorkloadAssertionHook != nil {
		h.WorkloadAssertionHook("current_execution_context_locked")
	}
	resolved, ok := h.resolveTaskWorkload(w, r, queries, false)
	if !ok {
		return
	}
	response, err := assembleCurrentExecutionContext(r.Context(), queries, resolved, h.currentWorkloadAssertionTime())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read current execution context")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit current execution context read")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func assembleCurrentExecutionContext(ctx context.Context, queries *db.Queries, resolved resolvedTaskWorkload, observedAt time.Time) (currentExecutionContextResponse, error) {
	task := resolved.Task
	response := currentExecutionContextResponse{
		Schema:     currentExecutionContextSchema,
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		Workspace: currentExecutionWorkspace{
			ID: uuidToString(resolved.Workspace.ID), Name: resolved.Workspace.Name, Slug: resolved.Workspace.Slug,
		},
		Agent: currentExecutionAgent{
			ID: uuidToString(resolved.Agent.ID), Name: resolved.Agent.Name, Status: resolved.Agent.Status,
		},
		Task: currentExecutionTask{
			ID: uuidToString(task.ID), Status: task.Status, Attempt: task.Attempt, MaxAttempts: task.MaxAttempts,
			CreatedAt: timestampString(task.CreatedAt), DispatchedAt: timestampString(task.DispatchedAt),
			StartedAt: timestampString(task.StartedAt), CompletedAt: timestampString(task.CompletedAt),
			ParentTaskID: uuidToString(task.ParentTaskID),
		},
		Run: currentExecutionRun{
			ID: resolved.Workload.RunID, TaskID: uuidToString(task.ID), Status: task.Status,
			Attempt: task.Attempt, MaxAttempts: task.MaxAttempts,
			CreatedAt: timestampString(task.CreatedAt), DispatchedAt: timestampString(task.DispatchedAt),
			StartedAt: timestampString(task.StartedAt), CompletedAt: timestampString(task.CompletedAt),
		},
		Attribution: taskAttributionBase(task),
	}

	if resolved.Issue != nil {
		response.Issue = &currentExecutionIssue{
			ID: uuidToString(resolved.Issue.ID), Key: resolved.Workload.IssueKey,
			Title: resolved.Issue.Title, Status: resolved.Issue.Status,
			CreatedAt: timestampString(resolved.Issue.CreatedAt), UpdatedAt: timestampString(resolved.Issue.UpdatedAt),
		}
	}
	if task.SquadID.Valid {
		response.Squad = &currentExecutionSquad{ID: uuidToString(task.SquadID)}
		squad, err := queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: task.SquadID, WorkspaceID: resolved.WorkspaceID})
		if err == nil {
			response.Squad.Name = squad.Name
			response.Squad.DetailsAvailable = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return currentExecutionContextResponse{}, err
		}
	}
	if task.RuntimeID.Valid {
		response.Runtime = &currentExecutionRuntime{ID: uuidToString(task.RuntimeID)}
		runtime, err := queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: task.RuntimeID, WorkspaceID: resolved.WorkspaceID})
		if err == nil {
			response.Runtime.Name = runtime.Name
			response.Runtime.CustomName = textString(runtime.CustomName)
			response.Runtime.Provider = runtime.Provider
			response.Runtime.Status = runtime.Status
			response.Runtime.DetailsAvailable = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return currentExecutionContextResponse{}, err
		}
	}
	response.Trigger = currentExecutionTriggerFromTask(task)
	hydrateCurrentExecutionAttributionNames(ctx, queries, response.Attribution)
	return response, nil
}

func currentExecutionTriggerFromTask(task db.AgentTaskQueue) *currentExecutionTrigger {
	kind := textString(task.TriggerEvidenceKind)
	id := uuidToString(task.TriggerEvidenceRefID)
	commentID := uuidToString(task.TriggerCommentID)
	autopilotRunID := uuidToString(task.AutopilotRunID)
	if kind == "" && commentID != "" {
		kind = "comment"
	}
	if kind == "" && autopilotRunID != "" {
		kind = "autopilot_run"
	}
	if id == "" {
		if commentID != "" {
			id = commentID
		} else {
			id = autopilotRunID
		}
	}
	if kind == "" || id == "" {
		return nil
	}
	return &currentExecutionTrigger{Kind: kind, ID: id, CommentID: commentID, AutopilotRunID: autopilotRunID}
}

func hydrateCurrentExecutionAttributionNames(ctx context.Context, queries *db.Queries, attr *TaskAttribution) {
	if attr == nil {
		return
	}
	ids := make([]pgtype.UUID, 0, 2)
	seen := make(map[string]struct{}, 2)
	add := func(ref *AttributionUser) {
		if ref == nil || ref.ID == "" {
			return
		}
		if _, exists := seen[ref.ID]; exists {
			return
		}
		parsed, err := util.ParseUUID(ref.ID)
		if err != nil {
			return
		}
		seen[ref.ID] = struct{}{}
		ids = append(ids, parsed)
	}
	add(attr.Initiator)
	add(attr.Originator)
	if len(ids) == 0 {
		return
	}
	users, err := queries.GetUsersByIDs(ctx, ids)
	if err != nil {
		return
	}
	names := make(map[string]string, len(users))
	for _, user := range users {
		names[uuidToString(user.ID)] = user.Name
	}
	fill := func(ref *AttributionUser) {
		if ref != nil {
			ref.Name = names[ref.ID]
		}
	}
	fill(attr.Initiator)
	fill(attr.Originator)
}

func timestampString(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
