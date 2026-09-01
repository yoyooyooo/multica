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
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const externalPRRequestLimit = 1 << 20

var (
	externalPRDigestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	externalPRSHA1Pattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	externalPRInstancePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	externalPRRepoPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	externalPRBranchPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
)

type externalPRRequest struct {
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
	TargetInstance          string `json:"target_instance"`
	CanonicalRepositoryID   string `json:"canonical_repository_id"`
	CanonicalRepository     string `json:"canonical_repository"`
	ProviderBindingID       string `json:"provider_binding_id"`
	ProviderBindingRevision string `json:"provider_binding_revision"`
	ProviderRepository      string `json:"provider_repository"`
	ExpectedHeadSHA         string `json:"expected_head_sha"`
	ExpectedBaseSHA         string `json:"expected_base_sha"`
	BaseRef                 string `json:"base_ref"`
	DelegatedMergeMethod    string `json:"delegated_merge_method"`
	ProjectionFactsRevision string `json:"projection_facts_revision"`
	CompletionIntent        *bool  `json:"completion_intent"`
	LinkConfidence          string `json:"link_confidence"`
	State                   string `json:"state"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type externalPRAdmission struct {
	WorkspaceID pgtype.UUID
	IssueID     pgtype.UUID
	Issue       db.Issue
	PayloadHash string
}

type externalPRLink struct {
	ID               pgtype.UUID
	WorkspaceID      pgtype.UUID
	IssueID          pgtype.UUID
	Provider         string
	ExternalRepo     string
	ExternalNumber   int32
	ExternalURL      string
	MergeProvider    string
	MergeRepo        string
	MergeNumber      int32
	MergeURL         string
	MergedSHA        string
	CompletionIntent bool
	State            string
	FactRevision     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type externalPRHTTPError struct {
	status  int
	message string
}

func (e *externalPRHTTPError) Error() string { return e.message }

func externalPRBadRequest(message string) error {
	return &externalPRHTTPError{status: http.StatusBadRequest, message: message}
}

func externalPRConflict(message string) error {
	return &externalPRHTTPError{status: http.StatusConflict, message: message}
}

func writeExternalPRError(w http.ResponseWriter, err error) {
	var public *externalPRHTTPError
	if errors.As(err, &public) {
		writeError(w, public.status, public.message)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "external PR integration temporarily unavailable")
}

func decodeExternalPRRequest(r *http.Request) (externalPRRequest, error) {
	var request externalPRRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, externalPRRequestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, externalPRBadRequest("invalid external PR request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return request, externalPRBadRequest("external PR request must contain one JSON object")
	}
	return request, nil
}

func (h *Handler) RegisterExternalPullRequestLink(w http.ResponseWriter, r *http.Request) {
	h.handleExternalPRCallback(w, r, false)
}

func (h *Handler) CompleteIssueFromExternalPR(w http.ResponseWriter, r *http.Request) {
	h.handleExternalPRCallback(w, r, true)
}

func (h *Handler) handleExternalPRCallback(w http.ResponseWriter, r *http.Request, mergePath bool) {
	if !requireExternalPRServiceToken(w, r) {
		return
	}
	request, err := decodeExternalPRRequest(r)
	if err != nil {
		writeExternalPRError(w, err)
		return
	}
	admission, err := h.validateExternalPRRequest(r.Context(), request, mergePath)
	if err != nil {
		writeExternalPRError(w, err)
		return
	}
	link, replayed, err := h.commitExternalPRFact(r.Context(), request, admission)
	if err != nil {
		writeExternalPRError(w, err)
		return
	}
	if !replayed {
		h.publish(protocol.EventPullRequestUpdated, request.WorkspaceID, "system", "", map[string]any{
			"issue_id":        request.IssueID,
			"provider":        request.Provider,
			"external_repo":   request.ExternalRepo,
			"external_number": request.ExternalNumber,
			"state":           link.State,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"outcome":  replayLabel(replayed),
		"issue_id": request.IssueID,
		"state":    link.State,
	})
}

func replayLabel(replayed bool) string {
	if replayed {
		return "replayed"
	}
	return "accepted"
}

func requireExternalPRServiceToken(w http.ResponseWriter, r *http.Request) bool {
	want := strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN"))
	if want == "" {
		writeError(w, http.StatusServiceUnavailable, "external PR service token is not configured")
		return false
	}
	const prefix = "Bearer "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		writeError(w, http.StatusUnauthorized, "invalid external PR service token")
		return false
	}
	got := strings.TrimPrefix(authorization, prefix)
	if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid external PR service token")
		return false
	}
	return true
}

func (h *Handler) validateExternalPRRequest(ctx context.Context, request externalPRRequest, mergePath bool) (externalPRAdmission, error) {
	var admission externalPRAdmission
	if request.Provider != "ags" || !externalPRProviderEnabled("ags") {
		return admission, externalPRBadRequest("provider must be enabled as ags")
	}
	if request.LinkConfidence != "authoritative" {
		return admission, externalPRBadRequest("link_confidence must be authoritative")
	}
	if request.CompletionIntent == nil {
		return admission, externalPRBadRequest("completion_intent is required")
	}
	if !canonicalExternalPRText(request.Workspace) || !canonicalExternalPRText(request.IssueKey) ||
		!canonicalExternalPRText(request.IdempotencyKey) || !canonicalExternalPRText(request.TargetInstance) {
		return admission, externalPRBadRequest("external PR identity fields must be non-empty and canonical")
	}
	if !externalPRInstancePattern.MatchString(request.TargetInstance) ||
		request.TargetInstance != strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID")) {
		return admission, externalPRBadRequest("target_instance does not match the configured service instance")
	}
	workspaceID, err := parseStrictUUID(request.WorkspaceID)
	if err != nil {
		return admission, externalPRBadRequest("workspace_id must be a UUID")
	}
	issueID, err := parseStrictUUID(request.IssueID)
	if err != nil {
		return admission, externalPRBadRequest("issue_id must be a UUID")
	}
	workspace, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil || workspace.Slug != request.Workspace {
		return admission, externalPRBadRequest("workspace does not match workspace_id")
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		return admission, externalPRBadRequest("issue does not belong to workspace")
	}
	issueKey := issuePrefixForWorkspace(workspace) + fmt.Sprintf("-%d", issue.Number)
	if request.IssueKey != issueKey {
		return admission, externalPRBadRequest("issue_key does not match issue_id")
	}
	if !canonicalExternalPRRepository(request.ExternalRepo) || request.ExternalRepo != request.CanonicalRepository || request.ExternalNumber < 1 {
		return admission, externalPRBadRequest("external pull request identity is not canonical")
	}
	if request.MergeProvider != "forgejo" || !canonicalExternalPRRepository(request.MergeRepo) ||
		request.MergeRepo != request.ProviderRepository || request.MergeNumber < 1 {
		return admission, externalPRBadRequest("Forgejo merge identity is not canonical")
	}
	if !canonicalExternalPRURL(request.ExternalURL) || !canonicalExternalPRURL(request.MergeURL) {
		return admission, externalPRBadRequest("external PR URLs must be absolute http or https URLs")
	}
	for _, digest := range []string{request.CanonicalRepositoryID, request.ProviderBindingID, request.ProviderBindingRevision, request.ProjectionFactsRevision} {
		if !externalPRDigestPattern.MatchString(digest) {
			return admission, externalPRBadRequest("external PR projection digest is not canonical")
		}
	}
	if !externalPRSHA1Pattern.MatchString(request.ExpectedHeadSHA) || !externalPRSHA1Pattern.MatchString(request.ExpectedBaseSHA) {
		return admission, externalPRBadRequest("expected head and base must be lowercase git SHA-1 values")
	}
	if !canonicalExternalPRBranch(request.BaseRef) {
		return admission, externalPRBadRequest("base_ref is not canonical")
	}
	switch request.DelegatedMergeMethod {
	case "merge", "rebase", "rebase-merge", "squash", "fast-forward-only":
	default:
		return admission, externalPRBadRequest("delegated_merge_method is not supported")
	}
	if mergePath {
		if request.State != "merged" || !*request.CompletionIntent || !externalPRSHA1Pattern.MatchString(request.MergedSHA) {
			return admission, externalPRBadRequest("merged callback requires merged state, intent, and merged_sha")
		}
	} else {
		switch request.State {
		case "open", "draft":
			if request.MergedSHA != "" {
				return admission, externalPRBadRequest("non-merged callback must omit merged_sha")
			}
		case "closed":
			if *request.CompletionIntent || request.MergedSHA != "" {
				return admission, externalPRBadRequest("closed callback must be record-only and omit merged_sha")
			}
		default:
			return admission, externalPRBadRequest("links callback accepts open, draft, or closed state")
		}
	}
	hashRequest := request
	hashRequest.IdempotencyKey = ""
	payload, err := json.Marshal(hashRequest)
	if err != nil {
		return admission, err
	}
	sum := sha256.Sum256(payload)
	return externalPRAdmission{
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		Issue:       issue,
		PayloadHash: hex.EncodeToString(sum[:]),
	}, nil
}

func externalPRProviderEnabled(provider string) bool {
	allowed := strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS"))
	if allowed == "" {
		return false
	}
	for _, candidate := range strings.Split(allowed, ",") {
		if strings.TrimSpace(candidate) == provider {
			return true
		}
	}
	return false
}

func canonicalExternalPRText(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func canonicalExternalPRRepository(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !externalPRRepoPartPattern.MatchString(part) || part == "." || part == ".." || strings.HasSuffix(strings.ToLower(part), ".git") {
			return false
		}
	}
	return true
}

func canonicalExternalPRURL(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.User == nil
}

func canonicalExternalPRBranch(value string) bool {
	if !externalPRBranchPattern.MatchString(value) || strings.Contains(value, "..") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (h *Handler) commitExternalPRFact(ctx context.Context, request externalPRRequest, admission externalPRAdmission) (externalPRLink, bool, error) {
	var out externalPRLink
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	identity := fmt.Sprintf("external:%s:%s:%s:%d", request.WorkspaceID, request.Provider, request.ExternalRepo, request.ExternalNumber)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 88492133))`, identity); err != nil {
		return out, false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 88492132))`, request.WorkspaceID+":"+request.IdempotencyKey); err != nil {
		return out, false, err
	}

	var receiptHash string
	var receiptLinkID, receiptIssueID pgtype.UUID
	err = tx.QueryRow(ctx, `
