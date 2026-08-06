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

func TestWorkloadAndExternalPRRoutesUseRealAuthBoundaries(t *testing.T) {
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
'router workload assertion', 'in_progress', 'none', 1, 'member', $2) RETURNING id`,
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

	t.Setenv("MULTICA_WORKLOAD_ASSERTION_SECRET", "route-workload-secret")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER", "urn:multica:deployment:route-test")
	t.Setenv("MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID", "multica-route-test")
	assertionBody, _ := json.Marshal(map[string]any{
		"purpose": "external_pr_link",
		"target":  map[string]any{"provider": "ags", "instance": "mini", "repository": "jackie/agent-kit"},
	})
	req, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/workload-assertions", bytes.NewReader(assertionBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+rawToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-Source", "forged-member")
	req.Header.Set("X-Workspace-ID", "forged-workspace")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("workload assertion route status=%d", resp.StatusCode)
	}
	var assertion struct {
		Assertion string `json:"assertion"`
		Workload  struct {
			WorkspaceID string `json:"workspace_id"`
			AgentID     string `json:"agent_id"`
			TaskID      string `json:"task_id"`
			IssueID     string `json:"issue_id"`
		} `json:"workload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assertion); err != nil {
		t.Fatal(err)
	}
	if assertion.Assertion == "" || assertion.Workload.WorkspaceID != testWorkspaceID || assertion.Workload.AgentID != agentID || assertion.Workload.TaskID != taskID || assertion.Workload.IssueID != issueID {
		t.Fatalf("route workload did not use token-bound identity: %#v", assertion)
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

	// Best-effort token deletion is not authority. Once the task is terminal,
	// the real Auth middleware must reject the still-unexpired token before the
	// assertion handler runs.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1`, taskID); err != nil {
		t.Fatalf("terminalize route task: %v", err)
	}
	terminalReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/workload-assertions", bytes.NewReader(assertionBody))
	if err != nil {
		t.Fatal(err)
	}
	terminalReq.Header.Set("Authorization", "Bearer "+rawToken)
	terminalReq.Header.Set("Content-Type", "application/json")
	terminalResp, err := http.DefaultClient.Do(terminalReq)
	if err != nil {
		t.Fatal(err)
	}
	terminalResp.Body.Close()
	if terminalResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal task token route status=%d, want 401", terminalResp.StatusCode)
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
