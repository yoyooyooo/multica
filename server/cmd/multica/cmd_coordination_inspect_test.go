package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func TestWorkCoordinationInspectExactRequestAndOutputs(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const (
		scopeID      = "00000000-0000-0000-0000-000000000010"
		workspaceID  = "00000000-0000-0000-0000-000000000020"
		rootID       = "00000000-0000-0000-0000-000000000001"
		downstreamID = "00000000-0000-0000-0000-000000000002"
		upstreamID   = "00000000-0000-0000-0000-000000000003"
		dependencyID = "00000000-0000-0000-0000-000000000011"
		blockerID    = "00000000-0000-0000-0000-000000000012"
		receiptID    = "00000000-0000-0000-0000-000000000013"
		actorID      = "00000000-0000-0000-0000-000000000030"
		cursor       = "opaque/+?"
	)
	scopeJSON := `{"id":"` + scopeID + `","workspace_id":"` + workspaceID + `","scope_kind":"root","state":"active","root_issue_id":"` + rootID + `","workflow_profile_key":"matt-loop","revision":2,"created_by":{"actor_type":"member","actor_id":"` + actorID + `","task_id":null},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	dependencyJSON := `{"id":"` + dependencyID + `","workspace_id":"` + workspaceID + `","coordination_scope_id":"` + scopeID + `","downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `","blocks_issue_id":"` + downstreamID + `","created_by":{"actor_type":"member","actor_id":"` + actorID + `","task_id":null},"created_at":"2026-01-01T00:00:00Z","resolved_by":null,"resolved_at":null}`
	blockerJSON := `{"id":"` + blockerID + `","workspace_id":"` + workspaceID + `","scope_id":"` + scopeID + `","kind":"blocker","schema_version":1,"status":"open","root_issue_id":"` + rootID + `","downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `","dependency_id":"` + dependencyID + `","reason_code":"waiting_on_issue","resolution_code":null,"create_evidence_refs":[],"resolution_evidence_refs":[],"created_by":{"type":"member","id":"` + actorID + `","task_id":null},"resolved_by":null,"created_at":"2026-01-01T00:00:00Z","resolved_at":null}`
	responseJSON := `{"scope":` + scopeJSON + `,"scope_revision":2,"active_dependencies":[` + dependencyJSON + `],"open_blockers":[` + blockerJSON + `],"receipt_refs":[{"id":"` + receiptID + `","receipt_ordinal":3,"operation":"append_blocker","resource_type":"blocker","resource_id":"` + blockerID + `","revision_before":1,"revision_after":2,"actor_type":"member","created_at":"2026-01-01T00:00:00Z"}],"next_receipt_cursor":"next-token"}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/coordination/scopes/"+scopeID+"/inspect" || r.URL.Query().Get("receipt_cursor") != cursor {
			t.Errorf("inspect request method=%s path=%s query=%s", r.Method, r.URL.Path, r.URL.RawQuery)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseJSON))
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	t.Setenv("MULTICA_TOKEN", "mul_test")

	cmd := newCoordinationInspectTestCommand()
	_ = cmd.Flags().Set("scope", strings.ToUpper(scopeID))
	_ = cmd.Flags().Set("receipt-cursor", cursor)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	coordinationOutput = "json"
	if err := runCoordinationInspect(cmd, nil); err != nil {
		t.Fatalf("inspect JSON: %v", err)
	}
	if strings.Count(strings.TrimSpace(stdout.String()), "\n") != 0 {
		t.Fatalf("inspect JSON emitted multiple values: %q", stdout.String())
	}
	var response coordinationInspectionCLIResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || response.ScopeRevision != 2 || len(response.ActiveDependencies) != 1 || len(response.OpenBlockers) != 1 || len(response.ReceiptRefs) != 1 || response.NextReceiptCursor == nil || *response.NextReceiptCursor != "next-token" {
		t.Fatalf("typed inspect response=%+v err=%v", response, err)
	}

	stdout.Reset()
	cmd.SetOut(&stdout)
	coordinationOutput = "table"
	if err := runCoordinationInspect(cmd, nil); err != nil {
		t.Fatalf("inspect table: %v", err)
	}
	output := stdout.String()
	for _, fragment := range []string{
		"scope=" + scopeID + " revision=2 active_dependencies=1 open_blockers=1 receipt_refs=1",
		"dependency " + dependencyID,
		"blocker " + blockerID,
		"receipt 3  append_blocker blocker=" + blockerID,
		"next_receipt_cursor=next-token revision=2",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("table output missing %q: %q", fragment, output)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d want=2", requests.Load())
	}
}

