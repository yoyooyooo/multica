package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	prMergeDelegationSchema = "workload.pr_merge_delegation.v1"
	maximumDelegationTTL    = 15 * time.Minute
)

var errActivePRMergeDelegationRequired = errors.New("active PR merge delegation required")

type createPRMergeDelegationRequest struct {
	TaskID                   string `json:"task_id"`
	RunID                    string `json:"run_id"`
	Repository               string `json:"repository"`
	PullRequestNumber        int64  `json:"pull_request_number"`
	ForgejoPullRequestNumber int64  `json:"forgejo_pull_request_number"`
	ExpectedHeadSHA          string `json:"expected_head_sha"`
	MergeMethod              string `json:"merge_method"`
	TTLSeconds               int64  `json:"ttl_seconds"`
}

type revokePRMergeDelegationRequest struct {
	Reason string `json:"reason"`
}

type prMergeDelegationResponse struct {
	Schema                   string  `json:"schema"`
	ID                       string  `json:"id"`
	WorkspaceID              string  `json:"workspace_id"`
	TaskID                   string  `json:"task_id"`
	RunID                    string  `json:"run_id"`
	Operation                string  `json:"operation"`
	Repository               string  `json:"repository"`
	PullRequestNumber        int64   `json:"pull_request_number"`
	ForgejoPullRequestNumber int64   `json:"forgejo_pull_request_number"`
	ExpectedHeadSHA          string  `json:"expected_head_sha"`
	MergeMethod              string  `json:"merge_method"`
	AuthorityRevision        string  `json:"authority_revision"`
	GrantedByUserID          string  `json:"granted_by_user_id"`
	GrantedAt                string  `json:"granted_at"`
	ExpiresAt                string  `json:"expires_at"`
	State                    string  `json:"state"`
	RevokedAt                *string `json:"revoked_at,omitempty"`
	RevokedByUserID          *string `json:"revoked_by_user_id,omitempty"`
	RevocationReason         *string `json:"revocation_reason,omitempty"`
}

// CreatePRMergeDelegation creates one human-authorized, exact workload grant.
// Task credentials cannot call this endpoint and cannot choose or widen any
// field persisted here.
func (h *Handler) CreatePRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	grantingUserID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-User-ID"), "user id")
	if !ok {
		return
	}

	var req createPRMergeDelegationRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, req.TaskID, "task id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, req.RunID, "run id")
	if !ok {
		return
	}
	if taskID != runID {
		writeError(w, http.StatusBadRequest, "run id must exactly match the current task run")
		return
	}
	repository, constraints, err := normalizePRMergeDelegationInput(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := h.currentWorkloadAssertionTime()
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create PR merge delegation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	task, err := qtx.LockTaskForPRMergeDelegation(r.Context(), db.LockTaskForPRMergeDelegationParams{
		ID: taskID, WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create PR merge delegation")
		return
	}
	if task.Status != "running" || !task.RuntimeID.Valid {
		writeError(w, http.StatusConflict, "task run is not active on an exact runtime")
		return
	}

	replacementReason := pgtype.Text{String: "superseded by a newer owner-authorized exact delegation", Valid: true}
	if err := qtx.RevokeCurrentPRMergeDelegation(r.Context(), db.RevokeCurrentPRMergeDelegationParams{
		RevokedAt: pgtype.Timestamptz{Time: now, Valid: true}, RevokedByUserID: grantingUserID,
		RevocationReason: replacementReason, WorkspaceID: workspaceID, TaskID: taskID, RunID: runID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create PR merge delegation")
		return
	}
	delegation, err := qtx.CreatePRMergeDelegation(r.Context(), db.CreatePRMergeDelegationParams{
		WorkspaceID: workspaceID, TaskID: taskID, RunID: runID, Repository: repository,
		PullRequestNumber: constraints.pullRequestNumber, ForgejoPullRequestNumber: constraints.forgejoPullRequestNumber,
		ExpectedHeadSha: constraints.expectedHeadSHA, MergeMethod: constraints.mergeMethod,
		GrantedByUserID: grantingUserID, GrantedAt: pgtype.Timestamptz{Time: now, Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Duration(req.TTLSeconds) * time.Second), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create PR merge delegation")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create PR merge delegation")
		return
	}
	writeJSON(w, http.StatusCreated, newPRMergeDelegationResponse(delegation, now))
}

func (h *Handler) GetPRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	delegationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "delegationId"), "delegation id")
	if !ok {
		return
	}
	delegation, err := h.Queries.GetPRMergeDelegationInWorkspace(r.Context(), db.GetPRMergeDelegationInWorkspaceParams{
		ID: delegationID, WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PR merge delegation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read PR merge delegation")
		return
	}
	writeJSON(w, http.StatusOK, newPRMergeDelegationResponse(delegation, h.currentWorkloadAssertionTime()))
}

