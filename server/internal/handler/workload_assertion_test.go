package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func authorizeWorkloadAssertionTestTask(t *testing.T, req *http.Request, agentID, taskID string) string {
	t.Helper()
	tokenHash := uuid.NewString()
	if _, err := testHandler.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{
		TokenHash: tokenHash,
		TaskID:    parseUUID(taskID), AgentID: parseUUID(agentID),
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create workload assertion task token: %v", err)
	}
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Task-Token-Hash", tokenHash)
	return tokenHash
}

func TestValidateWorkloadAssertionConfiguration(t *testing.T) {
	const issuer = "urn:multica:deployment:test-instance"
	for _, tc := range []struct {
		name, issuer, issuerInstanceID string
	}{
		{name: "missing issuer", issuerInstanceID: "multica-test"},
		{name: "placeholder issuer", issuer: "multica", issuerInstanceID: "multica-test"},
		{name: "missing issuer ID", issuer: issuer},
		{name: "issuer ID equals issuer", issuer: "multica-test", issuerInstanceID: "multica-test"},
		{name: "issuer ID whitespace", issuer: issuer, issuerInstanceID: " multica-test "},
		{name: "issuer ID unsafe unicode", issuer: issuer, issuerInstanceID: "multica-测试"},
		{name: "issuer ID placeholder", issuer: issuer, issuerInstanceID: "change-me"},
		{name: "issuer ID secret shaped", issuer: issuer, issuerInstanceID: "mat_secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWorkloadAssertionConfiguration(tc.issuer, tc.issuerInstanceID); err == nil {
				t.Fatal("configuration validation succeeded, want failure")
			}
		})
	}
	if err := ValidateWorkloadAssertionConfiguration(issuer, "multica-test"); err != nil {
		t.Fatalf("distinct canonical issuer linkage rejected: %v", err)
	}
}

func TestCreateWorkloadAssertionExternalPRUsesServerTaskContext(t *testing.T) {
	const (
		secret           = "workload-assertion-secret"
		issuer           = "https://multica.test"
		issuerInstanceID = "multica-test"
		keyID            = "current-key"
		audience         = "urn:multica:external-pr-link:v1"
	)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", secret)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", issuer)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", issuerInstanceID)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_KEY_ID", keyID)
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", "")
	t.Setenv("MULTICA_APP_URL", "https://app.multica.test")

	issueID := createExternalPRTestIssue(t, "workload assertion issue", "todo", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})
	agentID := createHandlerTestAgent(t, "workload-assertion-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose": "external_pr_link",
		"target": map[string]any{
			"provider":   "ags",
			"instance":   "mini",
			"repository": "jackie/agent-kit",
		},
	})
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var response struct {
		Assertion     string         `json:"assertion"`
		AssertionType string         `json:"assertion_type"`
		Purpose       string         `json:"purpose"`
		ExpiresAt     string         `json:"expires_at"`
		Workload      map[string]any `json:"workload"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Assertion == "" || response.AssertionType != "urn:multica:workload-assertion:jwt:v1" || response.Purpose != "external_pr_link" || response.ExpiresAt == "" {
		t.Fatalf("response = %#v", response)
	}
	if response.Workload["workspace_id"] != testWorkspaceID || response.Workload["agent_id"] != agentID || response.Workload["task_id"] != taskID || response.Workload["issue_id"] != issueID {
		t.Fatalf("response workload used client identity: %#v", response.Workload)
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(response.Assertion, claims, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithAudience(audience), jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		t.Fatalf("parse assertion: valid=%v err=%v", token != nil && token.Valid, err)
	}
	if token.Header["typ"] != "multica-workload-assertion+jwt" || token.Header["kid"] != keyID {
		t.Fatalf("unexpected JWT header: %#v", token.Header)
	}
	if claims["ver"] != float64(1) || claims["purpose"] != "external_pr_link" || claims["source"] != "task_token" || claims["sub"] != "urn:multica:workload:"+testWorkspaceID+":"+taskID {
		t.Fatalf("unexpected base claims: %#v", claims)
	}
	if _, present := claims["requested_ttl"]; present {
		t.Fatalf("absent requested_ttl must remain absent: %#v", claims)
	}
	if jti, _ := claims["jti"].(string); jti == "" {
		t.Fatalf("assertion jti is empty: %#v", claims)
	}
	capabilities, ok := claims["requested_capabilities"].([]any)
	if !ok || len(capabilities) != 0 {
		t.Fatalf("requested capabilities must be an empty array: %#v", claims["requested_capabilities"])
	}
	for _, temporalClaim := range []string{"iat", "nbf", "exp"} {
		if _, ok := claims[temporalClaim]; !ok {
			t.Fatalf("missing %s claim: %#v", temporalClaim, claims)
		}
	}
	workload, ok := claims["workload"].(map[string]any)
	if !ok {
		t.Fatalf("workload claim = %#v", claims["workload"])
	}
	if workload["workspace_id"] != testWorkspaceID || workload["agent_id"] != agentID || workload["agent_name"] != "workload-assertion-agent" || workload["task_id"] != taskID || workload["issue_id"] != issueID {
		t.Fatalf("unexpected workload claim: %#v", workload)
	}
	target, ok := claims["target"].(map[string]any)
	if !ok || target["provider"] != "ags" || target["instance"] != "mini" || target["repository"] != "jackie/agent-kit" {
		t.Fatalf("unexpected target claim: %#v", claims["target"])
	}
}

func newExternalPRWorkloadAssertionRequest() *http.Request {
	return newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose": "external_pr_link",
		"target":  map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
	})
}

func TestCreateWorkloadAssertionRejectsTerminalAndExpiredTaskAuthority(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "terminal-task-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "urn:multica:deployment:terminal-task-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-terminal-task-test")
	issueID := createExternalPRTestIssue(t, "terminal assertion authority", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "terminal-assertion-agent", []byte(`{}`))

	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
			req := newExternalPRWorkloadAssertionRequest()
			authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
			if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status=$2, completed_at=now() WHERE id=$1`, taskID, status); err != nil {
				t.Fatalf("terminalize task: %v", err)
			}
			rr := httptest.NewRecorder()
			testHandler.CreateWorkloadAssertion(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("terminal task status=%s response=%d body=%s", status, rr.Code, rr.Body.String())
			}
		})
	}

	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	req := newExternalPRWorkloadAssertionRequest()
	tokenHash := authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	if _, err := testPool.Exec(context.Background(), `UPDATE task_token SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, tokenHash); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	rr := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expired token response=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateWorkloadAssertionLinearizesBeforeTaskCompletion(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "linearized-task-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "urn:multica:deployment:linearized-task-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-linearized-task-test")
	issueID := createExternalPRTestIssue(t, "linearized assertion authority", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "linearized-assertion-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	req := newExternalPRWorkloadAssertionRequest()
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)

	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.WorkloadAssertionHook = func(stage string) {
		if stage == "authority_locked" {
			close(locked)
			<-release
		}
	}
	t.Cleanup(func() { testHandler.WorkloadAssertionHook = nil })

	rr := httptest.NewRecorder()
	assertionDone := make(chan struct{})
	go func() {
		testHandler.CreateWorkloadAssertion(rr, req)
		close(assertionDone)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("assertion did not acquire task authority lock")
	}

	completionDone := make(chan error, 1)
	go func() {
		_, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1 AND status='running'`, taskID)
		completionDone <- err
	}()
	select {
	case err := <-completionDone:
		t.Fatalf("completion crossed assertion lock early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-assertionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("assertion did not finish")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("assertion response=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case err := <-completionDone:
		if err != nil {
			t.Fatalf("complete task after assertion commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not resume after assertion commit")
	}
	testHandler.WorkloadAssertionHook = nil

	retryReq := newExternalPRWorkloadAssertionRequest()
	retryReq.Header = req.Header.Clone()
	retry := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(retry, retryReq)
	if retry.Code != http.StatusUnauthorized {
		t.Fatalf("post-completion assertion response=%d body=%s", retry.Code, retry.Body.String())
	}
}

func TestCreateWorkloadAssertionSessionExchangeSignsCanonicalRepoReadFixture(t *testing.T) {
	const (
		secret           = "workload-session-assertion-secret"
		issuer           = "https://multica.test"
		issuerInstanceID = "multica-test"
		keyID            = "current-key"
		audience         = "urn:ags:workload-session-exchange:v1"
	)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", secret)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", issuer)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", issuerInstanceID)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_KEY_ID", keyID)

	issueID := createExternalPRTestIssue(t, "session assertion issue", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "session-assertion-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	requestBody := map[string]any{
		"purpose":                "ags_session_exchange",
		"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
		"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
		"requested_operation":    map[string]any{"name": "repo.read", "constraints": map[string]any{}},
		"requested_capabilities": []string{"repo:read"},
		"requested_ttl":          "15m",
	}
	issue := func() (string, jwt.MapClaims) {
		req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", requestBody)
		authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
		rr := httptest.NewRecorder()
		testHandler.CreateWorkloadAssertion(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		var response workloadAssertionResponse
		if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Purpose != "ags_session_exchange" || response.Assertion == "" || response.Workload.WorkloadContext == nil || response.Workload.Authority == nil {
			t.Fatalf("response = %#v", response)
		}
		if response.Workload.WorkloadContext.Schema != workloadContextSchema || response.Workload.WorkloadContext.IssuerInstanceID != issuerInstanceID || response.Workload.WorkloadContext.AgentID != agentID || response.Workload.WorkloadContext.TaskID != taskID || response.Workload.WorkloadContext.RunID != taskID {
			t.Fatalf("response workload context = %#v", response.Workload.WorkloadContext)
		}
		if response.Workload.Actor == nil || response.Workload.Actor.Type != "agent" || response.Workload.Actor.ID != agentID {
			t.Fatalf("response actor = %#v", response.Workload.Actor)
		}
		if response.Workload.Authority.Schema != workloadAuthoritySchema || response.Workload.Authority.TeamIdentityID != testWorkspaceID || response.Workload.Authority.MembershipEpoch < 1 || response.Workload.Authority.PolicyClass != workspaceDefaultPolicyClass {
			t.Fatalf("response workload authority = %#v", response.Workload.Authority)
		}
		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(response.Assertion, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithAudience(audience), jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
		if err != nil || !token.Valid {
			t.Fatalf("parse assertion: valid=%v err=%v", token != nil && token.Valid, err)
		}
		return response.Assertion, claims
	}

	firstToken, first := issue()
	secondToken, second := issue()
	if firstToken == secondToken || first["jti"] == second["jti"] {
		t.Fatal("each session assertion must be a distinct token instance")
	}
	if first["purpose"] != "ags_session_exchange" || first["aud"] != audience || first["source"] != "task_token" || first["requested_ttl"] != "15m" {
		t.Fatalf("unexpected session claims: %#v", first)
	}
	capabilities, ok := first["requested_capabilities"].([]any)
	if !ok || len(capabilities) != 1 || capabilities[0] != "repo:read" {
		t.Fatalf("capabilities = %#v", first["requested_capabilities"])
	}
	target, ok := first["target"].(map[string]any)
	if !ok || target["provider"] != "ags" || target["instance"] != "mini" || target["repository"] != "jackie/agent-kit" {
		t.Fatalf("target = %#v", first["target"])
	}
	workload, ok := first["workload"].(map[string]any)
	if !ok {
		t.Fatalf("workload = %#v", first["workload"])
	}
	context, ok := workload["workload_context"].(map[string]any)
	if !ok || context["schema"] != workloadContextSchema || context["issuer_instance_id"] != issuerInstanceID || context["workspace_id"] != testWorkspaceID || context["agent_id"] != agentID || context["task_id"] != taskID || context["run_id"] != taskID || context["correlation_id"] != first["jti"] {
		t.Fatalf("workload context = %#v", workload["workload_context"])
	}
	authority, ok := workload["authority"].(map[string]any)
	epoch, epochOK := authority["membership_epoch"].(float64)
	if !ok || !epochOK || authority["schema"] != workloadAuthoritySchema || authority["team_identity_id"] != testWorkspaceID || epoch < 1 || authority["policy_class"] != workspaceDefaultPolicyClass {
		t.Fatalf("workload authority = %#v", workload["authority"])
	}
	scope, ok := first["scope"].(map[string]any)
	if !ok || scope["schema"] != workloadScopeSchema {
		t.Fatalf("scope = %#v", first["scope"])
	}
	resource, ok := scope["resource"].(map[string]any)
	if !ok || resource["service"] != "ags" || resource["repository"] != "jackie/agent-kit" {
		t.Fatalf("scope resource = %#v", scope["resource"])
	}
	operation, ok := scope["operation"].(map[string]any)
	if !ok || operation["name"] != "repo.read" {
		t.Fatalf("scope operation = %#v", scope["operation"])
	}
}

func TestCreateWorkloadAssertionSessionExchangeSignsAgentKitProductionConstraintFixtures(t *testing.T) {
	const (
		secret           = "workload-agentkit-fixture-secret"
		issuer           = "urn:multica:deployment:agentkit-fixture-test"
		issuerInstanceID = "multica-agentkit-fixture-test"
	)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", secret)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", issuer)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", issuerInstanceID)

	issueID := createExternalPRTestIssue(t, "AgentKit operation assertion fixtures", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "agentkit-operation-fixture-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for _, tc := range []struct {
		name         string
		operation    string
		constraints  map[string]any
		capabilities []string
	}{
		{name: "repo read", operation: "repo.read", constraints: map[string]any{}, capabilities: []string{"repo:read"}},
		{name: "git read", operation: "git.read", constraints: map[string]any{}, capabilities: []string{"repo:read"}},
		{name: "git push", operation: "git.push", constraints: map[string]any{}, capabilities: []string{"repo:read", "repo:write"}},
		{name: "pr create", operation: "pr.create", constraints: map[string]any{"base_ref": "main", "head_ref": "agent/delegated-pr"}, capabilities: []string{"pr:create", "repo:read"}},
		{name: "pr read by number", operation: "pr.read", constraints: map[string]any{"pull_request_number": float64(41)}, capabilities: []string{"repo:read"}},
		{name: "pr read by head", operation: "pr.read", constraints: map[string]any{"head_ref": "agent/delegated-pr"}, capabilities: []string{"repo:read"}},
		{name: "pr rebase", operation: "pr.rebase", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "expected_head_sha": sha, "expected_base_sha": sha}, capabilities: []string{"repo:read", "repo:write"}},
		{name: "review read", operation: "review.read", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52)}, capabilities: []string{"repo:read"}},
		{name: "ci read repository list", operation: "ci.read", constraints: map[string]any{}, capabilities: []string{"repo:read"}},
		{name: "ci read run log", operation: "ci.read", constraints: map[string]any{"run_id": float64(73)}, capabilities: []string{"repo:read"}},
		{name: "ci read PR runs", operation: "ci.read", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52)}, capabilities: []string{"repo:read"}},
		{name: "ci read PR runs at head", operation: "ci.read", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "head_sha": sha}, capabilities: []string{"repo:read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
				"purpose":                "ags_session_exchange",
				"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
				"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
				"requested_operation":    map[string]any{"name": tc.operation, "constraints": tc.constraints},
				"requested_capabilities": tc.capabilities,
			})
			authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
			rr := httptest.NewRecorder()
			testHandler.CreateWorkloadAssertion(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
			var response workloadAssertionResponse
			if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			claims := jwt.MapClaims{}
			token, err := jwt.ParseWithClaims(response.Assertion, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithAudience(workloadAssertionSessionExchangeAudience), jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
			if err != nil || !token.Valid {
				t.Fatalf("parse assertion: valid=%v err=%v", token != nil && token.Valid, err)
			}
			scope, ok := claims["scope"].(map[string]any)
			if !ok {
				t.Fatalf("scope = %#v", claims["scope"])
			}
			operation, ok := scope["operation"].(map[string]any)
			if !ok || operation["name"] != tc.operation || !reflect.DeepEqual(operation["constraints"], tc.constraints) {
				t.Fatalf("signed operation = %#v, want name=%s constraints=%#v", operation, tc.operation, tc.constraints)
			}
			workload, ok := claims["workload"].(map[string]any)
			authority, authorityOK := workload["authority"].(map[string]any)
			wantPolicy := workspaceDefaultPolicyClass
			if !ok || !authorityOK || authority["policy_class"] != wantPolicy {
				t.Fatalf("signed authority = %#v, want policy_class=%s", workload["authority"], wantPolicy)
			}
		})
	}
}

