package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Response mappers ────────────────────────────────────────────────────────

// vcsPullRequestToResponse maps a stored VCS PR onto the shared PR response
// shape for single-PR webhook broadcasts (no aggregated check counts; the
// frontend re-queries the issue's PR list for fresh counts).
func vcsPullRequestToResponse(p db.VcsPullRequest) GitHubPullRequestResponse {
	return GitHubPullRequestResponse{
		ID:               uuidToString(p.ID),
		Provider:         p.Provider,
		WorkspaceID:      uuidToString(p.WorkspaceID),
		RepoOwner:        p.RepoOwner,
		RepoName:         p.RepoName,
		Number:           p.PrNumber,
		Title:            p.Title,
		State:            p.State,
		HtmlURL:          p.HtmlUrl,
		Branch:           textToPtr(p.Branch),
		AuthorLogin:      textToPtr(p.AuthorLogin),
		AuthorAvatarURL:  textToPtr(p.AuthorAvatarUrl),
		MergedAt:         timestampToPtr(p.MergedAt),
		ClosedAt:         timestampToPtr(p.ClosedAt),
		PRCreatedAt:      timestampToString(p.PrCreatedAt),
		PRUpdatedAt:      timestampToString(p.PrUpdatedAt),
		MergeableState:   nil,
		ChecksConclusion: nil,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

// vcsPullRequestRowToResponse maps an issue's PR-list row, which carries the
// aggregated commit-status counts, onto the shared response shape.
func vcsPullRequestRowToResponse(p db.ListVCSPullRequestsByIssueRow) GitHubPullRequestResponse {
	return GitHubPullRequestResponse{
		ID:               uuidToString(p.ID),
		Provider:         p.Provider,
		WorkspaceID:      uuidToString(p.WorkspaceID),
		RepoOwner:        p.RepoOwner,
		RepoName:         p.RepoName,
		Number:           p.PrNumber,
		Title:            p.Title,
		State:            p.State,
		HtmlURL:          p.HtmlUrl,
		Branch:           textToPtr(p.Branch),
		AuthorLogin:      textToPtr(p.AuthorLogin),
		AuthorAvatarURL:  textToPtr(p.AuthorAvatarUrl),
		MergedAt:         timestampToPtr(p.MergedAt),
		ClosedAt:         timestampToPtr(p.ClosedAt),
		PRCreatedAt:      timestampToString(p.PrCreatedAt),
		PRUpdatedAt:      timestampToString(p.PrUpdatedAt),
		MergeableState:   nil,
		ChecksConclusion: aggregateChecksConclusion(p.ChecksFailed, p.ChecksPassed, p.ChecksPending, p.ChecksTotal),
		ChecksTotal:      p.ChecksTotal,
		ChecksPassed:     p.ChecksPassed,
		ChecksFailed:     p.ChecksFailed,
		ChecksPending:    p.ChecksPending,
		ChecksRunning:    p.ChecksPending,
		FailedCheckNames: []string{},
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

// ── Webhook ─────────────────────────────────────────────────────────────────

// HandleVCSWebhook (POST /api/webhooks/vcs/{connectionId}) authenticates and
// mirrors webhooks from any token-based Git provider. The connection id in the path
// selects the workspace, the provider, and the decryption secret; the provider
// adapter handles the provider-specific signature scheme, event header, and
// payload shape, returning normalized events to the shared mirror logic below.
func (h *Handler) HandleVCSWebhook(w http.ResponseWriter, r *http.Request) {
	// Where the integration is off (the managed cloud) the endpoint behaves as
	// if it does not exist — a bare 404 that reveals nothing about config, the
	// same response a genuinely unknown connection id gets below.
	if !h.isVCSAvailable() {
		writeError(w, http.StatusNotFound, "unknown connection")
		return
	}
	if !h.isVCSConfigured() {
		writeError(w, http.StatusServiceUnavailable, "vcs webhooks not configured")
		return
	}
	connUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}

	conn, err := h.Queries.GetVCSConnectionByID(r.Context(), connUUID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("vcs: lookup connection failed", "err", err)
		}
		writeError(w, http.StatusNotFound, "unknown connection")
		return
	}
	provider, ok := vcs.For(conn.Provider)
	if !ok {
		slog.Error("vcs: connection has unknown provider", "provider", conn.Provider)
		writeError(w, http.StatusInternalServerError, "unknown provider")
		return
	}

	secret, err := h.openVCSSecret(conn.WebhookSecretEncrypted)
	if err != nil {
		slog.Error("vcs: decrypt webhook secret failed", "err", err)
		writeError(w, http.StatusInternalServerError, "secret error")
		return
	}
	if !provider.VerifySignature(secret, r.Header, body) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	switch provider.EventKind(r.Header) {
	case vcs.EventPullRequest:
		if pr, err := provider.ParsePullRequest(body); err != nil {
			slog.Warn("vcs: bad pull_request payload", "provider", conn.Provider, "err", err)
		} else if err := h.mirrorVCSPullRequest(r.Context(), conn, pr); err != nil {
			slog.Warn("vcs: PR fact transaction failed", "provider", conn.Provider, "err", err)
			writeError(w, http.StatusServiceUnavailable, "vcs webhook temporarily unavailable")
			return
		}
	case vcs.EventCIStatus:
		if st, err := provider.ParseCIStatus(body); err != nil {
			slog.Warn("vcs: bad status payload", "provider", conn.Provider, "err", err)
		} else {
			h.mirrorVCSCIStatus(r.Context(), conn, st)
		}
	default:
		// Acknowledge unmodelled events so the provider doesn't flag the hook.
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) mirrorVCSPullRequest(ctx context.Context, conn db.VcsConnection, ev vcs.PullRequestEvent) error {
	if ev.RepoOwner == "" || ev.RepoName == "" || ev.Number == 0 {
		slog.Warn("vcs: pull_request missing repo identity", "provider", conn.Provider)
		return nil
	}
	idents := extractIdentifiers(ev.Title, ev.Body, ev.Branch)
	closingIdents := map[string]struct{}{}
	for _, id := range extractClosingIdentifiers(ev.Title, ev.Body) {
		closingIdents[id] = struct{}{}
	}
	qualifyingIdents := map[string]struct{}{}
	for _, id := range extractIdentifiers(ev.Title, ev.Branch) {
		qualifyingIdents[id] = struct{}{}
	}
	for id := range closingIdents {
		qualifyingIdents[id] = struct{}{}
	}
	preserveCloseIntent := !ev.Terminal() && (ev.State == "merged" || ev.State == "closed")
	prefix := h.getIssuePrefix(ctx, conn.WorkspaceID)

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PR fact transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{conn.WorkspaceID}); err != nil {
		return fmt.Errorf("lock provider workspace: %w", err)
	}
	currentConnection, err := qtx.GetVCSConnectionByID(ctx, conn.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reload VCS connection: %w", err)
	}
	if currentConnection.WorkspaceID != conn.WorkspaceID {
		return nil
	}
	identity := fmt.Sprintf("%s:%s:%s:%d", uuidToString(conn.ID), ev.RepoOwner, ev.RepoName, ev.Number)
	if err := lockPullRequestIdentity(ctx, tx, "vcs", identity); err != nil {
		return fmt.Errorf("lock PR identity: %w", err)
	}

	issueByID := map[string]db.Issue{}
	currentIssueByIdent := map[string]db.Issue{}
	currentIssueIDs := map[string]struct{}{}
	incomingUpdatedAt := parseGHTimeRequired(ev.UpdatedAt)
	existingPR, err := qtx.GetVCSPullRequestByIdentity(ctx, db.GetVCSPullRequestByIdentityParams{
		ConnectionID: conn.ID, RepoOwner: ev.RepoOwner, RepoName: ev.RepoName, PrNumber: ev.Number,
	})
	if err == nil {
		if !shouldApplyVCSPullRequestFact(existingPR, incomingUpdatedAt, ev.State) {
			return nil
		}
		oldIssueIDs, listErr := qtx.ListIssueIDsForVCSPullRequest(ctx, existingPR.ID)
		if listErr != nil {
			return fmt.Errorf("list existing PR links: %w", listErr)
		}
		for _, issueID := range oldIssueIDs {
			issue, loadErr := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: conn.WorkspaceID})
			if errors.Is(loadErr, pgx.ErrNoRows) {
				continue
			}
			if loadErr != nil {
				return fmt.Errorf("load existing linked Issue: %w", loadErr)
			}
			issueByID[uuidToString(issue.ID)] = issue
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read existing PR: %w", err)
	}
	for _, id := range idents {
		issue, ok, lookupErr := lookupIssueByIdentifierWithQueriesResult(ctx, qtx, conn.WorkspaceID, prefix, id)
		if lookupErr != nil {
			return fmt.Errorf("resolve Issue identifier %s: %w", id, lookupErr)
		}
		if ok {
			issueKey := uuidToString(issue.ID)
			issueByID[issueKey] = issue
			currentIssueByIdent[id] = issue
			currentIssueIDs[issueKey] = struct{}{}
		}
	}
	issueIDs := make([]pgtype.UUID, 0, len(issueByID))
	for _, issue := range issueByID {
		issueIDs = append(issueIDs, issue.ID)
	}
	if err := lockCompletionIssues(ctx, qtx, issueIDs); err != nil {
		return fmt.Errorf("lock linked Issues: %w", err)
	}
	for key, issue := range issueByID {
		reloaded, loadErr := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: conn.WorkspaceID})
		if errors.Is(loadErr, pgx.ErrNoRows) {
			delete(issueByID, key)
			delete(currentIssueIDs, key)
			for identifier, current := range currentIssueByIdent {
				if uuidToString(current.ID) == key {
					delete(currentIssueByIdent, identifier)
				}
			}
			continue
		}
		if loadErr != nil {
			return fmt.Errorf("reload linked Issue %s: %w", key, loadErr)
		}
		issueByID[key] = reloaded
	}

	pr, err := qtx.UpsertVCSPullRequest(ctx, db.UpsertVCSPullRequestParams{
		WorkspaceID: conn.WorkspaceID, ConnectionID: conn.ID, Provider: conn.Provider,
		RepoOwner: ev.RepoOwner, RepoName: ev.RepoName, PrNumber: ev.Number,
		Title: ev.Title, State: ev.State, HtmlUrl: ev.HTMLURL, Branch: ptrToText(strPtrOrNil(ev.Branch)),
		AuthorLogin: ptrToText(strPtrOrNil(ev.AuthorLogin)), AuthorAvatarUrl: ptrToText(strPtrOrNil(ev.AuthorAvatarURL)),
		MergedAt: parseGHTime(ev.MergedAt), ClosedAt: parseGHTime(ev.ClosedAt),
		PrCreatedAt: parseGHTimeRequired(ev.CreatedAt), PrUpdatedAt: incomingUpdatedAt,
		Additions: ev.Additions, Deletions: ev.Deletions, ChangedFiles: ev.ChangedFiles, HeadSha: ev.HeadSHA,
	})
	if err != nil {
		return fmt.Errorf("upsert PR: %w", err)
	}
	if h.PullRequestFactHook != nil {
		h.PullRequestFactHook("vcs", "state_written_before_links")
	}
	if h.PullRequestFactErrorHook != nil {
		if err := h.PullRequestFactErrorHook("vcs", "state_written_before_links"); err != nil {
			return fmt.Errorf("after VCS PR state write: %w", err)
		}
	}
	for _, id := range idents {
		issue, ok := currentIssueByIdent[id]
		if !ok {
			continue
		}
		_, declared := closingIdents[id]
		_, qualifies := qualifyingIdents[id]
		if err := qtx.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
			IssueID: issue.ID, PullRequestID: pr.ID, CloseIntent: declared && !preserveCloseIntent,
			ReferenceOnly: !qualifies, PreserveCloseIntent: preserveCloseIntent,
			LinkedByType: strToText("system"), LinkedByID: pgtype.UUID{},
		}); err != nil {
			return fmt.Errorf("link Issue to PR: %w", err)
		}
	}
	for key, issue := range issueByID {
		if _, stillReferenced := currentIssueIDs[key]; stillReferenced {
			continue
		}
		if err := qtx.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
			IssueID: issue.ID, PullRequestID: pr.ID, CloseIntent: false,
			ReferenceOnly: true, PreserveCloseIntent: preserveCloseIntent,
			LinkedByType: strToText("system"), LinkedByID: pgtype.UUID{},
		}); err != nil {
			return fmt.Errorf("clear removed PR identifier: %w", err)
		}
	}

	var committed []committedPullRequestCompletion
	if ev.State == "merged" || ev.State == "closed" {
		keys := make([]string, 0, len(issueByID))
		for key := range issueByID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, completion, evalErr := h.evaluatePullRequestCompletionLocked(ctx, qtx, issueByID[key], "vcs_pr_terminal", nil, pgtype.UUID{}, "")
			if evalErr != nil {
				return fmt.Errorf("evaluate terminal Issue %s: %w", key, evalErr)
			}
			if completion != nil {
				committed = append(committed, *completion)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PR fact transaction: %w", err)
	}
	for _, completion := range committed {
		if err := h.finalizePullRequestCompletionIntent(ctx, completion.finalization.ID); err != nil {
			slog.Warn("VCS PR completion finalization failed", "error", err, "issue_id", uuidToString(completion.updated.ID))
		}
	}
	linkedIssueIDs := make([]string, 0, len(issueByID))
	for key := range issueByID {
		linkedIssueIDs = append(linkedIssueIDs, key)
	}
	sort.Strings(linkedIssueIDs)
	h.publish(protocol.EventPullRequestUpdated, uuidToString(conn.WorkspaceID), "system", "", map[string]any{
		"pull_request": vcsPullRequestToResponse(pr), "linked_issue_ids": linkedIssueIDs,
	})
	return nil
}

