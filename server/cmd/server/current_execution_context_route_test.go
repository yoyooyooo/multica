package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func currentExecutionContextFixture(t *testing.T, taskStatus string) string {
	t.Helper()
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	runtimeID := fixture.Runtime(t, "Current execution context runtime")
	agentID := fixture.Agent(t, "Current execution context agent", runtimeID)
	taskID := fixture.Task(t, agentID, testutil.Cols{
		"status":     taskStatus,
		"attempt":    1,
		"runtime_id": runtimeID,
	})
	token := "mat_current_execution_context_test_token"
	fixture.Insert(t, "task_token", testutil.Cols{
		"token_hash":   auth.HashToken(token),
		"task_id":      taskID,
		"agent_id":     agentID,
		"workspace_id": testWorkspaceID,
		"user_id":      testUserID,
		"expires_at":   testutil.Raw("now() + interval '1 hour'"),
	})
	return token
}

func requestCurrentExecutionContext(t *testing.T, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/integrations/current-execution-context", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCurrentExecutionContextRouteServesRunningTask(t *testing.T) {
	token := currentExecutionContextFixture(t, "running")
	resp := requestCurrentExecutionContext(t, token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", got)
	}

	var body struct {
		Schema    string `json:"schema"`
		Workspace struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"workspace"`
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
		Task struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Attempt int32  `json:"attempt"`
		} `json:"task"`
		Claim struct {
			Generation string `json:"generation"`
			TaskID     string `json:"task_id"`
		} `json:"claim"`
		Run struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
		} `json:"run"`
		Attribution struct {
			Source string `json:"source"`
		} `json:"attribution"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Schema != "multica.current-execution-context.v2" {
		t.Fatalf("schema=%q", body.Schema)
	}
	if body.Workspace.ID != testWorkspaceID || body.Workspace.Slug != integrationTestWorkspaceSlug {
		t.Fatalf("workspace=%+v", body.Workspace)
	}
	if body.Agent.ID == "" || body.Task.ID == "" || body.Task.Status != "running" || body.Task.Attempt != 1 {
		t.Fatalf("agent/task=%+v/%+v", body.Agent, body.Task)
	}
	if body.Claim.Generation != body.Task.ID || body.Claim.TaskID != body.Task.ID || body.Run.ID != body.Task.ID || body.Run.TaskID != body.Task.ID {
		t.Fatalf("task/claim/run=%+v/%+v/%+v", body.Task, body.Claim, body.Run)
	}
	if body.Attribution.Source != "unattributed" {
		t.Fatalf("attribution=%+v", body.Attribution)
	}
}

func TestCurrentExecutionContextRouteRejectsTerminalTask(t *testing.T) {
	token := currentExecutionContextFixture(t, "completed")
	resp := requestCurrentExecutionContext(t, token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}
