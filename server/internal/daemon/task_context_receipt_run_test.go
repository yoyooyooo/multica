package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestRunTaskWritesReceiptStateMatrixBeforeBackend(t *testing.T) {
	t.Parallel()

	d, captureFile, cleanup := newTaskReceiptRunTestDaemon(t, false)
	defer cleanup()

	first := taskReceiptRunTestTask("task-first")
	firstResult, err := d.runTask(context.Background(), first, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("first runTask: %v", err)
	}
	if firstResult.SessionID == "" || firstResult.WorkDir == "" {
		t.Fatalf("first result missing resume state: %+v", firstResult)
	}
	assertTaskReceiptOnDisk(t, firstResult.WorkDir, "task-first", "", false, false)
	assertBackendObservedTaskReceipt(t, captureFile, "task-first", "", false, false)

	second := taskReceiptRunTestTask("task-second")
	second.TriggerCommentID = "comment-second"
	second.PriorSessionID = firstResult.SessionID
	second.PriorWorkDir = firstResult.WorkDir
	secondResult, err := d.runTask(context.Background(), second, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("second runTask: %v", err)
	}
	if secondResult.WorkDir != firstResult.WorkDir {
		t.Fatalf("second WorkDir = %q, want reused workdir %q", secondResult.WorkDir, firstResult.WorkDir)
	}
	assertTaskReceiptOnDisk(t, secondResult.WorkDir, "task-second", "comment-second", true, true)
	assertBackendObservedTaskReceipt(t, captureFile, "task-second", "comment-second", true, true)

	third := taskReceiptRunTestTask("task-third")
	third.TriggerCommentID = "comment-third"
	third.PriorWorkDir = firstResult.WorkDir
	thirdResult, err := d.runTask(context.Background(), third, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("third runTask: %v", err)
	}
	if thirdResult.WorkDir != firstResult.WorkDir {
		t.Fatalf("third WorkDir = %q, want reused workdir %q", thirdResult.WorkDir, firstResult.WorkDir)
	}
	assertTaskReceiptOnDisk(t, thirdResult.WorkDir, "task-third", "comment-third", false, true)
	assertBackendObservedTaskReceipt(t, captureFile, "task-third", "comment-third", false, true)
}

func TestRunTaskResumeFallbackKeepsConservativeReceipt(t *testing.T) {
	t.Parallel()

	d, captureFile, cleanup := newTaskReceiptRunTestDaemon(t, true)
	defer cleanup()

	first := taskReceiptRunTestTask("task-before-fallback")
	firstResult, err := d.runTask(context.Background(), first, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("first runTask: %v", err)
	}

	second := taskReceiptRunTestTask("task-resume-fallback")
	second.TriggerCommentID = "comment-resume-fallback"
	second.PriorSessionID = firstResult.SessionID
	second.PriorWorkDir = firstResult.WorkDir
	secondResult, err := d.runTask(context.Background(), second, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("fallback runTask: %v", err)
	}
	if secondResult.Status != "completed" || secondResult.SessionID == "" {
		t.Fatalf("fallback result = %+v, want completed fresh session", secondResult)
	}

	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read fallback capture: %v", err)
	}
	capture := string(data)
	if got := assertBackendObservedTaskReceipt(t, captureFile, "task-resume-fallback", "comment-resume-fallback", true, true); got != 2 {
		t.Fatalf("fallback backend observed matching task receipt %d times, want 2; capture:\n%s", got, capture)
	}
	if !strings.Contains(capture, "--has-resume=yes") || !strings.Contains(capture, "--has-resume=no") {
		t.Fatalf("capture does not prove resumed first launch plus fresh fallback:\n%s", capture)
	}
	assertTaskReceiptOnDisk(t, secondResult.WorkDir, "task-resume-fallback", "comment-resume-fallback", true, true)
}

