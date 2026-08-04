package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	maximumRequestedSessionTTL               = 15 * time.Minute
	workloadContextSchema                    = "workload.context.v1"
	workloadAuthoritySchema                  = "workload.authority.v1"
	workloadScopeSchema                      = "workload.scope.v1"
	workspaceDefaultPolicyClass              = "multica.workspace.default.v1"
	workspaceMaintainerPolicyClass           = "multica.workspace.maintainer.v1"
)

var (
	canonicalGitSHA1Pattern  = regexp.MustCompile(`^[a-f0-9]{40}$`)
	canonicalSafeIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/+\-]{0,254}$`)
	canonicalDurationPattern = regexp.MustCompile(`^[1-9][0-9]*(?:s|m|h)$`)
	secretShapedValuePattern = regexp.MustCompile(`(?i)(?:mat_[A-Za-z0-9_-]+|ags_sess_[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
)

const maxJSONSafeInteger = float64(9007199254740991)

var sessionOperationCapabilities = map[string][]string{
	"repo.read":     {"repo:read"},
	"repo.create":   {"repo:create"},
	"git.read":      {"repo:read"},
	"git.push":      {"repo:read", "repo:write"},
	"pr.create":     {"pr:create", "repo:read"},
	"pr.read":       {"repo:read"},
	"pr.rebase":     {"repo:read", "repo:write"},
	"ci.read":       {"repo:read"},
	"review.read":   {"repo:read"},
	"review.submit": {"repo:read"},
	"pr.merge":      {"repo:read", "repo:write"},
	"repo.admin":    {"repo:read"},
}

var (
	prCreateConstraintKeys = map[string]struct{}{
		"base_ref": {},
		"head_ref": {},
	}
	prRebaseConstraintKeys = map[string]struct{}{
		"pull_request_number":         {},
		"forgejo_pull_request_number": {},
		"expected_head_sha":           {},
		"expected_base_sha":           {},
	}
	prMergeConstraintKeys = map[string]struct{}{
		"pull_request_number":         {},
		"forgejo_pull_request_number": {},
		"expected_head_sha":           {},
		"merge_method":                {},
	}
	reviewReadConstraintKeys = map[string]struct{}{
		"pull_request_number":         {},
		"forgejo_pull_request_number": {},
	}
)

type workloadAssertionRequest struct {
	Purpose               string                      `json:"purpose"`
	Target                workloadAssertionTarget     `json:"target"`
	RequestedResource     *workloadAssertionResource  `json:"requested_resource,omitempty"`
	RequestedOperation    *workloadAssertionOperation `json:"requested_operation,omitempty"`
	RequestedCapabilities []string                    `json:"requested_capabilities,omitempty"`
	RequestedTTL          json.RawMessage             `json:"requested_ttl,omitempty"`
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
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
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

	requestedTTL, requestedTTLPresent, err := normalizeRequestedTTL(req.RequestedTTL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !isSessionExchange && requestedTTLPresent {
		writeError(w, http.StatusBadRequest, "external PR link assertions do not accept requested TTL")
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

	secret := workloadAssertionSigningSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "workload assertion signing is not configured")
		return
	}
	issuer := workloadAssertionIssuer()
	issuerInstanceID := workloadAssertionIssuerInstanceID()
	if err := ValidateWorkloadAssertionConfiguration(issuer, issuerInstanceID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "workload assertion identity is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate workload authority")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if !lockRunningTaskTokenForAssertion(w, r, qtx) {
		return
	}
	if h.WorkloadAssertionHook != nil {
		h.WorkloadAssertionHook("authority_locked")
	}
	resolved, ok := h.resolveTaskWorkload(w, r, qtx, requireIssue)
	if !ok {
		return
	}

	now := h.currentWorkloadAssertionTime()
	assertionID := h.newWorkloadAssertionID()
	var authorityExpiresAt time.Time
	if isSessionExchange {
		var enriched bool
		authorityExpiresAt, enriched = h.enrichSessionExchangeWorkload(w, r, qtx, &resolved, issuerInstanceID, assertionID, *scope, now)
		if !enriched {
			return
		}
	}
	expiresAt := now.Add(workloadAssertionTTL)
	if !authorityExpiresAt.IsZero() && authorityExpiresAt.Before(expiresAt) {
		expiresAt = authorityExpiresAt
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
	if requestedTTLPresent {
		claims["requested_ttl"] = requestedTTL
	}
	assertion, err := signAssertionJWT(claims, secret, workloadAssertionJWTType, keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign workload assertion")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workload authority")
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
	secret := legacyExternalPRSigningSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "external PR link token signing is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate workload authority")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if !lockRunningTaskTokenForAssertion(w, r, qtx) {
		return
	}
	if h.WorkloadAssertionHook != nil {
		h.WorkloadAssertionHook("authority_locked")
	}
	resolved, ok := h.resolveTaskWorkload(w, r, qtx, true)
	if !ok {
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
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workload authority")
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

func lockRunningTaskTokenForAssertion(w http.ResponseWriter, r *http.Request, queries *db.Queries) bool {
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
	if _, err := queries.LockRunningTaskTokenForAssertion(r.Context(), db.LockRunningTaskTokenForAssertionParams{
		TokenHash: tokenHash, TaskID: taskID, WorkspaceID: workspaceID,
	}); err != nil {
		writeError(w, http.StatusUnauthorized, "task token is no longer executable")
		return false
	}
	return true
}

func (h *Handler) resolveTaskWorkload(w http.ResponseWriter, r *http.Request, queries *db.Queries, requireIssue bool) (resolvedTaskWorkload, bool) {
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return resolvedTaskWorkload{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace id")
	if !ok {
		return resolvedTaskWorkload{}, false
	}
	task, err := queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return resolvedTaskWorkload{}, false
	}
	if requireIssue && !task.IssueID.Valid {
		writeError(w, http.StatusBadRequest, "task has no issue")
		return resolvedTaskWorkload{}, false
	}
	workspace, err := queries.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return resolvedTaskWorkload{}, false
	}
	agent, err := queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: task.AgentID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return resolvedTaskWorkload{}, false
	}
	workload := workloadAssertionWorkload{
		Workspace: workspace.Slug, WorkspaceID: uuidToString(workspaceID), AgentID: uuidToString(task.AgentID),
		AgentName: agent.Name, TaskID: uuidToString(task.ID), RunID: uuidToString(task.ID),
	}
	if task.IssueID.Valid {
		issue, err := queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: workspaceID})
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
func (h *Handler) enrichSessionExchangeWorkload(w http.ResponseWriter, r *http.Request, queries *db.Queries, resolved *resolvedTaskWorkload, issuerInstanceID, assertionID string, scope workloadAssertionScope, now time.Time) (time.Time, bool) {
	authority, err := queries.GetWorkspaceWorkloadAuthority(r.Context(), resolved.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "workload authority is unavailable")
		return time.Time{}, false
	}
	policyClass := authority.PolicyClass
	var authorityExpiresAt time.Time
	if scope.Operation.Name == "pr.merge" {
		delegation, err := h.lockActivePRMergeDelegationForAssertion(r, queries, *resolved, scope, now)
		if errors.Is(err, errActivePRMergeDelegationRequired) {
			writeError(w, http.StatusForbidden, "active exact PR merge delegation required")
			return time.Time{}, false
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "PR merge delegation is unavailable")
			return time.Time{}, false
		}
		policyClass = workspaceMaintainerPolicyClass
		authorityExpiresAt = delegation.ExpiresAt.Time.UTC()
	}
	workload, err := assembleSessionExchangeWorkload(*resolved, authority, issuerInstanceID, assertionID, policyClass)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "workload authority is unavailable")
		return time.Time{}, false
	}
	resolved.Workload = workload
	return authorityExpiresAt, true
}