func TestCreateWorkloadAssertionSessionExchangeRejectsDeferredOperationsBeforeSigning(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "deferred-operation-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "urn:multica:deployment:deferred-operation-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-deferred-operation-test")

	issueID := createExternalPRTestIssue(t, "deferred operation assertion", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "deferred-operation-assertion-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	for _, operation := range []string{"review.submit", "repo.admin", "repo.create"} {
		t.Run(operation, func(t *testing.T) {
			req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
				"purpose":                "ags_session_exchange",
				"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
				"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
				"requested_operation":    map[string]any{"name": operation, "constraints": map[string]any{}},
				"requested_capabilities": sessionOperationCapabilities[operation],
			})
			authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
			rr := httptest.NewRecorder()
			testHandler.CreateWorkloadAssertion(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
			if json.Valid(rr.Body.Bytes()) && strings.Contains(rr.Body.String(), `"assertion"`) {
				t.Fatalf("deferred operation returned an assertion: %s", rr.Body.String())
			}
		})
	}

	t.Run("pr.merge feature off remains deferred and default class is never signed", func(t *testing.T) {
		t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "0")
		req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
			"purpose":                "ags_session_exchange",
			"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
			"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
			"requested_operation":    map[string]any{"name": "pr.merge", "constraints": map[string]any{"pull_request_number": 41, "forgejo_pull_request_number": 52, "expected_head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "merge_method": "fast-forward-only"}},
			"requested_capabilities": []string{"repo:read", "repo:write"},
		})
		authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
		rr := httptest.NewRecorder()
		testHandler.CreateWorkloadAssertion(rr, req)
		if rr.Code != http.StatusBadRequest || strings.Contains(rr.Body.String(), `"assertion"`) {
			t.Fatalf("disabled merge status=%d body=%s", rr.Code, rr.Body.String())
		}
		var count int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM workload_pr_merge_delegation WHERE task_id=$1`, taskID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("disabled merge authority rows=%d err=%v", count, err)
		}
	})
}

func TestCreateWorkloadAssertionSessionExchangeSignsExactPRRebaseScope(t *testing.T) {
	const (
		secret           = "workload-pr-rebase-assertion-secret"
		issuer           = "urn:multica:deployment:pr-rebase-bridge-test"
		issuerInstanceID = "multica-pr-rebase-test"
		audience         = "urn:ags:workload-session-exchange:v1"
	)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", secret)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", issuer)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", issuerInstanceID)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_KEY_ID", "pr-rebase-bridge-key")

	issueID := createExternalPRTestIssue(t, "pr.rebase assertion bridge", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "pr-rebase-assertion-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	constraints := map[string]any{
		"pull_request_number":         41,
		"forgejo_pull_request_number": 52,
		"expected_head_sha":           "1111111111111111111111111111111111111111",
		"expected_base_sha":           "2222222222222222222222222222222222222222",
	}
	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose":                "ags_session_exchange",
		"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
		"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
		"requested_operation":    map[string]any{"name": "pr.rebase", "constraints": constraints},
		"requested_capabilities": []string{"repo:read", "repo:write"},
	})
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var response workloadAssertionResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AssertionType != workloadAssertionType || response.Purpose != workloadAssertionPurposeSessionExchange || response.Assertion == "" {
		t.Fatalf("response contract = %#v", response)
	}
	if response.Workload.WorkloadContext == nil || response.Workload.Authority == nil || response.Workload.Authority.PolicyClass != workspaceDefaultPolicyClass {
		t.Fatalf("response workload authority = %#v", response.Workload)
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(response.Assertion, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithAudience(audience), jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		t.Fatalf("parse assertion: valid=%v err=%v", token != nil && token.Valid, err)
	}
	if token.Header["typ"] != workloadAssertionJWTType || token.Header["kid"] != "pr-rebase-bridge-key" {
		t.Fatalf("JWT header = %#v", token.Header)
	}
	capabilities, ok := claims["requested_capabilities"].([]any)
	if !ok || len(capabilities) != 2 || capabilities[0] != "repo:read" || capabilities[1] != "repo:write" {
		t.Fatalf("requested capabilities = %#v", claims["requested_capabilities"])
	}
	scope, ok := claims["scope"].(map[string]any)
	if !ok || scope["schema"] != workloadScopeSchema {
		t.Fatalf("scope = %#v", claims["scope"])
	}
	operation, ok := scope["operation"].(map[string]any)
	if !ok || operation["name"] != "pr.rebase" {
		t.Fatalf("operation = %#v", scope["operation"])
	}
	signedConstraints, ok := operation["constraints"].(map[string]any)
	if !ok || len(signedConstraints) != len(constraints) || signedConstraints["pull_request_number"] != float64(41) || signedConstraints["forgejo_pull_request_number"] != float64(52) || signedConstraints["expected_head_sha"] != constraints["expected_head_sha"] || signedConstraints["expected_base_sha"] != constraints["expected_base_sha"] {
		t.Fatalf("operation constraints = %#v", operation["constraints"])
	}
	workload, ok := claims["workload"].(map[string]any)
	if !ok || workload["workload_context"] == nil || workload["authority"] == nil || workload["scope"] != nil {
		t.Fatalf("nested workload = %#v", claims["workload"])
	}
	if claims["workload_context"] != nil || claims["authority"] != nil {
		t.Fatalf("workload context and authority must remain nested: %#v", claims)
	}
	if _, present := claims["requested_ttl"]; present {
		t.Fatalf("absent requested_ttl must remain absent: %#v", claims)
	}
}

func TestNormalizeRequestedTTL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "absent"},
		{name: "one second", raw: `"1s"`, want: "1s", ok: true},
		{name: "fifteen minutes", raw: `"15m"`, want: "15m", ok: true},
		{name: "surrounding whitespace", raw: `" 15m "`},
		{name: "zero", raw: `"0s"`},
		{name: "over maximum", raw: `"16m"`},
		{name: "composite", raw: `"14m60s"`},
		{name: "fraction", raw: `"0.5m"`},
		{name: "leading zero", raw: `"015m"`},
		{name: "unknown unit", raw: `"15d"`},
		{name: "empty", raw: `""`},
		{name: "null", raw: `null`},
		{name: "number", raw: `900`},
		{name: "secret shaped", raw: `"eyJabc.def.ghi"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, present, err := normalizeRequestedTTL(json.RawMessage(tc.raw))
			if tc.ok {
				if err != nil || !present || got != tc.want {
					t.Fatalf("normalizeRequestedTTL(%s) = %q, %v, %v", tc.raw, got, present, err)
				}
				return
			}
			if tc.raw == "" {
				if err != nil || present || got != "" {
					t.Fatalf("absent requested_ttl = %q, %v, %v", got, present, err)
				}
				return
			}
			if err == nil || present {
				t.Fatalf("normalizeRequestedTTL(%s) succeeded, want failure", tc.raw)
			}
		})
	}
}

