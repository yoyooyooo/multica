package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/sourcechannel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const selfHostSourceChannelBodyLimit = 4 * 1024

// RecordSelfHostSourceChannel receives the anonymous source-channel report
// posted by self-hosted Multica instances. The payload deliberately excludes
// account/profile/workspace data and free-text answers; only a fixed channel
// enum and anonymous dedupe hashes are accepted.
func (h *Handler) RecordSelfHostSourceChannel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, selfHostSourceChannelBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req sourcechannel.Report
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	channel := sourcechannel.NormalizeChannel(req.Channel)
	instanceHash := sourcechannel.NormalizeHash(req.InstanceHash)
	subjectHash := sourcechannel.NormalizeHash(req.SubjectHash)
	if req.SchemaVersion != sourcechannel.SchemaVersion {
		writeError(w, http.StatusBadRequest, "unsupported schema_version")
		return
	}
	if !sourcechannel.ValidChannel(channel) {
		writeError(w, http.StatusBadRequest, "invalid channel")
		return
	}
	if !sourcechannel.ValidHash(instanceHash) {
		writeError(w, http.StatusBadRequest, "invalid instance_hash")
		return
	}
	if !sourcechannel.ValidHash(subjectHash) {
		writeError(w, http.StatusBadRequest, "invalid subject_hash")
		return
	}

	_, err := h.Queries.UpsertSelfHostSourceChannel(r.Context(), db.UpsertSelfHostSourceChannelParams{
		SchemaVersion: int32(req.SchemaVersion),
		Channel:       channel,
		InstanceHash:  instanceHash,
		SubjectHash:   subjectHash,
	})
	if err != nil {
		slog.Warn("self-host source channel upsert failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to record source channel")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