// assembleSessionExchangeWorkload is the canonical producer kernel used by the
// HTTP handler. Database-backed bridge tests call the handler rather than this
// helper; keeping assembly here gives every producer path one field mapping.
func assembleSessionExchangeWorkload(resolved resolvedTaskWorkload, authority db.WorkspaceWorkloadAuthority, issuerInstanceID, assertionID, policyClass string) (workloadAssertionWorkload, error) {
	if !authority.TeamIdentityID.Valid || authority.MembershipEpoch <= 0 || authority.PolicyClass != workspaceDefaultPolicyClass ||
		(policyClass != workspaceDefaultPolicyClass && policyClass != workspaceMaintainerPolicyClass) {
		return workloadAssertionWorkload{}, fmt.Errorf("invalid workload authority")
	}

	workload := resolved.Workload
	workload.Actor = &workloadActor{Type: "agent", ID: workload.AgentID}
	workload.WorkloadContext = &workloadContextV1{
		Schema:           workloadContextSchema,
		IssuerInstanceID: issuerInstanceID,
		Subject:          "urn:multica:agent:" + workload.AgentID,
		CorrelationID:    assertionID,
		WorkspaceID:      workload.WorkspaceID,
		AgentID:          workload.AgentID,
		SquadID:          uuidToString(resolved.Task.SquadID),
		IssueID:          workload.IssueID,
		IssueKey:         workload.IssueKey,
		TaskID:           workload.TaskID,
		RunID:            workload.RunID,
		TriggerID:        uuidToString(resolved.Task.TriggerCommentID),
		RuntimeID:        uuidToString(resolved.Task.RuntimeID),
	}
	workload.Authority = &workloadAuthorityV1{
		Schema:          workloadAuthoritySchema,
		TeamIdentityID:  uuidToString(authority.TeamIdentityID),
		MembershipEpoch: authority.MembershipEpoch,
		PolicyClass:     policyClass,
	}
	return workload, nil
}

