package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const defaultExternalPRLinkTokenAudience = "external-pr-link"

var (
	canonicalExternalPRDigestPattern    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	canonicalExternalPRInstancePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
	canonicalRepositoryComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	canonicalGitSHA1Pattern             = regexp.MustCompile(`^[a-f0-9]{40}$`)
	externalPRSecretShapedValuePattern  = regexp.MustCompile(`(?i)(?:mat_[A-Za-z0-9_-]+|ags_sess_[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
)

func configuredExternalPRServiceInstance() string {
	return os.Getenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID")
}

func decodeClosedJSONRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func normalizeCanonicalGitBranchRef(operation, field string, value any) (string, error) {
	ref, ok := value.(string)
	if !ok || !isCanonicalGitBranchRef(ref) {
		return "", fmt.Errorf("requested %s %s must be a canonical branch ref", operation, field)
	}
	return ref, nil
}

func isCanonicalGitBranchRef(ref string) bool {
	if ref == "" || len(ref) > 2048 || ref != strings.TrimSpace(ref) || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || externalPRSecretShapedValuePattern.MatchString(ref) {
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

type externalPullRequestLinkRequest struct {
	Provider                string `json:"provider"`
	IssueID                 string `json:"issue_id"`
	WorkspaceID             string `json:"workspace_id"`
	Workspace               string `json:"workspace"`
	IssueKey                string `json:"issue_key"`
	ExternalRepo            string `json:"external_repo"`
	ExternalNumber          int32  `json:"external_number"`
	ExternalURL             string `json:"external_url"`
	MergeProvider           string `json:"merge_provider"`
	MergeRepo               string `json:"merge_repo"`
	MergeNumber             int32  `json:"merge_number"`
	MergeURL                string `json:"merge_url"`
	MergedSHA               string `json:"merged_sha"`
	TargetInstance          string `json:"target_instance,omitempty"`
	CanonicalRepositoryID   string `json:"canonical_repository_id,omitempty"`
	CanonicalRepository     string `json:"canonical_repository,omitempty"`
	ProviderBindingID       string `json:"provider_binding_id,omitempty"`
	ProviderBindingRevision string `json:"provider_binding_revision,omitempty"`
	ProviderRepository      string `json:"provider_repository,omitempty"`
	ExpectedHeadSHA         string `json:"expected_head_sha,omitempty"`
	ExpectedBaseSHA         string `json:"expected_base_sha,omitempty"`
	BaseRef                 string `json:"base_ref,omitempty"`
	DelegatedMergeMethod    string `json:"delegated_merge_method,omitempty"`
	ProjectionFactsRevision string `json:"projection_facts_revision,omitempty"`
	CompletionIntent        *bool  `json:"completion_intent,omitempty"`
	LinkConfidence          string `json:"link_confidence"`
	State                   string `json:"state"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type validatedExternalPRMergeProjection struct {
	present                 bool
	targetInstance          string
	canonicalRepositoryID   string
	canonicalRepository     string
	providerBindingID       string
	providerBindingRevision string
	providerRepository      string
	expectedHeadSHA         string
	expectedBaseSHA         string
	baseRef                 string
	mergeMethod             string
	factsRevision           string
}

func validateExternalPRMergeProjection(req externalPullRequestLinkRequest, mergeProvider string) (validatedExternalPRMergeProjection, error) {
	values := []string{req.TargetInstance, req.CanonicalRepositoryID, req.CanonicalRepository, req.ProviderBindingID,
		req.ProviderBindingRevision, req.ProviderRepository, req.ExpectedHeadSHA, req.ExpectedBaseSHA,
		req.BaseRef, req.DelegatedMergeMethod, req.ProjectionFactsRevision}
	present := 0
	for _, value := range values {
		if value != "" {
			present++
		}
	}
	if present == 0 {
		return validatedExternalPRMergeProjection{}, nil
	}
	if present != len(values) || mergeProvider != "forgejo" || req.MergeNumber < 1 {
		return validatedExternalPRMergeProjection{}, fmt.Errorf("external PR merge projection facts require their exact complete set")
	}
	instance := strings.TrimSpace(req.TargetInstance)
	configuredInstance := configuredExternalPRServiceInstance()
	if instance != req.TargetInstance || !canonicalExternalPRInstancePattern.MatchString(instance) || configuredInstance == "" || instance != configuredInstance {
		return validatedExternalPRMergeProjection{}, fmt.Errorf("target_instance does not match the configured service instance")
	}
	canonicalRepository := strings.TrimSpace(req.CanonicalRepository)
	providerRepository := strings.TrimSpace(req.ProviderRepository)
	if !isCanonicalRepositoryName(canonicalRepository) || !isCanonicalRepositoryName(providerRepository) ||
		strings.TrimSpace(req.ExternalRepo) != canonicalRepository || strings.TrimSpace(req.MergeRepo) != providerRepository {
		return validatedExternalPRMergeProjection{}, fmt.Errorf("external PR merge projection repositories are not canonical")
	}
	if !canonicalExternalPRDigestPattern.MatchString(req.CanonicalRepositoryID) ||
		!canonicalExternalPRDigestPattern.MatchString(req.ProviderBindingID) ||
		!canonicalExternalPRDigestPattern.MatchString(req.ProviderBindingRevision) ||
		!canonicalExternalPRDigestPattern.MatchString(req.ProjectionFactsRevision) {
		return validatedExternalPRMergeProjection{}, fmt.Errorf("external PR merge projection binding identities are not canonical")
	}
	if !canonicalGitSHA1Pattern.MatchString(req.ExpectedHeadSHA) || !canonicalGitSHA1Pattern.MatchString(req.ExpectedBaseSHA) {
		return validatedExternalPRMergeProjection{}, fmt.Errorf("external PR merge projection head and base must be canonical git SHAs")
	}
	baseRef, err := normalizeCanonicalGitBranchRef("pr.merge", "base_ref", req.BaseRef)
	if err != nil || baseRef != req.BaseRef {
		return validatedExternalPRMergeProjection{}, fmt.Errorf("external PR merge projection base_ref is not canonical")
	}
	method := strings.TrimSpace(req.DelegatedMergeMethod)
	switch method {
	case "merge", "rebase", "rebase-merge", "squash", "fast-forward-only":
	default:
		return validatedExternalPRMergeProjection{}, fmt.Errorf("external PR merge projection method is not registered")
	}
	return validatedExternalPRMergeProjection{
		present: true, targetInstance: instance, canonicalRepositoryID: req.CanonicalRepositoryID,
		canonicalRepository: canonicalRepository, providerBindingID: req.ProviderBindingID,
		providerBindingRevision: req.ProviderBindingRevision, providerRepository: providerRepository,
		expectedHeadSHA: req.ExpectedHeadSHA, expectedBaseSHA: req.ExpectedBaseSHA,
		baseRef: baseRef, mergeMethod: method, factsRevision: req.ProjectionFactsRevision,
	}, nil
}

func isCanonicalRepositoryName(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !canonicalRepositoryComponentPattern.MatchString(parts[0]) || !canonicalRepositoryComponentPattern.MatchString(parts[1]) {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || strings.HasSuffix(strings.ToLower(part), ".git") {
			return false
		}
	}
	return true
}

type externalCompleteFromPRResponse struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	IssueID string `json:"issue_id,omitempty"`
}

type externalPullRequestLinkResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	IssueID          string  `json:"issue_id"`
	Provider         string  `json:"provider"`
	ExternalRepo     string  `json:"external_repo"`
	ExternalNumber   int32   `json:"external_number"`
	ExternalURL      *string `json:"external_url"`
	State            string  `json:"state"`
	LinkConfidence   string  `json:"link_confidence"`
	CompletionIntent bool    `json:"completion_intent"`
	MergeProvider    *string `json:"merge_provider"`
	MergeRepo        *string `json:"merge_repo"`
	MergeNumber      *int32  `json:"merge_number"`
	MergeURL         *string `json:"merge_url"`
	MergedSHA        *string `json:"merged_sha"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// validateTypedAGSExternalPRTerminal is deliberately narrower than the
// provider-neutral link validator below. Only the frozen AGS terminal seam
// uses it; generic open links and non-AGS providers retain their existing
// admission rules.
func validateTypedAGSExternalPRTerminal(req externalPullRequestLinkRequest, path string) error {
	if strings.TrimSpace(req.Provider) != "ags" {
		return externalPRValidation("typed external PR terminal provider must be ags")
	}
	if req.Workspace != strings.TrimSpace(req.Workspace) || req.IssueKey != strings.TrimSpace(req.IssueKey) || req.ExternalURL != strings.TrimSpace(req.ExternalURL) {
		return externalPRValidation("typed external PR terminal identity fields must be canonical")
	}
	if strings.TrimSpace(req.Workspace) == "" || strings.TrimSpace(req.IssueKey) == "" || strings.TrimSpace(req.ExternalURL) == "" {
		return externalPRValidation("typed external PR terminal workspace, issue_key, and external_url are required")
	}
	if _, err := parseExternalPRUUID(req.WorkspaceID); err != nil {
		return externalPRValidation("typed external PR terminal workspace_id must be a UUID")
	}
	if _, err := parseExternalPRUUID(req.IssueID); err != nil {
		return externalPRValidation("typed external PR terminal issue_id must be a UUID")
	}
	if strings.TrimSpace(req.ExternalRepo) == "" || req.ExternalRepo != strings.TrimSpace(req.ExternalRepo) || req.ExternalNumber <= 0 {
		return externalPRValidation("typed external PR terminal external repository identity is required")
	}
	if err := validateExternalPRURL("external_url", req.ExternalURL); err != nil {
		return externalPRValidation(err.Error())
	}
	if strings.TrimSpace(req.MergeProvider) != "forgejo" {
		return externalPRValidation("typed external PR terminal merge_provider must be forgejo")
	}
	if strings.TrimSpace(req.MergeRepo) == "" || req.MergeNumber <= 0 || strings.TrimSpace(req.MergeURL) == "" {
		return externalPRValidation("typed external PR terminal Forgejo merge facts are required")
	}
	if err := validateExternalPRURL("merge_url", req.MergeURL); err != nil {
		return externalPRValidation(err.Error())
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || req.IdempotencyKey != strings.TrimSpace(req.IdempotencyKey) {
		return externalPRValidation("typed external PR terminal idempotency_key is required in the request body")
	}
	if req.CompletionIntent == nil {
		return externalPRValidation("typed external PR terminal completion_intent is required in the request body")
	}
	if strings.TrimSpace(req.LinkConfidence) != "authoritative" {
		return externalPRValidation("typed external PR terminal link_confidence must be authoritative")
	}
	if req.State != strings.TrimSpace(req.State) {
		return externalPRValidation("typed external PR terminal state must be canonical")
	}
	if req.MergedSHA != strings.TrimSpace(req.MergedSHA) {
		return externalPRValidation("typed external PR terminal merged_sha must be canonical")
	}
	if _, err := validateExternalPRMergeProjection(req, "forgejo"); err != nil {
		return externalPRValidation(err.Error())
	}
	switch path {
	case "/api/integrations/external-pr/links":
		if req.State != "closed" {
			return externalPRValidation("closed typed external PR terminal facts must use the /links path")
		}
		if req.MergedSHA != "" {
			return externalPRValidation("closed typed external PR terminal facts must omit merged_sha")
		}
		if *req.CompletionIntent {
			return externalPRValidation("closed typed external PR terminal facts must set completion_intent to false")
		}
	case "/api/integrations/external-pr/complete-from-merge":
		if req.State != "merged" {
			return externalPRValidation("merged typed external PR terminal facts must use the /complete-from-merge path")
		}
		if !canonicalGitSHA1Pattern.MatchString(req.MergedSHA) {
			return externalPRValidation("merged typed external PR terminal merged_sha must be lowercase 40-hex")
		}
		if !*req.CompletionIntent {
			return externalPRValidation("merged typed external PR terminal completion_intent must be true")
		}
	default:
		return externalPRValidation("typed external PR terminal path is not registered")
	}
	return nil
}

func isTypedAGSExternalPRTerminal(req externalPullRequestLinkRequest) bool {
	state := strings.ToLower(strings.TrimSpace(req.State))
	return normalizeExternalPRProvider(req.Provider) == "ags" &&
		(state == "closed" || state == "merged")
}

func (h *Handler) RegisterExternalPullRequestLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireExternalPRServiceToken(w, r) {
		return
	}
	var req externalPullRequestLinkRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	typedTerminal := isTypedAGSExternalPRTerminal(req)
	if typedTerminal {
		if err := validateTypedAGSExternalPRTerminal(req, "/api/integrations/external-pr/links"); err != nil {
			writeExternalPRError(w, err)
			return
		}
	} else if req.IdempotencyKey == "" {
		// Header fallback remains part of the provider-neutral generic link API;
		// typed AGS terminal requests are required to carry the body key.
		req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	upserted, err := h.upsertExternalPullRequestLink(r.Context(), req)
	if err != nil {
		slog.Warn("external PR integration: register PR link failed", "error", err)
		writeExternalPRError(w, err)
		return
	}
	h.publishExternalPRProjectionUpdate(upserted, req)
	if !typedTerminal && (upserted.State == "merged" || upserted.State == "closed") {
		// This is the legacy provider-neutral synchronous path. It cannot satisfy
		// typed AGS admission because typed terminal requests are handled above
		// and are always worker-only.
		if _, err := h.evaluatePullRequestCompletionWithActivitiesResultAndWork(
			r.Context(),
			upserted.Issue,
			"external_pr_terminal_fact",
			[]completionActivitySpec{externalPRCompletionActivitySpec(req)},
			upserted.WorkID,
			upserted.SourceRevision,
		); err != nil {
			slog.Warn("external PR integration: terminal reevaluation failed", "error", err)
			writeExternalPRError(w, externalPRInfrastructureError{err: err})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) CompleteIssueFromExternalPR(w http.ResponseWriter, r *http.Request) {
	if !h.requireExternalPRServiceToken(w, r) {
		return
	}
	var req externalPullRequestLinkRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	if normalizeExternalPRProvider(req.Provider) == "ags" {
		// The typed AGS seam is body-authoritative. Do not infer state,
		// completion intent, confidence, or idempotency from headers/defaults.
		if err := validateTypedAGSExternalPRTerminal(req, "/api/integrations/external-pr/complete-from-merge"); err != nil {
			writeExternalPRError(w, err)
			return
		}
		upserted, err := h.upsertExternalPullRequestLink(r.Context(), req)
		if err != nil {
			writeExternalPRError(w, err)
			return
		}
		h.publishExternalPRProjectionUpdate(upserted, req)
		writeJSON(w, http.StatusOK, externalCompleteFromPRResponse{
			Outcome: "accepted", Reason: "queued_for_reconciliation", IssueID: req.IssueID,
		})
		return
	}

	// Explicit legacy/non-typed compatibility path. Its provider-neutral
	// behavior is intentionally isolated from the frozen AGS terminal seam.
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	req.State = "merged"
	completionIntent := true
	req.CompletionIntent = &completionIntent
	if strings.TrimSpace(req.LinkConfidence) == "" {
		req.LinkConfidence = "authoritative"
	}
	upserted, err := h.upsertExternalPullRequestLink(r.Context(), req)
	if err != nil {
		writeExternalPRError(w, err)
		return
	}
	h.publishExternalPRProjectionUpdate(upserted, req)
	out, err := h.completeLeafChildIssueFromExternalPR(r, req, upserted.WorkID, upserted.SourceRevision)
	if err != nil {
		slog.Warn("external PR integration: completion transaction failed", "error", err)
		writeExternalPRError(w, externalPRInfrastructureError{err: err})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) publishExternalPRProjectionUpdate(
	upserted externalPRUpsertResult,
	req externalPullRequestLinkRequest,
) {
	if upserted.Replayed {
		return
	}
	h.publish(
		protocol.EventPullRequestUpdated,
		uuidToString(upserted.Issue.WorkspaceID),
		"system",
		"",
		map[string]any{
			"issue_id":        uuidToString(upserted.Issue.ID),
			"provider":        req.Provider,
			"external_repo":   req.ExternalRepo,
			"external_number": req.ExternalNumber,
			"state":           upserted.State,
		},
	)
}

func (h *Handler) ListExternalPullRequestsForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	links, err := h.listExternalPullRequestLinks(r.Context(), issue)
	if err != nil {
		slog.Warn("external PR integration: list issue links failed", "error", err, "issue_id", uuidToString(issue.ID))
		writeError(w, http.StatusInternalServerError, "failed to list external pull requests")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"external_pull_requests": links})
}

func (h *Handler) requireExternalPRServiceToken(w http.ResponseWriter, r *http.Request) bool {
	want := strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN"))
	if want == "" {
		writeError(w, http.StatusServiceUnavailable, "external PR service token is not configured")
		return false
	}
	authorization := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) || len(authorization) <= len(prefix) {
		writeError(w, http.StatusUnauthorized, "invalid external PR service token")
		return false
	}
	got := authorization[len(prefix):]
	if strings.TrimSpace(got) != got || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid external PR service token")
		return false
	}
	return true
}

func (h *Handler) listExternalPullRequestLinks(ctx context.Context, issue db.Issue) ([]externalPullRequestLinkResponse, error) {
	rows, err := h.DB.Query(ctx, `
SELECT id, workspace_id, issue_id, provider, external_repo, external_number, external_url,
       state, link_confidence, completion_intent, merge_provider, merge_repo, merge_number,
       merge_url, merged_sha, created_at, updated_at
FROM external_pull_request_link
WHERE workspace_id=$1 AND issue_id=$2
ORDER BY updated_at DESC, created_at DESC, id DESC`, issue.WorkspaceID, issue.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	links := []externalPullRequestLinkResponse{}
	for rows.Next() {
		var (
			id, workspaceID, issueID                                   pgtype.UUID
			provider, externalRepo, state, confidence                  string
			externalNumber                                             int32
			externalURL, mergeProvider, mergeRepo, mergeURL, mergedSHA pgtype.Text
			mergeNumber                                                pgtype.Int4
			completionIntent                                           bool
			createdAt, updatedAt                                       pgtype.Timestamptz
		)
		if err := rows.Scan(&id, &workspaceID, &issueID, &provider, &externalRepo, &externalNumber, &externalURL, &state, &confidence, &completionIntent, &mergeProvider, &mergeRepo, &mergeNumber, &mergeURL, &mergedSHA, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		links = append(links, externalPullRequestLinkResponse{
			ID:               uuidToString(id),
			WorkspaceID:      uuidToString(workspaceID),
			IssueID:          uuidToString(issueID),
			Provider:         provider,
			ExternalRepo:     externalRepo,
			ExternalNumber:   externalNumber,
			ExternalURL:      textToPtr(externalURL),
			State:            state,
			LinkConfidence:   confidence,
			CompletionIntent: completionIntent,
			MergeProvider:    textToPtr(mergeProvider),
			MergeRepo:        textToPtr(mergeRepo),
			MergeNumber:      int4ToPtr(mergeNumber),
			MergeURL:         textToPtr(mergeURL),
			MergedSHA:        textToPtr(mergedSHA),
			CreatedAt:        timestampToString(createdAt),
			UpdatedAt:        timestampToString(updatedAt),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}

type externalPRValidationError struct{ message string }
type externalPRConflictError struct{ message string }
type externalPRInfrastructureError struct{ err error }
type externalPRActivityError struct{ err error }

func (e externalPRValidationError) Error() string     { return e.message }
func (e externalPRConflictError) Error() string       { return e.message }
func (e externalPRInfrastructureError) Error() string { return e.err.Error() }
func (e externalPRInfrastructureError) Unwrap() error { return e.err }
func (e externalPRActivityError) Error() string       { return e.err.Error() }
func (e externalPRActivityError) Unwrap() error       { return e.err }

func externalPRValidation(message string) error {
	return externalPRValidationError{message: message}
}

func externalPRErrorResponse(err error) (int, string) {
	var validation externalPRValidationError
	if errors.As(err, &validation) {
		return http.StatusBadRequest, validation.message
	}
	var conflict externalPRConflictError
	if errors.As(err, &conflict) {
		return http.StatusConflict, conflict.message
	}
	return http.StatusServiceUnavailable, "external PR integration temporarily unavailable"
}

func writeExternalPRError(w http.ResponseWriter, err error) {
	status, message := externalPRErrorResponse(err)
	writeError(w, status, message)
}

type externalPRUpsertResult struct {
	Issue          db.Issue
	LinkID         pgtype.UUID
	WorkID         pgtype.UUID
	SourceRevision string
	State          string
	Replayed       bool
}

type externalPRCanonicalPayload struct {
	Provider                string `json:"provider"`
	IssueID                 string `json:"issue_id"`
	WorkspaceID             string `json:"workspace_id"`
	Workspace               string `json:"workspace"`
	IssueKey                string `json:"issue_key"`
	ExternalRepo            string `json:"external_repo"`
	ExternalNumber          int32  `json:"external_number"`
	ExternalURL             string `json:"external_url"`
	MergeProvider           string `json:"merge_provider"`
	MergeRepo               string `json:"merge_repo"`
	MergeNumber             int32  `json:"merge_number"`
	MergeURL                string `json:"merge_url"`
	MergedSHA               string `json:"merged_sha"`
	TargetInstance          string `json:"target_instance,omitempty"`
	CanonicalRepositoryID   string `json:"canonical_repository_id,omitempty"`
	CanonicalRepository     string `json:"canonical_repository,omitempty"`
	ProviderBindingID       string `json:"provider_binding_id,omitempty"`
	ProviderBindingRevision string `json:"provider_binding_revision,omitempty"`
	ProviderRepository      string `json:"provider_repository,omitempty"`
	ExpectedHeadSHA         string `json:"expected_head_sha,omitempty"`
	ExpectedBaseSHA         string `json:"expected_base_sha,omitempty"`
	BaseRef                 string `json:"base_ref,omitempty"`
	DelegatedMergeMethod    string `json:"delegated_merge_method,omitempty"`
	ProjectionFactsRevision string `json:"projection_facts_revision,omitempty"`
	CompletionIntent        bool   `json:"completion_intent"`
	LinkConfidence          string `json:"link_confidence"`
	State                   string `json:"state"`
}

func hashExternalPRPayload(payload externalPRCanonicalPayload) string {
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (h *Handler) upsertExternalPullRequestLink(ctx context.Context, req externalPullRequestLinkRequest) (externalPRUpsertResult, error) {
	var out externalPRUpsertResult
	workspaceID, err := parseExternalPRUUID(req.WorkspaceID)
	if err != nil {
		return out, externalPRValidation("invalid workspace_id")
	}
	issueID, err := parseExternalPRUUID(req.IssueID)
	if err != nil {
		return out, externalPRValidation("invalid issue_id")
	}
	provider := normalizeExternalPRProvider(req.Provider)
	if provider == "" {
		return out, externalPRValidation("provider is required")
	}
	if !externalPRProviderAllowed(provider) {
		return out, externalPRValidation(fmt.Sprintf("provider %q is not allowed", provider))
	}
	externalRepo := strings.TrimSpace(req.ExternalRepo)
	if externalRepo == "" || req.ExternalNumber <= 0 {
		return out, externalPRValidation("external_repo and external_number are required")
	}
	if err := validateExternalPRURL("external_url", req.ExternalURL); err != nil {
		return out, externalPRValidation(err.Error())
	}
	if err := validateExternalPRURL("merge_url", req.MergeURL); err != nil {
		return out, externalPRValidation(err.Error())
	}
	confidence := strings.TrimSpace(strings.ToLower(req.LinkConfidence))
	if confidence == "" {
		confidence = "authoritative"
	}
	if confidence != "authoritative" && confidence != "inferred" {
		return out, externalPRValidation("invalid link_confidence")
	}
	state := strings.TrimSpace(strings.ToLower(req.State))
	if state == "" {
		state = "open"
	}
	switch state {
	case "open", "draft", "closed", "merged":
	default:
		return out, externalPRValidation("invalid state")
	}
	completionIntent := confidence == "authoritative"
	if req.CompletionIntent != nil {
		completionIntent = *req.CompletionIntent
	}
	mergeProvider := normalizeExternalPRProvider(req.MergeProvider)
	projection, err := validateExternalPRMergeProjection(req, mergeProvider)
	if err != nil {
		return out, externalPRValidation(err.Error())
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	payloadHash := ""
	if idempotencyKey != "" {
		payloadHash = hashExternalPRPayload(externalPRCanonicalPayload{
			Provider: provider, IssueID: uuidToString(issueID), WorkspaceID: uuidToString(workspaceID),
			Workspace: strings.TrimSpace(req.Workspace), IssueKey: strings.TrimSpace(req.IssueKey),
			ExternalRepo: externalRepo, ExternalNumber: req.ExternalNumber,
			ExternalURL: strings.TrimSpace(req.ExternalURL), MergeProvider: mergeProvider,
			MergeRepo: strings.TrimSpace(req.MergeRepo), MergeNumber: req.MergeNumber,
			MergeURL: strings.TrimSpace(req.MergeURL), MergedSHA: strings.TrimSpace(req.MergedSHA),
			TargetInstance: projection.targetInstance, CanonicalRepositoryID: projection.canonicalRepositoryID,
			CanonicalRepository: projection.canonicalRepository, ProviderBindingID: projection.providerBindingID,
			ProviderBindingRevision: projection.providerBindingRevision, ProviderRepository: projection.providerRepository,
			ExpectedHeadSHA: projection.expectedHeadSHA, ExpectedBaseSHA: projection.expectedBaseSHA,
			BaseRef: projection.baseRef, DelegatedMergeMethod: projection.mergeMethod,
			ProjectionFactsRevision: projection.factsRevision,
			CompletionIntent:        completionIntent, LinkConfidence: confidence, State: state,
		})
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("begin external PR fact transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{workspaceID}); err != nil {
		return out, fmt.Errorf("lock provider workspace: %w", err)
	}

	identity := fmt.Sprintf("%s:%s:%s:%d", uuidToString(workspaceID), provider, externalRepo, req.ExternalNumber)
	if err := lockPullRequestIdentity(ctx, tx, "external", identity); err != nil {
		return out, fmt.Errorf("lock external pull request identity: %w", err)
	}

	if idempotencyKey != "" {
		// Identity is always locked before the receipt key, so every external
		// callback uses the same lock order.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 88492132))`, uuidToString(workspaceID)+":"+idempotencyKey); err != nil {
			return out, fmt.Errorf("lock idempotency key: %w", err)
		}
	}

	// Discover affected Issues without row locks. The identity/idempotency
	// advisory locks stabilize cooperating provider writers; Issue advisory locks
	// must be acquired before any external link/receipt row lock so delete paths
	// cannot form row-lock -> Issue-lock / Issue-lock -> row-lock cycles.
	var discoveredLinkID, discoveredIssueID pgtype.UUID
	discovered := false
	err = tx.QueryRow(ctx, `
SELECT id, issue_id
FROM external_pull_request_link
WHERE workspace_id=$1 AND provider=$2 AND external_repo=$3 AND external_number=$4`,
		workspaceID, provider, externalRepo, req.ExternalNumber).Scan(&discoveredLinkID, &discoveredIssueID)
	if err == nil {
		discovered = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("discover external pull request identity: %w", err)
	}
	var discoveredReceiptIssueID pgtype.UUID
	discoveredReceipt := false
	if idempotencyKey != "" {
		err = tx.QueryRow(ctx, `
SELECT issue_id
FROM external_pull_request_receipt
WHERE workspace_id=$1 AND idempotency_key=$2`, workspaceID, idempotencyKey).Scan(&discoveredReceiptIssueID)
		if err == nil {
			discoveredReceipt = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, fmt.Errorf("discover idempotency receipt: %w", err)
		}
	}

	lockIDs := []pgtype.UUID{issueID}
	if discovered {
		lockIDs = append(lockIDs, discoveredIssueID)
	}
	if discoveredReceipt {
		lockIDs = append(lockIDs, discoveredReceiptIssueID)
	}
	if err := lockCompletionIssues(ctx, qtx, lockIDs); err != nil {
		return out, fmt.Errorf("lock issue completion transition: %w", err)
	}

	var existingIssueID pgtype.UUID
	var existingState string
	existing := false
	err = tx.QueryRow(ctx, `
SELECT issue_id, state
FROM external_pull_request_link
WHERE workspace_id=$1 AND provider=$2 AND external_repo=$3 AND external_number=$4
FOR UPDATE`, workspaceID, provider, externalRepo, req.ExternalNumber).Scan(&existingIssueID, &existingState)
	if err == nil {
		existing = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("lock external pull request identity: %w", err)
	}
	if existing && existingIssueID != issueID {
		return out, externalPRConflictError{message: "external pull request identity is already bound to another issue"}
	}

	issue, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, externalPRValidation("issue does not belong to workspace")
		}
		return out, fmt.Errorf("verify issue workspace: %w", err)
	}
	out.Issue = issue
	out.State = state

	if idempotencyKey != "" {
		var receiptIssueID pgtype.UUID
		var receiptHash, receiptProvider, receiptRepo string
		var receiptNumber int32
		err := tx.QueryRow(ctx, `
SELECT issue_id, payload_hash, provider, external_repo, external_number
FROM external_pull_request_receipt
WHERE workspace_id=$1 AND idempotency_key=$2
FOR UPDATE`, workspaceID, idempotencyKey).Scan(
			&receiptIssueID, &receiptHash, &receiptProvider, &receiptRepo, &receiptNumber,
		)
		if err == nil {
			if receiptIssueID != issueID || receiptHash != payloadHash || receiptProvider != provider || receiptRepo != externalRepo || receiptNumber != req.ExternalNumber {
				return out, externalPRConflictError{message: "idempotency key payload mismatch"}
			}
			if existing {
				out.State = existingState
				out.LinkID = discoveredLinkID
			}
			out.Replayed = true
			if out.State == "merged" || out.State == "closed" {
				work, err := h.enqueueExternalPRTerminalWork(ctx, tx, workspaceID, issueID, out.LinkID, provider, externalRepo, req.ExternalNumber, idempotencyKey)
				if err != nil {
					return out, err
				}
				out.WorkID = work.ID
				out.SourceRevision = work.SourceRevision
			}
			if err := tx.Commit(ctx); err != nil {
				return out, fmt.Errorf("commit replayed external PR terminal work: %w", err)
			}
			return out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, fmt.Errorf("lock idempotency receipt: %w", err)
		}
	}

	if existing && existingState == "merged" && state != "merged" {
		out.State = "merged"
		out.LinkID = discoveredLinkID
		out.Replayed = true
		work, err := h.enqueueExternalPRTerminalWork(ctx, tx, workspaceID, issueID, out.LinkID, provider, externalRepo, req.ExternalNumber, idempotencyKey)
		if err != nil {
			return out, err
		}
		out.WorkID = work.ID
		out.SourceRevision = work.SourceRevision
		if idempotencyKey != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO external_pull_request_receipt (
workspace_id, idempotency_key, payload_hash, issue_id, provider, external_repo, external_number
) VALUES ($1,$2,$3,$4,$5,$6,$7)`, workspaceID, idempotencyKey, payloadHash, issueID, provider, externalRepo, req.ExternalNumber); err != nil {
				return out, fmt.Errorf("write absorbed idempotency receipt: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return out, fmt.Errorf("commit absorbed external PR terminal work: %w", err)
		}
		return out, nil
	}

	if !existing {
		tag, insertErr := tx.Exec(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number, external_url,
    merge_provider, merge_repo, merge_number, merge_url, merged_sha,
    link_confidence, completion_intent, state
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (workspace_id, provider, external_repo, external_number) DO NOTHING`,
			workspaceID, issueID, provider, externalRepo, req.ExternalNumber,
			nilIfBlank(req.ExternalURL), nilIfBlank(mergeProvider), nilIfBlank(req.MergeRepo), nilIfZero(req.MergeNumber),
			nilIfBlank(req.MergeURL), nilIfBlank(req.MergedSHA), confidence, completionIntent, state)
		if insertErr != nil {
			return out, fmt.Errorf("insert external pull request fact: %w", insertErr)
		}
		if tag.RowsAffected() == 0 {
			// A non-cooperating writer may have won the unique-key race. Re-read
			// under the identity lock and never mutate a row bound to another Issue.
			if err := tx.QueryRow(ctx, `
SELECT id, issue_id, state FROM external_pull_request_link
WHERE workspace_id=$1 AND provider=$2 AND external_repo=$3 AND external_number=$4
FOR UPDATE`, workspaceID, provider, externalRepo, req.ExternalNumber).Scan(&discoveredLinkID, &existingIssueID, &existingState); err != nil {
				return out, fmt.Errorf("re-read external pull request identity: %w", err)
			}
			if existingIssueID != issueID {
				return out, externalPRConflictError{message: "external pull request identity is already bound to another issue"}
			}
			existing = true
		}
	}

	if existing {
		out.LinkID = discoveredLinkID
		tag, updateErr := tx.Exec(ctx, `
UPDATE external_pull_request_link SET
    external_url=COALESCE($6::text, external_url),
    merge_provider=CASE WHEN $7::text IS NULL THEN merge_provider ELSE $7::text END,
    merge_repo=CASE WHEN $8::text IS NULL THEN merge_repo ELSE $8::text END,
    merge_number=CASE WHEN $9::int4 IS NULL THEN merge_number ELSE $9::int4 END,
    merge_url=CASE WHEN $10::text IS NULL THEN merge_url ELSE $10::text END,
    merged_sha=CASE WHEN state='merged' THEN COALESCE(merged_sha, $11::text) ELSE $11::text END,
    link_confidence=$12,
    completion_intent=$13,
    state=CASE WHEN state='merged' THEN 'merged' ELSE $14 END,
    updated_at=now()
WHERE workspace_id=$1 AND issue_id=$2 AND provider=$3 AND external_repo=$4 AND external_number=$5`,
			workspaceID, issueID, provider, externalRepo, req.ExternalNumber,
			nilIfBlank(req.ExternalURL), nilIfBlank(mergeProvider), nilIfBlank(req.MergeRepo), nilIfZero(req.MergeNumber),
			nilIfBlank(req.MergeURL), nilIfBlank(req.MergedSHA), confidence, completionIntent, state)
		if updateErr != nil {
			return out, fmt.Errorf("update external pull request fact: %w", updateErr)
		}
		if tag.RowsAffected() != 1 {
			return out, externalPRConflictError{message: "external pull request identity binding changed"}
		}
		if existingState == "merged" {
			out.State = "merged"
		}
	}

	if !out.LinkID.Valid {
		if err := tx.QueryRow(ctx, `
SELECT id FROM external_pull_request_link
WHERE workspace_id=$1 AND provider=$2 AND external_repo=$3 AND external_number=$4
FOR UPDATE`, workspaceID, provider, externalRepo, req.ExternalNumber).Scan(&out.LinkID); err != nil {
			return out, fmt.Errorf("read external pull request link identity: %w", err)
		}
	}
	if idempotencyKey != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO external_pull_request_receipt (
workspace_id, idempotency_key, payload_hash, issue_id, provider, external_repo, external_number
) VALUES ($1,$2,$3,$4,$5,$6,$7)`, workspaceID, idempotencyKey, payloadHash, issueID, provider, externalRepo, req.ExternalNumber); err != nil {
			return out, fmt.Errorf("write idempotency receipt: %w", err)
		}
	}
	activityWriter := h.ExternalPRActivityWriter
	if activityWriter == nil {
		activityWriter = recordExternalPRActivity
	}
	for _, action := range []string{"external_pr_linked", "external_pr_merged"} {
		if action == "external_pr_merged" && out.State != "merged" {
			continue
		}
		if err := activityWriter(ctx, tx, action, req, ""); err != nil {
			return out, externalPRActivityError{err: fmt.Errorf("record %s in fact transaction: %w", action, err)}
		}
	}
	if out.State == "merged" || out.State == "closed" {
		work, err := h.enqueueExternalPRTerminalWork(ctx, tx, workspaceID, issueID, out.LinkID, provider, externalRepo, req.ExternalNumber, idempotencyKey)
		if err != nil {
			return out, fmt.Errorf("enqueue external PR terminal work: %w", err)
		}
		out.WorkID = work.ID
		out.SourceRevision = work.SourceRevision
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("commit external pull request fact: %w", err)
	}
	return out, nil
}

func recordExternalPRActivity(ctx context.Context, executor dbExecutor, action string, req externalPullRequestLinkRequest, outcome string) error {
	workspaceID, err := parseExternalPRUUID(req.WorkspaceID)
	if err != nil {
		return fmt.Errorf("invalid workspace_id for %s activity", action)
	}
	issueID, err := parseExternalPRUUID(req.IssueID)
	if err != nil {
		return fmt.Errorf("invalid issue_id for %s activity", action)
	}
	provider := normalizeExternalPRProvider(req.Provider)
	mergeProvider := normalizeExternalPRProvider(req.MergeProvider)
	state := strings.TrimSpace(strings.ToLower(req.State))
	if state == "" {
		state = "open"
	}
	confidence := strings.TrimSpace(strings.ToLower(req.LinkConfidence))
	if confidence == "" {
		confidence = "authoritative"
	}
	completionIntent := confidence == "authoritative"
	if req.CompletionIntent != nil {
		completionIntent = *req.CompletionIntent
	}
	details := map[string]any{
		"provider":          provider,
		"external_repo":     strings.TrimSpace(req.ExternalRepo),
		"external_number":   req.ExternalNumber,
		"external_url":      strings.TrimSpace(req.ExternalURL),
		"state":             state,
		"link_confidence":   confidence,
		"completion_intent": completionIntent,
		"merge_provider":    mergeProvider,
		"merge_repo":        strings.TrimSpace(req.MergeRepo),
		"merge_number":      req.MergeNumber,
		"merge_url":         strings.TrimSpace(req.MergeURL),
		"merged_sha":        strings.TrimSpace(req.MergedSHA),
	}
	if outcome != "" {
		details["completion_outcome"] = outcome
	}
	payload, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal %s activity details: %w", action, err)
	}
	if _, err := executor.Exec(ctx, `
INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details)
SELECT $1, $2, 'system', NULL, $3, $4::jsonb
WHERE NOT EXISTS (
    SELECT 1 FROM activity_log
    WHERE workspace_id=$1 AND issue_id=$2 AND action=$3
      AND details->>'provider'=$5
      AND details->>'external_repo'=$6
      AND details->>'external_number'=$7
)`, workspaceID, issueID, action, payload, provider, strings.TrimSpace(req.ExternalRepo), fmt.Sprint(req.ExternalNumber)); err != nil {
		return fmt.Errorf("create %s activity: %w", action, err)
	}
	return nil
}

func (h *Handler) completeLeafChildIssueFromExternalPR(r *http.Request, req externalPullRequestLinkRequest, workID pgtype.UUID, sourceRevision string) (externalCompleteFromPRResponse, error) {
	ctx := r.Context()
	workspaceID, err := parseExternalPRUUID(req.WorkspaceID)
	if err != nil {
		return externalCompleteFromPRResponse{Outcome: "recorded", Reason: "invalid_workspace_id"}, nil
	}
	issueID, err := parseExternalPRUUID(req.IssueID)
	if err != nil {
		return externalCompleteFromPRResponse{Outcome: "recorded", Reason: "invalid_issue_id"}, nil
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return externalCompleteFromPRResponse{Outcome: "recorded", Reason: "issue_not_found", IssueID: req.IssueID}, nil
		}
		return externalCompleteFromPRResponse{}, err
	}
	if strings.EqualFold(req.LinkConfidence, "inferred") || req.CompletionIntent == nil || !*req.CompletionIntent {
		return externalCompleteFromPRResponse{Outcome: "recorded", Reason: "unverified_link", IssueID: req.IssueID}, nil
	}
	result, err := h.evaluatePullRequestCompletionWithActivitiesResultAndWork(
		ctx,
		issue,
		"external_pr_merged",
		[]completionActivitySpec{externalPRCompletionActivitySpec(req)},
		workID,
		sourceRevision,
	)
	if err != nil {
		return externalCompleteFromPRResponse{}, err
	}
	return externalCompleteFromPRResponse{Outcome: result.Outcome, Reason: result.Reason, IssueID: req.IssueID}, nil
}

func externalPRCompletionActivitySpec(req externalPullRequestLinkRequest) completionActivitySpec {
	completionIntent := strings.EqualFold(strings.TrimSpace(req.LinkConfidence), "authoritative")
	if req.CompletionIntent != nil {
		completionIntent = *req.CompletionIntent
	}
	details, _ := json.Marshal(map[string]any{
		"provider":           normalizeExternalPRProvider(req.Provider),
		"external_repo":      strings.TrimSpace(req.ExternalRepo),
		"external_number":    req.ExternalNumber,
		"state":              strings.TrimSpace(strings.ToLower(req.State)),
		"completion_intent":  completionIntent,
		"completion_outcome": "completed",
	})
	return completionActivitySpec{action: "issue_completed_by_external_pr", details: details}
}

func externalPRLinkTokenAudience() string {
	if audience := strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE")); audience != "" {
		return audience
	}
	return defaultExternalPRLinkTokenAudience
}

func normalizeExternalPRProvider(provider string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(provider)), "/")
}

func externalPRProviderAllowed(provider string) bool {
	allowed := strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS"))
	if allowed == "" {
		return true
	}
	provider = normalizeExternalPRProvider(provider)
	for _, part := range strings.Split(allowed, ",") {
		if normalizeExternalPRProvider(part) == provider {
			return true
		}
	}
	return false
}

func validateExternalPRURL(field, value string) error {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute http(s) URL", field)
	}
	// Typed external-PR facts are immutable wire data. Do not normalize a URL
	// and continue: credentials, query strings, and fragments must never enter
	// the persisted fact or its canonical payload hash.
	if raw != value || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%s must not contain userinfo, query, fragment, or surrounding whitespace", field)
	}
	return nil
}

func parseExternalPRUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	err := u.Scan(strings.TrimSpace(s))
	return u, err
}

func nilIfBlank(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func nilIfZero(n int32) any {
	if n == 0 {
		return nil
	}
	return n
}
