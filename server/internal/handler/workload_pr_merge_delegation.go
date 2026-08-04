package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	prMergeDelegationSchema        = "workload.pr-merge-delegation.v2"
	prMergeDelegationEventSchema   = "workload.pr-merge-delegation-event.v2"
	prMergeDelegationReceiptSchema = "workload.pr-merge-delegation-consumption.v2"
	prMergeDelegationApprovalTTL   = 5 * time.Minute
)

var (
	errPRMergeApprovalRequired = errors.New("PR merge approval required")
	errPRMergeDelegationDenied = errors.New("PR merge delegation denied")
)

type emptyDelegationRequest struct{}

type prMergeDelegationFacts struct {
	Schema                  string `json:"schema"`
	DelegationID            string `json:"delegation_id"`
	AuthorityRevision       string `json:"authority_revision"`
	WorkspaceID             string `json:"workspace_id"`
	IssueID                 string `json:"issue_id"`
	TaskID                  string `json:"task_id"`
	RunID                   string `json:"run_id"`
	RuntimeID               string `json:"runtime_id"`
	NotAfter                string `json:"not_after,omitempty"`
	TargetInstance          string `json:"target_instance"`
	CanonicalRepositoryID   string `json:"canonical_repository_id"`
	CanonicalRepository     string `json:"canonical_repository"`
	Provider                string `json:"provider"`
	ProviderBindingID       string `json:"provider_binding_id"`
	ProviderBindingRevision string `json:"provider_binding_revision"`
	ProviderRepository      string `json:"provider_repository"`
	AGSPRNumber             int64  `json:"ags_pr_number"`
	ProviderPRNumber        int64  `json:"provider_pr_number"`
	ExpectedHeadSHA         string `json:"expected_head_sha"`
	ExpectedBaseSHA         string `json:"expected_base_sha"`
	BaseRef                 string `json:"base_ref"`
	MergeMethod             string `json:"merge_method"`
	ProjectionFactsRevision string `json:"projection_facts_revision"`
	FactsDigest             string `json:"facts_digest"`
}

type prMergeDelegationResponse struct {
	Schema                 string                 `json:"schema"`
	ID                     string                 `json:"id"`
	WorkspaceID            string                 `json:"workspace_id"`
	IssueID                string                 `json:"issue_id"`
	TaskID                 string                 `json:"task_id"`
	RunID                  string                 `json:"run_id"`
	RuntimeID              string                 `json:"runtime_id"`
	State                  string                 `json:"state"`
	ApprovalPolicyRevision string                 `json:"approval_policy_revision"`
	RequestedAt            string                 `json:"requested_at"`
	ApprovedAt             *string                `json:"approved_at,omitempty"`
	ApprovedByUserID       *string                `json:"approved_by_user_id,omitempty"`
	NotAfter               *string                `json:"not_after,omitempty"`
	RevokedAt              *string                `json:"revoked_at,omitempty"`
	RevokedByUserID        *string                `json:"revoked_by_user_id,omitempty"`
	RevocationReason       *string                `json:"revocation_reason,omitempty"`
	SupersededAt           *string                `json:"superseded_at,omitempty"`
	SupersedeReason        *string                `json:"supersede_reason,omitempty"`
	ConsumerInstanceID     *string                `json:"consumer_instance_id,omitempty"`
	ConsumerIntentID       *string                `json:"consumer_intent_id,omitempty"`
	ConsumptionReceiptID   *string                `json:"consumption_receipt_id,omitempty"`
	ConsumedAt             *string                `json:"consumed_at,omitempty"`
	Facts                  prMergeDelegationFacts `json:"facts"`
}

type prMergeDelegationEventResponse struct {
	Schema           string         `json:"schema"`
	ID               string         `json:"id"`
	DelegationID     string         `json:"delegation_id"`
	EventType        string         `json:"event_type"`
	ActorType        string         `json:"actor_type"`
	ActorID          string         `json:"actor_id"`
	ConsumerIntentID *string        `json:"consumer_intent_id,omitempty"`
	Details          map[string]any `json:"details"`
	CreatedAt        string         `json:"created_at"`
}

type prMergeDelegationServiceRequest struct {
	AuthorityRevision       string `json:"authority_revision"`
	FactsDigest             string `json:"facts_digest"`
	TargetInstance          string `json:"target_instance"`
	CanonicalRepositoryID   string `json:"canonical_repository_id"`
	CanonicalRepository     string `json:"canonical_repository"`
	ProviderBindingID       string `json:"provider_binding_id"`
	ProviderBindingRevision string `json:"provider_binding_revision"`
	ProviderRepository      string `json:"provider_repository"`
	AGSPRNumber             int64  `json:"ags_pr_number"`
	ProviderPRNumber        int64  `json:"provider_pr_number"`
	ExpectedHeadSHA         string `json:"expected_head_sha"`
	ExpectedBaseSHA         string `json:"expected_base_sha"`
	BaseRef                 string `json:"base_ref"`
	MergeMethod             string `json:"merge_method"`
	ProjectionFactsRevision string `json:"projection_facts_revision"`
	TaskID                  string `json:"task_id"`
	RunID                   string `json:"run_id"`
	SessionID               string `json:"session_id"`
	IntentID                string `json:"intent_id,omitempty"`
	Phase                   string `json:"phase"`
}