func (h *Handler) currentWorkloadAssertionTime() time.Time {
	if h.workloadAssertionNow != nil {
		return h.workloadAssertionNow().UTC()
	}
	return time.Now().UTC()
}

func (h *Handler) newWorkloadAssertionID() string {
	if h.workloadAssertionID != nil {
		return h.workloadAssertionID()
	}
	return uuid.NewString()
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
		operationName, mapErr := legacyOperationForCapabilities(capabilities)
		if mapErr != nil {
			return workloadAssertionScope{}, mapErr
		}
		operation, err = normalizeRequestedOperation(workloadAssertionOperation{Name: operationName, Constraints: map[string]any{}})
		if err != nil {
			return workloadAssertionScope{}, err
		}
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
	rawName := operation.Name
	operation.Name = strings.ToLower(strings.TrimSpace(rawName))
	if operation.Name == "" || rawName != operation.Name {
		return workloadAssertionOperation{}, fmt.Errorf("requested operation name must use canonical casing")
	}
	if _, ok := sessionOperationCapabilities[operation.Name]; !ok {
		return workloadAssertionOperation{}, fmt.Errorf("requested operation is not registered")
	}
	if operation.Constraints == nil {
		return workloadAssertionOperation{}, fmt.Errorf("requested operation constraints must be an object")
	}

	var (
		constraints map[string]any
		err         error
	)
	switch operation.Name {
	case "repo.read", "git.read", "git.push":
		constraints, err = normalizeExactEmptyConstraints(operation.Name, operation.Constraints)
	case "pr.create":
		constraints, err = normalizePRCreateConstraints(operation.Constraints)
	case "pr.read":
		constraints, err = normalizePRReadConstraints(operation.Constraints)
	case "pr.rebase":
		constraints, err = normalizePRRebaseConstraints(operation.Constraints)
	case "pr.merge":
		constraints, err = normalizePRMergeConstraints(operation.Constraints)
	case "review.read":
		constraints, err = normalizeReviewReadConstraints(operation.Constraints)
	case "ci.read":
		constraints, err = normalizeCIReadConstraints(operation.Constraints)
	case "review.submit", "repo.admin", "repo.create":
		return workloadAssertionOperation{}, fmt.Errorf("requested operation is deferred and cannot be signed by the default workload assertion issuer")
	default:
		return workloadAssertionOperation{}, fmt.Errorf("requested operation is not registered")
	}
	if err != nil {
		return workloadAssertionOperation{}, err
	}
	operation.Constraints = constraints
	return operation, nil
}

