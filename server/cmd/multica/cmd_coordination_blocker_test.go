package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func TestWorkCoordinationBlockerExactRequestsAndOutputs(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	const (
		scopeID      = "00000000-0000-0000-0000-000000000010"
		blockerID    = "00000000-0000-0000-0000-000000000011"
		dependencyID = "00000000-0000-0000-0000-000000000012"
		downstreamID = "00000000-0000-0000-0000-000000000002"
		upstreamID   = "00000000-0000-0000-0000-000000000003"
		evidenceID   = "00000000-0000-0000-0000-000000abcdef"
		workspaceID  = "00000000-0000-0000-0000-000000000020"
		actorID      = "00000000-0000-0000-0000-000000000030"
		receiptID    = "00000000-0000-0000-0000-000000000040"
	)
	resourceJSON := `{"id":"` + blockerID + `","workspace_id":"` + workspaceID + `","scope_id":"` + scopeID + `","kind":"blocker","schema_version":1,"status":"open","root_issue_id":"00000000-0000-0000-0000-000000000001","downstream_issue_id":"` + downstreamID + `","upstream_issue_id":"` + upstreamID + `","dependency_id":"` + dependencyID + `","reason_code":"waiting_on_issue","resolution_code":null,"create_evidence_refs":[{"kind":"issue","id":"` + evidenceID + `"}],"resolution_evidence_refs":[],"created_by":{"type":"member","id":"` + actorID + `","task_id":null},"resolved_by":null,"created_at":"2026-01-01T00:00:00Z","resolved_at":null}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-2":
			_, _ = w.Write([]byte(`{"id":"` + downstreamID + `","identifier":"MUL-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/MUL-3":
			_, _ = w.Write([]byte(`{"id":"` + upstreamID + `","identifier":"MUL-3"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/blockers":
			if r.Header.Get("Idempotency-Key") != "blocker-add-key" {
				t.Errorf("add idempotency key=%q", r.Header.Get("Idempotency-Key"))
			}
			var body struct {
				ExpectedRevision  int64   `json:"expected_revision"`
				DownstreamIssueID string  `json:"downstream_issue_id"`
				UpstreamIssueID   string  `json:"upstream_issue_id"`
				DependencyID      *string `json:"dependency_id"`
				SchemaVersion     int32   `json:"schema_version"`
				Payload           struct {
					ReasonCode   string                           `json:"reason_code"`
					EvidenceRefs []coordinationBlockerEvidenceCLI `json:"evidence_refs"`
				} `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ExpectedRevision != 2 || body.DownstreamIssueID != downstreamID ||
				body.UpstreamIssueID != upstreamID || body.DependencyID == nil || *body.DependencyID != dependencyID || body.SchemaVersion != 1 ||
				body.Payload.ReasonCode != "waiting_on_issue" || len(body.Payload.EvidenceRefs) != 1 || body.Payload.EvidenceRefs[0].ID != evidenceID {
				t.Errorf("add body=%+v err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"receipt":{"id":"` + receiptID + `","receipt_ordinal":4,"operation":"append_blocker","resource_type":"blocker","resource_id":"` + blockerID + `","revision_before":2,"revision_after":3,"created_at":"2026-01-01T00:00:00Z"},"resource":` + resourceJSON + `,"scope_revision":3,"changed":true,"replayed":false}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/blockers":
			if r.URL.Query().Get("status") != "all" || r.URL.Query().Get("cursor") != "opaque/+?" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("list query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"scope_id":"` + scopeID + `","scope_revision":3,"status_filter":"all","items":[` + resourceJSON + `],"next_cursor":"next-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/coordination/scopes/"+scopeID+"/blockers/"+blockerID+"/resolve":
			if r.Header.Get("Idempotency-Key") != "blocker-resolve-key" {
				t.Errorf("resolve idempotency key=%q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 3 || body["expected_revision"] != float64(3) || body["schema_version"] != float64(1) {
				t.Errorf("resolve body=%#v err=%v", body, err)
			} else {
				resolution, ok := body["resolution"].(map[string]any)
				refs, refsOK := resolution["evidence_refs"].([]any)
				var ref map[string]any
				var refOK bool
				if len(refs) == 1 {
					ref, refOK = refs[0].(map[string]any)
				}
				if !ok || len(resolution) != 2 || resolution["resolution_code"] != "no_longer_blocking" || !refsOK || len(refs) != 1 || !refOK || ref["id"] != evidenceID || ref["kind"] != "issue" {
					t.Errorf("resolve nested body=%#v", resolution)
				}
			}
			_, _ = w.Write([]byte(`{"receipt":{"id":"` + receiptID + `","receipt_ordinal":5,"operation":"resolve_blocker","resource_type":"blocker","resource_id":"` + blockerID + `","revision_before":3,"revision_after":4,"created_at":"2026-01-01T00:00:00Z"},"resource":` + resourceJSON + `,"scope_revision":4,"changed":true,"replayed":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	t.Setenv("MULTICA_TOKEN", "mul_test")

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	resolutionPath := filepath.Join(dir, "resolution.json")
	if err := os.WriteFile(payloadPath, []byte(`{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"issue","id":"`+strings.ToUpper(evidenceID)+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolutionPath, []byte(`{"resolution_code":"no_longer_blocking","evidence_refs":[{"kind":"issue","id":"`+strings.ToUpper(evidenceID)+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	add := newCoordinationBlockerAddTestCommand()
	_ = add.Flags().Set("scope", scopeID)
	_ = add.Flags().Set("downstream", "MUL-2")
	_ = add.Flags().Set("upstream", "MUL-3")
	_ = add.Flags().Set("dependency", dependencyID)
	_ = add.Flags().Set("payload-file", payloadPath)
	_ = add.Flags().Set("expected-revision", "2")
	_ = add.Flags().Set("idempotency-key", "blocker-add-key")
	var stdout bytes.Buffer
	add.SetOut(&stdout)
	coordinationOutput = "json"
	if err := runCoordinationBlockerAdd(add, nil); err != nil {
		t.Fatalf("add blocker: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, `"resource":{"id":"`+blockerID+`"`) || !strings.Contains(got, `"changed":true`) || strings.Count(strings.TrimSpace(got), "\n") != 0 {
		t.Fatalf("add output=%q", got)
	}
	var addOutput coordinationBlockerMutationCLIResponse
	if err := json.Unmarshal(stdout.Bytes(), &addOutput); err != nil || addOutput.Resource.DependencyID == nil || addOutput.Resource.ResolutionCode != nil || addOutput.Resource.ResolvedBy != nil || addOutput.Resource.ResolvedAt != nil || addOutput.Resource.CreatedBy.TaskID != nil || addOutput.Replayed {
		t.Fatalf("typed add JSON output=%+v err=%v", addOutput, err)
	}

	list := newCoordinationBlockerListTestCommand()
	_ = list.Flags().Set("scope", scopeID)
	_ = list.Flags().Set("status", "all")
	_ = list.Flags().Set("cursor", "opaque/+?")
	_ = list.Flags().Set("limit", "2")
	stdout.Reset()
	list.SetOut(&stdout)
	coordinationOutput = "table"
	if err := runCoordinationBlockerList(list, nil); err != nil {
		t.Fatalf("list blockers: %v", err)
	}
	if got := stdout.String(); got != blockerID+"\topen\t"+downstreamID+" blocked_by "+upstreamID+"\nnext_cursor=next-token revision=3\n" {
		t.Fatalf("list output=%q", got)
	}
	stdout.Reset()
	list.SetOut(&stdout)
	coordinationOutput = "json"
	if err := runCoordinationBlockerList(list, nil); err != nil {
		t.Fatalf("list blockers JSON: %v", err)
	}
	var listOutput coordinationBlockerPageCLIResponse
	if err := json.Unmarshal(stdout.Bytes(), &listOutput); err != nil || listOutput.ScopeID != scopeID || listOutput.StatusFilter != "all" || len(listOutput.Items) != 1 || listOutput.NextCursor == nil || *listOutput.NextCursor != "next-token" {
		t.Fatalf("typed list JSON output=%+v err=%v", listOutput, err)
	}

	resolve := newCoordinationBlockerResolveTestCommand()
	_ = resolve.Flags().Set("scope", scopeID)
	_ = resolve.Flags().Set("blocker", blockerID)
	_ = resolve.Flags().Set("resolution-file", resolutionPath)
	_ = resolve.Flags().Set("expected-revision", "3")
	_ = resolve.Flags().Set("idempotency-key", "blocker-resolve-key")
	stdout.Reset()
	resolve.SetOut(&stdout)
	coordinationOutput = "table"
	if err := runCoordinationBlockerResolve(resolve, nil); err != nil {
		t.Fatalf("resolve blocker: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "changed=true replayed=false revision=4") {
		t.Fatalf("resolve output=%q", got)
	}
	if requests.Load() != 6 {
		t.Fatalf("requests=%d want=6", requests.Load())
	}
}

func TestWorkCoordinationBlockerFileValidationMakesZeroRequests(t *testing.T) {
	resetWorkCoordinationCommandState()
	t.Cleanup(resetWorkCoordinationCommandState)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "00000000-0000-0000-0000-000000000020")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	dir := t.TempDir()
	evidenceID := "00000000-0000-0000-0000-000000000004"
	tooManyRefs := strings.TrimSuffix(strings.Repeat(`{"kind":"issue","id":"`+evidenceID+`"},`, 33), ",")
	cases := []string{
		`{"reason_code":"waiting_on_issue","reason_code":"waiting_on_issue","evidence_refs":[]}`,
		`{"Reason_Code":"waiting_on_issue","evidence_refs":[]}`,
		`{"reason_code":"waiting_on_issue","Reason_Code":"waiting_on_issue","evidence_refs":[]}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":[{"Kind":"issue","id":"` + evidenceID + `"}]}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":[],"unknown":true}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":[]} {}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":null}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"url","id":"` + evidenceID + `"}]}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":[{"kind":"issue","id":"` + evidenceID + `"},{"kind":"issue","id":"` + evidenceID + `"}]}`,
		`{"reason_code":"waiting_on_issue","evidence_refs":[` + tooManyRefs + `]}`,
		`{"schema_version":1,"reason_code":"waiting_on_issue","evidence_refs":[]}`,
		strings.Repeat("x", 4097),
	}
	for index, content := range cases {
		path := filepath.Join(dir, "invalid-"+strconv.Itoa(index)+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := newCoordinationBlockerAddTestCommand()
		_ = cmd.Flags().Set("scope", "00000000-0000-0000-0000-000000000010")
		_ = cmd.Flags().Set("downstream", "MUL-2")
		_ = cmd.Flags().Set("upstream", "MUL-3")
		_ = cmd.Flags().Set("payload-file", path)
		_ = cmd.Flags().Set("expected-revision", "0")
		_ = cmd.Flags().Set("idempotency-key", "invalid")
		if err := runCoordinationBlockerAdd(cmd, nil); err == nil {
			t.Fatalf("invalid payload case %d accepted", index)
		}
	}
	resolutionCases := []string{
		`{"resolution_code":"no_longer_blocking","resolution_code":"superseded","evidence_refs":[]}`,
		`{"Resolution_Code":"no_longer_blocking","evidence_refs":[]}`,
		`{"resolution_code":"no_longer_blocking","Resolution_Code":"superseded","evidence_refs":[]}`,
		`{"resolution_code":"no_longer_blocking","evidence_refs":[],"unknown":true}`,
		`{"resolution_code":"no_longer_blocking","evidence_refs":[]} {}`,
		`{"resolution_code":"no_longer_blocking","evidence_refs":null}`,
		`{"resolution_code":"unknown","evidence_refs":[]}`,
		`{"resolution_code":"no_longer_blocking","evidence_refs":[{"kind":"issue","id":"` + evidenceID + `"},{"kind":"issue","id":"` + evidenceID + `"}]}`,
		`{"resolution_code":"no_longer_blocking","evidence_refs":[` + tooManyRefs + `]}`,
		`{"schema_version":1,"resolution_code":"no_longer_blocking","evidence_refs":[]}`,
		strings.Repeat("x", 4097),
	}
	for index, content := range resolutionCases {
		path := filepath.Join(dir, "invalid-resolution-"+strconv.Itoa(index)+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := newCoordinationBlockerResolveTestCommand()
		_ = cmd.Flags().Set("scope", "00000000-0000-0000-0000-000000000010")
		_ = cmd.Flags().Set("blocker", "00000000-0000-0000-0000-000000000011")
		_ = cmd.Flags().Set("resolution-file", path)
		_ = cmd.Flags().Set("expected-revision", "0")
		_ = cmd.Flags().Set("idempotency-key", "invalid")
		if err := runCoordinationBlockerResolve(cmd, nil); err == nil {
			t.Fatalf("invalid resolution case %d accepted", index)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid blocker files made %d requests", requests.Load())
	}
}

func newCoordinationBlockerAddTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "add"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("downstream", "", "")
	cmd.Flags().String("upstream", "", "")
	cmd.Flags().String("dependency", "", "")
	cmd.Flags().String("payload-file", "", "")
	cmd.Flags().String("expected-revision", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	return cmd
}

func newCoordinationBlockerListTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("status", "open", "")
	cmd.Flags().String("cursor", "", "")
	cmd.Flags().Int("limit", 100, "")
	return cmd
}

func newCoordinationBlockerResolveTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "resolve"}
	cmd.Flags().String("scope", "", "")
	cmd.Flags().String("blocker", "", "")
	cmd.Flags().String("resolution-file", "", "")
	cmd.Flags().String("expected-revision", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	return cmd
}
