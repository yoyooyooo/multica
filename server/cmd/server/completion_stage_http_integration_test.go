package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type stageHTTPTask struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	IssueID string `json:"issue_id"`
}

type stageHTTPIssue struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func TestCompletionStageChainUsesRealHTTPAndPublicReadAPIs(t *testing.T) {
	ctx := context.Background()
	sequence := time.Now().UnixNano()
	var runtimeID, agentID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, owner_id)
VALUES ($1,$2,'cloud','completion-http-test','online','HTTP completion fixture','{}'::jsonb,now(),$3) RETURNING id`,
		testWorkspaceID, fmt.Sprintf("HTTP completion runtime %d", sequence), testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime fixture: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
VALUES ($1,$2,'','cloud','{}'::jsonb,$3,'workspace',1,$4) RETURNING id`,
		testWorkspaceID, fmt.Sprintf("HTTP completion agent %d", sequence), runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent fixture: %v", err)
	}

	createIssue := func(title, status string, parent *stageHTTPIssue, stage int) stageHTTPIssue {
		t.Helper()
		body := map[string]any{"title": title, "status": status, "priority": "medium"}
		if parent != nil {
			body["parent_issue_id"] = parent.ID
			body["stage"] = stage
		}
		resp := authRequest(t, http.MethodPost, "/api/issues/", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create issue status=%d", resp.StatusCode)
		}
		var issue stageHTTPIssue
		readJSON(t, resp, &issue)
		return issue
	}
	updateIssue := func(id string, body map[string]any) stageHTTPIssue {
		t.Helper()
		resp := authRequest(t, http.MethodPut, "/api/issues/"+id+"/", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update issue %s status=%d", id, resp.StatusCode)
		}
		var issue stageHTTPIssue
		readJSON(t, resp, &issue)
		return issue
	}
	getIssue := func(id string) stageHTTPIssue {
		t.Helper()
		resp := authRequest(t, http.MethodGet, "/api/issues/"+id+"/", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get issue %s status=%d", id, resp.StatusCode)
		}
		var issue stageHTTPIssue
		readJSON(t, resp, &issue)
		return issue
	}
	activeTask := func(issueID string) stageHTTPTask {
		t.Helper()
		resp := authRequest(t, http.MethodGet, "/api/issues/"+issueID+"/active-task", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("active task status=%d", resp.StatusCode)
		}
		var body struct {
			Tasks []stageHTTPTask `json:"tasks"`
		}
		readJSON(t, resp, &body)
		if len(body.Tasks) != 1 {
			t.Fatalf("issue %s active tasks=%#v", issueID, body.Tasks)
		}
		return body.Tasks[0]
	}
	claimTask := func(wantIssueID string) stageHTTPTask {
		t.Helper()
		resp := authRequest(t, http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("claim task status=%d", resp.StatusCode)
		}
		var body struct {
			Task *stageHTTPTask `json:"task"`
		}
		readJSON(t, resp, &body)
		if body.Task == nil || body.Task.IssueID != wantIssueID || body.Task.Status != "dispatched" {
			t.Fatalf("claimed task=%#v want issue=%s dispatched", body.Task, wantIssueID)
		}
		return *body.Task
	}
	startAndComplete := func(task stageHTTPTask) {
		t.Helper()
		resp := authRequest(t, http.MethodPost, "/api/daemon/tasks/"+task.ID+"/start", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("start task status=%d", resp.StatusCode)
		}
		started := activeTask(task.IssueID)
		if started.Status != "running" {
			t.Fatalf("task status=%q want running", started.Status)
		}
		resp = authRequest(t, http.MethodPost, "/api/daemon/tasks/"+task.ID+"/complete", map[string]any{"output": "HTTP completion proof"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("complete task status=%d", resp.StatusCode)
		}
		resp.Body.Close()
		resp = authRequest(t, http.MethodGet, "/api/daemon/tasks/"+task.ID+"/status", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("read completed daemon task status=%d", resp.StatusCode)
		}
		var taskStatus map[string]string
		readJSON(t, resp, &taskStatus)
		if taskStatus["status"] != "completed" {
			t.Fatalf("daemon task status=%q want completed", taskStatus["status"])
		}
		resp = authRequest(t, http.MethodGet, "/api/issues/"+task.IssueID+"/task-runs", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("read public task runs status=%d", resp.StatusCode)
		}
		var taskRuns []stageHTTPTask
		readJSON(t, resp, &taskRuns)
		foundCompleted := false
		for _, run := range taskRuns {
			if run.ID == task.ID && run.Status == "completed" {
				foundCompleted = true
				break
			}
		}
		if !foundCompleted {
			t.Fatalf("public task runs did not contain completed task %s: %#v", task.ID, taskRuns)
		}
	}
	completeProvider := func(issueID string, number int) {
		t.Helper()
		t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "completion-stage-http-token")
		body := map[string]any{
			"provider": "ags", "workspace_id": testWorkspaceID, "issue_id": issueID,
			"external_repo": "jackie/http-stage", "external_number": number,
			"state": "merged", "link_confidence": "authoritative", "completion_intent": true,
			"idempotency_key": fmt.Sprintf("http-stage-%d", number),
		}
		payload, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, testServer.URL+"/api/integrations/external-pr/complete-from-merge", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer completion-stage-http-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("provider completion status=%d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	inboxCounts := func(issueIDs ...string) map[string]int {
		t.Helper()
		resp := authRequest(t, http.MethodGet, "/api/inbox", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("inbox status=%d", resp.StatusCode)
		}
		var entries []struct {
			IssueID *string `json:"issue_id"`
		}
		readJSON(t, resp, &entries)
		counts := make(map[string]int, len(issueIDs))
		for _, id := range issueIDs {
			counts[id] = 0
		}
		for _, entry := range entries {
			if entry.IssueID != nil {
				if _, tracked := counts[*entry.IssueID]; tracked {
					counts[*entry.IssueID]++
				}
			}
		}
		return counts
	}

	parent := createIssue(fmt.Sprintf("HTTP Stage parent %d", sequence), "in_progress", nil, 0)
	stageOne := createIssue(fmt.Sprintf("HTTP Stage one %d", sequence), "todo", &parent, 1)
	stageTwo := createIssue(fmt.Sprintf("HTTP Stage two %d", sequence), "backlog", &parent, 2)
	for _, issue := range []stageHTTPIssue{parent, stageOne, stageTwo} {
		updateIssue(issue.ID, map[string]any{"assignee_type": "agent", "assignee_id": agentID, "suppress_run": true})
	}
	inboxBefore := inboxCounts(parent.ID, stageOne.ID, stageTwo.ID)

	stageOne = updateIssue(stageOne.ID, map[string]any{"status": "in_progress"})
	updateIssue(stageOne.ID, map[string]any{"assignee_type": nil, "assignee_id": nil})
	updateIssue(stageOne.ID, map[string]any{"assignee_type": "agent", "assignee_id": agentID})
	if activeTask(stageOne.ID).Status != "queued" {
		t.Fatal("stage one was not queued")
	}
	startAndComplete(claimTask(stageOne.ID))
	completeProvider(stageOne.ID, int(sequence%1000000)+2100000)
	if got := getIssue(stageOne.ID).Status; got != "done" {
		t.Fatalf("stage one status=%q", got)
	}

	startAndComplete(claimTask(parent.ID))
	stageTwo = updateIssue(stageTwo.ID, map[string]any{"status": "todo"})
	updateIssue(stageTwo.ID, map[string]any{"assignee_type": nil, "assignee_id": nil})
	updateIssue(stageTwo.ID, map[string]any{"assignee_type": "agent", "assignee_id": agentID})
	if activeTask(stageTwo.ID).Status != "queued" {
		t.Fatal("stage two was not queued")
	}
	startAndComplete(claimTask(stageTwo.ID))
	completeProvider(stageTwo.ID, int(sequence%1000000)+3100000)
	if got := getIssue(stageTwo.ID).Status; got != "done" {
		t.Fatalf("stage two status=%q", got)
	}
	inboxAfter := inboxCounts(parent.ID, stageOne.ID, stageTwo.ID)
	for _, issueID := range []string{parent.ID, stageOne.ID, stageTwo.ID} {
		if inboxAfter[issueID] != inboxBefore[issueID] {
			t.Fatalf("issue %s inbox count changed before=%d after=%d", issueID, inboxBefore[issueID], inboxAfter[issueID])
		}
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM activity_log WHERE issue_id=ANY($1::uuid[])`, []string{parent.ID, stageOne.ID, stageTwo.ID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []string{stageOne.ID, stageTwo.ID, parent.ID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, runtimeID)
	})
}