func (h *Handler) RevokePRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	delegationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "delegationId"), "delegation id")
	if !ok {
		return
	}
	revokingUserID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-User-ID"), "user id")
	if !ok {
		return
	}
	var req revokePRMergeDelegationRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || reason != req.Reason || len(reason) > 500 || secretShapedValuePattern.MatchString(reason) {
		writeError(w, http.StatusBadRequest, "revocation reason must be a canonical non-secret string")
		return
	}
	now := h.currentWorkloadAssertionTime()
	delegation, err := h.Queries.RevokePRMergeDelegationInWorkspace(r.Context(), db.RevokePRMergeDelegationInWorkspaceParams{
		RevokedAt: pgtype.Timestamptz{Time: now, Valid: true}, RevokedByUserID: revokingUserID,
		RevocationReason: pgtype.Text{String: reason, Valid: true}, ID: delegationID, WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "PR merge delegation is absent or already revoked")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke PR merge delegation")
		return
	}
	writeJSON(w, http.StatusOK, newPRMergeDelegationResponse(delegation, now))
}

func requireHumanDelegationActor(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "PR merge delegations require a human workspace operator")
		return false
	}
	return true
}

func decodeClosedJSONRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
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

type normalizedPRMergeDelegationConstraints struct {
	pullRequestNumber        int64
	forgejoPullRequestNumber int64
	expectedHeadSHA          string
	mergeMethod              string
}

func normalizePRMergeDelegationInput(req createPRMergeDelegationRequest) (string, normalizedPRMergeDelegationConstraints, error) {
	if req.TTLSeconds < 1 || req.TTLSeconds > int64(maximumDelegationTTL/time.Second) {
		return "", normalizedPRMergeDelegationConstraints{}, fmt.Errorf("ttl_seconds must be positive and at most 900")
	}
	if req.PullRequestNumber < 1 || req.ForgejoPullRequestNumber < 1 ||
		req.PullRequestNumber > int64(maxJSONSafeInteger) || req.ForgejoPullRequestNumber > int64(maxJSONSafeInteger) {
		return "", normalizedPRMergeDelegationConstraints{}, fmt.Errorf("PR numbers must be positive safe integers")
	}
	target, err := normalizeWorkloadAssertionTarget(workloadAssertionTarget{
		Provider: "ags", Instance: "server-owned", Repository: req.Repository,
	}, workloadAssertionPurposeSessionExchange)
	if err != nil || target.Repository != req.Repository || !canonicalSafeIDPattern.MatchString(req.Repository) || secretShapedValuePattern.MatchString(req.Repository) {
		return "", normalizedPRMergeDelegationConstraints{}, fmt.Errorf("repository must be a canonical non-secret owner/name")
	}
	normalized, err := normalizePRMergeConstraints(map[string]any{
		"pull_request_number":         float64(req.PullRequestNumber),
		"forgejo_pull_request_number": float64(req.ForgejoPullRequestNumber),
		"expected_head_sha":           req.ExpectedHeadSHA,
		"merge_method":                req.MergeMethod,
	})
	if err != nil {
		return "", normalizedPRMergeDelegationConstraints{}, err
	}
	return target.Repository, normalizedPRMergeDelegationConstraints{
		pullRequestNumber:        int64(normalized["pull_request_number"].(float64)),
		forgejoPullRequestNumber: int64(normalized["forgejo_pull_request_number"].(float64)),
		expectedHeadSHA:          normalized["expected_head_sha"].(string), mergeMethod: normalized["merge_method"].(string),
	}, nil
}