func TestRunTaskFreshCommentWritesReceiptBeforeBackend(t *testing.T) {
	t.Parallel()

	d, captureFile, cleanup := newTaskReceiptRunTestDaemon(t, false)
	defer cleanup()

	task := taskReceiptRunTestTask("task-fresh-comment")
	task.TriggerCommentID = "comment-fresh"
	result, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	assertTaskReceiptOnDisk(t, result.WorkDir, "task-fresh-comment", "comment-fresh", false, false)
	assertBackendObservedTaskReceipt(t, captureFile, "task-fresh-comment", "comment-fresh", false, false)
}

func TestRunTaskDropsResumeWhenPriorWorkdirMissing(t *testing.T) {
	t.Parallel()

	d, captureFile, cleanup := newTaskReceiptRunTestDaemon(t, false)
	defer cleanup()

	missingWorkDir := filepath.Join(t.TempDir(), "missing", "workdir")
	task := taskReceiptRunTestTask("task-missing-workdir")
	task.PriorSessionID = "session-task-receipt"
	task.PriorWorkDir = missingWorkDir

	result, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if result.WorkDir == missingWorkDir {
		t.Fatalf("runTask reused missing prior workdir %q", missingWorkDir)
	}
	assertTaskReceiptOnDisk(t, result.WorkDir, "task-missing-workdir", "", false, false)
	assertBackendObservedTaskReceipt(t, captureFile, "task-missing-workdir", "", false, false)
	capture, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read missing-workdir capture: %v", err)
	}
	if !strings.Contains(string(capture), "--has-resume=no") || strings.Contains(string(capture), "--has-resume=yes") {
		t.Fatalf("missing-workdir backend received a resume argument; capture:\n%s", capture)
	}
}