SELECT payload_hash, link_id, issue_id
FROM external_pull_request_receipt
WHERE workspace_id=$1 AND idempotency_key=$2
FOR UPDATE`, admission.WorkspaceID, request.IdempotencyKey).Scan(&receiptHash, &receiptLinkID, &receiptIssueID)
	if err == nil {
		if receiptHash != admission.PayloadHash || receiptIssueID != admission.IssueID || !receiptLinkID.Valid {
			return out, false, externalPRConflict("idempotency key payload mismatch")
		}
		out, err = readExternalPRLink(ctx, tx, receiptLinkID)
		if err != nil {
			return out, false, err
		}
		if err := enqueueExternalPRWork(ctx, tx, out, request.IdempotencyKey); err != nil {
			return out, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return out, false, err
		}
		return out, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}

	var existingID, existingIssueID pgtype.UUID
	var existingState string
	err = tx.QueryRow(ctx, `
SELECT id, issue_id, state
FROM external_pull_request_link
WHERE workspace_id=$1 AND provider=$2 AND external_repo=$3 AND external_number=$4
FOR UPDATE`, admission.WorkspaceID, request.Provider, request.ExternalRepo, request.ExternalNumber).Scan(&existingID, &existingIssueID, &existingState)
	existing := err == nil
	semanticReplay := false
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}
	if existing && existingIssueID != admission.IssueID {
		return out, false, externalPRConflict("external pull request identity is already bound to another issue")
	}
	if existing {
		current, readErr := readExternalPRLink(ctx, tx, existingID)
		if readErr != nil {
			return out, false, readErr
		}
		semanticReplay = current.FactRevision == admission.PayloadHash
		if existingState == "merged" && !semanticReplay {
			return out, false, externalPRConflict("merged external pull request fact is immutable")
		}
	}

	if !existing {
		err = tx.QueryRow(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number, external_url,
    merge_provider, merge_repo, merge_number, merge_url, merged_sha,
    link_confidence, completion_intent, state, fact_revision
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),'authoritative',$12,$13,$14)
RETURNING id`, admission.WorkspaceID, admission.IssueID, request.Provider, request.ExternalRepo,
			request.ExternalNumber, request.ExternalURL, request.MergeProvider, request.MergeRepo,
			request.MergeNumber, request.MergeURL, request.MergedSHA, *request.CompletionIntent,
			request.State, admission.PayloadHash).Scan(&existingID)
		if err != nil {
			return out, false, err
		}
	} else if existingState != "merged" && !semanticReplay {
		_, err = tx.Exec(ctx, `
UPDATE external_pull_request_link
SET external_url=$2, merge_provider=$3, merge_repo=$4, merge_number=$5,
    merge_url=$6, merged_sha=NULLIF($7,''), completion_intent=$8,
    state=$9, fact_revision=$10, updated_at=now()
WHERE id=$1`, existingID, request.ExternalURL, request.MergeProvider, request.MergeRepo,
			request.MergeNumber, request.MergeURL, request.MergedSHA, *request.CompletionIntent,
			request.State, admission.PayloadHash)
		if err != nil {
			return out, false, err
		}
	}

	out, err = readExternalPRLink(ctx, tx, existingID)
	if err != nil {
		return out, false, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO external_pull_request_receipt (
    workspace_id, idempotency_key, payload_hash, link_id, issue_id,
    provider, external_repo, external_number
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, admission.WorkspaceID, request.IdempotencyKey,
		admission.PayloadHash, existingID, admission.IssueID, request.Provider,
		request.ExternalRepo, request.ExternalNumber); err != nil {
		return out, false, err
	}
	if !semanticReplay {
		activityDetails, _ := json.Marshal(map[string]any{
			"provider": request.Provider, "external_repo": request.ExternalRepo,
			"external_number": request.ExternalNumber, "state": out.State,
		})
		if _, err := qtx.CreateActivity(ctx, db.CreateActivityParams{
			ID: dbid.NewV7(), WorkspaceID: admission.WorkspaceID, IssueID: admission.IssueID,
			ActorType: strToText("system"), Action: "external_pr_recorded", Details: activityDetails,
		}); err != nil {
			return out, false, err
		}
	}
	if err := enqueueExternalPRWork(ctx, tx, out, request.IdempotencyKey); err != nil {
		return out, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, false, err
	}
	return out, semanticReplay, nil
}

