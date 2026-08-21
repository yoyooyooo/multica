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

// T018: minimal current-execution-context.v2. Dual-read keeps `run` for one
// generation while `claim.generation` is the internal claim-generation id
// (execution_id, falling back to task id). Display enrichment is removed.
const currentExecutionContextSchema = "multica.current-execution-context.v2"

type currentExecutionContextResponse struct {
	Schema      string                    `json:"schema"`
	ObservedAt  string                    `json:"observed_at"`
	Workspace   currentExecutionWorkspace `json:"workspace"`
	Agent       currentExecutionAgent     `json:"agent"`
	Task        currentExecutionTask      `json:"task"`
	Claim       currentExecutionClaim     `json:"claim"`
	Run         currentExecutionRun       `json:"run"` // dual-read alias of claim.generation until Agent Kit/AGS cut over
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
	// Generation is the internal claim-generation coordinate (not a user-facing Run product).
	Generation string `json:"generation"`
	TaskID     string `json:"task_id"`
}

type currentExecutionRun struct {
	// ID mirrors claim.generation during the dual-read window.
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

// GetCurrentExecutionContext returns a task-token-bound, provider-neutral
// snapshot of the currently running execution. It does not mint credentials,
// choose a policy class, normalize an operation, or authorize an external
// effect. The running task/token lock ensures a terminal or revoked task cannot
// keep reading this port or mint a separate external-PR correlation token.
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
	if h.CurrentExecutionContextHook != nil {
		h.CurrentExecutionContextHook("current_execution_context_locked")
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
	RunID       string
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
		RunID: uuidToString(task.ID),
	}
	if task.ExecutionID.Valid {
		resolved.RunID = uuidToString(task.ExecutionID)
	}
	if task.IssueID.Valid {
		issue, err := queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: workspaceID})
		if err != nil {
			writeError(w, http.StatusNotFound, "issue not found")
			return resolvedCurrentExecutionWorkload{}, false
		}
		resolved.Issue = &issue
		issuePrefix := issuePrefixForWorkspace(workspace)
		resolved.IssueKey = fmt.Sprintf("%s-%d", issuePrefix, issue.Number)
	}
	return resolved, true
}

func assembleCurrentExecutionContext(ctx context.Context, queries *db.Queries, resolved resolvedCurrentExecutionWorkload, observedAt time.Time) (currentExecutionContextResponse, error) {
	task := resolved.Task
	response := currentExecutionContextResponse{
		Schema:     currentExecutionContextSchema,
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		Workspace: currentExecutionWorkspace{
			ID: uuidToString(resolved.Workspace.ID), Slug: resolved.Workspace.Slug,
		},
		Agent: currentExecutionAgent{ID: uuidToString(resolved.Agent.ID)},
		Task: currentExecutionTask{
			ID: uuidToString(task.ID), Status: task.Status, Attempt: task.Attempt,
		},
		Claim: currentExecutionClaim{
			Generation: resolved.RunID, TaskID: uuidToString(task.ID),
		},
		// Dual-read: run.id == claim.generation until Agent Kit/AGS drop Run product language.
		Run: currentExecutionRun{
			ID: resolved.RunID, TaskID: uuidToString(task.ID),
		},
		Attribution: taskAttributionBase(task),
	}

	if resolved.Issue != nil {
		response.Issue = &currentExecutionIssue{
			ID: uuidToString(resolved.Issue.ID), Key: resolved.IssueKey,
		}
	}
	if task.SquadID.Valid {
		response.Squad = &currentExecutionSquad{ID: uuidToString(task.SquadID)}
	}
	if task.RuntimeID.Valid {
		response.Runtime = &currentExecutionRuntime{ID: uuidToString(task.RuntimeID)}
		if runtime, err := queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
			ID: task.RuntimeID, WorkspaceID: resolved.WorkspaceID,
		}); err == nil && runtime.DaemonID.Valid {
			response.Runtime.DaemonID = strings.TrimSpace(runtime.DaemonID.String)
		}
	}
	response.Trigger = currentExecutionTriggerFromTask(task)
	// T018: do not hydrate display names onto attribution; IDs/source only.
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
	return &currentExecutionTrigger{Kind: kind, ID: id}
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