func TestCreateWorkloadAssertionRejectsUnknownAndNullTTLFields(t *testing.T) {
	for _, body := range []map[string]any{
		{"purpose": "ags_session_exchange", "unknown": true},
		{"purpose": "ags_session_exchange", "requested_ttl": nil},
		{"purpose": "ags_session_exchange", "requested_ttl": "ags_sess_secret"},
	} {
		req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", body)
		req.Header.Set("X-Actor-Source", "task_token")
		rr := httptest.NewRecorder()
		testHandler.CreateWorkloadAssertion(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body=%#v status=%d response=%s", body, rr.Code, rr.Body.String())
		}
	}
}

func TestNormalizeSessionExchangeScopeMatchesDefaultTeamV4AgentKitOperations(t *testing.T) {
	target := workloadAssertionTarget{Provider: "ags", Instance: "mini", Repository: "jackie/agent-kit"}
	resource := &workloadAssertionResource{Service: "ags", Repository: target.Repository}
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	seenOperations := map[string]bool{}
	for _, tc := range []struct {
		name         string
		operation    string
		constraints  map[string]any
		capabilities []string
	}{
		{name: "repo read exact empty", operation: "repo.read", constraints: map[string]any{}, capabilities: []string{"repo:read"}},
		{name: "git read exact empty", operation: "git.read", constraints: map[string]any{}, capabilities: []string{"repo:read"}},
		{name: "git push exact empty", operation: "git.push", constraints: map[string]any{}, capabilities: []string{"repo:read", "repo:write"}},
		{name: "pr create exact refs", operation: "pr.create", constraints: map[string]any{"base_ref": "main", "head_ref": "agent/delegated-pr"}, capabilities: []string{"pr:create", "repo:read"}},
		{name: "pr create fully qualified refs", operation: "pr.create", constraints: map[string]any{"base_ref": "refs/heads/main", "head_ref": "refs/heads/agent/delegated-pr"}, capabilities: []string{"pr:create", "repo:read"}},
		{name: "pr read numbered variant", operation: "pr.read", constraints: map[string]any{"pull_request_number": float64(41)}, capabilities: []string{"repo:read"}},
		{name: "pr read head variant", operation: "pr.read", constraints: map[string]any{"head_ref": "agent/delegated-pr"}, capabilities: []string{"repo:read"}},
		{name: "pr read fully qualified head variant", operation: "pr.read", constraints: map[string]any{"head_ref": "refs/heads/agent/delegated-pr"}, capabilities: []string{"repo:read"}},
		{name: "pr rebase exact intent", operation: "pr.rebase", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "expected_head_sha": sha, "expected_base_sha": sha}, capabilities: []string{"repo:read", "repo:write"}},
		{name: "pr merge exact intent", operation: "pr.merge", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "expected_head_sha": sha, "merge_method": "fast-forward-only"}, capabilities: []string{"repo:read", "repo:write"}},
		{name: "review read exact projection", operation: "review.read", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52)}, capabilities: []string{"repo:read"}},
		{name: "ci read repository list", operation: "ci.read", constraints: map[string]any{}, capabilities: []string{"repo:read"}},
		{name: "ci read run", operation: "ci.read", constraints: map[string]any{"run_id": float64(73)}, capabilities: []string{"repo:read"}},
		{name: "ci read exact projection", operation: "ci.read", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52)}, capabilities: []string{"repo:read"}},
		{name: "ci read exact head", operation: "ci.read", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "head_sha": sha}, capabilities: []string{"repo:read"}},
	} {
		seenOperations[tc.operation] = true
		t.Run(tc.name, func(t *testing.T) {
			scope, err := normalizeSessionExchangeScope(workloadAssertionRequest{
				RequestedResource:     resource,
				RequestedOperation:    &workloadAssertionOperation{Name: tc.operation, Constraints: tc.constraints},
				RequestedCapabilities: tc.capabilities,
			}, target)
			if err != nil {
				t.Fatalf("normalizeSessionExchangeScope: %v", err)
			}
			if scope.Operation.Name != tc.operation || !reflect.DeepEqual(scope.Operation.Constraints, tc.constraints) {
				t.Fatalf("operation = %#v, want name=%s constraints=%#v", scope.Operation, tc.operation, tc.constraints)
			}
		})
	}
	for _, operation := range []string{"repo.read", "git.read", "git.push", "pr.create", "pr.read", "pr.rebase", "pr.merge", "review.read", "ci.read"} {
		if !seenOperations[operation] {
			t.Errorf("positive matrix is missing default operation %q", operation)
		}
	}
	if len(seenOperations) != 9 {
		t.Fatalf("positive matrix operations = %#v, want exactly nine accepted operations", seenOperations)
	}
}