func normalizeExactEmptyConstraints(operation string, input map[string]any) (map[string]any, error) {
	if len(input) != 0 {
		return nil, fmt.Errorf("requested %s operation requires exact empty constraints", operation)
	}
	return map[string]any{}, nil
}

func normalizePRCreateConstraints(input map[string]any) (map[string]any, error) {
	if err := requireExactConstraintKeys("pr.create", input, prCreateConstraintKeys); err != nil {
		return nil, err
	}
	baseRef, err := normalizeCanonicalGitBranchRef("pr.create", "base_ref", input["base_ref"])
	if err != nil {
		return nil, err
	}
	headRef, err := normalizeCanonicalGitBranchRef("pr.create", "head_ref", input["head_ref"])
	if err != nil {
		return nil, err
	}
	return map[string]any{"base_ref": baseRef, "head_ref": headRef}, nil
}

func normalizePRReadConstraints(input map[string]any) (map[string]any, error) {
	if len(input) != 1 {
		return nil, fmt.Errorf("requested pr.read operation requires exactly one constraint variant")
	}
	if value, ok := input["pull_request_number"]; ok {
		number, err := normalizePositiveSafeInteger("pr.read", "pull_request_number", value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"pull_request_number": number}, nil
	}
	if value, ok := input["head_ref"]; ok {
		ref, err := normalizeCanonicalGitBranchRef("pr.read", "head_ref", value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"head_ref": ref}, nil
	}
	return nil, fmt.Errorf("requested pr.read constraint variant is not registered")
}

