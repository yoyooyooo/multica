package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const currentExecutionContextSchema = "multica.current-execution-context.v2"

type currentExecutionContextResponse struct {
	Schema      string                    `json:"schema"`
	ObservedAt  string                    `json:"observed_at"`
	Workspace   currentExecutionWorkspace `json:"workspace"`
	Agent       currentExecutionAgent     `json:"agent"`
	Task        currentExecutionTask      `json:"task"`
	Claim       currentExecutionClaim     `json:"claim"`
	Run         currentExecutionRun       `json:"run"`
	Issue       *currentExecutionIssue    `json:"issue,omitempty"`
	Squad       *currentExecutionSquad    `json:"squad,omitempty"`
	Runtime     *currentExecutionRuntime  `json:"runtime,omitempty"`
	Trigger     *currentExecutionTrigger  `json:"trigger,omitempty"`
	Attribution *TaskAttribution          `json:"attribution"`
}

type currentExecutionWorkspace struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type currentExecutionAgent struct {
	ID string `json:"id"`
}

type currentExecutionTask struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Attempt int32  `json:"attempt"`
}

type currentExecutionClaim struct {
	Generation string `json:"generation"`
	TaskID     string `json:"task_id"`
}

type currentExecutionRun struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
}

type currentExecutionIssue struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type currentExecutionSquad struct {
	ID string `json:"id"`
}

type currentExecutionRuntime struct {
	ID       string `json:"id"`
	DaemonID string `json:"daemon_id,omitempty"`
}

type currentExecutionTrigger struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// GetCurrentExecutionContext returns the minimal task-token-bound context used
// by external authorities. The task row lock linearizes this read against task
// completion, so terminal or revoked authority cannot mint a fresh grant.
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
	if !lockRunningTaskTokenForExecutionContext(w, r, queries) {
		return
	}
	resolved, ok := h.resolveCurrentExecutionWorkload(w, r, queries)
	if !ok {
		return
	}
	response, err := assembleCurrentExecutionContext(r.Context(), queries, resolved, time.Now().UTC())
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

type resolvedCurrentExecutionWorkload struct {
	Task        db.AgentTaskQueue
	Workspace   db.Workspace
	Agent       db.Agent
	Issue       *db.Issue
	WorkspaceID pgtype.UUID
	IssueKey    string
}

func lockRunningTaskTokenForExecutionContext(w http.ResponseWriter, r *http.Request, queries *db.Queries) bool {
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace id")
	if !ok {
		return false
	}
	tokenHash := strings.TrimSpace(r.Header.Get("X-Task-Token-Hash"))
	if tokenHash == "" {
		writeError(w, http.StatusUnauthorized, "task token is no longer executable")
		return false
	}
	if _, err := queries.LockRunningTaskTokenForExecutionContext(r.Context(), db.LockRunningTaskTokenForExecutionContextParams{
		TokenHash: tokenHash, TaskID: taskID, WorkspaceID: workspaceID,
	}); err != nil {
		writeError(w, http.StatusUnauthorized, "task token is no longer executable")
		return false
	}
	return true
}

func (h *Handler) resolveCurrentExecutionWorkload(w http.ResponseWriter, r *http.Request, queries *db.Queries) (resolvedCurrentExecutionWorkload, bool) {
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return resolvedCurrentExecutionWorkload{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace id")
	if !ok {
		return resolvedCurrentExecutionWorkload{}, false
	}
	task, err := queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return resolvedCurrentExecutionWorkload{}, false
	}
	workspace, err := queries.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return resolvedCurrentExecutionWorkload{}, false
	}
	agent, err := queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: task.AgentID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return resolvedCurrentExecutionWorkload{}, false
	}

	resolved := resolvedCurrentExecutionWorkload{
		Task: task, Workspace: workspace, Agent: agent, WorkspaceID: workspaceID,
	}
	if task.IssueID.Valid {
		issue, err := queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: workspaceID})
		if err != nil {
			writeError(w, http.StatusNotFound, "issue not found")
			return resolvedCurrentExecutionWorkload{}, false
		}
		resolved.Issue = &issue
		resolved.IssueKey = fmt.Sprintf("%s-%d", issuePrefixForWorkspace(workspace), issue.Number)
	}
	return resolved, true
}

func assembleCurrentExecutionContext(ctx context.Context, queries *db.Queries, resolved resolvedCurrentExecutionWorkload, observedAt time.Time) (currentExecutionContextResponse, error) {
	taskID := uuidToString(resolved.Task.ID)
	response := currentExecutionContextResponse{
		Schema:     currentExecutionContextSchema,
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		Workspace: currentExecutionWorkspace{
			ID: uuidToString(resolved.Workspace.ID), Slug: resolved.Workspace.Slug,
		},
		Agent: currentExecutionAgent{ID: uuidToString(resolved.Agent.ID)},
		Task: currentExecutionTask{
			ID: taskID, Status: resolved.Task.Status, Attempt: resolved.Task.Attempt,
		},
		Claim:       currentExecutionClaim{Generation: taskID, TaskID: taskID},
		Run:         currentExecutionRun{ID: taskID, TaskID: taskID},
		Attribution: taskAttributionBase(resolved.Task),
	}
	if resolved.Issue != nil {
		response.Issue = &currentExecutionIssue{ID: uuidToString(resolved.Issue.ID), Key: resolved.IssueKey}
	}
	if resolved.Task.SquadID.Valid {
		response.Squad = &currentExecutionSquad{ID: uuidToString(resolved.Task.SquadID)}
	}
	if resolved.Task.RuntimeID.Valid {
		response.Runtime = &currentExecutionRuntime{ID: uuidToString(resolved.Task.RuntimeID)}
		if runtime, err := queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
			ID: resolved.Task.RuntimeID, WorkspaceID: resolved.WorkspaceID,
		}); err == nil && runtime.DaemonID.Valid {
			response.Runtime.DaemonID = strings.TrimSpace(runtime.DaemonID.String)
		}
	}
	response.Trigger = currentExecutionTriggerFromTask(resolved.Task)
	return response, nil
}

func currentExecutionTriggerFromTask(task db.AgentTaskQueue) *currentExecutionTrigger {
	kind := strings.TrimSpace(task.TriggerEvidenceKind.String)
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
	return &currentExecutionTrigger{Kind: kind, ID: id}
}