type prMergeDelegationEffectRequest struct {
	ConsumerInstanceID   string `json:"consumer_instance_id"`
	IntentID             string `json:"intent_id"`
	ConsumptionReceiptID string `json:"consumption_receipt_id"`
	ProviderOutcome      string `json:"provider_outcome"`
	ProviderMergeSHA     string `json:"provider_merge_sha,omitempty"`
}

// ValidateDelegatedPRMergeConfiguration verifies only presence and non-secret
// identity shape. It never exposes the service token value.
func ValidateDelegatedPRMergeConfiguration() error {
	if !delegatedPRMergeEnabled() {
		return nil
	}
	if strings.TrimSpace(os.Getenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN")) == "" {
		return fmt.Errorf("external PR service token is required when delegated PR merge is enabled")
	}
	if !canonicalExternalPRInstancePattern.MatchString(configuredExternalPRServiceInstance()) {
		return fmt.Errorf("external PR service instance is required when delegated PR merge is enabled")
	}
	return nil
}

func delegatedPRMergeEnabled() bool {
	return os.Getenv("MULTICA_DELEGATED_PR_MERGE_ENABLED") == "1"
}

func configuredExternalPRServiceInstance() string {
	return os.Getenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID")
}

func (h *Handler) ListPRMergeDelegations(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) || !requireDelegatedPRMergeEnabled(w) {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	var rows []db.WorkloadPrMergeDelegation
	var err error
	if issueIDValue := strings.TrimSpace(r.URL.Query().Get("issue_id")); issueIDValue != "" {
		issueID, parseOK := parseUUIDOrBadRequest(w, issueIDValue, "issue id")
		if !parseOK {
			return
		}
		rows, err = h.Queries.ListCurrentPRMergeDelegationsForIssue(r.Context(), db.ListCurrentPRMergeDelegationsForIssueParams{WorkspaceID: workspaceID, IssueID: issueID})
	} else {
		rows, err = h.Queries.ListPRMergeDelegationsInWorkspace(r.Context(), workspaceID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list PR merge delegations")
		return
	}
	out := make([]prMergeDelegationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, newPRMergeDelegationResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"delegations": out})
}

func (h *Handler) GetPRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) || !requireDelegatedPRMergeEnabled(w) {
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
	row, err := h.Queries.GetPRMergeDelegationInWorkspace(r.Context(), db.GetPRMergeDelegationInWorkspaceParams{ID: delegationID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PR merge delegation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read PR merge delegation")
		return
	}
	events, err := h.Queries.ListPRMergeDelegationEvents(r.Context(), delegationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read PR merge delegation events")
		return
	}
	eventOut := make([]prMergeDelegationEventResponse, 0, len(events))
	for _, event := range events {
		eventOut = append(eventOut, newPRMergeDelegationEventResponse(event))
	}
	writeJSON(w, http.StatusOK, map[string]any{"delegation": newPRMergeDelegationResponse(row), "events": eventOut})
}

func (h *Handler) ApprovePRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) || !requireDelegatedPRMergeEnabled(w) {
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
	userID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-User-ID"), "user id")
	if !ok {
		return
	}
	var req emptyDelegationRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	now := h.currentWorkloadAssertionTime()
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to approve PR merge delegation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(r.Context(), tx, []pgtype.UUID{workspaceID}); err != nil {
		writeError(w, 500, "failed to approve PR merge delegation")
		return
	}
	if _, err := qtx.LockWorkspaceForPRMergeDelegation(r.Context(), workspaceID); err != nil {
		writeError(w, 409, "workspace is not active")
		return
	}
	row, err := qtx.GetPRMergeDelegationInWorkspace(r.Context(), db.GetPRMergeDelegationInWorkspaceParams{ID: delegationID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "PR merge delegation not found")
		return
	}
	if err != nil || row.State != "pending_approval" {
		writeError(w, 409, "PR merge delegation is not pending approval")
		return
	}
	if !h.prMergeDelegationFactsRemainCurrent(r.Context(), qtx, row, now) {
		writeError(w, 409, "PR merge delegation facts are stale")
		return
	}
	row, err = qtx.ApprovePRMergeDelegationInWorkspace(r.Context(), db.ApprovePRMergeDelegationInWorkspaceParams{
		ApprovedAt: pgtype.Timestamptz{Time: now, Valid: true}, ApprovedByUserID: userID,
		NotAfter: pgtype.Timestamptz{Time: now.Add(prMergeDelegationApprovalTTL), Valid: true}, ID: delegationID, WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, 409, "PR merge delegation is not pending approval")
		return
	}
	if err := createPRMergeDelegationEvent(r.Context(), qtx, row, "approved", "human", uuidToString(userID), pgtype.UUID{}, map[string]any{}); err != nil {
		writeError(w, 500, "failed to record PR merge delegation approval")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to approve PR merge delegation")
		return
	}
	writeJSON(w, http.StatusOK, newPRMergeDelegationResponse(row))
}