func enqueueExternalPRWork(ctx context.Context, tx pgx.Tx, link externalPRLink, idempotencyKey string) error {
	if link.State != "closed" && link.State != "merged" {
		return nil
	}
	_, err := tx.Exec(ctx, `
INSERT INTO external_pr_reconcile_work (
    workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, source_idempotency_key
) VALUES ($1,$2,$3,'external_pr_terminal',$4,$5,$6,$7,$8)
ON CONFLICT (link_id, source_revision) DO UPDATE
SET next_attempt_at=CASE
        WHEN external_pr_reconcile_work.state IN ('pending','retry_wait')
        THEN LEAST(external_pr_reconcile_work.next_attempt_at, now())
        ELSE external_pr_reconcile_work.next_attempt_at
    END,
    updated_at=now()`, link.WorkspaceID, link.IssueID, link.ID, link.Provider,
		link.ExternalRepo, link.ExternalNumber, link.FactRevision, idempotencyKey)
	return err
}

func readExternalPRLink(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id pgtype.UUID) (externalPRLink, error) {
	var link externalPRLink
	err := query.QueryRow(ctx, `
SELECT id, workspace_id, issue_id, provider, external_repo, external_number,
       COALESCE(external_url,''), COALESCE(merge_provider,''), COALESCE(merge_repo,''),
       COALESCE(merge_number,0), COALESCE(merge_url,''), COALESCE(merged_sha,''),
       completion_intent, state, fact_revision, created_at, updated_at
FROM external_pull_request_link WHERE id=$1`, id).Scan(
		&link.ID, &link.WorkspaceID, &link.IssueID, &link.Provider, &link.ExternalRepo,
		&link.ExternalNumber, &link.ExternalURL, &link.MergeProvider, &link.MergeRepo,
		&link.MergeNumber, &link.MergeURL, &link.MergedSHA, &link.CompletionIntent,
		&link.State, &link.FactRevision, &link.CreatedAt, &link.UpdatedAt,
	)
	return link, err
}

