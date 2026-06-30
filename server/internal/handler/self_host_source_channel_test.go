package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/sourcechannel"
)

func TestRecordSelfHostSourceChannelUpsertsByAnonymousSubject(t *testing.T) {
	instanceHash := strings.Repeat("a", 64)
	subjectHash := strings.Repeat("b", 64)
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM self_host_source_channel WHERE instance_hash = $1 AND subject_hash = $2`,
			instanceHash,
			subjectHash,
		)
	})

	postSourceReport(t, sourcechannel.Report{
		SchemaVersion: sourcechannel.SchemaVersion,
		Channel:       "social_github",
		InstanceHash:  instanceHash,
		SubjectHash:   subjectHash,
	})
	postSourceReport(t, sourcechannel.Report{
		SchemaVersion: sourcechannel.SchemaVersion,
		Channel:       "search",
		InstanceHash:  instanceHash,
		SubjectHash:   subjectHash,
	})

	var (
		channel     string
		reportCount int
	)
	if err := testPool.QueryRow(context.Background(), `
		SELECT channel, report_count
		  FROM self_host_source_channel
		 WHERE instance_hash = $1 AND subject_hash = $2
	`, instanceHash, subjectHash).Scan(&channel, &reportCount); err != nil {
		t.Fatalf("load source channel row: %v", err)
	}
	if channel != "search" {
		t.Fatalf("channel: want latest value search, got %q", channel)
	}
	if reportCount != 2 {
		t.Fatalf("report_count: want 2, got %d", reportCount)
	}
}

func TestRecordSelfHostSourceChannelRejectsFreeTextFields(t *testing.T) {
	body := []byte(`{
		"schema_version": 1,
		"channel": "other",
		"instance_hash": "` + strings.Repeat("c", 64) + `",
		"subject_hash": "` + strings.Repeat("d", 64) + `",
		"source_other": "private free text"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/acquisition/self-host-source", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	testHandler.RecordSelfHostSourceChannel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown free-text field, got %d: %s", w.Code, w.Body.String())
	}
}

func postSourceReport(t *testing.T, payload sourcechannel.Report) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/acquisition/self-host-source", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	testHandler.RecordSelfHostSourceChannel(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RecordSelfHostSourceChannel: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
