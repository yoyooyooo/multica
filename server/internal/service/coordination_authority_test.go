package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationReplayRevalidatesCurrentAuthority(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	input := EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "authority-replay"}
	if _, err := svc.EnsureScope(ctx, actor, input); err != nil {
		t.Fatalf("ensure scope: %v", err)
	}

	var otherUser pgtype.UUID
	suffix := time.Now().UnixNano()
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('WCS Other',$1) RETURNING id`, fmt.Sprintf("wcs-other-%d@multica.ai", suffix)).Scan(&otherUser); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, fixture.workspaceID, otherUser); err != nil {
		t.Fatalf("insert other member: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, otherUser) })
	otherActor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: otherUser}
	if _, err := svc.EnsureScope(ctx, otherActor, input); coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("different actor replay code=%q err=%v", coordinationCode(err), err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, fixture.workspaceID, fixture.userID); err != nil {
		t.Fatalf("revoke member: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("revoked member replay code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, fixture.workspaceID, fixture.userID); err != nil {
		t.Fatalf("restore member: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, fixture.issueID); err != nil {
		t.Fatalf("delete root out of band: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("missing root replay code=%q err=%v", coordinationCode(err), err)
	}
}

func TestWorkCoordinationAgentUsesExactCurrentTaskCredential(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	agent := createWorkCoordinationAgentFixture(t, pool, fixture)
	svc := NewCoordinationService(db.New(pool), pool)
	input := EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "agent-replay"}
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorAgent, ActorID: agent.agentID, TaskID: agent.taskID, TaskCredentialRef: agent.tokenOneID}
	if _, err := svc.EnsureScope(ctx, actor, input); err != nil {
		t.Fatalf("agent ensure: %v", err)
	}
	var otherTaskID, otherTokenID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority,context) VALUES ($1,$2,$3,'running',0,'{}'::jsonb) RETURNING id`, agent.agentID, agent.runtimeID, fixture.issueID).Scan(&otherTaskID); err != nil {
		t.Fatalf("insert other task: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, fmt.Sprintf("wcs-other-task-token-%d", time.Now().UnixNano()), otherTaskID, agent.agentID, fixture.workspaceID, fixture.userID).Scan(&otherTokenID); err != nil {
		t.Fatalf("insert other task token: %v", err)
	}
	otherTaskActor := actor
	otherTaskActor.TaskID = otherTaskID
	otherTaskActor.TaskCredentialRef = otherTokenID
	if _, err := svc.EnsureScope(ctx, otherTaskActor, input); coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("different task replay code=%q err=%v", coordinationCode(err), err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM task_token WHERE id=$1`, agent.tokenOneID); err != nil {
		t.Fatalf("revoke presented token: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("revoked exact token code=%q err=%v", coordinationCode(err), err)
	}
	actor.TaskCredentialRef = agent.tokenTwoID
	if result, err := svc.EnsureScope(ctx, actor, input); err != nil || result.Outcome != CoordinationOutcomeReplay {
		t.Fatalf("replacement credential exact replay result=%+v err=%v", result, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_token SET expires_at=now()-interval '1 second' WHERE id=$1`, agent.tokenTwoID); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("expired token code=%q err=%v", coordinationCode(err), err)
	}

	var tokenThree pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, fmt.Sprintf("wcs-token-three-%d", time.Now().UnixNano()), agent.taskID, agent.agentID, fixture.workspaceID, fixture.userID).Scan(&tokenThree); err != nil {
		t.Fatalf("insert third token: %v", err)
	}
	actor.TaskCredentialRef = tokenThree
	var unrelatedRoot pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS unrelated','member',$2,'none',99) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&unrelatedRoot); err != nil {
		t.Fatalf("insert unrelated root: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=$1 WHERE id=$2`, unrelatedRoot, agent.taskID); err != nil {
		t.Fatalf("move task authority: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("lost root authority code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=NULL WHERE id=$1`, agent.taskID); err != nil {
		t.Fatalf("clear task issue: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("issue-less task code=%q err=%v", coordinationCode(err), err)
	}
}

type workCoordinationAgentFixture struct {
	runtimeID  pgtype.UUID
	agentID    pgtype.UUID
	taskID     pgtype.UUID
	tokenOneID pgtype.UUID
	tokenTwoID pgtype.UUID
}

func createWorkCoordinationAgentFixture(t *testing.T, pool *pgxpool.Pool, fixture workCoordinationFixture) workCoordinationAgentFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	var result workCoordinationAgentFixture
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata,last_seen_at,visibility,owner_id) VALUES ($1,$2,$3,'cloud','wcs-test','online','test','{}'::jsonb,now(),'private',$4) RETURNING id`, fixture.workspaceID, fmt.Sprintf("wcs-daemon-%d", suffix), fmt.Sprintf("WCS Runtime %d", suffix), fixture.userID).Scan(&result.runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id) VALUES ($1,$2,'','cloud','{}'::jsonb,$3,'private',1,$4) RETURNING id`, fixture.workspaceID, fmt.Sprintf("WCS Agent %d", suffix), result.runtimeID, fixture.userID).Scan(&result.agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority,context) VALUES ($1,$2,$3,'running',0,'{}'::jsonb) RETURNING id`, result.agentID, result.runtimeID, fixture.issueID).Scan(&result.taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	for i, target := range []*pgtype.UUID{&result.tokenOneID, &result.tokenTwoID} {
		if err := pool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, fmt.Sprintf("wcs-token-%d-%d", suffix, i), result.taskID, result.agentID, fixture.workspaceID, fixture.userID).Scan(target); err != nil {
			t.Fatalf("insert task token %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id=$1`, result.taskID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, result.taskID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, result.agentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, result.runtimeID)
	})
	return result
}

