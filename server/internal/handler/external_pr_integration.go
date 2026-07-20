package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const defaultExternalPRLinkTokenAudience = "external-pr-link"

type externalPullRequestLinkRequest struct {
	Provider         string `json:"provider"`
	IssueID          string `json:"issue_id"`
	WorkspaceID      string `json:"workspace_id"`
	Workspace        string `json:"workspace"`
	IssueKey         string `json:"issue_key"`
	ExternalRepo     string `json:"external_repo"`
	ExternalNumber   int32  `json:"external_number"`
	ExternalURL      string `json:"external_url"`
	MergeProvider    string `json:"merge_provider"`
	MergeRepo        string `json:"merge_repo"`
	MergeNumber      int32  `json:"merge_number"`
	MergeURL         string `json:"merge_url"`
	MergedSHA        string `json:"merged_sha"`
	CompletionIntent *bool  `json:"completion_intent,omitempty"`
	LinkConfidence   string `json:"link_confidence"`
	State            string `json:"state"`
	IdempotencyKey   string `json:"idempotency_key"`
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

func (h *Handler) RegisterExternalPullRequestLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireExternalPRServiceToken(w, r) {
		return
	}
	var req externalPullRequestLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	if err := h.upsertExternalPullRequestLink(r.Context(), req); err != nil {
		slog.Warn("external PR integration: register PR link failed", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.recordExternalPRActivity(r.Context(), "external_pr_linked", req, "")
	if strings.EqualFold(strings.TrimSpace(req.State), "merged") {
		h.recordExternalPRActivity(r.Context(), "external_pr_merged", req, "")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) CompleteIssueFromExternalPR(w http.ResponseWriter, r *http.Request) {
	if !h.requireExternalPRServiceToken(w, r) {
		return
	}
	var req externalPullRequestLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	req.State = "merged"
	completionIntent := true
	req.CompletionIntent = &completionIntent
	if strings.TrimSpace(req.LinkConfidence) == "" {
		req.LinkConfidence = "authoritative"
	}
	if err := h.upsertExternalPullRequestLink(r.Context(), req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.recordExternalPRActivity(r.Context(), "external_pr_linked", req, "")
	h.recordExternalPRActivity(r.Context(), "external_pr_merged", req, "")
	out := h.completeLeafChildIssueFromExternalPR(r, req)
	if out.Outcome == "completed" {
		h.recordExternalPRActivity(r.Context(), "issue_completed_by_external_pr", req, out.Outcome)
	}
	writeJSON(w, http.StatusOK, out)
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
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
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

func (h *Handler) upsertExternalPullRequestLink(ctx context.Context, req externalPullRequestLinkRequest) error {
	workspaceID, err := parseExternalPRUUID(req.WorkspaceID)
	if err != nil {
		return fmt.Errorf("invalid workspace_id")
	}
	issueID, err := parseExternalPRUUID(req.IssueID)
	if err != nil {
		return fmt.Errorf("invalid issue_id")
	}
	if _, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("issue does not belong to workspace")
		}
		return fmt.Errorf("verify issue workspace: %w", err)
	}
	provider := normalizeExternalPRProvider(req.Provider)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if !externalPRProviderAllowed(provider) {
		return fmt.Errorf("provider %q is not allowed", provider)
	}
	if strings.TrimSpace(req.ExternalRepo) == "" || req.ExternalNumber <= 0 {
		return fmt.Errorf("external_repo and external_number are required")
	}
	if err := validateExternalPRURL("external_url", req.ExternalURL); err != nil {
		return err
	}
	if err := validateExternalPRURL("merge_url", req.MergeURL); err != nil {
		return err
	}
	confidence := strings.TrimSpace(strings.ToLower(req.LinkConfidence))
	if confidence == "" {
		confidence = "authoritative"
	}
	if confidence != "authoritative" && confidence != "inferred" {
		return fmt.Errorf("invalid link_confidence")
	}
	state := strings.TrimSpace(strings.ToLower(req.State))
	if state == "" {
		state = "open"
	}
	switch state {
	case "open", "draft", "closed", "merged":
	default:
		return fmt.Errorf("invalid state")
	}
	completionIntent := confidence == "authoritative"
	if req.CompletionIntent != nil {
		completionIntent = *req.CompletionIntent
	}
	mergeProvider := normalizeExternalPRProvider(req.MergeProvider)
	_, err = h.DB.Exec(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number, external_url,
    merge_provider, merge_repo, merge_number, merge_url, merged_sha,
    link_confidence, completion_intent, state, idempotency_key
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (workspace_id, provider, external_repo, external_number) DO UPDATE SET
    issue_id = EXCLUDED.issue_id,
    external_url = EXCLUDED.external_url,
    merge_provider = COALESCE(EXCLUDED.merge_provider, external_pull_request_link.merge_provider),
    merge_repo = COALESCE(EXCLUDED.merge_repo, external_pull_request_link.merge_repo),
    merge_number = COALESCE(EXCLUDED.merge_number, external_pull_request_link.merge_number),
    merge_url = COALESCE(EXCLUDED.merge_url, external_pull_request_link.merge_url),
    merged_sha = COALESCE(EXCLUDED.merged_sha, external_pull_request_link.merged_sha),
    link_confidence = EXCLUDED.link_confidence,
    completion_intent = EXCLUDED.completion_intent,
    state = EXCLUDED.state,
    idempotency_key = COALESCE(EXCLUDED.idempotency_key, external_pull_request_link.idempotency_key),
    updated_at = now()`, workspaceID, issueID, provider, strings.TrimSpace(req.ExternalRepo), req.ExternalNumber, nilIfBlank(req.ExternalURL), nilIfBlank(mergeProvider), nilIfBlank(req.MergeRepo), nilIfZero(req.MergeNumber), nilIfBlank(req.MergeURL), nilIfBlank(req.MergedSHA), confidence, completionIntent, state, nilIfBlank(req.IdempotencyKey))
	return err
}

func (h *Handler) recordExternalPRActivity(ctx context.Context, action string, req externalPullRequestLinkRequest, outcome string) {
	workspaceID, err := parseExternalPRUUID(req.WorkspaceID)
	if err != nil {
		slog.Warn("external PR integration: skip activity with invalid workspace_id", "action", action)
		return
	}
	issueID, err := parseExternalPRUUID(req.IssueID)
	if err != nil {
		slog.Warn("external PR integration: skip activity with invalid issue_id", "action", action)
		return
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
		slog.Warn("external PR integration: marshal activity details failed", "action", action, "error", err)
		return
	}
	if _, err := h.DB.Exec(ctx, `
INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details)
SELECT $1, $2, 'system', NULL, $3, $4::jsonb
WHERE NOT EXISTS (
    SELECT 1 FROM activity_log
    WHERE workspace_id=$1 AND issue_id=$2 AND action=$3
      AND details->>'provider'=$5
      AND details->>'external_repo'=$6
      AND details->>'external_number'=$7
)`, workspaceID, issueID, action, payload, provider, strings.TrimSpace(req.ExternalRepo), fmt.Sprint(req.ExternalNumber)); err != nil {
		slog.Warn("external PR integration: create activity failed", "action", action, "error", err)
	}
}

const externalPRCompletionPolicyTrimCutset = " \t\n\r\v\f"

func issueExternalPRCompletionPolicy(issue db.Issue) string {
	raw, exists := parseIssueMetadata(issue.Metadata)["external_pr_completion_policy"]
	if !exists {
		return ""
	}
	policy, ok := raw.(string)
	if !ok {
		return "unsupported"
	}
	return strings.ToLower(strings.Trim(policy, externalPRCompletionPolicyTrimCutset))
}

// completeExternalPRLeafStatus repeats the completion-policy gate inside the
// atomic status write. The JSON type check distinguishes an absent policy
// (legacy auto-complete) from an existing JSON null/non-string value (blocked).
// Its ASCII whitespace cutset exactly matches issueExternalPRCompletionPolicy.
func (h *Handler) completeExternalPRLeafStatus(ctx context.Context, issueID, workspaceID pgtype.UUID) (pgtype.UUID, error) {
	var updatedID pgtype.UUID
	err := h.DB.QueryRow(ctx, `
UPDATE issue SET status='done', updated_at=now()
WHERE id=$1 AND workspace_id=$2
  AND status NOT IN ('done','cancelled')
  AND parent_issue_id IS NOT NULL
  AND (
    NOT (issue.metadata ? 'external_pr_completion_policy')
    OR (
      jsonb_typeof(issue.metadata->'external_pr_completion_policy') = 'string'
      AND lower(btrim(issue.metadata->>'external_pr_completion_policy', E' \t\n\r\f' || chr(11))) IN ('', 'leaf_child_only')
    )
  )
  AND NOT EXISTS (SELECT 1 FROM issue child WHERE child.parent_issue_id = issue.id)
  AND NOT EXISTS (SELECT 1 FROM external_pull_request_link pr WHERE pr.workspace_id=issue.workspace_id AND pr.issue_id=issue.id AND pr.link_confidence='authoritative' AND pr.completion_intent AND pr.state IN ('open','draft'))
RETURNING id`, issueID, workspaceID).Scan(&updatedID)
	return updatedID, err
}

func (h *Handler) completeLeafChildIssueFromExternalPR(r *http.Request, req externalPullRequestLinkRequest) externalCompleteFromPRResponse {
	ctx := r.Context()
	workspaceID, err := parseExternalPRUUID(req.WorkspaceID)
	if err != nil {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "invalid_workspace_id"}
	}
	issueID, err := parseExternalPRUUID(req.IssueID)
	if err != nil {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "invalid_issue_id"}
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "issue_not_found", IssueID: req.IssueID}
	}
	if strings.EqualFold(req.LinkConfidence, "inferred") || req.CompletionIntent == nil || !*req.CompletionIntent {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "unverified_link", IssueID: req.IssueID}
	}
	if issue.Status == "done" {
		return externalCompleteFromPRResponse{Outcome: "already_done", IssueID: req.IssueID}
	}
	if issue.Status == "cancelled" {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "cancelled", IssueID: req.IssueID}
	}
	// Some workflows have terminal gates beyond provider merge (for example,
	// an independently verified outward backup). record_only keeps the external
	// PR link and merge activity authoritative while leaving the Issue status —
	// and therefore native parent/Stage wake — under the workflow's explicit
	// close after those gates pass.
	switch issueExternalPRCompletionPolicy(issue) {
	case "", "leaf_child_only":
		// Existing leaf completion behavior.
	case "record_only":
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "completion_policy_record_only", IssueID: req.IssueID}
	default:
		// Unknown policies are future or misspelled terminal contracts. Fail
		// closed rather than silently treating them as auto-complete.
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "completion_policy_unsupported", IssueID: req.IssueID}
	}
	if !issue.ParentIssueID.Valid {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "no_parent", IssueID: req.IssueID}
	}
	children, err := h.Queries.ListChildIssues(ctx, issue.ID)
	if err != nil {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "children_lookup_failed", IssueID: req.IssueID}
	}
	if len(children) > 0 {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "has_children", IssueID: req.IssueID}
	}
	var openPRs int64
	if err := h.DB.QueryRow(ctx, `SELECT COUNT(*)::bigint FROM external_pull_request_link WHERE workspace_id=$1 AND issue_id=$2 AND link_confidence='authoritative' AND completion_intent AND state IN ('open','draft')`, workspaceID, issueID).Scan(&openPRs); err != nil {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "open_pr_lookup_failed", IssueID: req.IssueID}
	}
	if openPRs > 0 {
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "open_pr_exists", IssueID: req.IssueID}
	}
	if _, err := h.completeExternalPRLeafStatus(ctx, issueID, workspaceID); err != nil {
		if err == pgx.ErrNoRows {
			return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "guard_not_satisfied", IssueID: req.IssueID}
		}
		return externalCompleteFromPRResponse{Outcome: "skipped", Reason: "update_failed", IssueID: req.IssueID}
	}
	updated, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err == nil {
		h.notifyParentOfChildDone(ctx, issue, updated)
		prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
		h.publish(protocol.EventIssueUpdated, req.WorkspaceID, "system", "", map[string]any{
			"issue":          issueToResponse(updated, prefix),
			"status_changed": true,
			"prev_status":    issue.Status,
			"creator_type":   issue.CreatorType,
			"creator_id":     uuidToString(issue.CreatorID),
			"source":         "external_pr_merged",
		})
	}
	return externalCompleteFromPRResponse{Outcome: "completed", IssueID: req.IssueID}
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