func TestNormalizeAgentKitForgejoCommandConstraintFixtures(t *testing.T) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name        string
		operation   workloadAssertionOperation
		wantAllowed bool
	}{
		{name: "forgejo prs state list", operation: workloadAssertionOperation{Name: "pr.read", Constraints: map[string]any{"state": "open"}}},
		{name: "forgejo prs numbered lookup", operation: workloadAssertionOperation{Name: "pr.read", Constraints: map[string]any{"pull_request_number": float64(41)}}, wantAllowed: true},
		{name: "forgejo prs head lookup", operation: workloadAssertionOperation{Name: "pr.read", Constraints: map[string]any{"head_ref": "agent/delegated-pr"}}, wantAllowed: true},
		{name: "forgejo runs repository list", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{}}, wantAllowed: true},
		{name: "forgejo runs event filter", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{"event": "push"}}},
		{name: "forgejo runs SHA filter", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{"head_sha": sha}}},
		{name: "forgejo runs PR projection", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "head_sha": sha}}, wantAllowed: true},
		{name: "forgejo log run", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{"run_id": float64(73)}}, wantAllowed: true},
		{name: "forgejo log mixed run and SHA", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{"run_id": float64(73), "head_sha": sha}}},
		{name: "forgejo runs unknown filter", operation: workloadAssertionOperation{Name: "ci.read", Constraints: map[string]any{"status": "success"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeRequestedOperation(tc.operation)
			if tc.wantAllowed && err != nil {
				t.Fatalf("production-shaped operation rejected: %v", err)
			}
			if !tc.wantAllowed && err == nil {
				t.Fatal("non-canonical production-shaped operation accepted")
			}
		})
	}
}

func TestNormalizeRequestedOperationDefaultTeamV4NegativeMatrix(t *testing.T) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	prCreate := func() map[string]any { return map[string]any{"base_ref": "main", "head_ref": "agent/delegated-pr"} }
	reviewRead := func() map[string]any {
		return map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52)}
	}
	ciRead := func() map[string]any {
		return map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "head_sha": sha}
	}
	with := func(input map[string]any, key string, value any) map[string]any {
		out := make(map[string]any, len(input)+1)
		for existingKey, existingValue := range input {
			out[existingKey] = existingValue
		}
		out[key] = value
		return out
	}
	without := func(input map[string]any, key string) map[string]any {
		out := with(input, key, input[key])
		delete(out, key)
		return out
	}

	cases := []struct {
		name        string
		operation   string
		constraints map[string]any
	}{
		{name: "unknown operation", operation: "pr.reads", constraints: map[string]any{}},
		{name: "mixed operation casing", operation: "PR.READ", constraints: map[string]any{"pull_request_number": float64(41)}},
		{name: "operation whitespace", operation: " pr.read", constraints: map[string]any{"pull_request_number": float64(41)}},
		{name: "null constraints", operation: "repo.read", constraints: nil},
		{name: "repo read nonempty", operation: "repo.read", constraints: map[string]any{"pull_request_number": float64(41)}},
		{name: "git read nonempty", operation: "git.read", constraints: map[string]any{"head_ref": "main"}},
		{name: "git push secret", operation: "git.push", constraints: map[string]any{"credential": "mat_secret"}},
		{name: "pr create empty", operation: "pr.create", constraints: map[string]any{}},
		{name: "pr create missing base", operation: "pr.create", constraints: without(prCreate(), "base_ref")},
		{name: "pr create missing head", operation: "pr.create", constraints: without(prCreate(), "head_ref")},
		{name: "pr create old exact head", operation: "pr.create", constraints: with(prCreate(), "exact_head", sha)},
		{name: "pr create base wrong type", operation: "pr.create", constraints: with(prCreate(), "base_ref", float64(1))},
		{name: "pr create head null", operation: "pr.create", constraints: with(prCreate(), "head_ref", nil)},
		{name: "pr create base secret", operation: "pr.create", constraints: with(prCreate(), "base_ref", "ags_sess_secret")},
		{name: "pr create non-head ref", operation: "pr.create", constraints: with(prCreate(), "base_ref", "refs/tags/main")},
		{name: "pr create invalid head", operation: "pr.create", constraints: with(prCreate(), "head_ref", "agent/../main")},
		{name: "pr read empty", operation: "pr.read", constraints: map[string]any{}},
		{name: "pr read mixed variants", operation: "pr.read", constraints: map[string]any{"pull_request_number": float64(41), "head_ref": "agent/delegated-pr"}},
		{name: "pr read old exact head", operation: "pr.read", constraints: map[string]any{"exact_head": sha}},
		{name: "pr read number string", operation: "pr.read", constraints: map[string]any{"pull_request_number": "41"}},
		{name: "pr read number zero", operation: "pr.read", constraints: map[string]any{"pull_request_number": float64(0)}},
		{name: "pr read number fraction", operation: "pr.read", constraints: map[string]any{"pull_request_number": 1.5}},
		{name: "pr read number unsafe", operation: "pr.read", constraints: map[string]any{"pull_request_number": float64(9007199254740992)}},
		{name: "pr read head null", operation: "pr.read", constraints: map[string]any{"head_ref": nil}},
		{name: "pr read head secret", operation: "pr.read", constraints: map[string]any{"head_ref": "mat_secret"}},
		{name: "review read missing provider number", operation: "review.read", constraints: without(reviewRead(), "forgejo_pull_request_number")},
		{name: "review read old exact head", operation: "review.read", constraints: with(reviewRead(), "exact_head", true)},
		{name: "review read wrong type", operation: "review.read", constraints: with(reviewRead(), "pull_request_number", "41")},
		{name: "review read null", operation: "review.read", constraints: with(reviewRead(), "forgejo_pull_request_number", nil)},
		{name: "ci read missing provider number", operation: "ci.read", constraints: without(ciRead(), "forgejo_pull_request_number")},
		{name: "ci read old exact head", operation: "ci.read", constraints: with(ciRead(), "exact_head", true)},
		{name: "ci read event only", operation: "ci.read", constraints: map[string]any{"event": "push"}},
		{name: "ci read sha only", operation: "ci.read", constraints: map[string]any{"head_sha": sha}},
		{name: "ci read unknown only", operation: "ci.read", constraints: map[string]any{"limit": float64(100)}},
		{name: "ci read event mixed with projection", operation: "ci.read", constraints: with(ciRead(), "event", "push")},
		{name: "ci read run mixed with projection", operation: "ci.read", constraints: with(ciRead(), "run_id", float64(73))},
		{name: "ci read run mixed with head", operation: "ci.read", constraints: map[string]any{"run_id": float64(73), "head_sha": sha}},
		{name: "ci read run string", operation: "ci.read", constraints: map[string]any{"run_id": "73"}},
		{name: "ci read run zero", operation: "ci.read", constraints: map[string]any{"run_id": float64(0)}},
		{name: "ci read run fraction", operation: "ci.read", constraints: map[string]any{"run_id": 7.3}},
		{name: "ci read run unsafe", operation: "ci.read", constraints: map[string]any{"run_id": float64(9007199254740992)}},
		{name: "ci read head wrong type", operation: "ci.read", constraints: with(ciRead(), "head_sha", true)},
		{name: "ci read head null", operation: "ci.read", constraints: with(ciRead(), "head_sha", nil)},
		{name: "ci read head uppercase", operation: "ci.read", constraints: with(ciRead(), "head_sha", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")},
		{name: "ci read head secret", operation: "ci.read", constraints: with(ciRead(), "head_sha", "secret")},
		{name: "merge exact empty rejected", operation: "pr.merge", constraints: map[string]any{}},
		{name: "merge incomplete rejected", operation: "pr.merge", constraints: map[string]any{"pull_request_number": float64(41)}},
		{name: "merge wrong capabilities shape rejected", operation: "pr.merge", constraints: map[string]any{"pull_request_number": float64(41), "forgejo_pull_request_number": float64(52), "expected_head_sha": sha, "merge_method": "octopus"}},
		{name: "deferred review submit exact empty rejected", operation: "review.submit", constraints: map[string]any{}},
		{name: "deferred repo admin exact empty rejected", operation: "repo.admin", constraints: map[string]any{}},
		{name: "deferred repo create exact empty rejected", operation: "repo.create", constraints: map[string]any{}},
		{name: "deferred review submit nonempty rejected", operation: "review.submit", constraints: map[string]any{"pull_request_number": float64(41)}},
		{name: "deferred repo admin nonempty rejected", operation: "repo.admin", constraints: map[string]any{"action": "onboard_forgejo"}},
		{name: "deferred repo create nonempty rejected", operation: "repo.create", constraints: map[string]any{"name": "new-repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeRequestedOperation(workloadAssertionOperation{Name: tc.operation, Constraints: tc.constraints}); err == nil {
				t.Fatal("normalizeRequestedOperation succeeded, want fail-closed rejection")
			}
		})
	}
}

