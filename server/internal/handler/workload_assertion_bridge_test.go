package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	producerBridgeUserID           = "10000000-0000-4000-8000-000000000001"
	producerBridgeWorkspaceID      = "20000000-0000-4000-8000-000000000002"
	producerBridgeRuntimeID        = "30000000-0000-4000-8000-000000000003"
	producerBridgeAgentID          = "40000000-0000-4000-8000-000000000004"
	producerBridgeSquadID          = "50000000-0000-4000-8000-000000000005"
	producerBridgeIssueID          = "60000000-0000-4000-8000-000000000006"
	producerBridgeCommentID        = "70000000-0000-4000-8000-000000000007"
	producerBridgeTaskID           = "80000000-0000-4000-8000-000000000008"
	producerBridgeTeamID           = "90000000-0000-4000-8000-000000000009"
	producerBridgeCorrelation      = "a0000000-0000-4000-8000-00000000000a"
	producerBridgeTokenHash        = "producer-bridge-task-token-hash-v1"
	producerBridgeIssuer           = "urn:multica:deployment:producer-bridge-test"
	producerBridgeIssuerID         = "multica-producer-bridge-test"
	producerBridgeKeyID            = "producer-bridge-key-v1"
	producerBridgeDefaultKey       = "producer-bridge-local-test-secret-v1"
	producerBridgeSecretEnv        = "PRINCIPAL_SESSION_BRIDGE_SECRET"
	producerBridgeMulticaOutputEnv = "PRINCIPAL_SESSION_BRIDGE_MULTICA_OUTPUT"
	producerBridgeAGSOutputEnv     = "PRINCIPAL_SESSION_BRIDGE_AGS_OUTPUT"
	producerBridgeFixtureName      = "multica-workload-assertion-producer-fixture.json"
)

