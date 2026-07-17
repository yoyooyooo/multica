package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RecoverOrphanedTasks is called by the daemon at startup for each runtime
// it owns. It atomically fails any dispatched/running tasks the server still
// believes belong to that runtime — those are the tasks the previous daemon
// process was running when it died — and triggers MaybeRetryFailedTask for
// each so the user sees a fresh attempt instead of a permanently stuck row.
//
// This is the targeted fix for "issue stuck at in_progress when daemon
// restarts mid-task": the runtime heartbeat sweeper takes up to 75s + the
// in-process task timeout (2.5h) to notice such tasks; the daemon itself
// knows the moment it comes back up, so we let it report orphan recovery.
func (h *Handler) RecoverOrphanedTasks(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	rows, err := h.Queries.RecoverOrphanedTasksForRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		slog.Warn("recover-orphans failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "recover orphans failed")
		return
	}

	// Funnel through the shared post-failure pipeline so we get the same
	// task:failed events, agent reconcile, issue rollback, and auto-retry
	// behaviour as the runtime sweeper. This was previously a fast-path
	// that bypassed those side effects, leaving the UI stale when no retry
	// was created (max_attempts exhausted, autopilot, non-retryable reason).
	retried := h.TaskService.HandleFailedTasks(r.Context(), rows)

	if len(rows) > 0 {
		slog.Info("recover-orphans completed",
			"runtime_id", runtimeID,
			"orphaned", len(rows),
			"retried", retried,
		)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orphaned": len(rows),
		"retried":  retried,
	})
}

// PinTaskSession lets the daemon persist the agent's session_id and
// work_dir as soon as they're known — typically right after the agent
// emits its first system message — so a crash mid-run doesn't lose the
// resume pointer needed to continue the conversation on the next attempt.
type PinTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

func (h *Handler) PinTaskSession(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}

	var req PinTaskSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" && req.WorkDir == "" {
		writeError(w, http.StatusBadRequest, "session_id or work_dir required")
		return
	}

	params := db.UpdateAgentTaskSessionParams{ID: parseUUID(taskID)}
	if req.SessionID != "" {
		params.SessionID = pgtype.Text{String: req.SessionID, Valid: true}
	}
	if req.WorkDir != "" {
		params.WorkDir = pgtype.Text{String: req.WorkDir, Valid: true}
	}
	if err := h.Queries.UpdateAgentTaskSession(r.Context(), params); err != nil {
		slog.Warn("pin-session failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RerunIssueRequest is the body shared by the legacy rerun endpoint and the
// dedicated fresh-provenance endpoint.
type RerunIssueRequest struct {
	// TaskID identifies the exact execution row whose agent, squad role, and
	// trigger-comment provenance should be preserved.
	TaskID string `json:"task_id,omitempty"`
}

func decodeRerunIssueRequest(body io.Reader) (RerunIssueRequest, error) {
	decoder := json.NewDecoder(body)
	var req RerunIssueRequest
	if err := decoder.Decode(&req); err != nil && err != io.EOF {
		return RerunIssueRequest{}, err
	}
	// A rerun request is exactly one JSON document. Reject concatenated values
	// and trailing garbage rather than acting on a valid first object.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return RerunIssueRequest{}, errors.New("multiple JSON values")
		}
		return RerunIssueRequest{}, err
	}
	return req, nil
}

// RerunIssue preserves the mini-runtime branch's existing behavior. An empty
// body targets the current assignee; task_id targets the exact historical
// agent. Both forms already set force_fresh_session and reuse no prior context.
func (h *Handler) RerunIssue(w http.ResponseWriter, r *http.Request) {
	h.rerunIssue(w, r, false)
}

// RerunIssueFresh is the fail-closed endpoint used by CLI --task-id. It
// requires a source task while retaining this runtime branch's established
// no-session/no-workdir rerun semantics.
func (h *Handler) RerunIssueFresh(w http.ResponseWriter, r *http.Request) {
	h.rerunIssue(w, r, true)
}

func (h *Handler) rerunIssue(w http.ResponseWriter, r *http.Request, requireSource bool) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	actorUserID, _ := util.ParseUUID(originatorUserID)
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, actorType, actorID, originatorUserID, workspaceID)
	}

	// Decode regardless of ContentLength: HTTP/2 and chunked callers may use
	// an unknown length (-1). EOF is the valid legacy empty-body request.
	req, err := decodeRerunIssueRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if requireSource && req.TaskID == "" {
		writeError(w, http.StatusBadRequest, "task_id is required for a fresh provenance rerun")
		return
	}

	var sourceTaskID pgtype.UUID
	if req.TaskID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, req.TaskID, "task_id")
		if !ok {
			return
		}
		sourceTaskID = parsed
	}

	var task *db.AgentTaskQueue
	if requireSource {
		task, err = h.TaskService.RerunIssueFresh(r.Context(), issue.ID, sourceTaskID, pgtype.UUID{}, actorUserID, canInvoke)
	} else {
		task, err = h.TaskService.RerunIssue(r.Context(), issue.ID, sourceTaskID, pgtype.UUID{}, actorUserID, canInvoke)
	}
	if errors.Is(err, service.ErrRerunInvokeNotAllowed) {
		writeError(w, http.StatusForbidden, "cannot rerun private agent")
		return
	}
	if err != nil {
		slog.Warn("issue rerun failed", "issue_id", id, "fresh_provenance", requireSource, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, taskToResponse(*task, uuidToString(issue.WorkspaceID)))
}