func TestWorkCoordinationInspectTopLevelConflictMatrix(t *testing.T) {
	const scopeID = "00000000-0000-0000-0000-000000000010"
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantExit    int
		wantCode    string
	}{
		{name: "revision", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_revision_conflict","message":"safe"}}`, wantExit: 6, wantCode: "coordination_revision_conflict"},
		{name: "capacity", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_capacity_exceeded","message":"safe"}}`, wantExit: 1},
		{name: "idempotency", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_idempotency_conflict","message":"safe"}}`, wantExit: 1},
		{name: "owner", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_dependency_scope_conflict","message":"safe"}}`, wantExit: 1},
		{name: "delete", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_delete_blocked","message":"safe"}}`, wantExit: 1},
		{name: "status mismatch", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_invalid_payload","message":"safe"}}`, wantExit: 1},
		{name: "unknown", status: http.StatusConflict, contentType: "application/json", body: `{"error":{"code":"coordination_unknown","message":"safe"}}`, wantExit: 1},
		{name: "legacy", status: http.StatusConflict, contentType: "text/plain", body: "conflict", wantExit: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resetWorkCoordinationCommandState()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", testCase.contentType)
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			t.Setenv("MULTICA_SERVER_URL", server.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
			t.Setenv("MULTICA_TOKEN", "mul_test")
			var stdout, stderr bytes.Buffer
			args := []string{"--debug", "coordination", "inspect", "--scope", scopeID, "--receipt-cursor", "opaque", "--output=json"}
			if got := executeRoot(args, &stdout, &stderr); got != testCase.wantExit {
				t.Fatalf("exit=%d want=%d stderr=%q", got, testCase.wantExit, stderr.String())
			}
			if stdout.Len() != 0 || requests.Load() != 1 {
				t.Fatalf("stdout=%q requests=%d", stdout.String(), requests.Load())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decoder := json.NewDecoder(strings.NewReader(stderr.String()))
			if err := decoder.Decode(&envelope); err != nil || envelope.Error.Code == "" {
				t.Fatalf("stderr envelope=%+v err=%v raw=%q", envelope, err, stderr.String())
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				t.Fatalf("stderr has trailing output: %q", stderr.String())
			}
			if testCase.wantCode != "" && envelope.Error.Code != testCase.wantCode {
				t.Fatalf("code=%q want=%q", envelope.Error.Code, testCase.wantCode)
			}
		})
	}
	resetWorkCoordinationCommandState()
}

func TestWorkCoordinationInspectLocalValidationMakesZeroRequests(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	cases := []struct {
		scope  string
		cursor string
	}{
		{scope: "not-a-uuid"},
		{scope: "00000000-0000-0000-0000-000000000010", cursor: " cursor"},
		{scope: "00000000-0000-0000-0000-000000000010", cursor: strings.Repeat("x", 3001)},
	}
	for index, testCase := range cases {
		cmd := newCoordinationInspectTestCommand()
		_ = cmd.Flags().Set("scope", testCase.scope)
		_ = cmd.Flags().Set("receipt-cursor", testCase.cursor)
		if err := runCoordinationInspect(cmd, nil); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid inspect inputs made %d requests", requests.Load())
	}
}

func newCoordinationInspectTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "inspect"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("receipt-cursor", "", "")
	return cmd
}