// TestExportCanonicalSessionExchangeProducerFixture is the Multica end of the
// cross-repository producer bridge. It seeds real durable authority and task
// facts, invokes the production HTTP handler, and writes the handler's exact
// response bytes to PRINCIPAL_SESSION_BRIDGE_MULTICA_OUTPUT. The exact bridge
// selector fails closed unless the complete cross-repository environment is
// present. A full package run still exercises the test with isolated defaults.
func TestExportCanonicalSessionExchangeProducerFixture(t *testing.T) {
	contract, err := resolveProducerBridgeContract(producerBridgeExplicitlySelected(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixtureHandler := setupProducerBridgeDatabaseFixture(t)
	// Golden assertions use a fixed non-production key. The exported bridge
	// artifact switches to the caller-provided one immediately before minting.
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", producerBridgeDefaultKey)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", producerBridgeIssuer)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", producerBridgeIssuerID)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_KEY_ID", producerBridgeKeyID)
	t.Setenv("MULTICA_APP_URL", "https://app.multica.test")

	t.Run("deterministic full provenance producer bytes", func(t *testing.T) {
		h := *fixtureHandler
		h.workloadAssertionNow = func() time.Time {
			return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		}
		h.workloadAssertionID = func() string { return producerBridgeCorrelation }
		body := invokeProducerBridgeHandler(t, &h, producerBridgeTaskID, producerBridgeTokenHash)
		assertGoldenBytes(t, body, "testdata/workload_assertion_producer_full_response.golden.json")
		response := decodeProducerBridgeResponse(t, body)
		assertWorkloadGoldenBytes(t, response.Workload, "testdata/workload_assertion_producer_full.golden.json")
		assertSignedWorkloadMatchesResponse(t, response, producerBridgeDefaultKey)
	})

	t.Run("optional provenance omissions", func(t *testing.T) {
		if _, err := testPool.Exec(context.Background(), `
			UPDATE agent_task_queue
			SET issue_id = NULL, squad_id = NULL, trigger_comment_id = NULL
			WHERE id = $1
		`, producerBridgeTaskID); err != nil {
			t.Fatalf("remove optional producer provenance: %v", err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `
				UPDATE agent_task_queue
				SET issue_id = $2, squad_id = $3, trigger_comment_id = $4
				WHERE id = $1
			`, producerBridgeTaskID, producerBridgeIssueID, producerBridgeSquadID, producerBridgeCommentID)
		})

		h := *fixtureHandler
		h.workloadAssertionNow = func() time.Time {
			return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		}
		h.workloadAssertionID = func() string { return producerBridgeCorrelation }
		body := invokeProducerBridgeHandler(t, &h, producerBridgeTaskID, producerBridgeTokenHash)
		response := decodeProducerBridgeResponse(t, body)
		assertWorkloadGoldenBytes(t, response.Workload, "testdata/workload_assertion_producer_required.golden.json")
		assertSignedWorkloadMatchesResponse(t, response, producerBridgeDefaultKey)
	})

	// Restore the canonical full-provenance row before producing the artifact.
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET issue_id = $2, squad_id = $3, trigger_comment_id = $4
		WHERE id = $1
	`, producerBridgeTaskID, producerBridgeIssueID, producerBridgeSquadID, producerBridgeCommentID); err != nil {
		t.Fatalf("restore canonical producer provenance: %v", err)
	}

	// The exported JWT uses the bridge key, production clock, and production ID
	// source so a downstream verifier receives a live assertion from the actual
	// producer rather than the deterministic golden token.
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", contract.Secret)
	exportedBody := invokeProducerBridgeHandler(t, fixtureHandler, producerBridgeTaskID, producerBridgeTokenHash)
	exported := decodeProducerBridgeResponse(t, exportedBody)
	assertSignedWorkloadMatchesResponse(t, exported, contract.Secret)
	assertCanonicalProducerFacts(t, exported.Workload)

	if err := os.MkdirAll(filepath.Dir(contract.MulticaOutputPath), 0o755); err != nil {
		t.Fatalf("create producer fixture directory: %v", err)
	}
	if err := os.WriteFile(contract.MulticaOutputPath, exportedBody, 0o600); err != nil {
		t.Fatalf("write producer fixture: %v", err)
	}
	persisted, err := os.ReadFile(contract.MulticaOutputPath)
	if err != nil {
		t.Fatalf("read producer fixture: %v", err)
	}
	if !bytes.Equal(persisted, exportedBody) {
		t.Fatal("persisted producer fixture differs from exact handler response bytes")
	}
	t.Logf("producer_fixture_path=%s", contract.MulticaOutputPath)
}

type producerBridgeContract struct {
	Secret            string
	MulticaOutputPath string
	AGSOutputPath     string
}

func resolveProducerBridgeContract(explicit bool, tempDir string) (producerBridgeContract, error) {
	contract := producerBridgeContract{
		Secret:            strings.TrimSpace(os.Getenv(producerBridgeSecretEnv)),
		MulticaOutputPath: strings.TrimSpace(os.Getenv(producerBridgeMulticaOutputEnv)),
		AGSOutputPath:     strings.TrimSpace(os.Getenv(producerBridgeAGSOutputEnv)),
	}
	if explicit {
		missing := make([]string, 0, 3)
		if contract.Secret == "" {
			missing = append(missing, producerBridgeSecretEnv)
		}
		if contract.MulticaOutputPath == "" {
			missing = append(missing, producerBridgeMulticaOutputEnv)
		}
		if contract.AGSOutputPath == "" {
			missing = append(missing, producerBridgeAGSOutputEnv)
		}
		if len(missing) > 0 {
			return producerBridgeContract{}, fmt.Errorf("explicit producer bridge requires %s", strings.Join(missing, ", "))
		}
	} else {
		if contract.Secret == "" {
			contract.Secret = producerBridgeDefaultKey
		}
		if contract.MulticaOutputPath == "" {
			contract.MulticaOutputPath = filepath.Join(tempDir, producerBridgeFixtureName)
		}
		if contract.AGSOutputPath == "" {
			contract.AGSOutputPath = filepath.Join(tempDir, "ags-principal-session.json")
		}
	}
	if filepath.Clean(contract.MulticaOutputPath) == filepath.Clean(contract.AGSOutputPath) {
		return producerBridgeContract{}, fmt.Errorf("%s and %s must name distinct files", producerBridgeMulticaOutputEnv, producerBridgeAGSOutputEnv)
	}
	return contract, nil
}

func producerBridgeExplicitlySelected() bool {
	runFlag := flag.Lookup("test.run")
	if runFlag == nil || runFlag.Value.String() == "" {
		return false
	}
	matched, err := regexp.MatchString(runFlag.Value.String(), "TestExportCanonicalSessionExchangeProducerFixture")
	return err == nil && matched
}

func TestResolveProducerBridgeContractFailsClosedForExplicitSelector(t *testing.T) {
	setCompleteContract := func(t *testing.T) {
		t.Helper()
		t.Setenv(producerBridgeSecretEnv, "bridge-secret")
		t.Setenv(producerBridgeMulticaOutputEnv, filepath.Join(t.TempDir(), "multica.json"))
		t.Setenv(producerBridgeAGSOutputEnv, filepath.Join(t.TempDir(), "ags.json"))
	}

	for _, missing := range []string{producerBridgeSecretEnv, producerBridgeMulticaOutputEnv, producerBridgeAGSOutputEnv} {
		t.Run("missing "+missing, func(t *testing.T) {
			setCompleteContract(t)
			t.Setenv(missing, "  ")
			if _, err := resolveProducerBridgeContract(true, t.TempDir()); err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("missing %s error = %v", missing, err)
			}
		})
	}

	t.Run("distinct output files required", func(t *testing.T) {
		setCompleteContract(t)
		shared := filepath.Join(t.TempDir(), "shared.json")
		t.Setenv(producerBridgeMulticaOutputEnv, shared)
		t.Setenv(producerBridgeAGSOutputEnv, shared)
		if _, err := resolveProducerBridgeContract(true, t.TempDir()); err == nil {
			t.Fatal("shared Multica and AGS output path was accepted")
		}
	})

	t.Run("complete explicit contract", func(t *testing.T) {
		setCompleteContract(t)
		contract, err := resolveProducerBridgeContract(true, t.TempDir())
		if err != nil || contract.Secret != "bridge-secret" {
			t.Fatalf("complete explicit contract = %#v, %v", contract, err)
		}
	})
}

func TestCanonicalSessionExchangeProducerRejectsInvalidDatabaseFacts(t *testing.T) {
	fixtureHandler := setupProducerBridgeDatabaseFixture(t)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", producerBridgeDefaultKey)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", producerBridgeIssuer)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", producerBridgeIssuerID)

	t.Run("token cannot name another task", func(t *testing.T) {
		req := newProducerBridgeRequest(producerBridgeCorrelation)
		req.Header.Set("X-Task-ID", producerBridgeCorrelation)
		rr := httptest.NewRecorder()
		fixtureHandler.CreateWorkloadAssertion(rr, req)
		if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), `"assertion"`) {
			t.Fatalf("mismatched task fact response=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("missing durable authority", func(t *testing.T) {
		if _, err := testPool.Exec(context.Background(), `DELETE FROM workspace_workload_authority WHERE workspace_id=$1`, producerBridgeWorkspaceID); err != nil {
			t.Fatalf("delete workload authority: %v", err)
		}
		rr := httptest.NewRecorder()
		fixtureHandler.CreateWorkloadAssertion(rr, newProducerBridgeRequest(producerBridgeTaskID))
		if rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), `"assertion"`) {
			t.Fatalf("missing authority response=%d body=%s", rr.Code, rr.Body.String())
		}
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
			VALUES ($1, $2, 7, $3)
		`, producerBridgeWorkspaceID, producerBridgeTeamID, workspaceDefaultPolicyClass); err != nil {
			t.Fatalf("restore workload authority: %v", err)
		}
	})

	t.Run("terminal task authority", func(t *testing.T) {
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1`, producerBridgeTaskID); err != nil {
			t.Fatalf("complete producer bridge task: %v", err)
		}
		rr := httptest.NewRecorder()
		fixtureHandler.CreateWorkloadAssertion(rr, newProducerBridgeRequest(producerBridgeTaskID))
		if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), `"assertion"`) {
			t.Fatalf("terminal task response=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func setupProducerBridgeDatabaseFixture(t *testing.T) *Handler {
	t.Helper()
	ctx := context.Background()
	cleanupProducerBridgeDatabaseFixture(ctx)
	t.Cleanup(func() { cleanupProducerBridgeDatabaseFixture(context.Background()) })

	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{name: "user", sql: `INSERT INTO "user" (id, name, email) VALUES ($1, 'Producer Bridge User', 'producer-bridge@multica.test')`, args: []any{producerBridgeUserID}},
		{name: "workspace", sql: `INSERT INTO workspace (id, name, slug, description, issue_prefix) VALUES ($1, 'Producer Bridge', 'producer-bridge', 'DB-backed producer bridge fixture', 'BRG')`, args: []any{producerBridgeWorkspaceID}},
		{name: "member", sql: `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, args: []any{producerBridgeWorkspaceID, producerBridgeUserID}},
		{name: "runtime", sql: `INSERT INTO agent_runtime (id, workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at) VALUES ($1, $2, NULL, 'Producer Bridge Runtime', 'cloud', 'producer_bridge', 'online', 'Producer bridge runtime', '{}'::jsonb, $3, now())`, args: []any{producerBridgeRuntimeID, producerBridgeWorkspaceID, producerBridgeUserID}},
		{name: "agent", sql: `INSERT INTO agent (id, workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id, instructions, custom_env, custom_args, mcp_config) VALUES ($1, $2, 'Producer Bridge Agent', '', 'cloud', '{}'::jsonb, $3, 'workspace', 'public_to', 1, $4, '', '{}'::jsonb, '[]'::jsonb, '{}'::jsonb)`, args: []any{producerBridgeAgentID, producerBridgeWorkspaceID, producerBridgeRuntimeID, producerBridgeUserID}},
		{name: "squad", sql: `INSERT INTO squad (id, workspace_id, name, description, leader_id, creator_id) VALUES ($1, $2, 'Producer Bridge Squad', '', $3, $4)`, args: []any{producerBridgeSquadID, producerBridgeWorkspaceID, producerBridgeAgentID, producerBridgeUserID}},
		{name: "issue", sql: `INSERT INTO issue (id, workspace_id, number, title, description, status, priority, creator_type, creator_id, acceptance_criteria, context_refs) VALUES ($1, $2, 41, 'Producer bridge issue', '', 'todo', 'none', 'member', $3, '[]'::jsonb, '[]'::jsonb)`, args: []any{producerBridgeIssueID, producerBridgeWorkspaceID, producerBridgeUserID}},
		{name: "comment", sql: `INSERT INTO comment (id, issue_id, workspace_id, author_type, author_id, content) VALUES ($1, $2, $3, 'member', $4, 'Start the canonical producer bridge run')`, args: []any{producerBridgeCommentID, producerBridgeIssueID, producerBridgeWorkspaceID, producerBridgeUserID}},
		{name: "task", sql: `INSERT INTO agent_task_queue (id, agent_id, runtime_id, status, priority, issue_id, started_at, squad_id, trigger_comment_id) VALUES ($1, $2, $3, 'running', 0, $4, now(), $5, $6)`, args: []any{producerBridgeTaskID, producerBridgeAgentID, producerBridgeRuntimeID, producerBridgeIssueID, producerBridgeSquadID, producerBridgeCommentID}},
	}
	for _, statement := range statements {
		if _, err := testPool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed producer bridge %s: %v", statement.name, err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE workspace_workload_authority SET team_identity_id=$2, membership_epoch=7, policy_class=$3 WHERE workspace_id=$1`, producerBridgeWorkspaceID, producerBridgeTeamID, workspaceDefaultPolicyClass); err != nil {
		t.Fatalf("seed producer bridge authority: %v", err)
	}
	if _, err := testHandler.Queries.CreateTaskToken(ctx, db.CreateTaskTokenParams{
		TokenHash: producerBridgeTokenHash,
		TaskID:    parseUUID(producerBridgeTaskID), AgentID: parseUUID(producerBridgeAgentID),
		WorkspaceID: parseUUID(producerBridgeWorkspaceID), UserID: parseUUID(producerBridgeUserID),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed producer bridge task token: %v", err)
	}

	h := *testHandler
	return &h
}

