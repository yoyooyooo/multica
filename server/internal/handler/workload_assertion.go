package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	workloadAssertionType                    = "urn:multica:workload-assertion:jwt:v1"
	workloadAssertionJWTType                 = "multica-workload-assertion+jwt"
	workloadAssertionVersion                 = 1
	workloadAssertionPurposeExternalPR       = "external_pr_link"
	workloadAssertionPurposeSessionExchange  = "ags_session_exchange"
	workloadAssertionExternalPRAudience      = "urn:multica:external-pr-link:v1"
	workloadAssertionSessionExchangeAudience = "urn:ags:workload-session-exchange:v1"
	defaultWorkloadAssertionIssuer           = "multica"
	defaultWorkloadAssertionKeyID            = "multica-workload-assertion-v1"
	workloadAssertionTTL                     = 5 * time.Minute
)

type workloadAssertionRequest struct {
	Purpose               string                  `json:"purpose"`
	Target                workloadAssertionTarget `json:"target"`
	RequestedCapabilities []string                `json:"requested_capabilities,omitempty"`
}

type workloadAssertionTarget struct {
	Provider   string `json:"provider"`
	Instance   string `json:"instance,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type workloadAssertionWorkload struct {
	Workspace   string `json:"workspace"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	TaskID      string `json:"task_id"`
	RunID       string `json:"run_id,omitempty"`
	IssueID     string `json:"issue_id,omitempty"`
	IssueKey    string `json:"issue_key,omitempty"`
	IssueURL    string `json:"issue_url,omitempty"`
}

type workloadAssertionResponse struct {
	Assertion     string                    `json:"assertion"`
	AssertionType string                    `json:"assertion_type"`
	Purpose       string                    `json:"purpose"`
	ExpiresAt     string                    `json:"expires_at"`
	Workload      workloadAssertionWorkload `json:"workload"`
}

type resolvedTaskWorkload struct {
	Workload workloadAssertionWorkload
}

// CreateWorkloadAssertion mints a short-lived, purpose-bound assertion from
// the server-authenticated task token context. Request fields may bind a target
// but can never override workload identity.
func (h *Handler) CreateWorkloadAssertion(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "task token required")
		return
	}
	var req workloadAssertionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Purpose = strings.TrimSpace(req.Purpose)
	audience := ""
	requireIssue := false
	switch req.Purpose {
	case workloadAssertionPurposeExternalPR:
		audience = workloadAssertionExternalPRAudience
		requireIssue = true
		if len(req.RequestedCapabilities) != 0 {
			writeError(w, http.StatusBadRequest, "external PR link assertions do not accept requested capabilities")
			return
		}
		req.RequestedCapabilities = []string{}
	case workloadAssertionPurposeSessionExchange:
		audience = workloadAssertionSessionExchangeAudience
		capabilities, err := normalizeRequestedCapabilities(req.RequestedCapabilities)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.RequestedCapabilities = capabilities
	default:
		writeError(w, http.StatusBadRequest, "unsupported workload assertion purpose")
		return
	}
	target, err := normalizeWorkloadAssertionTarget(req.Target, req.Purpose)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resolved, ok := h.resolveTaskWorkload(w, r, requireIssue)
	if !ok {
		return
	}

	secret := workloadAssertionSigningSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "workload assertion signing is not configured")
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(workloadAssertionTTL)
	issuer := workloadAssertionIssuer()
	keyID := workloadAssertionKeyID()
	claims := jwt.MapClaims{
		"ver":                    workloadAssertionVersion,
		"iss":                    issuer,
		"aud":                    audience,
		"sub":                    fmt.Sprintf("urn:multica:workload:%s:%s", resolved.Workload.WorkspaceID, resolved.Workload.TaskID),
		"jti":                    uuid.NewString(),
		"iat":                    now.Unix(),
		"nbf":                    now.Unix(),
		"exp":                    expiresAt.Unix(),
		"purpose":                req.Purpose,
		"source":                 "task_token",
		"workload":               resolved.Workload,
		"target":                 target,
		"requested_capabilities": req.RequestedCapabilities,
	}
	assertion, err := signAssertionJWT(claims, secret, workloadAssertionJWTType, keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign workload assertion")
		return
	}
	writeJSON(w, http.StatusOK, workloadAssertionResponse{
		Assertion:     assertion,
		AssertionType: workloadAssertionType,
		Purpose:       req.Purpose,
		ExpiresAt:     expiresAt.Format(time.RFC3339),
		Workload:      resolved.Workload,
	})
}