func (h *Handler) RevokePRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	if !requireHumanDelegationActor(w, r) || !requireDelegatedPRMergeEnabled(w) {
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
	userID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-User-ID"), "user id")
	if !ok {
		return
	}
	var req emptyDelegationRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	now := h.currentWorkloadAssertionTime()
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to revoke PR merge delegation")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(r.Context(), tx, []pgtype.UUID{workspaceID}); err != nil {
		writeError(w, 500, "failed to revoke PR merge delegation")
		return
	}
	if _, err := qtx.LockWorkspaceForPRMergeDelegation(r.Context(), workspaceID); err != nil {
		writeError(w, 409, "workspace is not active")
		return
	}
	current, err := qtx.GetPRMergeDelegationInWorkspace(r.Context(), db.GetPRMergeDelegationInWorkspaceParams{ID: delegationID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "PR merge delegation not found")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to revoke PR merge delegation")
		return
	}
	if current.State == "consumed" {
		writeError(w, 409, "PR merge delegation is already consumed")
		return
	}
	row, err := qtx.RevokePRMergeDelegationInWorkspace(r.Context(), db.RevokePRMergeDelegationInWorkspaceParams{
		RevokedAt: pgtype.Timestamptz{Time: now, Valid: true}, RevokedByUserID: userID,
		RevocationReason: pgtype.Text{String: "revoked by human workspace operator", Valid: true}, ID: delegationID, WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, 409, "PR merge delegation is not revocable")
		return
	}
	if err := createPRMergeDelegationEvent(r.Context(), qtx, row, "revoked", "human", uuidToString(userID), pgtype.UUID{}, map[string]any{}); err != nil {
		writeError(w, 500, "failed to record PR merge delegation revocation")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to revoke PR merge delegation")
		return
	}
	writeJSON(w, http.StatusOK, newPRMergeDelegationResponse(row))
}

func (h *Handler) IntrospectPRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	h.handlePRMergeDelegationServiceRequest(w, r, false)
}

func (h *Handler) ConsumePRMergeDelegation(w http.ResponseWriter, r *http.Request) {
	h.handlePRMergeDelegationServiceRequest(w, r, true)
}

func (h *Handler) handlePRMergeDelegationServiceRequest(w http.ResponseWriter, r *http.Request, consume bool) {
	if !h.requireExternalPRServiceToken(w, r) || !requireDelegatedPRMergeEnabled(w) {
		return
	}
	delegationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "delegationId"), "delegation id")
	if !ok {
		return
	}
	var req prMergeDelegationServiceRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	if err := validatePRMergeDelegationServiceRequest(req, consume); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	now := h.currentWorkloadAssertionTime()
	preflight, err := h.Queries.GetPRMergeDelegationByID(r.Context(), delegationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "PR merge delegation not found")
		return
	}
	if err != nil {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(r.Context(), tx, []pgtype.UUID{preflight.WorkspaceID}); err != nil {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	if _, err := qtx.LockWorkspaceForPRMergeDelegation(r.Context(), preflight.WorkspaceID); err != nil {
		writeError(w, 409, "workspace is not active")
		return
	}
	var lockedTask db.AgentTaskQueue
	var lockedLink db.ExternalPullRequestLink
	if preflight.State != "consumed" {
		lockedTask, err = qtx.LockTaskForPRMergeDelegation(r.Context(), db.LockTaskForPRMergeDelegationParams{ID: preflight.TaskID, WorkspaceID: preflight.WorkspaceID})
		if err != nil {
			writeError(w, 409, "workload execution is not active")
			return
		}
		lockedLink, err = qtx.LockExternalPRProjectionForMerge(r.Context(), db.LockExternalPRProjectionForMergeParams{
			WorkspaceID: preflight.WorkspaceID, IssueID: preflight.IssueID,
			AgsPrNumber: int32(preflight.AgsPrNumber), ProviderPrNumber: pgtype.Int4{Int32: int32(preflight.ProviderPrNumber), Valid: true},
		})
		if err != nil {
			writeError(w, 409, "PR merge delegation facts do not match")
			return
		}
	}
	row, err := qtx.LockPRMergeDelegationForService(r.Context(), delegationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "PR merge delegation not found")
		return
	}
	if err != nil || row.WorkspaceID != preflight.WorkspaceID {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	if _, err := qtx.LockWorkspaceForPRMergeDelegation(r.Context(), row.WorkspaceID); err != nil {
		writeError(w, 409, "workspace is not active")
		return
	}
	if !serviceRequestMatchesDelegation(req, row) {
		writeError(w, 409, "PR merge delegation facts do not match")
		return
	}
	intentID := pgtype.UUID{}
	if req.IntentID != "" {
		intentID = parseUUID(req.IntentID)
	}
	if row.State == "consumed" {
		if consume && row.ConsumerInstanceID.Valid && row.ConsumerInstanceID.String == req.TargetInstance && row.ConsumerIntentID == intentID && row.ConsumeRequestDigest.Valid && row.ConsumeRequestDigest.String == serviceRequestDigest(req) {
			if err := tx.Commit(r.Context()); err != nil {
				writeError(w, 503, "PR merge delegation is unavailable")
				return
			}
			writeJSON(w, 200, map[string]any{"schema": prMergeDelegationReceiptSchema, "outcome": "already_consumed", "delegation": newPRMergeDelegationResponse(row)})
			return
		}
		writeError(w, 409, "PR merge delegation was consumed by another intent")
		return
	}
	if row.State == "approved" && row.NotAfter.Valid && !row.NotAfter.Time.After(now) {
		expired, expireErr := qtx.ExpirePRMergeDelegationByID(r.Context(), db.ExpirePRMergeDelegationByIDParams{
			ExpiredAt: pgtype.Timestamptz{Time: now, Valid: true}, ID: row.ID,
		})
		if expireErr != nil {
			writeError(w, 409, "PR merge delegation is not active")
			return
		}
		if eventErr := createPRMergeDelegationEvent(r.Context(), qtx, expired, "expired", "system", "multica", pgtype.UUID{}, map[string]any{}); eventErr != nil {
			writeError(w, 503, "failed to record PR merge delegation expiry")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, 503, "PR merge delegation is unavailable")
			return
		}
		writeError(w, 409, "PR merge delegation is not active")
		return
	}
	if !prMergeDelegationFactsRemainCurrent(lockedTask, lockedLink, row, now) {
		writeError(w, 409, "PR merge delegation facts do not match")
		return
	}
	if row.State != "approved" || !row.NotAfter.Valid || !row.NotAfter.Time.After(now) {
		writeError(w, 409, "PR merge delegation is not active")
		return
	}
	if !consume {
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, 503, "PR merge delegation is unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"schema": prMergeDelegationReceiptSchema, "outcome": "active", "delegation": newPRMergeDelegationResponse(row)})
		return
	}
	receiptID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	row, err = qtx.ConsumePRMergeDelegation(r.Context(), db.ConsumePRMergeDelegationParams{
		ConsumerInstanceID: pgtype.Text{String: req.TargetInstance, Valid: true}, ConsumerIntentID: intentID,
		ConsumeRequestDigest: pgtype.Text{String: serviceRequestDigest(req), Valid: true}, ConsumptionReceiptID: receiptID,
		ConsumedAt: pgtype.Timestamptz{Time: now, Valid: true}, ID: delegationID,
	})
	if err != nil {
		writeError(w, 409, "PR merge delegation could not be consumed")
		return
	}
	if err := createPRMergeDelegationEvent(r.Context(), qtx, row, "consumed", "ags_service", req.TargetInstance, intentID, map[string]any{"session_id": req.SessionID}); err != nil {
		writeError(w, 503, "failed to record PR merge delegation consumption")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"schema": prMergeDelegationReceiptSchema, "outcome": "consumed", "delegation": newPRMergeDelegationResponse(row)})
}