func shouldApplyVCSPullRequestFact(existing db.VcsPullRequest, incomingUpdatedAt pgtype.Timestamptz, incomingState string) bool {
	// Merged is an accepted fact and is absorbing regardless of how a provider
	// timestamps (or fails to timestamp) a later non-merged delivery.
	if existing.State == "merged" && incomingState != "merged" {
		return false
	}
	// Identity locking makes equal provider timestamps deterministic within the
	// whole PR/link transaction. Only strictly older facts are stale.
	if existing.PrUpdatedAt.Valid && incomingUpdatedAt.Valid && incomingUpdatedAt.Time.Before(existing.PrUpdatedAt.Time) {
		return false
	}
	return true
}

func (h *Handler) mirrorVCSCIStatus(ctx context.Context, conn db.VcsConnection, ev vcs.CIStatusEvent) {
	if ev.SHA == "" || ev.State == "" {
		return
	}
	// Use the provider's own event timestamp so UpsertVCSCommitStatus's
	// monotonic guard has something real to compare — writing time.Now() here
	// made the guard always true, so an out-of-order redelivery could regress a
	// status. Falls back to now() only when the payload carried no timestamp.
	if err := h.Queries.UpsertVCSCommitStatus(ctx, db.UpsertVCSCommitStatusParams{
		ConnectionID: conn.ID,
		Sha:          ev.SHA,
		Context:      ev.Context,
		State:        ev.State,
		TargetUrl:    ptrToText(strPtrOrNil(ev.TargetURL)),
		Description:  ptrToText(strPtrOrNil(ev.Description)),
		UpdatedAt:    parseGHTimeRequired(ev.UpdatedAt),
	}); err != nil {
		slog.Warn("vcs: upsert commit status failed", "err", err)
		return
	}

	issueIDs, err := h.Queries.ListIssueIDsForVCSPRHead(ctx, db.ListIssueIDsForVCSPRHeadParams{
		ConnectionID: conn.ID,
		HeadSha:      ev.SHA,
	})
	if err != nil {
		slog.Warn("vcs: lookup issues for status failed", "err", err)
		return
	}
	workspaceID := uuidToString(conn.WorkspaceID)
	for _, issueID := range issueIDs {
		h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
			"issue_id": uuidToString(issueID),
		})
	}
}
