package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	workloadContextSchema                    = "workload.context.v1"
	workloadAuthoritySchema                  = "workload.authority.v1"
	workloadScopeSchema                      = "workload.scope.v1"
	workspaceDefaultPolicyClass              = "multica.workspace.default.v1"
)

var operationConstraintKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var sessionOperationCapabilities = map[string][]string{
	"repo.read":     {"repo:read"},
	"repo.create":   {"repo:create"},
	"git.read":      {"repo:read"},
	"git.push":      {"repo:read", "repo:write"},
	"pr.create":     {"pr:create", "repo:read"},
	"pr.read":       {"repo:read"},
	"ci.read":       {"repo:read"},
	"review.read":   {"repo:read"},
	"review.submit": {"repo:read"},
	"pr.merge":      {"repo:read"},
	"repo.admin":    {"repo:read"},
}

type workloadAssertionRequest struct {
	Purpose               string                      `json:"purpose"`
	Target                workloadAssertionTarget     `json:"target"`
	RequestedResource     *workloadAssertionResource  `json:"requested_resource,omitempty"`
	RequestedOperation    *workloadAssertionOperation `json:"requested_operation,omitempty"`
	RequestedCapabilities []string                    `json:"requested_capabilities,omitempty"`
}

type workloadAssertionTarget struct {
	Provider   string `json:"provider"`
	Instance   string `json:"instance,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type workloadAssertionResource struct {
	Service    string `json:"service"`
	Repository string `json:"repository"`
}

type workloadAssertionOperation struct {
	Name        string         `json:"name"`
	Constraints map[string]any `json:"constraints"`
}

type workloadAssertionScope struct {
	Schema                string                     `json:"schema"`
	Resource              workloadAssertionResource  `json:"resource"`
	Operation             workloadAssertionOperation `json:"operation"`
	RequestedCapabilities []string                   `json:"requested_capabilities"`
	CompatibilityInput    string                     `json:"compatibility_input,omitempty"`
}

type workloadContextV1 struct {
	Schema           string `json:"schema"`
	IssuerInstanceID string `json:"issuer_instance_id"`
	Subject          string `json:"subject"`
	CorrelationID    string `json:"correlation_id"`
	WorkspaceID      string `json:"workspace_id"`
	AgentID          string `json:"agent_id"`
	SquadID          string `json:"squad_id,omitempty"`
	IssueID          string `json:"issue_id,omitempty"`
	IssueKey         string `json:"issue_key,omitempty"`
	TaskID           string `json:"task_id"`
	RunID            string `json:"run_id"`
	TriggerID        string `json:"trigger_id,omitempty"`
	RuntimeID        string `json:"runtime_id,omitempty"`
}

type workloadAuthorityV1 struct {
	Schema          string `json:"schema"`
	TeamIdentityID  string `json:"team_identity_id"`
	MembershipEpoch int64  `json:"membership_epoch"`
	PolicyClass     string `json:"policy_class"`
}

type workloadActor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type workloadAssertionWorkload struct {
	Workspace       string               `json:"workspace"`
	WorkspaceID     string               `json:"workspace_id"`
	AgentID         string               `json:"agent_id"`
	AgentName       string               `json:"agent_name"`
	TaskID          string               `json:"task_id"`
	RunID           string               `json:"run_id,omitempty"`
	IssueID         string               `json:"issue_id,omitempty"`
	IssueKey        string               `json:"issue_key,omitempty"`
	IssueURL        string               `json:"issue_url,omitempty"`
	Actor           *workloadActor       `json:"actor,omitempty"`
	WorkloadContext *workloadContextV1   `json:"workload_context,omitempty"`
	Authority       *workloadAuthorityV1 `json:"authority,omitempty"`
}

type workloadAssertionResponse struct {
	Assertion     string                    `json:"assertion"`
	AssertionType string                    `json:"assertion_type"`
	Purpose       string                    `json:"purpose"`
	ExpiresAt     string                    `json:"expires_at"`
	Workload      workloadAssertionWorkload `json:"workload"`
}

type resolvedTaskWorkload struct {
	Workload    workloadAssertionWorkload
	Task        db.AgentTaskQueue
	WorkspaceID pgtype.UUID
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
	isSessionExchange := false
	switch req.Purpose {
	case workloadAssertionPurposeExternalPR:
		audience = workloadAssertionExternalPRAudience
		requireIssue = true
		if len(req.RequestedCapabilities) != 0 || req.RequestedResource != nil || req.RequestedOperation != nil {
			writeError(w, http.StatusBadRequest, "external PR link assertions do not accept requested session scope")
			return
		}
		req.RequestedCapabilities = []string{}
	case workloadAssertionPurposeSessionExchange:
		audience = workloadAssertionSessionExchangeAudience
		isSessionExchange = true
	default:
		writeError(w, http.StatusBadRequest, "unsupported workload assertion purpose")
		return
	}
	target, err := normalizeWorkloadAssertionTarget(req.Target, req.Purpose)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var scope *workloadAssertionScope
	if isSessionExchange {
		resolvedScope, err := normalizeSessionExchangeScope(req, target)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		scope = &resolvedScope
		req.RequestedCapabilities = resolvedScope.RequestedCapabilities
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
	assertionID := uuid.NewString()
	if isSessionExchange {
		if issuer == defaultWorkloadAssertionIssuer {
			writeError(w, http.StatusServiceUnavailable, "workload assertion issuer is not configured")
			return
		}
		if !h.enrichSessionExchangeWorkload(w, r, &resolved, issuer, assertionID) {
			return
		}
	}
	keyID := workloadAssertionKeyID()
	claims := jwt.MapClaims{
		"ver":                    workloadAssertionVersion,
		"iss":                    issuer,
		"aud":                    audience,
		"sub":                    fmt.Sprintf("urn:multica:workload:%s:%s", resolved.Workload.WorkspaceID, resolved.Workload.TaskID),
		"jti":                    assertionID,
		"iat":                    now.Unix(),
		"nbf":                    now.Unix(),
		"exp":                    expiresAt.Unix(),
		"purpose":                req.Purpose,
		"source":                 "task_token",
		"workload":               resolved.Workload,
		"target":                 target,
		"requested_capabilities": req.RequestedCapabilities,
	}
	if scope != nil {
		claims["scope"] = scope
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
		AgentName: agent.Name, TaskID: uuidToString(task.ID), RunID: uuidToString(task.ID),
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
	return resolvedTaskWorkload{Workload: workload, Task: task, WorkspaceID: workspaceID}, true
}

// enrichSessionExchangeWorkload adds the signed v1 envelope and server-owned
// Team authority projection. The projection is resolved from durable server
// state; neither the request nor Agent/Squad labels can influence it.
func (h *Handler) enrichSessionExchangeWorkload(w http.ResponseWriter, r *http.Request, resolved *resolvedTaskWorkload, issuer, assertionID string) bool {
	authority, err := h.Queries.GetWorkspaceWorkloadAuthority(r.Context(), resolved.WorkspaceID)
	if err != nil || !authority.TeamIdentityID.Valid || authority.MembershipEpoch <= 0 || authority.PolicyClass != workspaceDefaultPolicyClass {
		writeError(w, http.StatusServiceUnavailable, "workload authority is unavailable")
		return false
	}

	context := &workloadContextV1{
		Schema:           workloadContextSchema,
		IssuerInstanceID: issuer,
		Subject:          "urn:multica:agent:" + resolved.Workload.AgentID,
		CorrelationID:    assertionID,
		WorkspaceID:      resolved.Workload.WorkspaceID,
		AgentID:          resolved.Workload.AgentID,
		SquadID:          uuidToString(resolved.Task.SquadID),
		IssueID:          resolved.Workload.IssueID,
		IssueKey:         resolved.Workload.IssueKey,
		TaskID:           resolved.Workload.TaskID,
		RunID:            resolved.Workload.RunID,
		TriggerID:        uuidToString(resolved.Task.TriggerCommentID),
		RuntimeID:        uuidToString(resolved.Task.RuntimeID),
	}
	resolved.Workload.Actor = &workloadActor{Type: "agent", ID: resolved.Workload.AgentID}
	resolved.Workload.WorkloadContext = context
	resolved.Workload.Authority = &workloadAuthorityV1{
		Schema:          workloadAuthoritySchema,
		TeamIdentityID:  uuidToString(authority.TeamIdentityID),
		MembershipEpoch: authority.MembershipEpoch,
		PolicyClass:     authority.PolicyClass,
	}
	return true
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

func normalizeSessionExchangeScope(req workloadAssertionRequest, target workloadAssertionTarget) (workloadAssertionScope, error) {
	capabilities, err := normalizeRequestedCapabilities(req.RequestedCapabilities)
	if err != nil {
		return workloadAssertionScope{}, err
	}
	if (req.RequestedResource == nil) != (req.RequestedOperation == nil) {
		return workloadAssertionScope{}, fmt.Errorf("session exchange requires both requested resource and operation")
	}

	resource := workloadAssertionResource{Service: "ags", Repository: target.Repository}
	operation := workloadAssertionOperation{}
	compatibilityInput := ""
	if req.RequestedResource == nil {
		operation.Name, err = legacyOperationForCapabilities(capabilities)
		if err != nil {
			return workloadAssertionScope{}, err
		}
		operation.Constraints = map[string]any{}
		compatibilityInput = "legacy_capability_mapping_v1"
	} else {
		resource, err = normalizeRequestedResource(*req.RequestedResource, target)
		if err != nil {
			return workloadAssertionScope{}, err
		}
		operation, err = normalizeRequestedOperation(*req.RequestedOperation)
		if err != nil {
			return workloadAssertionScope{}, err
		}
	}

	expectedCapabilities, knownOperation := sessionOperationCapabilities[operation.Name]
	if !knownOperation || !sameStrings(capabilities, expectedCapabilities) {
		return workloadAssertionScope{}, fmt.Errorf("requested capabilities do not match the registered operation")
	}
	return workloadAssertionScope{
		Schema:                workloadScopeSchema,
		Resource:              resource,
		Operation:             operation,
		RequestedCapabilities: capabilities,
		CompatibilityInput:    compatibilityInput,
	}, nil
}

func normalizeRequestedResource(resource workloadAssertionResource, target workloadAssertionTarget) (workloadAssertionResource, error) {
	resource.Service = strings.ToLower(strings.TrimSpace(resource.Service))
	resource.Repository = strings.Trim(strings.TrimSpace(resource.Repository), "/")
	if resource.Service != "ags" || resource.Repository != target.Repository {
		return workloadAssertionResource{}, fmt.Errorf("requested resource must match the AGS target repository")
	}
	return resource, nil
}

func normalizeRequestedOperation(operation workloadAssertionOperation) (workloadAssertionOperation, error) {
	operation.Name = strings.ToLower(strings.TrimSpace(operation.Name))
	if _, ok := sessionOperationCapabilities[operation.Name]; !ok {
		return workloadAssertionOperation{}, fmt.Errorf("requested operation is not registered")
	}
	if len(operation.Constraints) > 32 {
		return workloadAssertionOperation{}, fmt.Errorf("requested operation has too many constraints")
	}
	constraints := make(map[string]any, len(operation.Constraints))
	for key, value := range operation.Constraints {
		if !operationConstraintKeyPattern.MatchString(key) || strings.Contains(key, "role") || strings.Contains(key, "agent") || strings.Contains(key, "task") || strings.Contains(key, "credential") || strings.Contains(key, "profile") || strings.Contains(key, "principal") || strings.Contains(key, "capabilit") {
			return workloadAssertionOperation{}, fmt.Errorf("requested operation constraint key is not allowed")
		}
		switch typed := value.(type) {
		case string:
			normalized := strings.TrimSpace(typed)
			if normalized == "" || strings.ContainsAny(normalized, "\r\n\x00") {
				return workloadAssertionOperation{}, fmt.Errorf("requested operation constraint must be a non-empty single line scalar")
			}
			constraints[key] = normalized
		case bool, float64:
			// JSON decoding restricts numeric constraint values to finite float64 values.
			constraints[key] = typed
		default:
			return workloadAssertionOperation{}, fmt.Errorf("requested operation constraint must be a scalar")
		}
	}
	operation.Constraints = constraints
	return operation, nil
}

func legacyOperationForCapabilities(capabilities []string) (string, error) {
	switch strings.Join(capabilities, ",") {
	case "repo:read":
		return "repo.read", nil
	case "repo:create":
		return "repo.create", nil
	case "repo:read,repo:write":
		return "git.push", nil
	case "pr:create,repo:read":
		return "pr.create", nil
	default:
		return "", fmt.Errorf("legacy requested capabilities do not map to a registered operation")
	}
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
	sort.Strings(out)
	return out, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index, value := range left {
		if value != right[index] {
			return false
		}
	}
	return true
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