func TestWorkCoordinationActualRootValidation(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	foreign := createWorkCoordinationFixture(t, pool)
	q := db.New(pool)

	var child pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,'WCS child','member',$2,'none',2,$3) RETURNING id`, fixture.workspaceID, fixture.userID, fixture.issueID).Scan(&child); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	actual, err := q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: child})
	if err != nil || actual.Status != "ok" || !uuidEqual(actual.RootIssueID, fixture.issueID) {
		t.Fatalf("child actual root=%+v err=%v", actual, err)
	}
	missing := util.MustParseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")
	actual, err = q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: missing})
	if err != nil || actual.Status != "missing" {
		t.Fatalf("missing status=%+v err=%v", actual, err)
	}
	actual, err = q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: foreign.issueID})
	if err != nil || actual.Status != "cross_workspace" {
		t.Fatalf("foreign status=%+v err=%v", actual, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET parent_issue_id=$1 WHERE id=$2`, foreign.issueID, child); err != nil {
		t.Fatalf("set foreign parent: %v", err)
	}
	actual, err = q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: child})
	if err != nil || actual.Status != "foreign_parent" {
		t.Fatalf("foreign parent status=%+v err=%v", actual, err)
	}
	svc := NewCoordinationService(q, pool)
	if err := svc.revalidateWorkspaceAndRoot(ctx, q, fixture.workspaceID, child); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("foreign parent service code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET parent_issue_id=$1 WHERE id=$2`, fixture.issueID, child); err != nil {
		t.Fatalf("restore child parent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET parent_issue_id=$1 WHERE id=$2`, child, fixture.issueID); err != nil {
		t.Fatalf("create cycle: %v", err)
	}
	actual, err = q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: child})
	if err != nil || actual.Status != "cycle" {
		t.Fatalf("cycle status=%+v err=%v", actual, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET parent_issue_id=NULL WHERE id=$1`, fixture.issueID); err != nil {
		t.Fatalf("clear cycle: %v", err)
	}

	parent := fixture.issueID
	for i := 1; i <= 256; i++ {
		var next pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,$2,'member',$3,'none',$4,$5) RETURNING id`, fixture.workspaceID, fmt.Sprintf("WCS depth %d", i), fixture.userID, 1000+i, parent).Scan(&next); err != nil {
			t.Fatalf("insert depth %d: %v", i, err)
		}
		parent = next
	}
	actual, err = q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: parent})
	if err != nil || actual.Status != "ok" || actual.Depth != 256 || !uuidEqual(actual.RootIssueID, fixture.issueID) {
		t.Fatalf("depth-256 status=%+v err=%v", actual, err)
	}
	var overflow pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id) VALUES ($1,'WCS depth 257','member',$2,'none',2000,$3) RETURNING id`, fixture.workspaceID, fixture.userID, parent).Scan(&overflow); err != nil {
		t.Fatalf("insert depth 257: %v", err)
	}
	actual, err = q.ValidateIssueActualRoot(ctx, db.ValidateIssueActualRootParams{WorkspaceID: fixture.workspaceID, IssueID: overflow})
	if err != nil || actual.Status != "depth_exceeded" {
		t.Fatalf("depth-257 status=%+v err=%v", actual, err)
	}
}