func (h *Handler) RecordPRMergeDelegationEffect(w http.ResponseWriter, r *http.Request) {
	if !h.requireExternalPRServiceToken(w, r) || !requireDelegatedPRMergeEnabled(w) {
		return
	}
	delegationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "delegationId"), "delegation id")
	if !ok {
		return
	}
	var req prMergeDelegationEffectRequest
	if !decodeClosedJSONRequest(w, r, &req) {
		return
	}
	if req.ConsumerInstanceID != configuredExternalPRServiceInstance() || !canonicalExternalPRInstancePattern.MatchString(req.ConsumerInstanceID) {
		writeError(w, 400, "consumer instance is not canonical")
		return
	}
	intentID, err := uuid.Parse(req.IntentID)
	if err != nil {
		writeError(w, 400, "intent id is invalid")
		return
	}
	receiptID, err := uuid.Parse(req.ConsumptionReceiptID)
	if err != nil {
		writeError(w, 400, "consumption receipt id is invalid")
		return
	}
	eventType := "effect_outcome_unknown"
	if req.ProviderOutcome == "confirmed" {
		eventType = "effect_confirmed"
	} else if req.ProviderOutcome != "outcome_unknown" {
		writeError(w, 400, "provider outcome is invalid")
		return
	}
	if req.ProviderMergeSHA != "" && !canonicalGitSHA1Pattern.MatchString(req.ProviderMergeSHA) {
		writeError(w, 400, "provider merge SHA is invalid")
		return
	}
	preflight, err := h.Queries.GetPRMergeDelegationByID(r.Context(), delegationID)
	if err != nil {
		writeError(w, 404, "PR merge delegation not found")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(r.Context(), tx, []pgtype.UUID{preflight.WorkspaceID}); err != nil {
		writeError(w, 503, "PR merge delegation is unavailable")
		return
	}
	row, err := qtx.LockPRMergeDelegationForService(r.Context(), delegationID)
	if err != nil || row.WorkspaceID != preflight.WorkspaceID || row.State != "consumed" || !row.ConsumerInstanceID.Valid || row.ConsumerInstanceID.String != req.ConsumerInstanceID || row.ConsumerIntentID.Bytes != intentID || row.ConsumptionReceiptID.Bytes != receiptID {
		writeError(w, 409, "consumption receipt does not match")
		return
	}
	if err := createPRMergeDelegationEvent(r.Context(), qtx, row, eventType, "ags_service", req.ConsumerInstanceID, row.ConsumerIntentID, map[string]any{"provider_outcome": req.ProviderOutcome, "provider_merge_sha": req.ProviderMergeSHA}); err != nil {
		writeError(w, 503, "failed to record PR merge effect")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 503, "failed to record PR merge effect")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "recorded", "event_type": eventType})
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

func requireHumanDelegationActor(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "PR merge delegations require a human workspace operator")
		return false
	}
	return true
}

func requireDelegatedPRMergeEnabled(w http.ResponseWriter) bool {
	if !delegatedPRMergeEnabled() {
		writeError(w, http.StatusServiceUnavailable, "delegated PR merge is disabled")
		return false
	}
	return true
}

