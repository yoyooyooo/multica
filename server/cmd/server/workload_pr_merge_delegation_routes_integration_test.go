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

func TestPRMergeDelegationRoutesRequireHumanWorkspaceOperator(t *testing.T) {
	ctx := context.Background()
	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT id, runtime_id FROM agent
WHERE workspace_id=$1 AND runtime_id IS NOT NULL ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("load integration agent: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (
workspace_id, number, title, status, priority, position, creator_type, creator_id
) VALUES ($1, (SELECT issue_counter + 1100 FROM workspace WHERE id=$1),
'router PR merge delegation', 'in_progress', 'none', 1, 'member', $2) RETURNING id`,
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
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation WHERE task_id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM activity_log WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})

	var memberUserID string
	memberEmail := "pr-merge-delegation-member@multica.test"
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('Delegation Member',$1) RETURNING id`, memberEmail).Scan(&memberUserID); err != nil {
		t.Fatalf("create non-operator member: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, testWorkspaceID, memberUserID); err != nil {
		t.Fatalf("add non-operator member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, memberUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, memberUserID)
	})
	memberToken, err := generateTestJWT(memberUserID, memberEmail, "Delegation Member")
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"task_id": taskID, "run_id": taskID, "repository": "jackie/agent-kit",
		"pull_request_number": 41, "forgejo_pull_request_number": 52,
		"expected_head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"merge_method":      "fast-forward-only", "ttl_seconds": 600,
	}
	encoded, _ := json.Marshal(body)
	machineReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/workspaces/"+testWorkspaceID+"/workload-delegations/pr-merge", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	machineReq.Header.Set("Authorization", "Bearer "+rawToken)
	machineReq.Header.Set("Content-Type", "application/json")
	machineResp, err := http.DefaultClient.Do(machineReq)
	if err != nil {
		t.Fatal(err)
	}
	machineResp.Body.Close()
	if machineResp.StatusCode != http.StatusForbidden {
		t.Fatalf("task token create status=%d, want 403", machineResp.StatusCode)
	}

	memberReq, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/workspaces/"+testWorkspaceID+"/workload-delegations/pr-merge", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	memberReq.Header.Set("Authorization", "Bearer "+memberToken)
	memberReq.Header.Set("Content-Type", "application/json")
	memberResp, err := http.DefaultClient.Do(memberReq)
	if err != nil {
		t.Fatal(err)
	}
	memberResp.Body.Close()
	if memberResp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-operator member create status=%d, want 403", memberResp.StatusCode)
	}

	createResp := authRequest(t, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/workload-delegations/pr-merge", body)
	if createResp.StatusCode != http.StatusCreated {
		createResp.Body.Close()
		t.Fatalf("human create status=%d", createResp.StatusCode)
	}
	var created struct {
		ID                string `json:"id"`
		State             string `json:"state"`
		AuthorityRevision string `json:"authority_revision"`
		GrantedByUserID   string `json:"granted_by_user_id"`
	}
	readJSON(t, createResp, &created)
	if created.ID == "" || created.State != "active" || created.AuthorityRevision == "" || created.GrantedByUserID != testUserID {
		t.Fatalf("created delegation=%#v", created)
	}

	getPath := "/api/workspaces/" + testWorkspaceID + "/workload-delegations/pr-merge/" + created.ID
	getResp := authRequest(t, http.MethodGet, getPath, nil)
	if getResp.StatusCode != http.StatusOK {
		getResp.Body.Close()
		t.Fatalf("human read status=%d", getResp.StatusCode)
	}
	getResp.Body.Close()

	machineGetReq, err := http.NewRequest(http.MethodGet, testServer.URL+getPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	machineGetReq.Header.Set("Authorization", "Bearer "+rawToken)
	machineGetResp, err := http.DefaultClient.Do(machineGetReq)
	if err != nil {
		t.Fatal(err)
	}
	machineGetResp.Body.Close()
	if machineGetResp.StatusCode != http.StatusForbidden {
		t.Fatalf("task token read status=%d, want 403", machineGetResp.StatusCode)
	}

	revokePath := getPath + "/revoke"
	machineRevokeBody, _ := json.Marshal(map[string]any{"reason": "machine attempted revocation"})
	machineRevokeReq, err := http.NewRequest(http.MethodPost, testServer.URL+revokePath, bytes.NewReader(machineRevokeBody))
	if err != nil {
		t.Fatal(err)
	}
	machineRevokeReq.Header.Set("Authorization", "Bearer "+rawToken)
	machineRevokeReq.Header.Set("Content-Type", "application/json")
	machineRevokeResp, err := http.DefaultClient.Do(machineRevokeReq)
	if err != nil {
		t.Fatal(err)
	}
	machineRevokeResp.Body.Close()
	if machineRevokeResp.StatusCode != http.StatusForbidden {
		t.Fatalf("task token revoke status=%d, want 403", machineRevokeResp.StatusCode)
	}

	revokeResp := authRequest(t, http.MethodPost, revokePath, map[string]any{
		"reason": "operator cancelled exact merge authority",
	})
	if revokeResp.StatusCode != http.StatusOK {
		revokeResp.Body.Close()
		t.Fatalf("human revoke status=%d", revokeResp.StatusCode)
	}
	var revoked struct {
		State string `json:"state"`
	}
	readJSON(t, revokeResp, &revoked)
	if revoked.State != "revoked" {
		t.Fatalf("revoked state=%q", revoked.State)
	}
}
