package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/integrations/slack"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SlackInstallationResponse is the wire shape for a Slack installation row. The
// encrypted bot token in config is INTENTIONALLY absent — it is server-internal
// (only the outbound sender decrypts it). WS lease columns are runtime state,
// not API surface, so they are omitted too.
type SlackInstallationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	AgentID         string `json:"agent_id"`
	TeamID          string `json:"team_id"`
	BotUserID       string `json:"bot_user_id"`
	InstallerUserID string `json:"installer_user_id"`
	Status          string `json:"status"`
	InstalledAt     string `json:"installed_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func slackInstallationToResponse(row db.ChannelInstallation) SlackInstallationResponse {
	info := slack.DecodePublicConfig(row.Config)
	return SlackInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		TeamID:          info.TeamID,
		BotUserID:       info.BotUserID,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

// ListSlackInstallations (GET /api/workspaces/{id}/slack/installations) is
// member-visible so the Integrations tab renders for non-admins. Response
// flags mirror Lark:
//   - configured: at-rest encryption key is set (SlackInstall != nil).
//   - install_supported: the OAuth client credentials are wired, so the
//     "Connect Slack" flow can actually run.
func (h *Handler) ListSlackInstallations(w http.ResponseWriter, r *http.Request) {
	if h.SlackInstall == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []SlackInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.SlackInstall.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list slack installations")
		return
	}
	out := make([]SlackInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, slackInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": h.SlackInstall.InstallSupported(),
	})
}

// BeginSlackInstallResponse carries the Slack authorize URL the browser is
// redirected (or popped) to.
type BeginSlackInstallResponse struct {
	URL string `json:"url"`
}

// BeginSlackInstall (POST /api/workspaces/{id}/slack/install/begin?agent_id=…)
// starts the OAuth flow. Admin-only at the router. The agent_id picks which
// Multica agent the installed bot represents; it must belong to this workspace.
func (h *Handler) BeginSlackInstall(w http.ResponseWriter, r *http.Request) {
	if h.SlackInstall == nil || !h.SlackInstall.InstallSupported() {
		writeError(w, http.StatusServiceUnavailable, "slack install not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	// Ownership pre-check at the boundary so a wrong agent_id is a clear 404.
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	authorizeURL, err := h.SlackInstall.Begin(slack.BeginParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start slack install")
		return
	}
	writeJSON(w, http.StatusOK, BeginSlackInstallResponse{URL: authorizeURL})
}

// SlackOAuthCallback (GET /api/slack/oauth/callback?code=…&state=…) is the
// redirect Slack sends after the admin authorizes the app. It is NOT
// workspace-scoped in the path — the workspace/agent/initiator are recovered
// from the sealed state. It exchanges the code for a bot token, upserts the
// installation, then bounces the browser back to the Settings → Integrations
// tab with a success/error flag (mirroring GitHubSetupCallback).
func (h *Handler) SlackOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	settingsURL := slackSettingsRedirect()

	// The user declined the consent screen, or Slack reported an error.
	if oauthErr := strings.TrimSpace(q.Get("error")); oauthErr != "" {
		http.Redirect(w, r, settingsURL+"&slack_error="+url.QueryEscape(oauthErr), http.StatusFound)
		return
	}
	if h.SlackInstall == nil || !h.SlackInstall.InstallSupported() {
		http.Redirect(w, r, settingsURL+"&slack_error=not_configured", http.StatusFound)
		return
	}
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	if code == "" || state == "" {
		http.Redirect(w, r, settingsURL+"&slack_error=missing_params", http.StatusFound)
		return
	}

	res, err := h.SlackInstall.Complete(r.Context(), code, state)
	if err != nil {
		reason := "internal_error"
		switch {
		case errors.Is(err, slack.ErrInvalidState):
			reason = "invalid_state"
		case errors.Is(err, slack.ErrTeamOwnedByAnotherWorkspace):
			reason = "team_in_other_workspace"
		}
		slog.Error("slack: oauth callback failed", "error", err, "reason", reason)
		http.Redirect(w, r, settingsURL+"&slack_error="+reason, http.StatusFound)
		return
	}
	h.publish(protocol.EventSlackInstallationCreated, uuidToString(res.WorkspaceID), "system", "", map[string]any{
		"id": uuidToString(res.InstallationID),
	})
	http.Redirect(w, r, settingsURL+"&slack_connected=1", http.StatusFound)
}

// RevokeSlackInstallation (DELETE /api/workspaces/{id}/slack/installations/{installationId})
// flips status to 'revoked'. Admin-only at the router. The row is preserved for
// audit; a re-install via OAuth flips status back to 'active'.
func (h *Handler) RevokeSlackInstallation(w http.ResponseWriter, r *http.Request) {
	if h.SlackInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	// Workspace-scoped lookup so one workspace cannot revoke another's
	// installation by guessing the UUID.
	if _, err := h.SlackInstall.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, slack.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "slack installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := h.SlackInstall.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventSlackInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemSlackBindingTokenRequest carries the raw token the user clicked through
// from the bot's "link your account" prompt.
type RedeemSlackBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemSlackBindingTokenResponse echoes the bound workspace/installation/user
// so the frontend can confirm without a second fetch.
type RedeemSlackBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	SlackUserID    string `json:"slack_user_id"`
}

// RedeemSlackBindingToken (POST /api/slack/binding/redeem) binds the Slack user
// id carried by the token to the logged-in Multica user. The redeemer's identity
// comes from the session, not the token, so a stolen token cannot bind a Slack
// id to an attacker's account. Failure modes map to distinct status codes:
//   - 410 Gone:      token unknown / consumed / expired
//   - 409 Conflict:  this Slack id is already bound to a different user
//   - 403 Forbidden: redeemer is not a workspace member
func (h *Handler) RedeemSlackBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.SlackBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemSlackBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	redeemed, err := h.SlackBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, slack.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, slack.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this Slack account is already bound to a different Multica user")
		case errors.Is(err, slack.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, RedeemSlackBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		SlackUserID:    redeemed.SlackUserID,
	})
}

// slackSettingsRedirect builds the Settings → Integrations URL the OAuth
// callback bounces the browser back to, carrying a result flag. Mirrors
// GitHubSetupCallback's FRONTEND_ORIGIN handling.
func slackSettingsRedirect() string {
	frontend := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	return strings.TrimRight(frontend, "/") + "/settings?tab=integrations"
}