func cleanupProducerBridgeDatabaseFixture(ctx context.Context) {
	_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE id=$1`, producerBridgeSquadID)
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, producerBridgeWorkspaceID)
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace_workload_authority WHERE workspace_id=$1`, producerBridgeWorkspaceID)
	_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id=$1 OR email='producer-bridge@multica.test'`, producerBridgeUserID)
}

func invokeProducerBridgeHandler(t *testing.T, h *Handler, taskID, tokenHash string) []byte {
	t.Helper()
	req := newProducerBridgeRequest(taskID)
	req.Header.Set("X-Task-Token-Hash", tokenHash)
	rr := httptest.NewRecorder()
	h.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("producer bridge handler status=%d body=%s", rr.Code, rr.Body.String())
	}
	return bytes.Clone(rr.Body.Bytes())
}

func newProducerBridgeRequest(taskID string) *http.Request {
	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose":            "ags_session_exchange",
		"target":             map[string]any{"provider": "ags", "instance": "bridge", "repository": "multica/producer-bridge"},
		"requested_resource": map[string]any{"service": "ags", "repository": "multica/producer-bridge"},
		"requested_operation": map[string]any{
			"name": "pr.rebase",
			"constraints": map[string]any{
				"pull_request_number":         41,
				"forgejo_pull_request_number": 52,
				"expected_head_sha":           "1111111111111111111111111111111111111111",
				"expected_base_sha":           "2222222222222222222222222222222222222222",
			},
		},
		"requested_capabilities": []string{"repo:write", "repo:read"},
		"requested_ttl":          "15m",
	})
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", producerBridgeAgentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", producerBridgeWorkspaceID)
	req.Header.Set("X-Task-Token-Hash", producerBridgeTokenHash)
	return req
}

func decodeProducerBridgeResponse(t *testing.T, body []byte) workloadAssertionResponse {
	t.Helper()
	var response workloadAssertionResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode producer response: %v", err)
	}
	if response.Assertion == "" || response.AssertionType != workloadAssertionType || response.Purpose != workloadAssertionPurposeSessionExchange {
		t.Fatalf("producer response contract = %#v", response)
	}
	return response
}

func assertWorkloadGoldenBytes(t *testing.T, workload workloadAssertionWorkload, path string) {
	t.Helper()
	actual, err := json.Marshal(workload)
	if err != nil {
		t.Fatalf("marshal actual producer workload: %v", err)
	}
	assertGoldenBytes(t, actual, path)
}

func assertGoldenBytes(t *testing.T, actual []byte, path string) {
	t.Helper()
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read producer golden: %v", err)
	}
	expected = bytes.TrimSuffix(expected, []byte("\n"))
	actual = bytes.TrimSuffix(actual, []byte("\n"))
	if !bytes.Equal(actual, expected) {
		t.Fatalf("actual producer bytes differ from %s\nactual: %s\nexpected: %s", path, actual, expected)
	}
}

func assertSignedWorkloadMatchesResponse(t *testing.T, response workloadAssertionResponse, secret string) {
	t.Helper()
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(response.Assertion, claims, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithAudience(workloadAssertionSessionExchangeAudience), jwt.WithIssuer(producerBridgeIssuer), jwt.WithoutClaimsValidation())
	if err != nil || !token.Valid {
		t.Fatalf("verify producer assertion: valid=%v err=%v", token != nil && token.Valid, err)
	}
	if token.Method != jwt.SigningMethodHS256 || token.Header["typ"] != workloadAssertionJWTType || token.Header["kid"] != producerBridgeKeyID {
		t.Fatalf("producer JWT header = %#v", token.Header)
	}
	if claims["iss"] != producerBridgeIssuer || claims["aud"] != workloadAssertionSessionExchangeAudience || claims["purpose"] != workloadAssertionPurposeSessionExchange || claims["source"] != "task_token" {
		t.Fatalf("producer JWT boundary claims = %#v", claims)
	}
	responseWorkload, err := json.Marshal(response.Workload)
	if err != nil {
		t.Fatalf("marshal response workload: %v", err)
	}
	claimWorkload, err := json.Marshal(claims["workload"])
	if err != nil {
		t.Fatalf("marshal signed workload: %v", err)
	}
	var responseValue, claimValue any
	if err := json.Unmarshal(responseWorkload, &responseValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(claimWorkload, &claimValue); err != nil {
		t.Fatal(err)
	}
	canonicalResponse, _ := json.Marshal(responseValue)
	canonicalClaim, _ := json.Marshal(claimValue)
	if !bytes.Equal(canonicalResponse, canonicalClaim) {
		t.Fatalf("signed workload differs from response workload\nsigned: %s\nresponse: %s", canonicalClaim, canonicalResponse)
	}

	parts := strings.Split(response.Assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("producer assertion has %d segments", len(parts))
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		t.Fatalf("producer assertion payload is not canonical base64url: %v", err)
	}
}

func assertCanonicalProducerFacts(t *testing.T, workload workloadAssertionWorkload) {
	t.Helper()
	context := workload.WorkloadContext
	authority := workload.Authority
	if workload.WorkspaceID != producerBridgeWorkspaceID || workload.AgentID != producerBridgeAgentID || workload.TaskID != producerBridgeTaskID || workload.RunID != producerBridgeTaskID || workload.IssueID != producerBridgeIssueID || workload.IssueKey != "BRG-41" {
		t.Fatalf("producer workload identity facts = %#v", workload)
	}
	if workload.Actor == nil || workload.Actor.Type != "agent" || workload.Actor.ID != producerBridgeAgentID {
		t.Fatalf("producer actor facts = %#v", workload.Actor)
	}
	if context == nil || context.Subject != "urn:multica:agent:"+producerBridgeAgentID || context.WorkspaceID != producerBridgeWorkspaceID || context.AgentID != producerBridgeAgentID || context.TaskID != producerBridgeTaskID || context.RunID != producerBridgeTaskID || context.SquadID != producerBridgeSquadID || context.IssueID != producerBridgeIssueID || context.IssueKey != "BRG-41" || context.TriggerID != producerBridgeCommentID || context.RuntimeID != producerBridgeRuntimeID {
		t.Fatalf("producer context facts = %#v", context)
	}
	if authority == nil || authority.TeamIdentityID != producerBridgeTeamID || authority.MembershipEpoch != 7 || authority.PolicyClass != workspaceDefaultPolicyClass {
		t.Fatalf("producer authority facts = %#v", authority)
	}
}
