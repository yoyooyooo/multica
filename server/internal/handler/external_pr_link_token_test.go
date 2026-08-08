package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestCreateExternalPRLinkTokenRejectsWeakSigningSecret(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", "too-short")
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	rr := httptest.NewRecorder()

	testHandler.CreateExternalPRLinkToken(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateExternalPRLinkTokenUsesServerBoundTaskContext(t *testing.T) {
	const secret = "external-pr-link-test-secret-at-least-32-bytes"
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", secret)
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE", "external-pr-link")

	issueID := createExternalPRTestIssue(t, "external PR link token issue", "todo", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})
	agentID := createHandlerTestAgent(t, "external-pr-link-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
	req.Header.Set("X-Agent-ID", "00000000-0000-0000-0000-000000000001")
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rr := httptest.NewRecorder()

	testHandler.CreateExternalPRLinkToken(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	linkToken, _ := response["link_token"].(string)
	if linkToken == "" || response["workspace_id"] != testWorkspaceID || response["agent_id"] != agentID || response["task_id"] != taskID || response["issue_id"] != issueID {
		t.Fatalf("response = %#v", response)
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(linkToken, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			t.Fatalf("signing method = %v, want HS256", token.Method.Alg())
		}
		return []byte(secret), nil
	}, jwt.WithAudience("external-pr-link"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		t.Fatalf("parse link token: valid=%v err=%v", token != nil && token.Valid, err)
	}
	if claims["workspace_id"] != testWorkspaceID || claims["agent_id"] != agentID || claims["task_id"] != taskID || claims["issue_id"] != issueID || claims["source"] != "task_token" {
		t.Fatalf("claims = %#v", claims)
	}
	issuedAt, issuedOK := claims["iat"].(float64)
	expiresAt, expiresOK := claims["exp"].(float64)
	if !issuedOK || !expiresOK || expiresAt-issuedAt <= 0 || expiresAt-issuedAt > externalPRLinkTokenTTL.Seconds() {
		t.Fatalf("token lifetime iat=%v exp=%v", claims["iat"], claims["exp"])
	}
	if _, exists := claims["purpose"]; exists {
		t.Fatalf("link token must not carry assertion purpose: %#v", claims)
	}
	if _, exists := token.Header["kid"]; exists {
		t.Fatalf("link token must not carry assertion kid: %#v", token.Header)
	}
}

func TestCreateExternalPRLinkTokenRejectsTerminalRevokedExpiredAndCrossScopeAuthority(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", "link-token-authority-test-secret-at-least-32-bytes")
	issueID := createExternalPRTestIssue(t, "external PR link token authority", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "external-pr-link-authority-agent", []byte(`{}`))
	otherAgentID := createHandlerTestAgent(t, "external-pr-link-other-agent", []byte(`{}`))

	invoke := func(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		testHandler.CreateExternalPRLinkToken(rr, req)
		return rr
	}

	t.Run("cross task and agent", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		otherTaskID := createHandlerTestTaskForAgentOnIssue(t, otherAgentID, issueID)
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
		authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		req.Header.Set("X-Task-ID", otherTaskID)
		req.Header.Set("X-Agent-ID", otherAgentID)
		if rr := invoke(t, req); rr.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("cross workspace", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
		authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		req.Header.Set("X-Workspace-ID", uuid.NewString())
		if rr := invoke(t, req); rr.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run("terminal "+status, func(t *testing.T) {
			taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
			req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
			authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
			if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status=$2, completed_at=now() WHERE id=$1`, taskID, status); err != nil {
				t.Fatal(err)
			}
			if rr := invoke(t, req); rr.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}

	t.Run("revoked token", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
		tokenHash := authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		if _, err := testPool.Exec(context.Background(), `DELETE FROM task_token WHERE token_hash=$1`, tokenHash); err != nil {
			t.Fatal(err)
		}
		if rr := invoke(t, req); rr.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("expired token", func(t *testing.T) {
		taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
		tokenHash := authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)
		if _, err := testPool.Exec(context.Background(), `UPDATE task_token SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, tokenHash); err != nil {
			t.Fatal(err)
		}
		if rr := invoke(t, req); rr.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestCreateExternalPRLinkTokenLinearizesBeforeTaskCompletion(t *testing.T) {
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", "linearized-link-token-secret-at-least-32-bytes")
	issueID := createExternalPRTestIssue(t, "linearized external PR link token", "todo", "", nil)
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID) })
	agentID := createHandlerTestAgent(t, "linearized-external-pr-link-agent", []byte(`{}`))
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/link-token", nil)
	authorizeCurrentExecutionContextTestTask(t, req, agentID, taskID)

	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.ExternalPRLinkTokenHook = func(stage string) {
		if stage == "external_pr_link_token_locked" {
			close(locked)
			<-release
		}
	}
	t.Cleanup(func() { testHandler.ExternalPRLinkTokenHook = nil })

	rr := httptest.NewRecorder()
	mintDone := make(chan struct{})
	go func() {
		testHandler.CreateExternalPRLinkToken(rr, req)
		close(mintDone)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("link-token mint did not acquire task authority lock")
	}
	completionDone := make(chan error, 1)
	go func() {
		_, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1 AND status='running'`, taskID)
		completionDone <- err
	}()
	select {
	case err := <-completionDone:
		t.Fatalf("completion crossed link-token lock early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-mintDone:
	case <-time.After(5 * time.Second):
		t.Fatal("link-token mint did not finish")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case err := <-completionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not resume after link-token mint")
	}
}
