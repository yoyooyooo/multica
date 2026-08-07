package handler

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const externalPRLinkTokenTTL = 5 * time.Minute

// CreateExternalPRLinkToken mints the task-bound correlation token used by the
// external PR link callback. It does not authorize repository operations.
// Workload identity is derived only from the authenticated, still-running task token.
func (h *Handler) CreateExternalPRLinkToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "task token required")
		return
	}
	secret := strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET"))
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "external PR link token signing is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate external PR link context")
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
	if resolved.Issue == nil {
		writeError(w, http.StatusBadRequest, "task has no issue")
		return
	}
	if h.ExternalPRLinkTokenHook != nil {
		h.ExternalPRLinkTokenHook("external_pr_link_token_locked")
	}

	workspaceID := uuidToString(resolved.WorkspaceID)
	issueID := uuidToString(resolved.Issue.ID)
	taskID := uuidToString(resolved.Task.ID)
	agentID := uuidToString(resolved.Agent.ID)
	issueURL := ""
	if appURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/"); appURL != "" {
		issueURL = fmt.Sprintf("%s/%s/issues/%s", appURL, resolved.Workspace.Slug, resolved.IssueKey)
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"aud": externalPRLinkTokenAudience(), "iat": now.Unix(), "exp": now.Add(externalPRLinkTokenTTL).Unix(),
		"workspace": resolved.Workspace.Slug, "workspace_id": workspaceID,
		"issue_id": issueID, "issue_key": resolved.IssueKey, "issue_url": issueURL,
		"task_id": taskID, "agent_id": agentID, "source": "task_token",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	linkToken, err := token.SignedString([]byte(secret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign link token")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit external PR link context")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"link_token": linkToken, "workspace": resolved.Workspace.Slug, "workspace_id": workspaceID,
		"issue_id": issueID, "issue_key": resolved.IssueKey, "issue_url": issueURL,
		"task_id": taskID, "agent_id": agentID,
	})
}
