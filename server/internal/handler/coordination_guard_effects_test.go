package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestWorkCoordinationDeleteGuardsHaveNoDBOrExternalEffects(t *testing.T) {
	ctx := context.Background()
	workspaceID := mustHandlerUUID(t, testWorkspaceID)
	userID := mustHandlerUUID(t, testUserID)
	suffix := time.Now().UnixNano()
	var rootID, secondID, runtimeID, agentID, taskID, tokenID, autopilotID, runID pgtype.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS guarded root','member',$2,'none',990301) RETURNING id`, workspaceID, userID).Scan(&rootID); err != nil {
		t.Fatalf("insert guarded root: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,'WCS guarded second','member',$2,'none',990302,$3) RETURNING id`, workspaceID, userID, rootID).Scan(&secondID); err != nil {
		t.Fatalf("insert guarded second: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata,last_seen_at,visibility,owner_id) VALUES ($1,$2,$3,'cloud','wcs-test','online','test','{}'::jsonb,now(),'private',$4) RETURNING id`, workspaceID, fmt.Sprintf("wcs-guard-daemon-%d", suffix), fmt.Sprintf("WCS Guard Runtime %d", suffix), userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id) VALUES ($1,$2,'','cloud','{}'::jsonb,$3,'private',1,$4) RETURNING id`, workspaceID, fmt.Sprintf("WCS Guard Agent %d", suffix), runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority,context) VALUES ($1,$2,$3,'queued',0,'{}'::jsonb) RETURNING id`, agentID, runtimeID, secondID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, fmt.Sprintf("wcs-guard-token-%d", suffix), taskID, agentID, workspaceID, userID).Scan(&tokenID); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO autopilot (workspace_id,title,assignee_id,execution_mode,created_by_type,created_by_id) VALUES ($1,$2,$3,'run_only','member',$4) RETURNING id`, workspaceID, fmt.Sprintf("WCS Guard Autopilot %d", suffix), agentID, userID).Scan(&autopilotID); err != nil {
		t.Fatalf("insert autopilot: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO autopilot_run (autopilot_id,source,status,issue_id) VALUES ($1,'manual','running',$2) RETURNING id`, autopilotID, secondID).Scan(&runID); err != nil {
		t.Fatalf("insert autopilot run: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO attachment (id,workspace_id,issue_id,uploader_type,uploader_id,filename,url,content_type,size_bytes) VALUES (gen_random_uuid(),$1,$2,'member',$3,'guard.txt','https://storage.test/wcs/guard.txt','text/plain',5)`, workspaceID, secondID, userID); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_dependency WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE id=$1`, runID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id=$1`, autopilotID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE id=$1`, tokenID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []pgtype.UUID{rootID, secondID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, runtimeID)
	})

	actor := service.CoordinationActor{WorkspaceID: workspaceID, ActorType: service.CoordinationActorMember, ActorID: userID}
	ensured, err := testHandler.CoordinationService.EnsureScope(ctx, actor, service.EnsureScopeInput{RootIssueID: rootID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "delete-guard-effects"})
	if err != nil {
		t.Fatalf("ensure guard scope: %v", err)
	}
	if _, err := testHandler.CoordinationService.AddDependency(ctx, actor, service.AddDependencyInput{ScopeID: ensured.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: secondID, UpstreamIssueID: rootID, IdempotencyKey: "delete-guard-effects-dependency"}); err != nil {
		t.Fatalf("add guarded dependency: %v", err)
	}
	bus := events.New()
	eventCount := 0
	bus.SubscribeAll(func(events.Event) { eventCount++ })
	storageDeletes := 0
	h := *testHandler
	h.Bus = bus
	h.Storage = &coordinationEffectStorage{onDeleteKeys: func([]string) { storageDeletes++ }}

	singleReq := withURLParam(newRequest(http.MethodDelete, "/api/issues/"+uuidToString(secondID), nil), "id", uuidToString(secondID))
	singleW := httptest.NewRecorder()
	h.DeleteIssue(singleW, singleReq)
	if singleW.Code != http.StatusConflict {
		t.Fatalf("single guard status=%d body=%s", singleW.Code, singleW.Body.String())
	}
	batchReq := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{"issue_ids": []string{uuidToString(rootID), uuidToString(secondID)}})
	batchW := httptest.NewRecorder()
	h.BatchDeleteIssues(batchW, batchReq)
	if batchW.Code != http.StatusConflict {
		t.Fatalf("batch guard status=%d body=%s", batchW.Code, batchW.Body.String())
	}
	workspaceReq := withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID, nil), "id", testWorkspaceID)
	workspaceW := httptest.NewRecorder()
	h.DeleteWorkspace(workspaceW, workspaceReq)
	if workspaceW.Code != http.StatusConflict {
		t.Fatalf("workspace guard status=%d body=%s", workspaceW.Code, workspaceW.Body.String())
	}

	var issueCount, tokenCount int
	var taskStatus, runStatus string
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=ANY($1::uuid[])`, []pgtype.UUID{rootID, secondID}).Scan(&issueCount); err != nil {
		t.Fatalf("count guarded issues: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1`, taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("load guarded task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE id=$1`, tokenID).Scan(&tokenCount); err != nil {
		t.Fatalf("count guarded token: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM autopilot_run WHERE id=$1`, runID).Scan(&runStatus); err != nil {
		t.Fatalf("load guarded autopilot run: %v", err)
	}
	if issueCount != 2 || taskStatus != "queued" || tokenCount != 1 || runStatus != "running" || eventCount != 0 || storageDeletes != 0 {
		t.Fatalf("guard side effects issues=%d task=%s tokens=%d run=%s events=%d storage=%d", issueCount, taskStatus, tokenCount, runStatus, eventCount, storageDeletes)
	}
}
