package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func TestWorkCoordinationOutputArgMatrix(t *testing.T) {
	valid := [][]string{
		{"coordination", "--output", "json", "scope", "get", "--scope", "x"},
		{"coordination", "scope", "--output=table", "get", "--scope", "x"},
		{"coordination", "scope", "get", "--scope", "x", "--output", "json"},
		{"coordination", "scope", "get", "--scope", "--output"},
		{"coordination", "scope", "ensure", "--root=abc--output=json", "--workflow-profile", "--output=json", "--idempotency-key", "x"},
		{"coordination", "scope", "get", "--scope", "x", "--server-url", "--output=json"},
		{"coordination", "dependency", "add", "--scope", "x", "--expected-revision", "--output", "--idempotency-key", "key", "--downstream", "a", "--upstream", "b"},
		{"coordination", "dependency", "list", "--help"},
		{"issue", "list", "--output", "anything"},
		{"--profile", "coordination", "issue", "list", "--output", "anything"},
		{"--profile=coordination", "issue", "list", "--output", "anything"},
		{"coordination", "scope", "get", "--", "--output", "bad"},
		{"coordination", "scope", "get", "--help"},
		{"coordination", "-h"},
	}
	for _, args := range valid {
		if err := prepareCoordinationArgs(args); err != nil {
			t.Fatalf("valid args %v: %v", args, err)
		}
	}
	invalid := [][]string{
		{"coordination", "--output"},
		{"coordination", "--output="},
		{"coordination", "--output", "yaml"},
		{"coordination", "--output", "json", "scope", "--output=table", "get"},
		{"coordination", "scope", "--output=json", "get", "--output=json"},
		{"coordination", "scope", "get", "--scope", "x", "--bogus", "table"},
		{"coordination", "scope", "get", "--scope"},
		{"coordination", "dependency", "resolve", "--expected-revision"},
	}
	for _, args := range invalid {
		err := prepareCoordinationArgs(args)
		if err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
		if got := err.Error(); !strings.Contains(got, "coordination_invalid_payload") {
			t.Fatalf("invalid args %v returned %q", args, got)
		}
	}
}

