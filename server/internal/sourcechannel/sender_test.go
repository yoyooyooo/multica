package sourcechannel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeSettingStore struct {
	value string
}

func (f fakeSettingStore) GetOrCreateSystemSetting(context.Context, db.GetOrCreateSystemSettingParams) (string, error) {
	return f.value, nil
}

func TestSenderReportsOnlyChannelAndAnonymousHashes(t *testing.T) {
	got := make(chan Report, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ReportPath {
			t.Errorf("path: want %s, got %s", ReportPath, r.URL.Path)
		}
		var payload Report
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		got <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := MustNewSender(fakeSettingStore{value: strings.Repeat("f", 64)}, SenderConfig{
		APIBaseURL: server.URL,
		Timeout:    time.Second,
	})
	sender.ReportSelfHostSourceChannel("user-123", "Social_GitHub")

	select {
	case payload := <-got:
		if payload.SchemaVersion != SchemaVersion {
			t.Fatalf("schema_version: want %d, got %d", SchemaVersion, payload.SchemaVersion)
		}
		if payload.Channel != "social_github" {
			t.Fatalf("channel: want social_github, got %q", payload.Channel)
		}
		if !ValidHash(payload.InstanceHash) || !ValidHash(payload.SubjectHash) {
			t.Fatalf("hashes should be 64-char lowercase hex: %+v", payload)
		}
		if payload.SubjectHash == "user-123" || strings.Contains(payload.SubjectHash, "user-123") {
			t.Fatalf("subject_hash leaked raw user id: %q", payload.SubjectHash)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source channel report")
	}
}

func TestSenderDropsUnknownChannel(t *testing.T) {
	got := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := MustNewSender(fakeSettingStore{value: strings.Repeat("f", 64)}, SenderConfig{
		APIBaseURL: server.URL,
		Timeout:    50 * time.Millisecond,
	})
	sender.ReportSelfHostSourceChannel("user-123", "private_text")

	select {
	case <-got:
		t.Fatal("unexpected report for invalid channel")
	case <-time.After(100 * time.Millisecond):
	}
}