func validatePRMergeDelegationServiceRequest(req prMergeDelegationServiceRequest, consume bool) error {
	if req.TargetInstance != configuredExternalPRServiceInstance() || !canonicalExternalPRInstancePattern.MatchString(req.TargetInstance) {
		return fmt.Errorf("target instance is not canonical")
	}
	if _, err := uuid.Parse(req.AuthorityRevision); err != nil {
		return fmt.Errorf("authority revision is invalid")
	}
	if !canonicalExternalPRDigestPattern.MatchString(req.FactsDigest) || !canonicalExternalPRDigestPattern.MatchString(req.CanonicalRepositoryID) || !canonicalExternalPRDigestPattern.MatchString(req.ProviderBindingID) || !canonicalExternalPRDigestPattern.MatchString(req.ProviderBindingRevision) || !canonicalExternalPRDigestPattern.MatchString(req.ProjectionFactsRevision) {
		return fmt.Errorf("delegation digest is invalid")
	}
	if !isCanonicalRepositoryName(req.CanonicalRepository) || !isCanonicalRepositoryName(req.ProviderRepository) || !canonicalGitSHA1Pattern.MatchString(req.ExpectedHeadSHA) || !canonicalGitSHA1Pattern.MatchString(req.ExpectedBaseSHA) {
		return fmt.Errorf("delegation resource is not canonical")
	}
	if _, err := uuid.Parse(req.TaskID); err != nil {
		return fmt.Errorf("task id is invalid")
	}
	if _, err := uuid.Parse(req.RunID); err != nil {
		return fmt.Errorf("run id is invalid")
	}
	if strings.TrimSpace(req.SessionID) == "" || req.SessionID != strings.TrimSpace(req.SessionID) {
		return fmt.Errorf("session id is invalid")
	}
	if req.Phase != "exchange" && req.Phase != "preflight" && req.Phase != "pre_effect" {
		return fmt.Errorf("delegation phase is invalid")
	}
	if consume {
		if req.Phase != "pre_effect" {
			return fmt.Errorf("consume requires pre_effect phase")
		}
		if _, err := uuid.Parse(req.IntentID); err != nil {
			return fmt.Errorf("intent id is invalid")
		}
	} else if req.IntentID != "" {
		if _, err := uuid.Parse(req.IntentID); err != nil {
			return fmt.Errorf("intent id is invalid")
		}
	}
	return nil
}

func serviceRequestMatchesDelegation(req prMergeDelegationServiceRequest, row db.WorkloadPrMergeDelegation) bool {
	return req.AuthorityRevision == uuidToString(row.AuthorityRevision) && req.FactsDigest == row.FactsDigest &&
		req.TargetInstance == row.TargetInstance && req.CanonicalRepositoryID == row.CanonicalRepositoryID &&
		req.CanonicalRepository == row.CanonicalRepository && req.ProviderBindingID == row.ProviderBindingID &&
		req.ProviderBindingRevision == row.ProviderBindingRevision && req.ProviderRepository == row.ProviderRepository &&
		req.AGSPRNumber == row.AgsPrNumber && req.ProviderPRNumber == row.ProviderPrNumber &&
		req.ExpectedHeadSHA == row.ExpectedHeadSha && req.ExpectedBaseSHA == row.ExpectedBaseSha &&
		req.BaseRef == row.BaseRef && req.MergeMethod == row.MergeMethod && req.ProjectionFactsRevision == row.ProjectionFactsRevision &&
		req.TaskID == uuidToString(row.TaskID) && req.RunID == uuidToString(row.ExecutionID)
}

