package handler

import (
	"strings"
	"testing"
)

func TestDecodeRerunIssueRequest(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		req, err := decodeRerunIssueRequest(strings.NewReader(""))
		if err != nil {
			t.Fatalf("decode empty body: %v", err)
		}
		if req.TaskID != "" {
			t.Fatalf("TaskID = %q, want empty", req.TaskID)
		}
	})

	t.Run("unknown-length body content", func(t *testing.T) {
		req, err := decodeRerunIssueRequest(strings.NewReader(`{"task_id":"task-123"}`))
		if err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.TaskID != "task-123" {
			t.Fatalf("TaskID = %q, want task-123", req.TaskID)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if _, err := decodeRerunIssueRequest(strings.NewReader(`{"task_id":`)); err == nil {
			t.Fatal("expected invalid JSON error")
		}
	})
}
