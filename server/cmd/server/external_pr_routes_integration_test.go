package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

func TestCurrentExecutionContextAndExternalPRRoutesUseRealAuthBoundaries(t *testing.T) {
	ctx := context.Background()
	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT id, runtime_id FROM agent
WHERE workspace_id=$1 AND runtime_id IS NOT NULL ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("load integration agent: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (
workspace_id, number, title, status, priority, position, creator_type, creator_id
) VALUES ($1, (SELECT issue_counter + 1000 FROM workspace WHERE id=$1),
'router current execution context', 'in_progress', 'none', 1, 'member', $2) RETURNING id`,
		testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create route issue: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (
agent_id, runtime_id, issue_id, status, priority, started_at
) VALUES ($1,$2,$3,'running',0,now()) RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create route task: %v", err)
	}
	rawToken, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO task_token (
token_hash, task_id, agent_id, workspace_id, user_id, expires_at
) VALUES ($1,$2,$3,$4,$5,$6)`, auth.HashToken(rawToken), taskID, agentID, testWorkspaceID, testUserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create route task token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM activity_log WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})

	delegationID := "00000000-0000-0000-0000-000000000001"
	for _, retired := range []struct {
		method, path, authorization string
	}{
		{http.MethodPost, "/api/integrations/workload-assertions", "Bearer " + rawToken},
		{http.MethodGet, "/api/workspaces/" + testWorkspaceID + "/workload-delegations/pr-merge", "Bearer " + testToken},
		{http.MethodGet, "/api/workspaces/" + testWorkspaceID + "/workload-delegations/pr-merge/" + delegationID, "Bearer " + testToken},
		{http.MethodPost, "/api/workspaces/" + testWorkspaceID + "/workload-delegations/pr-merge/" + delegationID + "/approve", "Bearer " + testToken},
		{http.MethodPost, "/api/workspaces/" + testWorkspaceID + "/workload-delegations/pr-merge/" + delegationID + "/revoke", "Bearer " + testToken},
		{http.MethodPost, "/api/integrations/ags/workload-delegations/pr-merge/" + delegationID + "/introspect", "Bearer retired-service-token"},
		{http.MethodPost, "/api/integrations/ags/workload-delegations/pr-merge/" + delegationID + "/consume", "Bearer retired-service-token"},
		{http.MethodPost, "/api/integrations/ags/workload-delegations/pr-merge/" + delegationID + "/effects", "Bearer retired-service-token"},
	} {
		retiredReq, err := http.NewRequest(retired.method, testServer.URL+retired.path, bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		retiredReq.Header.Set("Authorization", retired.authorization)
		retiredReq.Header.Set("Content-Type", "application/json")
		retiredResp, err := http.DefaultClient.Do(retiredReq)
		if err != nil {
			t.Fatal(err)
		}
		retiredResp.Body.Close()
		if retiredResp.StatusCode != http.StatusNotFound {
			t.Fatalf("retired route %s %s status=%d, want 404", retired.method, retired.path, retiredResp.StatusCode)
		}
	}

	contextReq, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/integrations/current-execution-context", nil)
	if err != nil {
		t.Fatal(err)
	}
	contextReq.Header.Set("Authorization", "Bearer "+rawToken)
	contextReq.Header.Set("X-Actor-Source", "forged-member")
	contextReq.Header.Set("X-Agent-ID", "forged-agent")
	contextReq.Header.Set("X-Workspace-ID", "forged-workspace")
	contextResp, err := http.DefaultClient.Do(contextReq)
	if err != nil {
		t.Fatal(err)
	}
	defer contextResp.Body.Close()
	if contextResp.StatusCode != http.StatusOK {
		t.Fatalf("current execution context route status=%d", contextResp.StatusCode)
	}
	var currentContext struct {
		Schema    string `json:"schema"`
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
		Issue *struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := json.NewDecoder(contextResp.Body).Decode(&currentContext); err != nil {
		t.Fatal(err)
	}
	if currentContext.Schema != "multica.current-execution-context.v1" || currentContext.Workspace.ID != testWorkspaceID || currentContext.Agent.ID != agentID || currentContext.Task.ID != taskID || currentContext.Task.Status != "running" || currentContext.Issue == nil || currentContext.Issue.ID != issueID {
		t.Fatalf("context route did not use token-bound identity: %#v", currentContext)
	}

	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_SECRET", "router-link-token-secret")
	t.Setenv("MULTICA_EXTERNAL_PR_LINK_TOKEN_AUDIENCE", "external-pr-link")
	linkTokenReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/external-pr/link-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	linkTokenReq.Header.Set("Authorization", "Bearer "+rawToken)
	linkTokenReq.Header.Set("X-Actor-Source", "forged-member")
	linkTokenReq.Header.Set("X-Agent-ID", "forged-agent")
	linkTokenReq.Header.Set("X-Workspace-ID", "forged-workspace")
	linkTokenResp, err := http.DefaultClient.Do(linkTokenReq)
	if err != nil {
		t.Fatal(err)
	}
	if linkTokenResp.StatusCode != http.StatusOK {
		linkTokenResp.Body.Close()
		t.Fatalf("external PR link-token route status=%d", linkTokenResp.StatusCode)
	}
	var linkTokenContext struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		TaskID      string `json:"task_id"`
		IssueID     string `json:"issue_id"`
		LinkToken   string `json:"link_token"`
	}
	if err := json.NewDecoder(linkTokenResp.Body).Decode(&linkTokenContext); err != nil {
		linkTokenResp.Body.Close()
		t.Fatal(err)
	}
	linkTokenResp.Body.Close()
	if linkTokenContext.WorkspaceID != testWorkspaceID || linkTokenContext.AgentID != agentID || linkTokenContext.TaskID != taskID || linkTokenContext.IssueID != issueID || linkTokenContext.LinkToken == "" {
		t.Fatalf("link-token route did not use token-bound identity")
	}

	// Best-effort token deletion is not authority. Once the task is terminal,
	// the real Auth middleware must reject the still-unexpired token before the
	// current-context handler runs.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1`, taskID); err != nil {
		t.Fatalf("terminalize route task: %v", err)
	}
	terminalContextReq, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/integrations/current-execution-context", nil)
	if err != nil {
		t.Fatal(err)
	}
	terminalContextReq.Header.Set("Authorization", "Bearer "+rawToken)
	terminalContextResp, err := http.DefaultClient.Do(terminalContextReq)
	if err != nil {
		t.Fatal(err)
	}
	terminalContextResp.Body.Close()
	if terminalContextResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal context route status=%d, want 401", terminalContextResp.StatusCode)
	}
	terminalLinkReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/external-pr/link-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	terminalLinkReq.Header.Set("Authorization", "Bearer "+rawToken)
	terminalLinkResp, err := http.DefaultClient.Do(terminalLinkReq)
	if err != nil {
		t.Fatal(err)
	}
	terminalLinkResp.Body.Close()
	if terminalLinkResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal link-token route status=%d, want 401", terminalLinkResp.StatusCode)
	}

	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "route-service-token")
	intent := false
	linkBody, _ := json.Marshal(map[string]any{
		"provider": "ags", "workspace_id": testWorkspaceID, "issue_id": issueID,
		"external_repo": "jackie/agent-kit", "external_number": 1701,
		"state": "open", "link_confidence": "authoritative", "completion_intent": intent,
		"idempotency_key": "route-service-token-1701",
	})
	for _, authorization := range []string{
		"route-service-token",
		"Basic route-service-token",
		"bearer route-service-token",
		"Bearer  route-service-token",
	} {
		invalidReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/external-pr/links", bytes.NewReader(linkBody))
		if err != nil {
			t.Fatal(err)
		}
		invalidReq.Header.Set("Authorization", authorization)
		invalidReq.Header.Set("Content-Type", "application/json")
		invalidResp, err := http.DefaultClient.Do(invalidReq)
		if err != nil {
			t.Fatal(err)
		}
		invalidResp.Body.Close()
		if invalidResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("external PR authorization %q status=%d, want 401", authorization, invalidResp.StatusCode)
		}
	}
	linkReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/external-pr/links", bytes.NewReader(linkBody))
	if err != nil {
		t.Fatal(err)
	}
	linkReq.Header.Set("Authorization", "Bearer route-service-token")
	linkReq.Header.Set("Content-Type", "application/json")
	linkResp, err := http.DefaultClient.Do(linkReq)
	if err != nil {
		t.Fatal(err)
	}
	defer linkResp.Body.Close()
	if linkResp.StatusCode != http.StatusOK {
		t.Fatalf("external PR service route status=%d", linkResp.StatusCode)
	}
	var persisted int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM external_pull_request_link
WHERE issue_id=$1 AND external_repo='jackie/agent-kit' AND external_number=1701`, issueID).Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("service route persisted facts=%d err=%v", persisted, err)
	}
}