func (h *Handler) listExternalPullRequestsForIssue(ctx context.Context, issue db.Issue) ([]GitHubPullRequestResponse, error) {
	rows, err := h.DB.Query(ctx, `
SELECT id, provider, external_repo, external_number, COALESCE(NULLIF(external_url,''), merge_url, ''), state,
       created_at, updated_at
FROM external_pull_request_link
WHERE workspace_id=$1 AND issue_id=$2
ORDER BY created_at DESC, id DESC`, issue.WorkspaceID, issue.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]GitHubPullRequestResponse, 0)
	for rows.Next() {
		var id pgtype.UUID
		var provider, repository, htmlURL, state string
		var number int32
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &provider, &repository, &number, &htmlURL, &state, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		parts := strings.SplitN(repository, "/", 2)
		repoOwner, repoName := "", repository
		if len(parts) == 2 {
			repoOwner, repoName = parts[0], parts[1]
		}
		row := GitHubPullRequestResponse{
			ID: uuidToString(id), Provider: provider, WorkspaceID: uuidToString(issue.WorkspaceID),
			RepoOwner: repoOwner, RepoName: repoName, Number: number,
			Title: fmt.Sprintf("%s#%d", repository, number), State: state, HtmlURL: htmlURL,
			PRCreatedAt:      createdAt.UTC().Format(time.RFC3339Nano),
			PRUpdatedAt:      updatedAt.UTC().Format(time.RFC3339Nano),
			FailedCheckNames: []string{},
		}
		if state == "merged" {
			value := row.PRUpdatedAt
			row.MergedAt = &value
		} else if state == "closed" {
			value := row.PRUpdatedAt
			row.ClosedAt = &value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
