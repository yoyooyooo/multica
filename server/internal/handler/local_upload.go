package handler

import (
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/storage"
)

// ServeLocalUpload returns an http.HandlerFunc that authorizes a request for
// /uploads/<key> and then delegates to the LocalStorage's ServeFile.
//
// The route is normally registered behind middleware.Auth, so by the time
// this handler runs the caller already has a valid session/PAT and X-User-ID
// is set. This handler enforces the second-layer access check: the user
// must actually be entitled to read the bytes at the requested key.
//
// The key layout follows handler.UploadFile:
//
//   - workspaces/{workspaceID}/{filename}  → requires membership in workspaceID
//   - users/{userID}/{filename}            → requires the caller to be authenticated
//     (any logged-in user; this path is used for avatars and similar
//     user-scoped assets that can legitimately be referenced from
//     other-workspace surfaces, e.g. the member list)
//
// Anything that doesn't match those two prefixes is rejected with 404 — we
// don't want a future feature to accidentally drop content under
// /uploads/<some-other-prefix>/ and inherit the relaxed policy.
//
// The disclosure (security-findings-2026-06-02) called out that /uploads/*
// being unauthenticated was one of the layers that made the SVG-XSS chain
// weaponizable end-to-end (directory listing leaked UUID filenames; this
// handler cuts off both the listing and the unauthenticated read).
func (h *Handler) ServeLocalUpload(local *storage.LocalStorage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := requireUserID(w, r)
		if !ok {
			return
		}

		key := strings.TrimPrefix(r.URL.Path, "/uploads/")
		// Reject empty / directory-style paths up front. The storage
		// layer will also catch this, but rejecting here means the
		// 404 carries the same shape as the unrelated 404s on
		// non-existent keys instead of leaking that the directory
		// existed.
		if key == "" || strings.HasSuffix(key, "/") {
			http.NotFound(w, r)
			return
		}

		switch {
		case strings.HasPrefix(key, "workspaces/"):
			rest := strings.TrimPrefix(key, "workspaces/")
			slash := strings.Index(rest, "/")
			if slash <= 0 {
				// "workspaces/" or "workspaces/<id>" with no file —
				// directory listing in disguise.
				http.NotFound(w, r)
				return
			}
			workspaceID := rest[:slash]
			if !h.canReadWorkspaceUpload(r, userID, workspaceID) {
				// 404 rather than 403 so the absence of a workspace
				// and the lack of membership look identical from the
				// outside — denies the IDOR oracle.
				http.NotFound(w, r)
				return
			}

		case strings.HasPrefix(key, "users/"):
			// Avatars and similar user-scoped assets. Any
			// authenticated user can read these — they're routinely
			// embedded in cross-workspace surfaces (member lists,
			// inbox items, mention chips). The auth gate above is
			// the access boundary; we don't gate on userID match.

		default:
			// Unknown prefix — don't serve. New upload key shapes
			// must opt in here explicitly so they can't inherit a
			// relaxed policy by accident.
			http.NotFound(w, r)
			return
		}

		local.ServeFile(w, r, key)
	}
}

// canReadWorkspaceUpload returns true when the user is a member of the
// workspace whose ID is embedded in a /uploads/workspaces/{id}/* path.
// Uses the membership cache when available so every image fetch on a
// busy issue page doesn't hit Postgres. The cache itself nil-handles,
// so the explicit checks below are only for the empty-string inputs.
func (h *Handler) canReadWorkspaceUpload(r *http.Request, userID, workspaceID string) bool {
	if workspaceID == "" || userID == "" {
		return false
	}
	if h.MembershipCache.Get(r.Context(), userID, workspaceID) {
		return true
	}
	if _, err := h.getWorkspaceMember(r.Context(), userID, workspaceID); err != nil {
		return false
	}
	h.MembershipCache.Set(r.Context(), userID, workspaceID)
	return true
}