func normalizePRRebaseConstraints(input map[string]any) (map[string]any, error) {
	if len(input) != len(prRebaseConstraintKeys) {
		return nil, fmt.Errorf("requested pr.rebase operation requires its exact constraint set")
	}
	for key := range input {
		if _, ok := prRebaseConstraintKeys[key]; !ok {
			return nil, fmt.Errorf("requested pr.rebase constraint key is not registered")
		}
	}

	pullRequestNumber, err := normalizePositiveSafeInteger("pr.rebase", "pull_request_number", input["pull_request_number"])
	if err != nil {
		return nil, err
	}
	forgejoPullRequestNumber, err := normalizePositiveSafeInteger("pr.rebase", "forgejo_pull_request_number", input["forgejo_pull_request_number"])
	if err != nil {
		return nil, err
	}
	expectedHeadSHA, err := normalizeCanonicalGitSHA("pr.rebase", "expected_head_sha", input["expected_head_sha"])
	if err != nil {
		return nil, err
	}
	expectedBaseSHA, err := normalizeCanonicalGitSHA("pr.rebase", "expected_base_sha", input["expected_base_sha"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"pull_request_number":         pullRequestNumber,
		"forgejo_pull_request_number": forgejoPullRequestNumber,
		"expected_head_sha":           expectedHeadSHA,
		"expected_base_sha":           expectedBaseSHA,
	}, nil
}

func normalizePRMergeConstraints(input map[string]any) (map[string]any, error) {
	if err := requireExactConstraintKeys("pr.merge", input, prMergeConstraintKeys); err != nil {
		return nil, err
	}
	pullRequestNumber, err := normalizePositiveSafeInteger("pr.merge", "pull_request_number", input["pull_request_number"])
	if err != nil {
		return nil, err
	}
	forgejoPullRequestNumber, err := normalizePositiveSafeInteger("pr.merge", "forgejo_pull_request_number", input["forgejo_pull_request_number"])
	if err != nil {
		return nil, err
	}
	expectedHeadSHA, err := normalizeCanonicalGitSHA("pr.merge", "expected_head_sha", input["expected_head_sha"])
	if err != nil {
		return nil, err
	}
	mergeMethod, ok := input["merge_method"].(string)
	if !ok || mergeMethod != strings.TrimSpace(mergeMethod) {
		return nil, fmt.Errorf("requested pr.merge merge_method must be canonical")
	}
	switch mergeMethod {
	case "merge", "rebase", "rebase-merge", "squash", "fast-forward-only":
	default:
		return nil, fmt.Errorf("requested pr.merge merge_method is not registered")
	}
	return map[string]any{
		"pull_request_number":         pullRequestNumber,
		"forgejo_pull_request_number": forgejoPullRequestNumber,
		"expected_head_sha":           expectedHeadSHA,
		"merge_method":                mergeMethod,
	}, nil
}

func normalizeReviewReadConstraints(input map[string]any) (map[string]any, error) {
	if err := requireExactConstraintKeys("review.read", input, reviewReadConstraintKeys); err != nil {
		return nil, err
	}
	pullRequestNumber, err := normalizePositiveSafeInteger("review.read", "pull_request_number", input["pull_request_number"])
	if err != nil {
		return nil, err
	}
	forgejoPullRequestNumber, err := normalizePositiveSafeInteger("review.read", "forgejo_pull_request_number", input["forgejo_pull_request_number"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"pull_request_number":         pullRequestNumber,
		"forgejo_pull_request_number": forgejoPullRequestNumber,
	}, nil
}

func normalizeCIReadConstraints(input map[string]any) (map[string]any, error) {
	switch len(input) {
	case 0:
		return map[string]any{}, nil
	case 1:
		value, ok := input["run_id"]
		if !ok {
			return nil, fmt.Errorf("requested ci.read constraint variant is not registered")
		}
		runID, err := normalizePositiveSafeInteger("ci.read", "run_id", value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run_id": runID}, nil
	case 2, 3:
		for key := range input {
			if _, ok := reviewReadConstraintKeys[key]; !ok && key != "head_sha" {
				return nil, fmt.Errorf("requested ci.read constraint key is not registered")
			}
		}
	default:
		return nil, fmt.Errorf("requested ci.read operation requires one exact constraint variant")
	}

	pullRequestNumber, err := normalizePositiveSafeInteger("ci.read", "pull_request_number", input["pull_request_number"])
	if err != nil {
		return nil, err
	}
	forgejoPullRequestNumber, err := normalizePositiveSafeInteger("ci.read", "forgejo_pull_request_number", input["forgejo_pull_request_number"])
	if err != nil {
		return nil, err
	}
	constraints := map[string]any{
		"pull_request_number":         pullRequestNumber,
		"forgejo_pull_request_number": forgejoPullRequestNumber,
	}
	if value, ok := input["head_sha"]; ok {
		headSHA, err := normalizeCanonicalGitSHA("ci.read", "head_sha", value)
		if err != nil {
			return nil, err
		}
		constraints["head_sha"] = headSHA
	}
	return constraints, nil
}

func requireExactConstraintKeys(operation string, input map[string]any, expected map[string]struct{}) error {
	if len(input) != len(expected) {
		return fmt.Errorf("requested %s operation requires its exact constraint set", operation)
	}
	for key := range input {
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("requested %s constraint key is not registered", operation)
		}
	}
	return nil
}

func normalizePositiveSafeInteger(operation, field string, value any) (float64, error) {
	number, ok := value.(float64)
	if !ok || number < 1 || number > maxJSONSafeInteger || number != float64(int64(number)) {
		return 0, fmt.Errorf("requested %s %s must be a positive safe integer", operation, field)
	}
	return number, nil
}

func normalizeCanonicalGitSHA(operation, field string, value any) (string, error) {
	sha, ok := value.(string)
	if !ok || !canonicalGitSHA1Pattern.MatchString(sha) {
		return "", fmt.Errorf("requested %s %s must be canonical lowercase 40 hex", operation, field)
	}
	return sha, nil
}

func normalizeCanonicalGitBranchRef(operation, field string, value any) (string, error) {
	ref, ok := value.(string)
	if !ok || !isCanonicalGitBranchRef(ref) {
		return "", fmt.Errorf("requested %s %s must be a canonical branch ref", operation, field)
	}
	return ref, nil
}

func isCanonicalGitBranchRef(ref string) bool {
	if ref == "" || len(ref) > 2048 || ref != strings.TrimSpace(ref) || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || secretShapedValuePattern.MatchString(ref) {
		return false
	}
	branch := ref
	if strings.HasPrefix(branch, "refs/") {
		if !strings.HasPrefix(branch, "refs/heads/") {
			return false
		}
		branch = strings.TrimPrefix(branch, "refs/heads/")
	}
	if branch == "" || branch == "@" || strings.HasPrefix(branch, ".") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "//") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, char := range branch {
		if char < 0x20 || char == 0x7f || strings.ContainsRune(` ~^:?*[\\`, char) {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
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

func normalizeRequestedTTL(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, fmt.Errorf("requested_ttl must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("requested_ttl must be a string")
	}
	if value != strings.TrimSpace(value) || !canonicalDurationPattern.MatchString(value) || secretShapedValuePattern.MatchString(value) {
		return "", false, fmt.Errorf("requested_ttl must be a canonical positive duration")
	}
	ttl, err := time.ParseDuration(value)
	if err != nil || ttl <= 0 || ttl > maximumRequestedSessionTTL {
		return "", false, fmt.Errorf("requested_ttl must be positive and at most 15m")
	}
	return value, true, nil
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

// ValidateWorkloadAssertionConfiguration keeps the deployment-unique JWT
// issuer separate from the canonical AGS trusted_issuers[].id linkage.
func ValidateWorkloadAssertionConfiguration(rawIssuer, rawIssuerInstanceID string) error {
	issuer := strings.TrimSpace(rawIssuer)
	if issuer == "" {
		return fmt.Errorf("MULTICA_WORKLOAD_ASSERTION_ISSUER is required")
	}
	if issuer == defaultWorkloadAssertionIssuer {
		return fmt.Errorf("MULTICA_WORKLOAD_ASSERTION_ISSUER must be deployment-unique, not %q", defaultWorkloadAssertionIssuer)
	}

	issuerInstanceID := strings.TrimSpace(rawIssuerInstanceID)
	if issuerInstanceID == "" {
		return fmt.Errorf("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID is required")
	}
	if issuerInstanceID != rawIssuerInstanceID || !canonicalSafeIDPattern.MatchString(issuerInstanceID) || secretShapedValuePattern.MatchString(issuerInstanceID) {
		return fmt.Errorf("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID must be a canonical secret-free safe ID")
	}
	switch strings.ToLower(issuerInstanceID) {
	case "multica", "placeholder", "example", "change-me", "changeme", "replace-me", "issuer-instance-id":
		return fmt.Errorf("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID must not be a placeholder")
	}
	if issuerInstanceID == issuer {
		return fmt.Errorf("workload assertion issuer instance ID must differ from the JWT issuer")
	}
	return nil
}

func workloadAssertionIssuer() string {
	return strings.TrimSpace(os.Getenv("MULTICA_WORKLOAD_ASSERTION_ISSUER"))
}

func workloadAssertionIssuerInstanceID() string {
	return os.Getenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID")
}

func workloadAssertionKeyID() string {
	if keyID := strings.TrimSpace(os.Getenv("MULTICA_WORKLOAD_ASSERTION_KEY_ID")); keyID != "" {
		return keyID
	}
	return defaultWorkloadAssertionKeyID
}