func TestNormalizeSessionExchangeScopePRRebaseFailsClosed(t *testing.T) {
	target := workloadAssertionTarget{Provider: "ags", Instance: "mini", Repository: "jackie/agent-kit"}
	resource := &workloadAssertionResource{Service: "ags", Repository: "jackie/agent-kit"}
	validConstraints := map[string]any{
		"pull_request_number":         float64(41),
		"forgejo_pull_request_number": float64(52),
		"expected_head_sha":           "1111111111111111111111111111111111111111",
		"expected_base_sha":           "2222222222222222222222222222222222222222",
	}
	operation := func(name string, constraints map[string]any) *workloadAssertionOperation {
		return &workloadAssertionOperation{Name: name, Constraints: constraints}
	}
	withConstraint := func(key string, value any) map[string]any {
		out := make(map[string]any, len(validConstraints))
		for existingKey, existingValue := range validConstraints {
			out[existingKey] = existingValue
		}
		out[key] = value
		return out
	}
	withoutConstraint := func(key string) map[string]any {
		out := withConstraint(key, validConstraints[key])
		delete(out, key)
		return out
	}
	request := func(name string, constraints map[string]any, capabilities ...string) workloadAssertionRequest {
		return workloadAssertionRequest{RequestedResource: resource, RequestedOperation: operation(name, constraints), RequestedCapabilities: capabilities}
	}

	cases := []struct {
		name string
		req  workloadAssertionRequest
	}{
		{name: "missing capabilities", req: workloadAssertionRequest{RequestedResource: resource, RequestedOperation: operation("pr.rebase", validConstraints)}},
		{name: "missing operation", req: workloadAssertionRequest{RequestedResource: resource, RequestedCapabilities: []string{"repo:read", "repo:write"}}},
		{name: "wrong capability", req: request("pr.rebase", validConstraints, "repo:read")},
		{name: "extra capability", req: request("pr.rebase", validConstraints, "repo:read", "repo:write", "repo:admin")},
		{name: "wrong operation casing", req: request("PR.REBASE", validConstraints, "repo:read", "repo:write")},
		{name: "unknown operation", req: request("pr.rebases", validConstraints, "repo:read", "repo:write")},
		{name: "empty constraints", req: request("pr.rebase", map[string]any{}, "repo:read", "repo:write")},
		{name: "missing pull request number", req: request("pr.rebase", withoutConstraint("pull_request_number"), "repo:read", "repo:write")},
		{name: "missing forgejo pull request number", req: request("pr.rebase", withoutConstraint("forgejo_pull_request_number"), "repo:read", "repo:write")},
		{name: "missing expected head sha", req: request("pr.rebase", withoutConstraint("expected_head_sha"), "repo:read", "repo:write")},
		{name: "missing expected base sha", req: request("pr.rebase", withoutConstraint("expected_base_sha"), "repo:read", "repo:write")},
		{name: "unknown constraint", req: request("pr.rebase", withConstraint("unexpected_scope", "write"), "repo:read", "repo:write")},
		{name: "secret shaped constraint key", req: request("pr.rebase", withConstraint("credential_hint", "secret"), "repo:read", "repo:write")},
		{name: "legacy base ref", req: request("pr.rebase", withConstraint("base_ref", "refs/heads/main"), "repo:read", "repo:write")},
		{name: "legacy head ref", req: request("pr.rebase", withConstraint("head_ref", "refs/heads/feature"), "repo:read", "repo:write")},
		{name: "legacy exact head", req: request("pr.rebase", withConstraint("exact_head", validConstraints["expected_head_sha"]), "repo:read", "repo:write")},
		{name: "pull request number string", req: request("pr.rebase", withConstraint("pull_request_number", "41"), "repo:read", "repo:write")},
		{name: "pull request number boolean", req: request("pr.rebase", withConstraint("pull_request_number", true), "repo:read", "repo:write")},
		{name: "pull request number zero", req: request("pr.rebase", withConstraint("pull_request_number", float64(0)), "repo:read", "repo:write")},
		{name: "pull request number negative", req: request("pr.rebase", withConstraint("pull_request_number", float64(-1)), "repo:read", "repo:write")},
		{name: "pull request number fraction", req: request("pr.rebase", withConstraint("pull_request_number", 1.5), "repo:read", "repo:write")},
		{name: "pull request number overflow", req: request("pr.rebase", withConstraint("pull_request_number", float64(9007199254740992)), "repo:read", "repo:write")},
		{name: "forgejo pull request number string", req: request("pr.rebase", withConstraint("forgejo_pull_request_number", "52"), "repo:read", "repo:write")},
		{name: "forgejo pull request number boolean", req: request("pr.rebase", withConstraint("forgejo_pull_request_number", false), "repo:read", "repo:write")},
		{name: "forgejo pull request number zero", req: request("pr.rebase", withConstraint("forgejo_pull_request_number", float64(0)), "repo:read", "repo:write")},
		{name: "forgejo pull request number negative", req: request("pr.rebase", withConstraint("forgejo_pull_request_number", float64(-1)), "repo:read", "repo:write")},
		{name: "forgejo pull request number fraction", req: request("pr.rebase", withConstraint("forgejo_pull_request_number", 2.5), "repo:read", "repo:write")},
		{name: "forgejo pull request number overflow", req: request("pr.rebase", withConstraint("forgejo_pull_request_number", float64(9007199254740992)), "repo:read", "repo:write")},
		{name: "expected head sha boolean", req: request("pr.rebase", withConstraint("expected_head_sha", true), "repo:read", "repo:write")},
		{name: "expected head sha uppercase", req: request("pr.rebase", withConstraint("expected_head_sha", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), "repo:read", "repo:write")},
		{name: "expected head sha short", req: request("pr.rebase", withConstraint("expected_head_sha", "abc123"), "repo:read", "repo:write")},
		{name: "expected head sha nonhex", req: request("pr.rebase", withConstraint("expected_head_sha", "gggggggggggggggggggggggggggggggggggggggg"), "repo:read", "repo:write")},
		{name: "expected head sha secret shaped", req: request("pr.rebase", withConstraint("expected_head_sha", "secret"), "repo:read", "repo:write")},
		{name: "expected base sha number", req: request("pr.rebase", withConstraint("expected_base_sha", float64(2)), "repo:read", "repo:write")},
		{name: "expected base sha uppercase", req: request("pr.rebase", withConstraint("expected_base_sha", "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"), "repo:read", "repo:write")},
		{name: "expected base sha short", req: request("pr.rebase", withConstraint("expected_base_sha", "def456"), "repo:read", "repo:write")},
		{name: "expected base sha nonhex", req: request("pr.rebase", withConstraint("expected_base_sha", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"), "repo:read", "repo:write")},
		{name: "expected base sha secret shaped", req: request("pr.rebase", withConstraint("expected_base_sha", "secret"), "repo:read", "repo:write")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeSessionExchangeScope(tc.req, target); err == nil {
				t.Fatal("normalizeSessionExchangeScope succeeded, want fail-closed rejection")
			}
		})
	}
}

func TestPRRebaseOperationBridgeMatchesDefaultTeamAuthorityContract(t *testing.T) {
	capabilities, ok := sessionOperationCapabilities["pr.rebase"]
	if !ok || !sameStrings(capabilities, []string{"repo:read", "repo:write"}) {
		t.Fatalf("pr.rebase capabilities = %#v", capabilities)
	}
	if workspaceDefaultPolicyClass != "multica.workspace.default.v1" {
		t.Fatalf("default policy class = %q", workspaceDefaultPolicyClass)
	}
	if len(prRebaseConstraintKeys) != 4 {
		t.Fatalf("pr.rebase constraint registry = %#v", prRebaseConstraintKeys)
	}
	for _, key := range []string{"pull_request_number", "forgejo_pull_request_number", "expected_head_sha", "expected_base_sha"} {
		if _, ok := prRebaseConstraintKeys[key]; !ok {
			t.Fatalf("missing pr.rebase constraint key %q", key)
		}
	}
}

func TestCreateWorkloadAssertionSessionExchangeRejectsIncompleteScope(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-session-assertion-secret")
	cases := []map[string]any{
		{"purpose": "ags_session_exchange", "target": map[string]any{"provider": "ags", "repository": "jackie/agent-kit"}, "requested_capabilities": []string{"repo:read"}},
		{"purpose": "ags_session_exchange", "target": map[string]any{"provider": "forgejo", "instance": "mini", "repository": "jackie/agent-kit"}, "requested_capabilities": []string{"repo:read"}},
		{"purpose": "ags_session_exchange", "target": map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"}, "requested_capabilities": []string{}},
		{"purpose": "ags_session_exchange", "target": map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"}, "requested_resource": map[string]any{"service": "ags", "repository": "jackie/agent-kit"}, "requested_capabilities": []string{"repo:read"}},
		{"purpose": "ags_session_exchange", "target": map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"}, "requested_resource": map[string]any{"service": "ags", "repository": "jackie/other"}, "requested_operation": map[string]any{"name": "repo.read", "constraints": map[string]any{}}, "requested_capabilities": []string{"repo:read"}},
		{"purpose": "ags_session_exchange", "target": map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"}, "requested_resource": map[string]any{"service": "ags", "repository": "jackie/agent-kit"}, "requested_operation": map[string]any{"name": "git.push", "constraints": map[string]any{}}, "requested_capabilities": []string{"repo:read"}},
	}

	for index, body := range cases {
		req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", body)
		req.Header.Set("X-Actor-Source", "task_token")
		rr := httptest.NewRecorder()
		testHandler.CreateWorkloadAssertion(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("case %d status=%d body=%s", index, rr.Code, rr.Body.String())
		}
	}
}

func TestCreateWorkloadAssertionSessionExchangeFailsClosedWithoutAuthority(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-session-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "https://multica.test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-test")

	issueID := createExternalPRTestIssue(t, "missing workload authority", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "missing-workload-authority-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	if _, err := testPool.Exec(context.Background(), `DELETE FROM workspace_workload_authority WHERE workspace_id=$1`, testWorkspaceID); err != nil {
		t.Fatalf("delete workload authority: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
			VALUES ($1, $1, 1, $2)
			ON CONFLICT (workspace_id) DO NOTHING`, testWorkspaceID, workspaceDefaultPolicyClass)
	})

	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose":                "ags_session_exchange",
		"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
		"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
		"requested_operation":    map[string]any{"name": "repo.read", "constraints": map[string]any{}},
		"requested_capabilities": []string{"repo:read"},
	})
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateWorkloadAssertionSessionExchangeRejectsEqualIssuerMapping(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-session-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "multica-equal-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-equal-test")
	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose":                "ags_session_exchange",
		"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
		"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
		"requested_operation":    map[string]any{"name": "repo.read", "constraints": map[string]any{}},
		"requested_capabilities": []string{"repo:read"},
	})
	req.Header.Set("X-Actor-Source", "task_token")
	rr := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateWorkloadAssertionSessionExchangeRequiresConfiguredIssuer(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-session-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-test")
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
		VALUES ($1, $1, 1, $2)
		ON CONFLICT (workspace_id) DO NOTHING`, testWorkspaceID, workspaceDefaultPolicyClass); err != nil {
		t.Fatalf("ensure workload authority: %v", err)
	}

	issueID := createExternalPRTestIssue(t, "missing workload issuer", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "missing-workload-issuer-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose":                "ags_session_exchange",
		"target":                 map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
		"requested_resource":     map[string]any{"service": "ags", "repository": "jackie/agent-kit"},
		"requested_operation":    map[string]any{"name": "repo.read", "constraints": map[string]any{}},
		"requested_capabilities": []string{"repo:read"},
	})
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestNormalizeSessionExchangeScopePreservesOnlyExpressibleLegacyCapabilities(t *testing.T) {
	target := workloadAssertionTarget{Provider: "ags", Instance: "mini", Repository: "jackie/agent-kit"}
	scope, err := normalizeSessionExchangeScope(workloadAssertionRequest{RequestedCapabilities: []string{"repo:read"}}, target)
	if err != nil {
		t.Fatalf("normalize legacy scope: %v", err)
	}
	if scope.CompatibilityInput != "legacy_capability_mapping_v1" || scope.Operation.Name != "repo.read" || scope.Resource != (workloadAssertionResource{Service: "ags", Repository: "jackie/agent-kit"}) {
		t.Fatalf("scope = %#v", scope)
	}
	if _, err := normalizeSessionExchangeScope(workloadAssertionRequest{RequestedCapabilities: []string{"pr:create", "repo:read"}}, target); err == nil {
		t.Fatal("legacy capabilities synthesized an unconstrained pr.create operation")
	}
}

func TestWorkspaceWorkloadAuthorityAdvancesMembershipEpoch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("requires test database")
	}
	ctx := context.Background()
	workspace, err := testHandler.Queries.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		Name: "workload authority trigger",
		Slug: "workload-authority-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace_workload_authority WHERE workspace_id=$1`, workspace.ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, workspace.ID)
	})

	assertAuthority := func(wantEpoch int64) {
		t.Helper()
		authority, err := testHandler.Queries.GetWorkspaceWorkloadAuthority(ctx, workspace.ID)
		if err != nil {
			t.Fatalf("get workload authority: %v", err)
		}
		if authority.TeamIdentityID != workspace.ID || authority.MembershipEpoch != wantEpoch || authority.PolicyClass != workspaceDefaultPolicyClass {
			t.Fatalf("authority = %#v, want workspace=%s epoch=%d", authority, uuidToString(workspace.ID), wantEpoch)
		}
	}
	assertAuthority(1)

	member, err := testHandler.Queries.CreateMember(ctx, db.CreateMemberParams{WorkspaceID: workspace.ID, UserID: parseUUID(testUserID), Role: "owner"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	assertAuthority(2)
	if _, err := testHandler.Queries.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{ID: member.ID, Role: "admin"}); err != nil {
		t.Fatalf("update member: %v", err)
	}
	assertAuthority(3)
	if err := testHandler.Queries.DeleteMember(ctx, member.ID); err != nil {
		t.Fatalf("delete member: %v", err)
	}
	assertAuthority(4)
}

func TestWorkspaceWorkloadAuthorityCleansUpWithWorkspace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("requires test database")
	}
	ctx := context.Background()
	workspace, err := testHandler.Queries.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		Name: "workload authority cleanup",
		Slug: "workload-authority-cleanup-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		// Keep the test database clean if the regression assertion fails before
		// the workspace cascade can complete.
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation_event WHERE workspace_id=$1`, workspace.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation WHERE workspace_id=$1`, workspace.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1`, workspace.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace_workload_authority WHERE workspace_id=$1`, workspace.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, workspace.ID)
	})

	if _, err := testHandler.Queries.CreateMember(ctx, db.CreateMemberParams{
		WorkspaceID: workspace.ID,
		UserID:      parseUUID(testUserID),
		Role:        "owner",
	}); err != nil {
		t.Fatalf("create member: %v", err)
	}
	var delegationID string
	if err := testPool.QueryRow(ctx, `INSERT INTO workload_pr_merge_delegation (
		workspace_id,issue_id,external_pr_link_id,task_id,execution_id,runtime_id,
		target_instance,canonical_repository_id,canonical_repository,provider_binding_id,
		provider_binding_revision,provider_repository,ags_pr_number,provider_pr_number,
		expected_head_sha,expected_base_sha,base_ref,merge_method,projection_facts_revision,facts_digest,state
	) VALUES ($1,$2,$3,$4,$5,$6,'mini',$7,'jackie/agent-kit',$8,$9,'jackie/agent-kit',41,52,$10,$11,'main','rebase',$12,$13,'pending_approval') RETURNING id`,
		workspace.ID, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(),
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222",
		"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee").Scan(&delegationID); err != nil {
		t.Fatalf("seed workspace merge delegation: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO workload_pr_merge_delegation_event (workspace_id,issue_id,delegation_id,event_type,actor_type,actor_id) SELECT workspace_id,issue_id,id,'request_created','system','workspace-delete-test' FROM workload_pr_merge_delegation WHERE id=$1`, delegationID); err != nil {
		t.Fatalf("seed workspace merge event: %v", err)
	}
	w := httptest.NewRecorder()
	req := newRequest(http.MethodDelete, "/api/workspaces/"+uuidToString(workspace.ID), nil)
	req = withURLParam(req, "id", uuidToString(workspace.ID))
	testHandler.DeleteWorkspace(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("workspace cleanup status=%d body=%s", w.Code, w.Body.String())
	}

	var authorityCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM workspace_workload_authority WHERE workspace_id=$1`, workspace.ID).Scan(&authorityCount); err != nil {
		t.Fatalf("count authority rows: %v", err)
	}
	if authorityCount != 0 {
		t.Fatalf("authority rows after workspace cleanup = %d, want 0", authorityCount)
	}
	for table := range map[string]struct{}{"workload_pr_merge_delegation": {}, "workload_pr_merge_delegation_event": {}} {
		var count int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id=$1`, workspace.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows after workspace cleanup=%d err=%v", table, count, err)
		}
	}
}