func serviceRequestDigest(req prMergeDelegationServiceRequest) string {
	encoded, _ := json.Marshal(req)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (h *Handler) prMergeDelegationFactsRemainCurrent(ctx context.Context, queries *db.Queries, row db.WorkloadPrMergeDelegation, now time.Time) bool {
	task, err := queries.LockTaskForPRMergeDelegation(ctx, db.LockTaskForPRMergeDelegationParams{ID: row.TaskID, WorkspaceID: row.WorkspaceID})
	if err != nil || task.Status != "running" || !task.ExecutionID.Valid || task.ExecutionID != row.ExecutionID || !task.RuntimeID.Valid || task.RuntimeID != row.RuntimeID {
		return false
	}
	link, err := queries.LockExternalPRProjectionForMerge(ctx, db.LockExternalPRProjectionForMergeParams{WorkspaceID: row.WorkspaceID, IssueID: row.IssueID, AgsPrNumber: int32(row.AgsPrNumber), ProviderPrNumber: pgtype.Int4{Int32: int32(row.ProviderPrNumber), Valid: true}})
	if err != nil {
		return false
	}
	return externalProjectionMatchesDelegation(link, row) && (row.State != "approved" || (row.NotAfter.Valid && row.NotAfter.Time.After(now)))
}

func prMergeDelegationFactsRemainCurrent(task db.AgentTaskQueue, link db.ExternalPullRequestLink, row db.WorkloadPrMergeDelegation, now time.Time) bool {
	return task.Status == "running" && task.ExecutionID.Valid && task.ExecutionID == row.ExecutionID &&
		task.RuntimeID.Valid && task.RuntimeID == row.RuntimeID && externalProjectionMatchesDelegation(link, row) &&
		(row.State != "approved" || (row.NotAfter.Valid && row.NotAfter.Time.After(now)))
}

func externalProjectionMatchesDelegation(link db.ExternalPullRequestLink, row db.WorkloadPrMergeDelegation) bool {
	return link.ID == row.ExternalPrLinkID && link.TargetInstance.Valid && link.TargetInstance.String == row.TargetInstance &&
		link.CanonicalRepositoryID.Valid && link.CanonicalRepositoryID.String == row.CanonicalRepositoryID &&
		link.CanonicalRepository.Valid && link.CanonicalRepository.String == row.CanonicalRepository &&
		link.ProviderBindingID.Valid && link.ProviderBindingID.String == row.ProviderBindingID &&
		link.ProviderBindingRevision.Valid && link.ProviderBindingRevision.String == row.ProviderBindingRevision &&
		link.ProviderRepository.Valid && link.ProviderRepository.String == row.ProviderRepository &&
		link.ExpectedHeadSha.Valid && link.ExpectedHeadSha.String == row.ExpectedHeadSha &&
		link.ExpectedBaseSha.Valid && link.ExpectedBaseSha.String == row.ExpectedBaseSha && link.BaseRef.Valid && link.BaseRef.String == row.BaseRef &&
		link.DelegatedMergeMethod.Valid && link.DelegatedMergeMethod.String == row.MergeMethod &&
		link.ProjectionFactsRevision.Valid && link.ProjectionFactsRevision.String == row.ProjectionFactsRevision
}

func createPRMergeDelegationEvent(ctx context.Context, queries *db.Queries, row db.WorkloadPrMergeDelegation, eventType, actorType, actorID string, intentID pgtype.UUID, details map[string]any) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = queries.CreatePRMergeDelegationEvent(ctx, db.CreatePRMergeDelegationEventParams{WorkspaceID: row.WorkspaceID, IssueID: row.IssueID, DelegationID: row.ID, EventType: eventType, ActorType: actorType, ActorID: actorID, ConsumerIntentID: intentID, Details: encoded})
	return err
}

func newPRMergeDelegationResponse(row db.WorkloadPrMergeDelegation) prMergeDelegationResponse {
	response := prMergeDelegationResponse{Schema: prMergeDelegationSchema, ID: uuidToString(row.ID), WorkspaceID: uuidToString(row.WorkspaceID), IssueID: uuidToString(row.IssueID), TaskID: uuidToString(row.TaskID), RunID: uuidToString(row.ExecutionID), RuntimeID: uuidToString(row.RuntimeID), State: row.State, ApprovalPolicyRevision: row.ApprovalPolicyRevision, RequestedAt: row.RequestedAt.Time.UTC().Format(time.RFC3339Nano), Facts: mergeDelegationFactsFromRow(row)}
	response.ApprovedAt = timestampPtr(row.ApprovedAt)
	response.ApprovedByUserID = uuidPtr(row.ApprovedByUserID)
	response.NotAfter = timestampPtr(row.NotAfter)
	response.RevokedAt = timestampPtr(row.RevokedAt)
	response.RevokedByUserID = uuidPtr(row.RevokedByUserID)
	response.RevocationReason = textToPtr(row.RevocationReason)
	response.SupersededAt = timestampPtr(row.SupersededAt)
	response.SupersedeReason = textToPtr(row.SupersedeReason)
	response.ConsumerInstanceID = textToPtr(row.ConsumerInstanceID)
	response.ConsumerIntentID = uuidPtr(row.ConsumerIntentID)
	response.ConsumptionReceiptID = uuidPtr(row.ConsumptionReceiptID)
	response.ConsumedAt = timestampPtr(row.ConsumedAt)
	return response
}

func mergeDelegationFactsFromRow(row db.WorkloadPrMergeDelegation) prMergeDelegationFacts {
	facts := prMergeDelegationFacts{Schema: prMergeDelegationSchema, DelegationID: uuidToString(row.ID), AuthorityRevision: uuidToString(row.AuthorityRevision), WorkspaceID: uuidToString(row.WorkspaceID), IssueID: uuidToString(row.IssueID), TaskID: uuidToString(row.TaskID), RunID: uuidToString(row.ExecutionID), RuntimeID: uuidToString(row.RuntimeID), TargetInstance: row.TargetInstance, CanonicalRepositoryID: row.CanonicalRepositoryID, CanonicalRepository: row.CanonicalRepository, Provider: row.Provider, ProviderBindingID: row.ProviderBindingID, ProviderBindingRevision: row.ProviderBindingRevision, ProviderRepository: row.ProviderRepository, AGSPRNumber: row.AgsPrNumber, ProviderPRNumber: row.ProviderPrNumber, ExpectedHeadSHA: row.ExpectedHeadSha, ExpectedBaseSHA: row.ExpectedBaseSha, BaseRef: row.BaseRef, MergeMethod: row.MergeMethod, ProjectionFactsRevision: row.ProjectionFactsRevision, FactsDigest: row.FactsDigest}
	if row.NotAfter.Valid {
		facts.NotAfter = row.NotAfter.Time.UTC().Format(time.RFC3339Nano)
	}
	return facts
}

