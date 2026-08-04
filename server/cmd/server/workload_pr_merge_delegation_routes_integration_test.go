package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
)

func TestPRMergeDelegationRoutesRequireHumanWorkspaceOperator(t *testing.T) {
	t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "1")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "router-v2-service-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "imile-win")
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
'router PR merge delegation v2', 'in_progress', 'none', 1, 'member', $2) RETURNING id`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create route issue: %v", err)
	}
	executionID := uuid.NewString()
	var taskID string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (
agent_id, runtime_id, issue_id, execution_id, status, priority, started_at
) VALUES ($1,$2,$3,$4,'running',0,now()) RETURNING id`, agentID, runtimeID, issueID, executionID).Scan(&taskID); err != nil {
		t.Fatalf("create route task: %v", err)
	}
	rawToken, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO task_token (
token_hash, task_id, agent_id, workspace_id, user_id, execution_id, expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7)`, auth.HashToken(rawToken), taskID, agentID, testWorkspaceID, testUserID, executionID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create route task token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})

	var memberUserID string
	memberEmail := "pr-merge-delegation-v2-member@multica.test"
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('Delegation V2 Member',$1) RETURNING id`, memberEmail).Scan(&memberUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, testWorkspaceID, memberUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, memberUserID)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id=$1`, memberUserID)
	})
	memberToken, err := generateTestJWT(memberUserID, memberEmail, "Delegation V2 Member")
	if err != nil {
		t.Fatal(err)
	}

	listPath := "/api/workspaces/" + testWorkspaceID + "/workload-delegations/pr-merge?issue_id=" + issueID
	for name, token := range map[string]string{"task": rawToken, "ordinary_member": memberToken} {
		req, err := http.NewRequest(http.MethodGet, testServer.URL+listPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s list status=%d, want 403", name, resp.StatusCode)
		}
	}
	listResp := authRequest(t, http.MethodGet, listPath, nil)
	if listResp.StatusCode != http.StatusOK {
		listResp.Body.Close()
		t.Fatalf("human list status=%d", listResp.StatusCode)
	}
	listResp.Body.Close()

	// The raw human grant route was removed: authority requests can only be
	// derived by an authenticated workload assertion attempt.
	legacyResp := authRequest(t, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/workload-delegations/pr-merge", map[string]any{"task_id": taskID})
	if legacyResp.StatusCode != http.StatusMethodNotAllowed {
		legacyResp.Body.Close()
		t.Fatalf("legacy raw grant status=%d, want 405", legacyResp.StatusCode)
	}
	legacyResp.Body.Close()

	unknownID := uuid.NewString()
	approveResp := authRequest(t, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/workload-delegations/pr-merge/"+unknownID+"/approve", map[string]any{})
	if approveResp.StatusCode != http.StatusNotFound {
		approveResp.Body.Close()
		t.Fatalf("unknown approve status=%d", approveResp.StatusCode)
	}
	approveResp.Body.Close()
}