func TestNormalizeWorkloadAssertionTargetTrimsRepositorySegments(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_ALLOWED_PROVIDERS", "ags")
	target, err := normalizeWorkloadAssertionTarget(workloadAssertionTarget{
		Provider:   " AGS ",
		Instance:   " mini ",
		Repository: " jackie / agent-kit ",
	}, workloadAssertionPurposeExternalPR)
	if err != nil {
		t.Fatalf("normalize target: %v", err)
	}
	if target.Provider != "ags" || target.Instance != "mini" || target.Repository != "jackie/agent-kit" {
		t.Fatalf("target = %#v", target)
	}
}

func TestCreateWorkloadAssertionRejectsExternalPRCapabilities(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-assertion-secret")
	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose":                "external_pr_link",
		"requested_capabilities": []string{"pr:merge"},
	})
	req.Header.Set("X-Actor-Source", "task_token")
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestCreateWorkloadAssertionRejectsUnsupportedPurpose(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-assertion-secret")
	req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
		"purpose": "universal_token",
	})
	req.Header.Set("X-Actor-Source", "task_token")
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestCreateExternalPRLinkTokenKeepsLegacyContract(t *testing.T) {
	const secret = "legacy-link-secret"
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "")
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", secret)
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE", "external-pr-link")

	issueID := createExternalPRTestIssue(t, "legacy link token issue", "todo", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})
	agentID := createHandlerTestAgent(t, "legacy-link-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
	authorizeWorkloadAssertionTestTask(t, req, agentID, taskID)
	rr := httptest.NewRecorder()

	testHandler.CreateExternalPRLinkToken(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	linkToken, _ := response["link_token"].(string)
	if linkToken == "" || response["workspace_id"] != testWorkspaceID || response["agent_id"] != agentID || response["task_id"] != taskID || response["issue_id"] != issueID {
		t.Fatalf("legacy response = %#v", response)
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(linkToken, claims, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithAudience("external-pr-link"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		t.Fatalf("parse legacy link token: valid=%v err=%v", token != nil && token.Valid, err)
	}
	if claims["workspace_id"] != testWorkspaceID || claims["agent_id"] != agentID || claims["task_id"] != taskID || claims["issue_id"] != issueID {
		t.Fatalf("legacy claims = %#v", claims)
	}
}

func TestPRMergeDelegationV2PendingApproveConsumeLifecycle(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "1")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "merge-v2-service-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "mini")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "merge-v2-signing-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "urn:multica:deployment:merge-v2-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "merge-v2-test")

	issueID := createExternalPRTestIssue(t, "merge v2 lifecycle", "in_review", "", nil)
	agentID := createHandlerTestAgent(t, "merge-v2-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	executionID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET execution_id=$2 WHERE id=$1`, taskID, executionID); err != nil {
		t.Fatalf("set execution id: %v", err)
	}
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent_task_queue WHERE id=$1`, taskID).Scan(&runtimeID); err != nil {
		t.Fatalf("read runtime id: %v", err)
	}
	linkID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO external_pull_request_link (
		id, workspace_id, issue_id, provider, external_repo, external_number,
		merge_provider, merge_repo, merge_number, link_confidence, completion_intent, state,
		target_instance, canonical_repository_id, canonical_repository,
		provider_binding_id, provider_binding_revision, provider_repository,
		expected_head_sha, expected_base_sha, base_ref, delegated_merge_method, projection_facts_revision
	) VALUES ($1,$2,$3,'ags','ux/smip',41,'forgejo','ux/smip',52,'authoritative',false,'open',
		'mini',$4,'ux/smip',$5,$6,'ux/smip',$7,$8,'main','rebase',$9)`,
		linkID, testWorkspaceID, issueID,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222",
		"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); err != nil {
		t.Fatalf("seed merge projection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workload_pr_merge_delegation_event WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM workload_pr_merge_delegation WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pull_request_link WHERE id=$1`, linkID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, issueID)
	})
	tokenHash := uuid.NewString()
	if _, err := testHandler.Queries.CreateTaskToken(ctx, db.CreateTaskTokenParams{
		TokenHash: tokenHash, TaskID: parseUUID(taskID), AgentID: parseUUID(agentID),
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID),
		ExecutionID: parseUUID(executionID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create execution token: %v", err)
	}

	newAssertionRequest := func() *http.Request {
		req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", map[string]any{
			"purpose":            "ags_session_exchange",
			"target":             map[string]any{"provider": "ags", "instance": "mini", "repository": "ux/smip"},
			"requested_resource": map[string]any{"service": "ags", "repository": "ux/smip"},
			"requested_operation": map[string]any{"name": "pr.merge", "constraints": map[string]any{
				"pull_request_number": 41, "forgejo_pull_request_number": 52,
				"expected_head_sha": "1111111111111111111111111111111111111111", "merge_method": "rebase",
			}},
			"requested_capabilities": []string{"repo:read", "repo:write"},
		})
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Task-ID", taskID)
		req.Header.Set("X-Run-ID", executionID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("X-Task-Token-Hash", tokenHash)
		return req
	}
	pendingRecorder := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(pendingRecorder, newAssertionRequest())
	if pendingRecorder.Code != http.StatusConflict || !strings.Contains(pendingRecorder.Body.String(), "merge_approval_required") {
		t.Fatalf("pending response status=%d body=%s", pendingRecorder.Code, pendingRecorder.Body.String())
	}
	var delegation db.WorkloadPrMergeDelegation
	if err := testPool.QueryRow(ctx, `SELECT `+workloadPRMergeDelegationColumnsForTest+` FROM workload_pr_merge_delegation WHERE task_id=$1`, taskID).Scan(workloadPRMergeDelegationScanTargetsForTest(&delegation)...); err != nil {
		t.Fatalf("read pending delegation: %v", err)
	}
	if delegation.State != "pending_approval" || uuidToString(delegation.ExecutionID) != executionID {
		t.Fatalf("pending delegation=%#v", delegation)
	}

	approveReq := newRequest(http.MethodPost, "/", map[string]any{})
	approveReq.Header.Set("X-User-ID", testUserID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", testWorkspaceID)
	routeCtx.URLParams.Add("delegationId", uuidToString(delegation.ID))
	approveReq = approveReq.WithContext(context.WithValue(approveReq.Context(), chi.RouteCtxKey, routeCtx))
	approveRecorder := httptest.NewRecorder()
	testHandler.ApprovePRMergeDelegation(approveRecorder, approveReq)
	if approveRecorder.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", approveRecorder.Code, approveRecorder.Body.String())
	}

	signedRecorder := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(signedRecorder, newAssertionRequest())
	if signedRecorder.Code != http.StatusOK {
		t.Fatalf("signed status=%d body=%s", signedRecorder.Code, signedRecorder.Body.String())
	}
	var signed workloadAssertionResponse
	if err := json.NewDecoder(signedRecorder.Body).Decode(&signed); err != nil {
		t.Fatal(err)
	}
	if signed.Workload.Authority == nil || signed.Workload.Authority.PolicyClass != workspaceMaintainerPolicyClass || signed.Workload.MergeDelegation == nil || signed.Workload.MergeDelegation.RunID != executionID {
		t.Fatalf("signed merge workload=%#v", signed.Workload)
	}

	facts := signed.Workload.MergeDelegation
	consumeBody := prMergeDelegationServiceRequest{
		AuthorityRevision: facts.AuthorityRevision, FactsDigest: facts.FactsDigest,
		TargetInstance: facts.TargetInstance, CanonicalRepositoryID: facts.CanonicalRepositoryID,
		CanonicalRepository: facts.CanonicalRepository, ProviderBindingID: facts.ProviderBindingID,
		ProviderBindingRevision: facts.ProviderBindingRevision, ProviderRepository: facts.ProviderRepository,
		AGSPRNumber: facts.AGSPRNumber, ProviderPRNumber: facts.ProviderPRNumber,
		ExpectedHeadSHA: facts.ExpectedHeadSHA, ExpectedBaseSHA: facts.ExpectedBaseSHA,
		BaseRef: facts.BaseRef, MergeMethod: facts.MergeMethod, ProjectionFactsRevision: facts.ProjectionFactsRevision,
		TaskID: facts.TaskID, RunID: facts.RunID, SessionID: "ags-session-v2-test",
		IntentID: "99999999-9999-9999-9999-999999999999", Phase: "pre_effect",
	}
	introspectBody := consumeBody
	introspectBody.IntentID = ""
	introspectBody.Phase = "exchange"
	introspectReq := newRequest(http.MethodPost, "/", introspectBody)
	introspectReq.Header.Set("Authorization", "Bearer merge-v2-service-token")
	introspectRoute := chi.NewRouteContext()
	introspectRoute.URLParams.Add("delegationId", facts.DelegationID)
	introspectReq = introspectReq.WithContext(context.WithValue(introspectReq.Context(), chi.RouteCtxKey, introspectRoute))
	introspectRecorder := httptest.NewRecorder()
	testHandler.IntrospectPRMergeDelegation(introspectRecorder, introspectReq)
	if introspectRecorder.Code != http.StatusOK || !strings.Contains(introspectRecorder.Body.String(), `"outcome":"active"`) {
		t.Fatalf("introspect status=%d body=%s", introspectRecorder.Code, introspectRecorder.Body.String())
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	terminalReq := newRequest(http.MethodPost, "/", introspectBody)
	terminalReq.Header.Set("Authorization", "Bearer merge-v2-service-token")
	terminalRoute := chi.NewRouteContext()
	terminalRoute.URLParams.Add("delegationId", facts.DelegationID)
	terminalReq = terminalReq.WithContext(context.WithValue(terminalReq.Context(), chi.RouteCtxKey, terminalRoute))
	terminalRecorder := httptest.NewRecorder()
	testHandler.IntrospectPRMergeDelegation(terminalRecorder, terminalReq)
	if terminalRecorder.Code != http.StatusConflict {
		t.Fatalf("terminal introspect status=%d body=%s", terminalRecorder.Code, terminalRecorder.Body.String())
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}

	consumeReq := newRequest(http.MethodPost, "/", consumeBody)
	consumeReq.Header.Set("Authorization", "Bearer merge-v2-service-token")
	consumeRoute := chi.NewRouteContext()
	consumeRoute.URLParams.Add("delegationId", facts.DelegationID)
	consumeReq = consumeReq.WithContext(context.WithValue(consumeReq.Context(), chi.RouteCtxKey, consumeRoute))
	consumeRecorder := httptest.NewRecorder()
	testHandler.ConsumePRMergeDelegation(consumeRecorder, consumeReq)
	if consumeRecorder.Code != http.StatusOK || !strings.Contains(consumeRecorder.Body.String(), `"outcome":"consumed"`) {
		t.Fatalf("consume status=%d body=%s", consumeRecorder.Code, consumeRecorder.Body.String())
	}
	// Same intent is idempotent and cannot produce a second authority commit.
	consumeRetryReq := newRequest(http.MethodPost, "/", consumeBody)
	consumeRetryReq.Header.Set("Authorization", "Bearer merge-v2-service-token")
	consumeRetryRoute := chi.NewRouteContext()
	consumeRetryRoute.URLParams.Add("delegationId", facts.DelegationID)
	consumeRetryReq = consumeRetryReq.WithContext(context.WithValue(consumeRetryReq.Context(), chi.RouteCtxKey, consumeRetryRoute))
	consumeRetryRecorder := httptest.NewRecorder()
	testHandler.ConsumePRMergeDelegation(consumeRetryRecorder, consumeRetryReq)
	if consumeRetryRecorder.Code != http.StatusOK || !strings.Contains(consumeRetryRecorder.Body.String(), "already_consumed") {
		t.Fatalf("consume retry status=%d body=%s", consumeRetryRecorder.Code, consumeRetryRecorder.Body.String())
	}
	wrongIntent := consumeBody
	wrongIntent.IntentID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	wrongIntentReq := newRequest(http.MethodPost, "/", wrongIntent)
	wrongIntentReq.Header.Set("Authorization", "Bearer merge-v2-service-token")
	wrongIntentRoute := chi.NewRouteContext()
	wrongIntentRoute.URLParams.Add("delegationId", facts.DelegationID)
	wrongIntentReq = wrongIntentReq.WithContext(context.WithValue(wrongIntentReq.Context(), chi.RouteCtxKey, wrongIntentRoute))
	wrongIntentRecorder := httptest.NewRecorder()
	testHandler.ConsumePRMergeDelegation(wrongIntentRecorder, wrongIntentReq)
	if wrongIntentRecorder.Code != http.StatusConflict {
		t.Fatalf("different intent status=%d body=%s", wrongIntentRecorder.Code, wrongIntentRecorder.Body.String())
	}

	revokeReq := newRequest(http.MethodPost, "/", map[string]any{})
	revokeReq.Header.Set("X-User-ID", testUserID)
	revokeRoute := chi.NewRouteContext()
	revokeRoute.URLParams.Add("id", testWorkspaceID)
	revokeRoute.URLParams.Add("delegationId", facts.DelegationID)
	revokeReq = revokeReq.WithContext(context.WithValue(revokeReq.Context(), chi.RouteCtxKey, revokeRoute))
	revokeRecorder := httptest.NewRecorder()
	testHandler.RevokePRMergeDelegation(revokeRecorder, revokeReq)
	if revokeRecorder.Code != http.StatusConflict {
		t.Fatalf("post-consume revoke status=%d body=%s", revokeRecorder.Code, revokeRecorder.Body.String())
	}

	// A new one-shot request can be approved after the consumed authority, and
	// revocation committed before consume must win the same-row state race.
	secondPendingRecorder := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(secondPendingRecorder, newAssertionRequest())
	if secondPendingRecorder.Code != http.StatusConflict {
		t.Fatalf("second pending status=%d body=%s", secondPendingRecorder.Code, secondPendingRecorder.Body.String())
	}
	var second db.WorkloadPrMergeDelegation
	if err := testPool.QueryRow(ctx, `SELECT `+workloadPRMergeDelegationColumnsForTest+` FROM workload_pr_merge_delegation WHERE task_id=$1 AND state='pending_approval'`, taskID).Scan(workloadPRMergeDelegationScanTargetsForTest(&second)...); err != nil {
		t.Fatal(err)
	}
	secondApproveReq := newRequest(http.MethodPost, "/", map[string]any{})
	secondApproveReq.Header.Set("X-User-ID", testUserID)
	secondApproveRoute := chi.NewRouteContext()
	secondApproveRoute.URLParams.Add("id", testWorkspaceID)
	secondApproveRoute.URLParams.Add("delegationId", uuidToString(second.ID))
	secondApproveReq = secondApproveReq.WithContext(context.WithValue(secondApproveReq.Context(), chi.RouteCtxKey, secondApproveRoute))
	secondApproveRecorder := httptest.NewRecorder()
	testHandler.ApprovePRMergeDelegation(secondApproveRecorder, secondApproveReq)
	if secondApproveRecorder.Code != http.StatusOK {
		t.Fatalf("second approve status=%d body=%s", secondApproveRecorder.Code, secondApproveRecorder.Body.String())
	}
	secondSignedRecorder := httptest.NewRecorder()
	testHandler.CreateWorkloadAssertion(secondSignedRecorder, newAssertionRequest())
	if secondSignedRecorder.Code != http.StatusOK {
		t.Fatalf("second signed status=%d body=%s", secondSignedRecorder.Code, secondSignedRecorder.Body.String())
	}
	var secondSigned workloadAssertionResponse
	if err := json.NewDecoder(secondSignedRecorder.Body).Decode(&secondSigned); err != nil {
		t.Fatal(err)
	}
	secondFacts := secondSigned.Workload.MergeDelegation
	secondRevokeReq := newRequest(http.MethodPost, "/", map[string]any{})
	secondRevokeReq.Header.Set("X-User-ID", testUserID)
	secondRevokeRoute := chi.NewRouteContext()
	secondRevokeRoute.URLParams.Add("id", testWorkspaceID)
	secondRevokeRoute.URLParams.Add("delegationId", secondFacts.DelegationID)
	secondRevokeReq = secondRevokeReq.WithContext(context.WithValue(secondRevokeReq.Context(), chi.RouteCtxKey, secondRevokeRoute))
	secondRevokeRecorder := httptest.NewRecorder()
	testHandler.RevokePRMergeDelegation(secondRevokeRecorder, secondRevokeReq)
	if secondRevokeRecorder.Code != http.StatusOK {
		t.Fatalf("pre-consume revoke status=%d body=%s", secondRevokeRecorder.Code, secondRevokeRecorder.Body.String())
	}
	secondConsumeBody := consumeBody
	secondConsumeBody.AuthorityRevision = secondFacts.AuthorityRevision
	secondConsumeBody.FactsDigest = secondFacts.FactsDigest
	secondConsumeBody.IntentID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	secondConsumeReq := newRequest(http.MethodPost, "/", secondConsumeBody)
	secondConsumeReq.Header.Set("Authorization", "Bearer merge-v2-service-token")
	secondConsumeRoute := chi.NewRouteContext()
	secondConsumeRoute.URLParams.Add("delegationId", secondFacts.DelegationID)
	secondConsumeReq = secondConsumeReq.WithContext(context.WithValue(secondConsumeReq.Context(), chi.RouteCtxKey, secondConsumeRoute))
	secondConsumeRecorder := httptest.NewRecorder()
	testHandler.ConsumePRMergeDelegation(secondConsumeRecorder, secondConsumeReq)
	if secondConsumeRecorder.Code != http.StatusConflict {
		t.Fatalf("revoked consume status=%d body=%s", secondConsumeRecorder.Code, secondConsumeRecorder.Body.String())
	}
}

func TestClaimAgentTaskGeneratesDistinctExecutionIdentity(t *testing.T) {
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "execution identity claim", "todo", "", nil)
	agentID := createHandlerTestAgent(t, "execution-identity-agent", []byte(`{}`))
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id=$1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority) VALUES ($1,$2,$3,'queued',0) RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, issueID)
	})
	claimed, err := testHandler.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{AgentID: parseUUID(agentID), PrepareLeaseSecs: 30})
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if !claimed.ExecutionID.Valid || uuidToString(claimed.ExecutionID) == taskID {
		t.Fatalf("claim task_id=%s execution_id=%s valid=%v", taskID, uuidToString(claimed.ExecutionID), claimed.ExecutionID.Valid)
	}
	// Stale-dispatch redelivery refreshes only the lease and preserves the same execution.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET prepare_lease_expires_at=now()-interval '1 second' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := testHandler.Queries.ReclaimStaleDispatchedTaskForRuntime(ctx, db.ReclaimStaleDispatchedTaskForRuntimeParams{
		RuntimeID: parseUUID(runtimeID), PrepareLeaseSecs: 30, ClaimRecoverySecs: 0,
	})
	if err != nil {
		t.Fatalf("reclaim task: %v", err)
	}
	if reclaimed.ID != claimed.ID || reclaimed.ExecutionID != claimed.ExecutionID {
		t.Fatalf("stale reclaim changed execution: first=%s second=%s", uuidToString(claimed.ExecutionID), uuidToString(reclaimed.ExecutionID))
	}
}