func newPRMergeDelegationEventResponse(row db.WorkloadPrMergeDelegationEvent) prMergeDelegationEventResponse {
	details := map[string]any{}
	_ = json.Unmarshal(row.Details, &details)
	return prMergeDelegationEventResponse{Schema: prMergeDelegationEventSchema, ID: uuidToString(row.ID), DelegationID: uuidToString(row.DelegationID), EventType: row.EventType, ActorType: row.ActorType, ActorID: row.ActorID, ConsumerIntentID: uuidPtr(row.ConsumerIntentID), Details: details, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)}
}

func timestampPtr(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC().Format(time.RFC3339Nano)
	return &out
}
func uuidPtr(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	out := uuidToString(value)
	return &out
}

type prMergeDelegationBindingFacts struct {
	WorkspaceID             string `json:"workspace_id"`
	IssueID                 string `json:"issue_id"`
	ExternalPRLinkID        string `json:"external_pr_link_id"`
	TaskID                  string `json:"task_id"`
	ExecutionID             string `json:"execution_id"`
	RuntimeID               string `json:"runtime_id"`
	TargetInstance          string `json:"target_instance"`
	CanonicalRepositoryID   string `json:"canonical_repository_id"`
	CanonicalRepository     string `json:"canonical_repository"`
	Provider                string `json:"provider"`
	ProviderBindingID       string `json:"provider_binding_id"`
	ProviderBindingRevision string `json:"provider_binding_revision"`
	ProviderRepository      string `json:"provider_repository"`
	AGSPRNumber             int64  `json:"ags_pr_number"`
	ProviderPRNumber        int64  `json:"provider_pr_number"`
	ExpectedHeadSHA         string `json:"expected_head_sha"`
	ExpectedBaseSHA         string `json:"expected_base_sha"`
	BaseRef                 string `json:"base_ref"`
	MergeMethod             string `json:"merge_method"`
	ProjectionFactsRevision string `json:"projection_facts_revision"`
}

