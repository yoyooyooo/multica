package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreateWorkloadAssertionExternalPRUsesServerTaskContext(t *testing.T) {
	const (
		secret   = "workload-assertion-secret"
		issuer   = "https://multica.test"
		keyID    = "current-key"
		audience = "urn:multica:external-pr-link:v1"
	)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", secret)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", issuer)
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
		"workspace_id": "forged-workspace",
		"agent_id":     "forged-agent",
		"task_id":      "forged-task",
		"issue_id":     "forged-issue",
	})
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
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

func TestCreateWorkloadAssertionSessionExchangeUsesDistinctAudienceAndSignedScope(t *testing.T) {
	const (
		secret   = "workload-session-assertion-secret"
		issuer   = "https://multica.test"
		keyID    = "current-key"
		audience = "urn:ags:workload-session-exchange:v1"
	)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", secret)
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", issuer)
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
	}
	issue := func() (string, jwt.MapClaims) {
		req := newRequest(http.MethodPost, "/api/integrations/workload-assertions", requestBody)
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Task-ID", taskID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
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
		if response.Workload.WorkloadContext.Schema != workloadContextSchema || response.Workload.WorkloadContext.IssuerInstanceID != issuer || response.Workload.WorkloadContext.AgentID != agentID || response.Workload.WorkloadContext.TaskID != taskID || response.Workload.WorkloadContext.RunID != taskID {
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
	if first["purpose"] != "ags_session_exchange" || first["aud"] != audience || first["source"] != "task_token" {
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
	if !ok || context["schema"] != workloadContextSchema || context["issuer_instance_id"] != issuer || context["workspace_id"] != testWorkspaceID || context["agent_id"] != agentID || context["task_id"] != taskID || context["run_id"] != taskID || context["correlation_id"] != first["jti"] {
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
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateWorkloadAssertionSessionExchangeRequiresConfiguredIssuer(t *testing.T) {
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "workload-session-assertion-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "")
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
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rr := httptest.NewRecorder()

	testHandler.CreateWorkloadAssertion(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestNormalizeSessionExchangeScopePreservesLegacyCapabilitiesAsTypedInput(t *testing.T) {
	target := workloadAssertionTarget{Provider: "ags", Instance: "mini", Repository: "jackie/agent-kit"}
	scope, err := normalizeSessionExchangeScope(workloadAssertionRequest{RequestedCapabilities: []string{"repo:read"}}, target)
	if err != nil {
		t.Fatalf("normalize legacy scope: %v", err)
	}
	if scope.CompatibilityInput != "legacy_capability_mapping_v1" || scope.Operation.Name != "repo.read" || scope.Resource != (workloadAssertionResource{Service: "ags", Repository: "jackie/agent-kit"}) {
		t.Fatalf("scope = %#v", scope)
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

func TestWorkspaceWorkloadAuthorityDoesNotBlockWorkspaceFixtureCleanup(t *testing.T) {
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
	if _, err := testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, workspace.ID); err != nil {
		t.Fatalf("workspace fixture cleanup must cascade without refreshing deleted authority: %v", err)
	}

	var authorityCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM workspace_workload_authority WHERE workspace_id=$1`, workspace.ID).Scan(&authorityCount); err != nil {
		t.Fatalf("count authority rows: %v", err)
	}
	if authorityCount != 0 {
		t.Fatalf("authority rows after workspace cleanup = %d, want 0", authorityCount)
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
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
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