func TestWorkCoordinationHelpSurvivesOutputPreParser(t *testing.T) {
	for _, args := range [][]string{
		{"coordination", "scope", "get", "--help"},
		{"coordination", "scope", "get", "-h"},
	} {
		resetWorkCoordinationCommandState()
		var stdout, stderr bytes.Buffer
		if got := executeRoot(args, &stdout, &stderr); got != 0 {
			t.Fatalf("args=%v exit=%d stderr=%q", args, got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "USAGE") || strings.Contains(stdout.String(), "coordination_invalid_payload") || stderr.Len() != 0 {
			t.Fatalf("args=%v stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
	resetWorkCoordinationCommandState()
}

func TestWorkCoordinationEnsureExactRequest(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const rootID = "00000000-0000-0000-0000-000000000001"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+rootID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + rootID + `","identifier":"MUL-1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes":
			if got := r.Header.Get("Idempotency-Key"); got != "ensure-key" {
				t.Errorf("idempotency key=%q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if len(body) != 2 || body["root_issue_id"] != rootID || body["workflow_profile_key"] != "matt-loop" {
				t.Errorf("unexpected body: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"scope":{"id":"00000000-0000-0000-0000-000000000010","workspace_id":"00000000-0000-0000-0000-000000000020","scope_kind":"root","state":"active","root_issue_id":"` + rootID + `","workflow_profile_key":"matt-loop","revision":0,"created_by":{"actor_type":"member","actor_id":"00000000-0000-0000-0000-000000000030","task_id":null},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},"receipt":{"id":"00000000-0000-0000-0000-000000000040","receipt_ordinal":1,"operation":"ensure_scope","resource_type":"scope","resource_id":"00000000-0000-0000-0000-000000000010","revision_before":0,"revision_after":0,"created_at":"2026-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")

	cmd := newCoordinationEnsureTestCommand()
	if err := cmd.Flags().Set("root", rootID); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("workflow-profile", "matt-loop"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("idempotency-key", "ensure-key"); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	coordinationOutput = "json"
	if err := runCoordinationScopeEnsure(cmd, nil); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d, want 2", requests.Load())
	}
	if !strings.Contains(stdout.String(), `"receipt_ordinal":1`) || strings.Contains(stdout.String(), `"outcome"`) {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestWorkCoordinationGetExactRequestsAndTableOutput(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const (
		scopeID = "00000000-0000-0000-0000-000000000010"
		rootID  = "00000000-0000-0000-0000-000000000001"
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-1":
			_, _ = w.Write([]byte(`{"id":"` + rootID + `","identifier":"MUL-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/coordination/scopes/by-root":
			if r.URL.Query().Get("root_issue_id") != rootID || r.URL.Query().Get("workflow_profile_key") != "matt-loop" {
				t.Errorf("unexpected query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"scope":{"id":"` + scopeID + `","workspace_id":"00000000-0000-0000-0000-000000000020","scope_kind":"root","state":"active","root_issue_id":"` + rootID + `","workflow_profile_key":"matt-loop","revision":0,"created_by":{"actor_type":"member","actor_id":"00000000-0000-0000-0000-000000000030","task_id":null},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	cmd := newCoordinationGetTestCommand()
	_ = cmd.Flags().Set("root", "MUL-1")
	_ = cmd.Flags().Set("workflow-profile", "matt-loop")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	coordinationOutput = "table"
	if err := runCoordinationScopeGet(cmd, nil); err != nil {
		t.Fatalf("get by root: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d want=2", requests.Load())
	}
	if got := stdout.String(); got != "scope="+scopeID+" root="+rootID+" profile=matt-loop revision=0 state=active\n" {
		t.Fatalf("table output=%q", got)
	}
}

func TestWorkCoordinationDependencyExactRequestsAndOutputs(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const (
		scopeID      = "00000000-0000-0000-0000-000000000010"
		dependencyID = "00000000-0000-0000-0000-000000000011"
		downstreamID = "00000000-0000-0000-0000-000000000002"
		upstreamID   = "00000000-0000-0000-0000-000000000003"
		workspaceID  = "00000000-0000-0000-0000-000000000020"
		actorID      = "00000000-0000-0000-0000-000000000030"
		receiptID    = "00000000-0000-0000-0000-000000000040"
	)
	dependencyJSON := `{"id":"` + dependencyID + `","workspace_id":"` + workspaceID + `","coordination_scope_id":"` + scopeID + `","downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `","blocks_issue_id":"` + downstreamID + `","created_by":{"actor_type":"member","actor_id":"` + actorID + `","task_id":null},"created_at":"2026-01-01T00:00:00Z","resolved_by":null,"resolved_at":null}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-2":
			_, _ = w.Write([]byte(`{"id":"` + downstreamID + `","identifier":"MUL-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-3":
			_, _ = w.Write([]byte(`{"id":"` + upstreamID + `","identifier":"MUL-3"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/dependencies":
			if r.Header.Get("Idempotency-Key") != "dependency-add-key" {
				t.Errorf("add idempotency key=%q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 3 || body["expected_revision"] != float64(0) || body["downstream_issue_id"] != downstreamID || body["upstream_issue_id"] != upstreamID {
				t.Errorf("add body=%#v err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"dependency":` + dependencyJSON + `,"scope_revision":1,"receipt":{"id":"` + receiptID + `","receipt_ordinal":2,"operation":"add_dependency","resource_type":"dependency","resource_id":"` + dependencyID + `","revision_before":0,"revision_after":1,"created_at":"2026-01-01T00:00:00Z"},"outcome":"created"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/dependencies":
			if r.URL.Query().Get("cursor") != "opaque cursor/+?" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("list query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"dependencies":[` + dependencyJSON + `],"scope_revision":1,"next_cursor":"next-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/dependencies/"+dependencyID+"/resolve":
			if r.Header.Get("Idempotency-Key") != "dependency-resolve-key" {
				t.Errorf("resolve idempotency key=%q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["expected_revision"] != float64(1) {
				t.Errorf("resolve body=%#v err=%v", body, err)
			}
			_, _ = w.Write([]byte(`{"dependency":` + dependencyJSON + `,"scope_revision":2,"receipt":{"id":"` + receiptID + `","receipt_ordinal":3,"operation":"resolve_dependency","resource_type":"dependency","resource_id":"` + dependencyID + `","revision_before":1,"revision_after":2,"created_at":"2026-01-01T00:00:00Z"},"outcome":"resolved"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	t.Setenv("MULTICA_TOKEN", "mul_test")

	add := newCoordinationDependencyAddTestCommand()
	_ = add.Flags().Set("scope", scopeID)
	_ = add.Flags().Set("downstream", "MUL-2")
	_ = add.Flags().Set("upstream", "MUL-3")
	_ = add.Flags().Set("expected-revision", "000")
	_ = add.Flags().Set("idempotency-key", "dependency-add-key")
	var stdout bytes.Buffer
	add.SetOut(&stdout)
	coordinationOutput = "json"
	if err := runCoordinationDependencyAdd(add, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, `"outcome":"created"`) || strings.Count(strings.TrimSpace(got), "\n") != 0 {
		t.Fatalf("add output=%q", got)
	}

	list := newCoordinationDependencyListTestCommand()
	_ = list.Flags().Set("scope", scopeID)
	_ = list.Flags().Set("cursor", "opaque cursor/+?")
	_ = list.Flags().Set("limit", "2")
	stdout.Reset()
	list.SetOut(&stdout)
	coordinationOutput = "table"
	if err := runCoordinationDependencyList(list, nil); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := stdout.String(); got != dependencyID+"  "+downstreamID+" blocked_by "+upstreamID+"\nnext_cursor=next-token revision=1\n" {
		t.Fatalf("list output=%q", got)
	}

	resolve := newCoordinationDependencyResolveTestCommand()
	_ = resolve.Flags().Set("scope", scopeID)
	_ = resolve.Flags().Set("dependency", dependencyID)
	_ = resolve.Flags().Set("expected-revision", "1")
	_ = resolve.Flags().Set("idempotency-key", "dependency-resolve-key")
	stdout.Reset()
	resolve.SetOut(&stdout)
	coordinationOutput = "table"
	if err := runCoordinationDependencyResolve(resolve, nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "outcome=resolved revision=2") {
		t.Fatalf("resolve output=%q", got)
	}
	if requests.Load() != 5 {
		t.Fatalf("requests=%d want=5", requests.Load())
	}
}

func TestWorkCoordinationTopLevelExitAndJSONFailureMatrix(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const scopeID = "00000000-0000-0000-0000-000000000010"
	tests := []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantExit   int
		debug      bool
		table      bool
		validation bool
		parseError bool
	}{
		{name: "generic", status: http.StatusConflict, body: `{"error":{"code":"unknown","message":"safe"}}`, wantCode: "coordination_internal", wantExit: 1},
		{name: "auth", status: http.StatusForbidden, body: `{"error":{"code":"coordination_forbidden","message":"forbidden"}}`, wantCode: "coordination_forbidden", wantExit: 3},
		{name: "not-found", status: http.StatusNotFound, body: `{"error":{"code":"coordination_not_found","message":"missing"}}`, wantCode: "coordination_not_found", wantExit: 4},
		{name: "validation", wantCode: "coordination_invalid_payload", wantExit: 5, validation: true},
		{name: "parse-error", wantCode: "coordination_invalid_payload", wantExit: 5, parseError: true},
		{name: "capacity-on-v1-get", status: http.StatusConflict, body: `{"error":{"code":"coordination_capacity_exceeded","message":"conflict"}}`, wantCode: "coordination_internal", wantExit: 1},
		{name: "revision-on-v1-get", status: http.StatusConflict, body: `{"error":{"code":"coordination_revision_conflict","message":"conflict"}}`, wantCode: "coordination_internal", wantExit: 1},
		{name: "idempotency-on-v1-get", status: http.StatusConflict, body: `{"error":{"code":"coordination_idempotency_conflict","message":"conflict"}}`, wantCode: "coordination_internal", wantExit: 1},
		{name: "dependency-scope-on-v1-get", status: http.StatusConflict, body: `{"error":{"code":"coordination_dependency_scope_conflict","message":"conflict"}}`, wantCode: "coordination_internal", wantExit: 1},
		{name: "delete-blocked-on-v1-get-debug", status: http.StatusConflict, body: `{"error":{"code":"coordination_delete_blocked","message":"conflict"}}`, wantCode: "coordination_internal", wantExit: 1, debug: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/coordination/scopes/"+scopeID {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			t.Setenv("MULTICA_SERVER_URL", server.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
			t.Setenv("MULTICA_TOKEN", "mul_test")
			debugFlag = false
			coordinationOutput = "json"
			_ = rootCmd.PersistentFlags().Set("debug", "false")
			_ = coordinationCmd.PersistentFlags().Set("output", "json")
			args := []string{"coordination", "scope", "get", "--scope", scopeID, "--output=json"}
			if tt.table {
				args[len(args)-1] = "--output=table"
			}
			if tt.validation {
				args = []string{"coordination", "scope", "get", "--scope", scopeID, "--output=yaml"}
			} else if tt.parseError {
				args = []string{"coordination", "scope", "get", "--scope", scopeID, "--bogus", "--output=json"}
			} else if tt.debug {
				args = append([]string{"--debug"}, args...)
			}
			var stdout, stderr bytes.Buffer
			if got := executeRoot(args, &stdout, &stderr); got != tt.wantExit {
				t.Fatalf("exit=%d want=%d stderr=%s", got, tt.wantExit, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("failure stdout=%q", stdout.String())
			}
			decoder := json.NewDecoder(strings.NewReader(stderr.String()))
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatalf("stderr is not one JSON envelope: %q: %v", stderr.String(), err)
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				t.Fatalf("stderr has trailing value: %q err=%v", stderr.String(), err)
			}
			if envelope.Error.Code != tt.wantCode || envelope.Error.Message == "" {
				t.Fatalf("envelope=%+v", envelope)
			}
			wantRequests := int32(1)
			if tt.validation || tt.parseError {
				wantRequests = 0
			}
			if requests.Load() != wantRequests {
				t.Fatalf("requests=%d want=%d", requests.Load(), wantRequests)
			}
		})
	}
}

func TestWorkCoordinationScopePostConflictAndTableErrorRendering(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const rootID = "00000000-0000-0000-0000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+rootID:
			_, _ = w.Write([]byte(`{"id":"` + rootID + `","identifier":"MUL-1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"coordination_idempotency_conflict","message":"safe conflict"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/coordination/scopes/"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"unknown","message":"safe"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")

	var stdout, stderr bytes.Buffer
	args := []string{"coordination", "scope", "ensure", "--root", rootID, "--workflow-profile", "matt-loop", "--idempotency-key", "ensure-key", "--output=json"}
	if got := executeRoot(args, &stdout, &stderr); got != 6 {
		t.Fatalf("scope post exit=%d stderr=%s", got, stderr.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil || envelope.Error.Code != "coordination_idempotency_conflict" {
		t.Fatalf("scope post stderr=%q envelope=%+v err=%v", stderr.String(), envelope, err)
	}

	resetWorkCoordinationCommandState()
	stdout.Reset()
	stderr.Reset()
	args = []string{"coordination", "scope", "get", "--scope", "00000000-0000-0000-0000-000000000010", "--output=table"}
	if got := executeRoot(args, &stdout, &stderr); got != 1 {
		t.Fatalf("table exit=%d stderr=%s", got, stderr.String())
	}
	if stdout.Len() != 0 || strings.HasPrefix(strings.TrimSpace(stderr.String()), "{") || strings.Contains(stderr.String(), "[debug]") {
		t.Fatalf("unsafe table rendering stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWorkCoordinationDependencyConflictExitAndSingleEnvelope(t *testing.T) {
	const (
		scopeID      = "00000000-0000-0000-0000-000000000010"
		downstreamID = "00000000-0000-0000-0000-000000000002"
		upstreamID   = "00000000-0000-0000-0000-000000000003"
	)
	for _, tc := range []struct {
		code   string
		status int
		exit   int
	}{
		{"coordination_capacity_exceeded", http.StatusConflict, 6},
		{"coordination_revision_conflict", http.StatusConflict, 6},
		{"coordination_idempotency_conflict", http.StatusConflict, 6},
		{"coordination_dependency_scope_conflict", http.StatusConflict, 6},
		{"coordination_self_dependency", http.StatusUnprocessableEntity, 5},
		{"coordination_cycle", http.StatusUnprocessableEntity, 5},
	} {
		t.Run(tc.code, func(t *testing.T) {
			resetWorkCoordinationCommandState()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-2":
					_, _ = w.Write([]byte(`{"id":"` + downstreamID + `","identifier":"MUL-2"}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-3":
					_, _ = w.Write([]byte(`{"id":"` + upstreamID + `","identifier":"MUL-3"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/dependencies":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"error":{"code":"` + tc.code + `","message":"safe"}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			t.Setenv("MULTICA_SERVER_URL", server.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
			t.Setenv("MULTICA_TOKEN", "mul_test")
			args := []string{"coordination", "dependency", "add", "--scope", scopeID, "--downstream", "MUL-2", "--upstream", "MUL-3", "--expected-revision", "0", "--idempotency-key", "key", "--output=json"}
			var stdout, stderr bytes.Buffer
			if got := executeRoot(args, &stdout, &stderr); got != tc.exit {
				t.Fatalf("exit=%d want=%d stderr=%q", got, tc.exit, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout=%q", stdout.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decoder := json.NewDecoder(strings.NewReader(stderr.String()))
			if err := decoder.Decode(&envelope); err != nil || envelope.Error.Code != tc.code {
				t.Fatalf("envelope=%+v err=%v raw=%q", envelope, err, stderr.String())
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				t.Fatalf("trailing stderr=%q", stderr.String())
			}
		})
	}
	resetWorkCoordinationCommandState()
}

func TestWorkCoordinationDependencyTableConflictUsesSafeProse(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const scopeID = "00000000-0000-0000-0000-000000000010"
	const downstreamID = "00000000-0000-0000-0000-000000000002"
	const upstreamID = "00000000-0000-0000-0000-000000000003"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/issues/MUL-2":
			_, _ = w.Write([]byte(`{"id":"` + downstreamID + `","identifier":"MUL-2"}`))
		case "/api/issues/MUL-3":
			_, _ = w.Write([]byte(`{"id":"` + upstreamID + `","identifier":"MUL-3"}`))
		case "/api/coordination/scopes/" + scopeID + "/dependencies":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"coordination_revision_conflict","message":"safe conflict"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	args := []string{"coordination", "dependency", "add", "--scope", scopeID, "--downstream", "MUL-2", "--upstream", "MUL-3", "--expected-revision", "0", "--idempotency-key", "key", "--output=table"}
	var stdout, stderr bytes.Buffer
	if got := executeRoot(args, &stdout, &stderr); got != 6 {
		t.Fatalf("exit=%d stderr=%q", got, stderr.String())
	}
	if stdout.Len() != 0 || strings.HasPrefix(strings.TrimSpace(stderr.String()), "{") || !strings.Contains(stderr.String(), "safe conflict") || strings.Contains(strings.ToLower(stderr.String()), "sql") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWorkCoordinationSubprocessPreservesExitAndSingleEnvelope(t *testing.T) {
	const (
		scopeID = "00000000-0000-0000-0000-000000000010"
		rootID  = "00000000-0000-0000-0000-000000000001"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+rootID {
			_, _ = w.Write([]byte(`{"id":"` + rootID + `","identifier":"MUL-1"}`))
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"coordination_idempotency_conflict","message":"safe conflict"}}`))
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")

	for _, tc := range []struct {
		name     string
		args     []string
		wantExit int
		wantCode string
	}{
		{name: "strict conflict debug", args: []string{"--debug", "coordination", "scope", "ensure", "--root", rootID, "--workflow-profile", "matt-loop", "--idempotency-key", "subprocess-key", "--output=json"}, wantExit: 6, wantCode: "coordination_idempotency_conflict"},
		{name: "preparse validation", args: []string{"coordination", "scope", "get", "--scope", scopeID, "--output=yaml"}, wantExit: 5, wantCode: "coordination_invalid_payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-test.run=^TestWorkCoordinationCLIHelperProcess$", "--", "--server-url", server.URL, "--workspace-id", "00000000-0000-0000-0000-000000000020"}
			args = append(args, tc.args...)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "MULTICA_COORDINATION_HELPER=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.wantExit {
				t.Fatalf("subprocess err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("subprocess failure stdout=%q", stdout.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decoder := json.NewDecoder(strings.NewReader(stderr.String()))
			if err := decoder.Decode(&envelope); err != nil || envelope.Error.Code != tc.wantCode {
				t.Fatalf("subprocess envelope=%+v err=%v raw=%q", envelope, err, stderr.String())
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				t.Fatalf("subprocess stderr has trailing output: %q", stderr.String())
			}
		})
	}
}

func TestWorkCoordinationCLIHelperProcess(t *testing.T) {
	if os.Getenv("MULTICA_COORDINATION_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(99)
	}
	os.Exit(executeRoot(os.Args[separator+1:], os.Stdout, os.Stderr))
}

func TestWorkCoordinationValidationMakesZeroRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	ensureCases := []struct {
		root, profile, key string
	}{
		{},
		{root: " MUL-1 ", profile: "matt-loop", key: "key"},
		{root: "MUL-1", profile: "Matt-Loop", key: "key"},
		{root: "MUL-1", profile: "matt-loop", key: " key "},
		{root: "MUL-1", profile: "matt-loop", key: strings.Repeat("k", 201)},
	}
	for i, tc := range ensureCases {
		cmd := newCoordinationEnsureTestCommand()
		_ = cmd.Flags().Set("root", tc.root)
		_ = cmd.Flags().Set("workflow-profile", tc.profile)
		_ = cmd.Flags().Set("idempotency-key", tc.key)
		if err := runCoordinationScopeEnsure(cmd, nil); err == nil {
			t.Fatalf("ensure validation case %d accepted", i)
		}
	}
	getCases := []struct {
		scope, root, profile string
	}{
		{},
		{scope: "not-a-uuid"},
		{scope: "00000000-0000-0000-0000-000000000010", root: "MUL-1", profile: "matt-loop"},
		{root: "MUL-1"},
		{root: "MUL-1", profile: "Matt-Loop"},
	}
	for i, tc := range getCases {
		cmd := newCoordinationGetTestCommand()
		_ = cmd.Flags().Set("scope", tc.scope)
		_ = cmd.Flags().Set("root", tc.root)
		_ = cmd.Flags().Set("workflow-profile", tc.profile)
		if err := runCoordinationScopeGet(cmd, nil); err == nil {
			t.Fatalf("get validation case %d accepted", i)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("validation made %d requests", requests.Load())
	}
}

func TestWorkCoordinationDependencyValidationMakesZeroRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	const validScope = "00000000-0000-0000-0000-000000000010"
	const validDependency = "00000000-0000-0000-0000-000000000011"

	addCases := []struct {
		scope, downstream, upstream, revision, key string
		setRevision                                bool
	}{
		{scope: "bad", downstream: "MUL-2", upstream: "MUL-3", revision: "0", key: "key", setRevision: true},
		{scope: validScope, downstream: " MUL-2", upstream: "MUL-3", revision: "0", key: "key", setRevision: true},
		{scope: validScope, downstream: "MUL-2", upstream: "", revision: "0", key: "key", setRevision: true},
		{scope: validScope, downstream: "MUL-2", upstream: "MUL-3", key: "key"},
		{scope: validScope, downstream: "MUL-2", upstream: "MUL-3", revision: "-1", key: "key", setRevision: true},
		{scope: validScope, downstream: "MUL-2", upstream: "MUL-3", revision: "9223372036854775808", key: "key", setRevision: true},
		{scope: validScope, downstream: "MUL-2", upstream: "MUL-3", revision: "0", key: " key ", setRevision: true},
	}
	for i, tc := range addCases {
		cmd := newCoordinationDependencyAddTestCommand()
		_ = cmd.Flags().Set("scope", tc.scope)
		_ = cmd.Flags().Set("downstream", tc.downstream)
		_ = cmd.Flags().Set("upstream", tc.upstream)
		if tc.setRevision {
			_ = cmd.Flags().Set("expected-revision", tc.revision)
		}
		_ = cmd.Flags().Set("idempotency-key", tc.key)
		if err := runCoordinationDependencyAdd(cmd, nil); err == nil {
			t.Fatalf("add validation case %d accepted", i)
		}
	}

	for i, tc := range []struct {
		scope, cursor string
		limit         int
	}{
		{scope: "bad", limit: 100},
		{scope: validScope, cursor: " cursor ", limit: 100},
		{scope: validScope, limit: 0},
		{scope: validScope, limit: 101},
	} {
		cmd := newCoordinationDependencyListTestCommand()
		_ = cmd.Flags().Set("scope", tc.scope)
		_ = cmd.Flags().Set("cursor", tc.cursor)
		_ = cmd.Flags().Set("limit", fmt.Sprint(tc.limit))
		if err := runCoordinationDependencyList(cmd, nil); err == nil {
			t.Fatalf("list validation case %d accepted", i)
		}
	}

	for i, tc := range []struct {
		scope, dependency, revision, key string
		setRevision                      bool
	}{
		{scope: "bad", dependency: validDependency, revision: "0", key: "key", setRevision: true},
		{scope: validScope, dependency: "bad", revision: "0", key: "key", setRevision: true},
		{scope: validScope, dependency: validDependency, key: "key"},
		{scope: validScope, dependency: validDependency, revision: "-1", key: "key", setRevision: true},
		{scope: validScope, dependency: validDependency, revision: "0", key: "", setRevision: true},
	} {
		cmd := newCoordinationDependencyResolveTestCommand()
		_ = cmd.Flags().Set("scope", tc.scope)
		_ = cmd.Flags().Set("dependency", tc.dependency)
		if tc.setRevision {
			_ = cmd.Flags().Set("expected-revision", tc.revision)
		}
		_ = cmd.Flags().Set("idempotency-key", tc.key)
		if err := runCoordinationDependencyResolve(cmd, nil); err == nil {
			t.Fatalf("resolve validation case %d accepted", i)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("dependency validation made %d requests", requests.Load())
	}
}

func resetWorkCoordinationCommandState() {
	rootCmd.SetArgs(nil)
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	debugFlag = false
	coordinationOutput = "json"
	for _, cmd := range []*cobra.Command{rootCmd, coordinationCmd, coordinationScopeCmd, coordinationScopeEnsureCmd, coordinationScopeGetCmd, coordinationDependencyCmd, coordinationDependencyAddCmd, coordinationDependencyListCmd, coordinationDependencyResolveCmd} {
		cmd.InitDefaultHelpFlag()
		_ = cmd.Flags().Set("help", "false")
		if flag := cmd.Flags().Lookup("help"); flag != nil {
			flag.Changed = false
		}
	}
	for _, item := range []struct {
		cmd   *cobra.Command
		name  string
		value string
	}{
		{rootCmd, "debug", "false"},
		{coordinationCmd, "output", "json"},
		{coordinationScopeEnsureCmd, "root", ""},
		{coordinationScopeEnsureCmd, "workflow-profile", ""},
		{coordinationScopeEnsureCmd, "idempotency-key", ""},
		{coordinationScopeGetCmd, "scope", ""},
		{coordinationScopeGetCmd, "root", ""},
		{coordinationScopeGetCmd, "workflow-profile", ""},
		{coordinationDependencyAddCmd, "scope", ""},
		{coordinationDependencyAddCmd, "downstream", ""},
		{coordinationDependencyAddCmd, "upstream", ""},
		{coordinationDependencyAddCmd, "expected-revision", ""},
		{coordinationDependencyAddCmd, "idempotency-key", ""},
		{coordinationDependencyListCmd, "scope", ""},
		{coordinationDependencyListCmd, "cursor", ""},
		{coordinationDependencyListCmd, "limit", "100"},
		{coordinationDependencyResolveCmd, "scope", ""},
		{coordinationDependencyResolveCmd, "dependency", ""},
		{coordinationDependencyResolveCmd, "expected-revision", ""},
		{coordinationDependencyResolveCmd, "idempotency-key", ""},
	} {
		_ = item.cmd.Flags().Set(item.name, item.value)
		if flag := item.cmd.Flags().Lookup(item.name); flag != nil {
			flag.Changed = false
		}
	}
}

func newCoordinationGetTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("root", "", "")
	cmd.Flags().String("workflow-profile", "", "")
	return cmd
}

func newCoordinationEnsureTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "ensure"}
	cmd.Flags().String("root", "", "")
	cmd.Flags().String("workflow-profile", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	return cmd
}

func newCoordinationDependencyAddTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "add"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("downstream", "", "")
	cmd.Flags().String("upstream", "", "")
	cmd.Flags().String("expected-revision", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	return cmd
}

func newCoordinationDependencyListTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("cursor", "", "")
	cmd.Flags().Int("limit", 100, "")
	return cmd
}

func newCoordinationDependencyResolveTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "resolve"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("dependency", "", "")
	cmd.Flags().String("expected-revision", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	return cmd
}