const workloadPRMergeDelegationColumnsForTest = `id, workspace_id, issue_id, external_pr_link_id, task_id, execution_id, runtime_id, operation,
 target_instance, canonical_repository_id, canonical_repository, provider, provider_binding_id, provider_binding_revision,
 provider_repository, ags_pr_number, provider_pr_number, expected_head_sha, expected_base_sha, base_ref, merge_method,
 projection_facts_revision, facts_digest, authority_revision, approval_policy_revision, state, requested_at, approved_at,
 approved_by_user_id, not_after, revoked_at, revoked_by_user_id, revocation_reason, superseded_at, supersede_reason,
 consumer_instance_id, consumer_intent_id, consume_request_digest, consumption_receipt_id, consumed_at, created_at, updated_at`

func workloadPRMergeDelegationScanTargetsForTest(row *db.WorkloadPrMergeDelegation) []any {
	return []any{&row.ID, &row.WorkspaceID, &row.IssueID, &row.ExternalPrLinkID, &row.TaskID, &row.ExecutionID, &row.RuntimeID,
		&row.Operation, &row.TargetInstance, &row.CanonicalRepositoryID, &row.CanonicalRepository, &row.Provider,
		&row.ProviderBindingID, &row.ProviderBindingRevision, &row.ProviderRepository, &row.AgsPrNumber, &row.ProviderPrNumber,
		&row.ExpectedHeadSha, &row.ExpectedBaseSha, &row.BaseRef, &row.MergeMethod, &row.ProjectionFactsRevision, &row.FactsDigest,
		&row.AuthorityRevision, &row.ApprovalPolicyRevision, &row.State, &row.RequestedAt, &row.ApprovedAt, &row.ApprovedByUserID,
		&row.NotAfter, &row.RevokedAt, &row.RevokedByUserID, &row.RevocationReason, &row.SupersededAt, &row.SupersedeReason,
		&row.ConsumerInstanceID, &row.ConsumerIntentID, &row.ConsumeRequestDigest, &row.ConsumptionReceiptID, &row.ConsumedAt,
		&row.CreatedAt, &row.UpdatedAt}
}