// CreateExternalPRLinkToken preserves the legacy response and audience while
// sharing the same server-derived workload resolver and signing primitive as
// the canonical workload assertion endpoint.
func (h *Handler) CreateExternalPRLinkToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "task token required")
		return
	}
	resolved, ok := h.resolveTaskWorkload(w, r, true)
	if !ok {
		return
	}
	secret := legacyExternalPRSigningSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "external PR link token signing is not configured")
		return
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"aud":          externalPRLinkTokenAudience(),
		"iat":          now.Unix(),
		"exp":          now.Add(workloadAssertionTTL).Unix(),
		"workspace":    resolved.Workload.Workspace,
		"workspace_id": resolved.Workload.WorkspaceID,
		"issue_id":     resolved.Workload.IssueID,
		"issue_key":    resolved.Workload.IssueKey,
		"issue_url":    resolved.Workload.IssueURL,
		"task_id":      resolved.Workload.TaskID,
		"agent_id":     resolved.Workload.AgentID,
		"source":       "task_token",
	}
	linkToken, err := signAssertionJWT(claims, secret, "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign link token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"link_token":   linkToken,
		"workspace":    resolved.Workload.Workspace,
		"workspace_id": resolved.Workload.WorkspaceID,
		"issue_id":     resolved.Workload.IssueID,
		"issue_key":    resolved.Workload.IssueKey,
		"issue_url":    resolved.Workload.IssueURL,
		"task_id":      resolved.Workload.TaskID,
		"agent_id":     resolved.Workload.AgentID,
	})
}

func (h *Handler) resolveTaskWorkload(w http.ResponseWriter, r *http.Request, requireIssue bool) (resolvedTaskWorkload, bool) {
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return resolvedTaskWorkload{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace id")
	if !ok {
		return resolvedTaskWorkload{}, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return resolvedTaskWorkload{}, false
	}
	if requireIssue && !task.IssueID.Valid {
		writeError(w, http.StatusBadRequest, "task has no issue")
		return resolvedTaskWorkload{}, false
	}
	workspace, err := h.Queries.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return resolvedTaskWorkload{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: task.AgentID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return resolvedTaskWorkload{}, false
	}
	workload := workloadAssertionWorkload{
		Workspace: workspace.Slug, WorkspaceID: uuidToString(workspaceID), AgentID: uuidToString(task.AgentID),
		AgentName: agent.Name, TaskID: uuidToString(task.ID),
	}
	if task.IssueID.Valid {
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: workspaceID})
		if err != nil {
			writeError(w, http.StatusNotFound, "issue not found")
			return resolvedTaskWorkload{}, false
		}
		prefix := h.getIssuePrefix(r.Context(), workspaceID)
		workload.IssueID = uuidToString(issue.ID)
		workload.IssueKey = fmt.Sprintf("%s-%d", prefix, issue.Number)
		if appURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/"); appURL != "" {
			workload.IssueURL = fmt.Sprintf("%s/%s/issues/%s", appURL, workspace.Slug, workload.IssueKey)
		}
	}
	return resolvedTaskWorkload{Workload: workload}, true
}

func normalizeWorkloadAssertionTarget(target workloadAssertionTarget, purpose string) (workloadAssertionTarget, error) {
	target.Provider = normalizeExternalPRProvider(target.Provider)
	target.Instance = strings.TrimSpace(target.Instance)
	target.Repository = strings.Trim(strings.TrimSpace(target.Repository), "/")
	if target.Provider == "" {
		return workloadAssertionTarget{}, fmt.Errorf("target provider is required")
	}
	if purpose == workloadAssertionPurposeSessionExchange {
		if target.Provider != "ags" {
			return workloadAssertionTarget{}, fmt.Errorf("session exchange target provider must be ags")
		}
		if target.Instance == "" {
			return workloadAssertionTarget{}, fmt.Errorf("session exchange target instance is required")
		}
	} else if !externalPRProviderAllowed(target.Provider) {
		return workloadAssertionTarget{}, fmt.Errorf("target provider %q is not allowed", target.Provider)
	}
	parts := strings.Split(target.Repository, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return workloadAssertionTarget{}, fmt.Errorf("target repository must be owner/name")
	}
	target.Repository = strings.TrimSpace(parts[0]) + "/" + strings.TrimSpace(parts[1])
	return target, nil
}

func normalizeRequestedCapabilities(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("session exchange requires requested capabilities")
	}
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, raw := range input {
		capability := strings.ToLower(strings.TrimSpace(raw))
		if capability == "" {
			return nil, fmt.Errorf("requested capabilities must not contain empty values")
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, fmt.Errorf("requested capabilities must not contain duplicates")
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out, nil
}

func signAssertionJWT(claims jwt.MapClaims, secret, typ, keyID string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	if typ != "" {
		token.Header["typ"] = typ
	}
	if keyID != "" {
		token.Header["kid"] = keyID
	}
	return token.SignedString([]byte(secret))
}

func workloadAssertionSigningSecret() string {
	if secret := strings.TrimSpace(os.Getenv("MULTICA_WORKLOAD_ASSERTION_SECRET")); secret != "" {
		return secret
	}
	return strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET"))
}

func legacyExternalPRSigningSecret() string {
	return strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET"))
}

func workloadAssertionIssuer() string {
	if issuer := strings.TrimSpace(os.Getenv("MULTICA_WORKLOAD_ASSERTION_ISSUER")); issuer != "" {
		return issuer
	}
	return defaultWorkloadAssertionIssuer
}

func workloadAssertionKeyID() string {
	if keyID := strings.TrimSpace(os.Getenv("MULTICA_WORKLOAD_ASSERTION_KEY_ID")); keyID != "" {
		return keyID
	}
	return defaultWorkloadAssertionKeyID
}