func assertTaskReceiptOnDisk(t *testing.T, workDir, taskID, triggerCommentID string, resumeSession, reuseWorkdir bool) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(workDir, execenv.TaskContextMarkerRelPath))
	if err != nil {
		t.Fatalf("read daemon task receipt: %v", err)
	}
	var receipt struct {
		Schema           string `json:"schema"`
		ManagedBy        string `json:"managed_by"`
		TaskID           string `json:"task_id"`
		AgentID          string `json:"agent_id"`
		IssueID          string `json:"issue_id"`
		TriggerCommentID string `json:"trigger_comment_id"`
		ResumeSession    bool   `json:"resume_session"`
		ReuseWorkdir     bool   `json:"reuse_workdir"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("unmarshal daemon task receipt: %v\n%s", err, string(data))
	}
	if receipt.Schema != execenv.TaskContextReceiptSchema || receipt.ManagedBy != execenv.TaskContextMarkerManagedBy {
		t.Fatalf("receipt authority = schema %q managed_by %q", receipt.Schema, receipt.ManagedBy)
	}
	if receipt.TaskID != taskID || receipt.AgentID != "agent-task-receipt" || receipt.IssueID != "issue-task-receipt" {
		t.Fatalf("receipt provenance = task %q agent %q issue %q", receipt.TaskID, receipt.AgentID, receipt.IssueID)
	}
	if receipt.TriggerCommentID != triggerCommentID || receipt.ResumeSession != resumeSession || receipt.ReuseWorkdir != reuseWorkdir {
		t.Fatalf("receipt execution = trigger %q resume %t reuse %t; want trigger %q resume %t reuse %t", receipt.TriggerCommentID, receipt.ResumeSession, receipt.ReuseWorkdir, triggerCommentID, resumeSession, reuseWorkdir)
	}
}

func assertBackendObservedTaskReceipt(t *testing.T, captureFile, taskID, triggerCommentID string, resumeSession, reuseWorkdir bool) int {
	t.Helper()

	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read backend receipt capture: %v", err)
	}
	found := 0
	for _, chunk := range strings.Split(string(data), "--receipt-start--\n")[1:] {
		end := strings.Index(chunk, "\n--invocation-end--")
		if end < 0 {
			t.Fatalf("backend receipt capture is missing invocation terminator:\n%s", data)
		}
		var receipt struct {
			Schema           string `json:"schema"`
			ManagedBy        string `json:"managed_by"`
			TaskID           string `json:"task_id"`
			TriggerCommentID string `json:"trigger_comment_id"`
			ResumeSession    bool   `json:"resume_session"`
			ReuseWorkdir     bool   `json:"reuse_workdir"`
		}
		if err := json.Unmarshal([]byte(chunk[:end]), &receipt); err != nil {
			t.Fatalf("unmarshal backend-observed daemon task receipt: %v\n%s", err, chunk[:end])
		}
		if receipt.TaskID != taskID {
			continue
		}
		found++
		if receipt.Schema != execenv.TaskContextReceiptSchema || receipt.ManagedBy != execenv.TaskContextMarkerManagedBy ||
			receipt.TriggerCommentID != triggerCommentID || receipt.ResumeSession != resumeSession || receipt.ReuseWorkdir != reuseWorkdir {
			t.Fatalf("backend-observed receipt for task %q = schema %q managed_by %q trigger %q resume %t reuse %t; want schema %q managed_by %q trigger %q resume %t reuse %t", taskID, receipt.Schema, receipt.ManagedBy, receipt.TriggerCommentID, receipt.ResumeSession, receipt.ReuseWorkdir, execenv.TaskContextReceiptSchema, execenv.TaskContextMarkerManagedBy, triggerCommentID, resumeSession, reuseWorkdir)
		}
	}
	if found == 0 {
		t.Fatalf("backend did not observe receipt for task %q before launch; capture:\n%s", taskID, data)
	}
	return found
}

func newTaskReceiptRunTestDaemon(t *testing.T, failResume bool) (*Daemon, string, func()) {
	t.Helper()

	testDir := t.TempDir()
	fakeBin := filepath.Join(testDir, "claude")
	captureFile := filepath.Join(testDir, "claude-capture.txt")
	failResumeValue := "no"
	if failResume {
		failResumeValue = "yes"
	}
	script := `#!/bin/sh
has_resume=no
for arg in "$@"; do
  if [ "$arg" = "--resume" ]; then
    has_resume=yes
  fi
done
printf '%s\n' "$@" >> "` + captureFile + `"
printf '%s\n' "--has-resume=$has_resume" >> "` + captureFile + `"
printf '%s\n' '--receipt-start--' >> "` + captureFile + `"
if ! cat "$PWD/.multica/daemon_task_context.json" >> "` + captureFile + `"; then
  exit 97
fi
printf '\n%s\n' '--invocation-end--' >> "` + captureFile + `"
IFS= read -r _
if [ "` + failResumeValue + `" = "yes" ] && [ "$has_resume" = "yes" ]; then
  printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"resume failed"}'
  exit 0
fi
printf '%s\n' '{"type":"system","session_id":"session-task-receipt"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"session-task-receipt","result":"done"}'
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         logger,
		workspaces:     make(map[string]*workspaceState),
		runtimeIndex:   map[string]Runtime{"rt-task-receipt": {ID: "rt-task-receipt", Provider: "claude"}},
		activeEnvRoots: make(map[string]int),
		cfg: Config{
			WorkspacesRoot: t.TempDir(),
			AgentTimeout:   5 * time.Second,
			ServerBaseURL:  srv.URL,
			Agents: map[string]AgentEntry{
				"claude": {Path: fakeBin},
			},
		},
	}
	return d, captureFile, srv.Close
}

func taskReceiptRunTestTask(id string) Task {
	return Task{
		ID:          id,
		WorkspaceID: "ws-task-receipt",
		RuntimeID:   "rt-task-receipt",
		IssueID:     "issue-task-receipt",
		AgentID:     "agent-task-receipt",
		AuthToken:   "mat_task_receipt",
		Agent: &AgentData{
			ID:   "agent-task-receipt",
			Name: "task-receipt-agent",
		},
	}
}