func digestPRMergeDelegationFacts(facts prMergeDelegationBindingFacts) string {
	encoded, _ := json.Marshal(facts)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (h *Handler) ensureApprovedPRMergeDelegationForAssertion(ctx context.Context, queries *db.Queries, resolved resolvedTaskWorkload, scope workloadAssertionScope, target workloadAssertionTarget, now time.Time) (db.WorkloadPrMergeDelegation, error) {
	if !delegatedPRMergeEnabled() || !resolved.Task.ExecutionID.Valid || !resolved.Task.RuntimeID.Valid || !resolved.Task.IssueID.Valid {
		return db.WorkloadPrMergeDelegation{}, errPRMergeDelegationDenied
	}
	constraints := scope.Operation.Constraints
	agsNumber, agsOK := constraints["pull_request_number"].(float64)
	providerNumber, providerOK := constraints["forgejo_pull_request_number"].(float64)
	head, headOK := constraints["expected_head_sha"].(string)
	method, methodOK := constraints["merge_method"].(string)
	if !agsOK || !providerOK || !headOK || !methodOK || agsNumber > 2147483647 || providerNumber > 2147483647 {
		return db.WorkloadPrMergeDelegation{}, errPRMergeDelegationDenied
	}
	link, err := queries.LockExternalPRProjectionForMerge(ctx, db.LockExternalPRProjectionForMergeParams{
		WorkspaceID: resolved.WorkspaceID, IssueID: resolved.Task.IssueID,
		AgsPrNumber: int32(agsNumber), ProviderPrNumber: pgtype.Int4{Int32: int32(providerNumber), Valid: true},
	})
	if err != nil {
		return db.WorkloadPrMergeDelegation{}, errPRMergeDelegationDenied
	}
	if !link.TargetInstance.Valid || !link.CanonicalRepositoryID.Valid || !link.CanonicalRepository.Valid ||
		!link.ProviderBindingID.Valid || !link.ProviderBindingRevision.Valid || !link.ProviderRepository.Valid ||
		!link.ExpectedHeadSha.Valid || !link.ExpectedBaseSha.Valid || !link.BaseRef.Valid ||
		!link.DelegatedMergeMethod.Valid || !link.ProjectionFactsRevision.Valid ||
		link.TargetInstance.String != target.Instance || link.CanonicalRepository.String != scope.Resource.Repository ||
		link.ExpectedHeadSha.String != head || link.DelegatedMergeMethod.String != method {
		return db.WorkloadPrMergeDelegation{}, errPRMergeDelegationDenied
	}
	facts := prMergeDelegationBindingFacts{
		WorkspaceID: uuidToString(resolved.WorkspaceID), IssueID: uuidToString(resolved.Task.IssueID),
		ExternalPRLinkID: uuidToString(link.ID), TaskID: uuidToString(resolved.Task.ID),
		ExecutionID: uuidToString(resolved.Task.ExecutionID), RuntimeID: uuidToString(resolved.Task.RuntimeID),
		TargetInstance: link.TargetInstance.String, CanonicalRepositoryID: link.CanonicalRepositoryID.String,
		CanonicalRepository: link.CanonicalRepository.String, Provider: "forgejo",
		ProviderBindingID: link.ProviderBindingID.String, ProviderBindingRevision: link.ProviderBindingRevision.String,
		ProviderRepository: link.ProviderRepository.String, AGSPRNumber: int64(agsNumber),
		ProviderPRNumber: int64(providerNumber), ExpectedHeadSHA: link.ExpectedHeadSha.String,
		ExpectedBaseSHA: link.ExpectedBaseSha.String, BaseRef: link.BaseRef.String,
		MergeMethod: link.DelegatedMergeMethod.String, ProjectionFactsRevision: link.ProjectionFactsRevision.String,
	}
	digest := digestPRMergeDelegationFacts(facts)
	current, err := queries.GetCurrentPRMergeDelegationForExecution(ctx, db.GetCurrentPRMergeDelegationForExecutionParams{
		WorkspaceID: resolved.WorkspaceID, TaskID: resolved.Task.ID, ExecutionID: resolved.Task.ExecutionID,
	})
	if err == nil {
		if current.FactsDigest == digest && current.ProjectionFactsRevision == facts.ProjectionFactsRevision {
			if current.State == "approved" && current.NotAfter.Valid && current.NotAfter.Time.After(now) {
				return current, nil
			}
			if current.State == "pending_approval" {
				return current, errPRMergeApprovalRequired
			}
			if current.State == "approved" {
				expired, expireErr := queries.ExpirePRMergeDelegationByID(ctx, db.ExpirePRMergeDelegationByIDParams{ExpiredAt: pgtype.Timestamptz{Time: now, Valid: true}, ID: current.ID})
				if expireErr != nil {
					return db.WorkloadPrMergeDelegation{}, expireErr
				}
				if eventErr := createPRMergeDelegationEvent(ctx, queries, expired, "expired", "system", "multica", pgtype.UUID{}, map[string]any{}); eventErr != nil {
					return db.WorkloadPrMergeDelegation{}, eventErr
				}
			}
		} else {
			superseded, supersedeErr := queries.SupersedePRMergeDelegationByID(ctx, db.SupersedePRMergeDelegationByIDParams{
				SupersededAt:    pgtype.Timestamptz{Time: now, Valid: true},
				SupersedeReason: pgtype.Text{String: "server-owned merge facts changed", Valid: true}, ID: current.ID,
			})
			if supersedeErr != nil {
				return db.WorkloadPrMergeDelegation{}, supersedeErr
			}
			if eventErr := createPRMergeDelegationEvent(ctx, queries, superseded, "superseded", "system", "multica", pgtype.UUID{}, map[string]any{}); eventErr != nil {
				return db.WorkloadPrMergeDelegation{}, eventErr
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.WorkloadPrMergeDelegation{}, err
	}
	pending, err := queries.CreatePendingPRMergeDelegation(ctx, db.CreatePendingPRMergeDelegationParams{
		WorkspaceID: resolved.WorkspaceID, IssueID: resolved.Task.IssueID, ExternalPrLinkID: link.ID,
		TaskID: resolved.Task.ID, ExecutionID: resolved.Task.ExecutionID, RuntimeID: resolved.Task.RuntimeID,
		TargetInstance: facts.TargetInstance, CanonicalRepositoryID: facts.CanonicalRepositoryID,
		CanonicalRepository: facts.CanonicalRepository, ProviderBindingID: facts.ProviderBindingID,
		ProviderBindingRevision: facts.ProviderBindingRevision, ProviderRepository: facts.ProviderRepository,
		AgsPrNumber: facts.AGSPRNumber, ProviderPrNumber: facts.ProviderPRNumber,
		ExpectedHeadSha: facts.ExpectedHeadSHA, ExpectedBaseSha: facts.ExpectedBaseSHA,
		BaseRef: facts.BaseRef, MergeMethod: facts.MergeMethod,
		ProjectionFactsRevision: facts.ProjectionFactsRevision, FactsDigest: digest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, currentErr := queries.GetCurrentPRMergeDelegationForExecution(ctx, db.GetCurrentPRMergeDelegationForExecutionParams{
			WorkspaceID: resolved.WorkspaceID, TaskID: resolved.Task.ID, ExecutionID: resolved.Task.ExecutionID,
		})
		if currentErr != nil || current.FactsDigest != digest || current.ProjectionFactsRevision != facts.ProjectionFactsRevision {
			return db.WorkloadPrMergeDelegation{}, errPRMergeDelegationDenied
		}
		if current.State == "approved" && current.NotAfter.Valid && current.NotAfter.Time.After(now) {
			return current, nil
		}
		return current, errPRMergeApprovalRequired
	}
	if err != nil {
		return db.WorkloadPrMergeDelegation{}, err
	}
	if err := createPRMergeDelegationEvent(ctx, queries, pending, "request_created", "task", uuidToString(resolved.Task.ID), pgtype.UUID{}, map[string]any{}); err != nil {
		return db.WorkloadPrMergeDelegation{}, err
	}
	return pending, errPRMergeApprovalRequired
}

func writePRMergeApprovalRequired(w http.ResponseWriter, row db.WorkloadPrMergeDelegation) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":       "PR merge approval required",
		"reason_code": "merge_approval_required",
		"approval": map[string]any{
			"schema":        prMergeDelegationSchema,
			"delegation_id": uuidToString(row.ID),
			"issue_id":      uuidToString(row.IssueID),
			"state":         row.State,
			"locator":       fmt.Sprintf("/api/workspaces/%s/workload-delegations/pr-merge/%s", uuidToString(row.WorkspaceID), uuidToString(row.ID)),
		},
	})
}