func (h *Handler) lockActivePRMergeDelegationForAssertion(r *http.Request, queries *db.Queries, resolved resolvedTaskWorkload, scope workloadAssertionScope, now time.Time) (db.WorkloadPrMergeDelegation, error) {
	constraints := scope.Operation.Constraints
	pullRequestNumber, pullOK := constraints["pull_request_number"].(float64)
	forgejoPullRequestNumber, forgejoOK := constraints["forgejo_pull_request_number"].(float64)
	expectedHeadSHA, headOK := constraints["expected_head_sha"].(string)
	mergeMethod, methodOK := constraints["merge_method"].(string)
	if !pullOK || !forgejoOK || !headOK || !methodOK {
		return db.WorkloadPrMergeDelegation{}, errActivePRMergeDelegationRequired
	}
	delegation, err := queries.LockActivePRMergeDelegationForAssertion(r.Context(), db.LockActivePRMergeDelegationForAssertionParams{
		WorkspaceID: resolved.WorkspaceID, TaskID: resolved.Task.ID, RunID: resolved.Task.ID,
		Repository: scope.Resource.Repository, PullRequestNumber: int64(pullRequestNumber),
		ForgejoPullRequestNumber: int64(forgejoPullRequestNumber), ExpectedHeadSha: expectedHeadSHA,
		MergeMethod: mergeMethod, AssertedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.WorkloadPrMergeDelegation{}, errActivePRMergeDelegationRequired
	}
	return delegation, err
}

func newPRMergeDelegationResponse(delegation db.WorkloadPrMergeDelegation, now time.Time) prMergeDelegationResponse {
	state := "active"
	if delegation.RevokedAt.Valid {
		state = "revoked"
	} else if !delegation.ExpiresAt.Time.After(now) {
		state = "expired"
	}
	response := prMergeDelegationResponse{
		Schema: prMergeDelegationSchema, ID: uuidToString(delegation.ID), WorkspaceID: uuidToString(delegation.WorkspaceID),
		TaskID: uuidToString(delegation.TaskID), RunID: uuidToString(delegation.RunID), Operation: delegation.Operation,
		Repository: delegation.Repository, PullRequestNumber: delegation.PullRequestNumber,
		ForgejoPullRequestNumber: delegation.ForgejoPullRequestNumber, ExpectedHeadSHA: delegation.ExpectedHeadSha,
		MergeMethod: delegation.MergeMethod, AuthorityRevision: uuidToString(delegation.AuthorityRevision),
		GrantedByUserID: uuidToString(delegation.GrantedByUserID), GrantedAt: delegation.GrantedAt.Time.UTC().Format(time.RFC3339Nano),
		ExpiresAt: delegation.ExpiresAt.Time.UTC().Format(time.RFC3339Nano), State: state,
	}
	if delegation.RevokedAt.Valid {
		value := delegation.RevokedAt.Time.UTC().Format(time.RFC3339Nano)
		response.RevokedAt = &value
	}
	if delegation.RevokedByUserID.Valid {
		value := uuidToString(delegation.RevokedByUserID)
		response.RevokedByUserID = &value
	}
	if delegation.RevocationReason.Valid {
		value := delegation.RevocationReason.String
		response.RevocationReason = &value
	}
	return response
}
