package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	authn "github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
)

func TestWorkCoordinationTaskTokenAuthCarriesExactCredentialRef(t *testing.T) {
	ctx := context.Background()
	workspaceID := mustHandlerUUID(t, testWorkspaceID)
	userID := mustHandlerUUID(t, testUserID)
	suffix := time.Now().UnixNano()
	var rootID, runtimeID, agentID, taskID, tokenOneID, tokenTwoID pgtype.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS auth root','member',$2,'none',990401) RETURNING id`, workspaceID, userID).Scan(&rootID); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata,last_seen_at,visibility,owner_id) VALUES ($1,$2,$3,'cloud','wcs-test','online','test','{}'::jsonb,now(),'private',$4) RETURNING id`, workspaceID, fmt.Sprintf("wcs-auth-daemon-%d", suffix), fmt.Sprintf("WCS Auth Runtime %d", suffix), userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id) VALUES ($1,$2,'','cloud','{}'::jsonb,$3,'private',1,$4) RETURNING id`, workspaceID, fmt.Sprintf("WCS Auth Agent %d", suffix), runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority,context) VALUES ($1,$2,$3,'running',0,'{}'::jsonb) RETURNING id`, agentID, runtimeID, rootID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	rawTokenOne := fmt.Sprintf("mat_wcs_exact_one_%d", suffix)
	rawTokenTwo := fmt.Sprintf("mat_wcs_exact_two_%d", suffix)
	if err := testPool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, authn.HashToken(rawTokenOne), taskID, agentID, workspaceID, userID).Scan(&tokenOneID); err != nil {
		t.Fatalf("insert token one: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, authn.HashToken(rawTokenTwo), taskID, agentID, workspaceID, userID).Scan(&tokenTwoID); err != nil {
		t.Fatalf("insert token two: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, rootID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, runtimeID)
	})

	deletePresentedBeforeHandler := middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := testPool.Exec(r.Context(), `DELETE FROM task_token WHERE id=$1`, tokenOneID); err != nil {
			t.Fatalf("delete presented token: %v", err)
		}
		testHandler.EnsureCoordinationScope(w, r)
	}))
	revokedReq := newTaskTokenCoordinationRequest(rawTokenOne, rootID, "auth-exact-revoked")
	revokedReq.Header.Set("X-Agent-ID", "00000000-0000-0000-0000-000000000099")
	revokedReq.Header.Set("X-Task-ID", "00000000-0000-0000-0000-000000000098")
	revokedReq.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000097")
	revokedReq.Header.Set("X-Actor-Source", "task_token")
	revokedW := httptest.NewRecorder()
	deletePresentedBeforeHandler.ServeHTTP(revokedW, revokedReq)
	if revokedW.Code != http.StatusForbidden || !strings.Contains(revokedW.Body.String(), `"code":"coordination_forbidden"`) {
		t.Fatalf("revoked exact credential status=%d body=%s", revokedW.Code, revokedW.Body.String())
	}
	var scopeCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM coordination_scope WHERE workspace_id=$1`, workspaceID).Scan(&scopeCount); err != nil || scopeCount != 0 {
		t.Fatalf("revoked exact credential wrote scopes=%d err=%v", scopeCount, err)
	}

	authenticated := middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(testHandler.EnsureCoordinationScope))
	validReq := newTaskTokenCoordinationRequest(rawTokenTwo, rootID, "auth-exact-valid")
	validW := httptest.NewRecorder()
	authenticated.ServeHTTP(validW, validReq)
	if validW.Code != http.StatusCreated {
		t.Fatalf("valid exact credential status=%d body=%s", validW.Code, validW.Body.String())
	}
	var response struct {
		Scope struct {
			CreatedBy struct {
				ActorType string  `json:"actor_type"`
				ActorID   string  `json:"actor_id"`
				TaskID    *string `json:"task_id"`
			} `json:"created_by"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(validW.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode valid response: %v", err)
	}
	if response.Scope.CreatedBy.ActorType != "agent" || response.Scope.CreatedBy.ActorID != uuidToString(agentID) || response.Scope.CreatedBy.TaskID == nil || *response.Scope.CreatedBy.TaskID != uuidToString(taskID) {
		t.Fatalf("server-stamped creator=%+v", response.Scope.CreatedBy)
	}
}

func newTaskTokenCoordinationRequest(token string, rootID pgtype.UUID, key string) *http.Request {
	body := fmt.Sprintf(`{"root_issue_id":%q,"workflow_profile_key":"matt-loop"}`, uuidToString(rootID))
	req := httptest.NewRequest(http.MethodPost, "/api/coordination/scopes", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	return req
}
