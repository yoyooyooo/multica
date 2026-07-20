package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationDeletionEffectsCaptureCompactMetricsContext(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	suffix := time.Now().UnixNano()
	var runtimeID, agentID, taskID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata,last_seen_at,visibility,owner_id) VALUES ($1,$2,$3,'cloud','wcs-test','online','test','{}'::jsonb,now(),'private',$4) RETURNING id`, fixture.workspaceID, fmt.Sprintf("wcs-effects-daemon-%d", suffix), fmt.Sprintf("WCS Effects Runtime %d", suffix), fixture.userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id) VALUES ($1,$2,'','cloud','{}'::jsonb,$3,'private',1,$4) RETURNING id`, fixture.workspaceID, fmt.Sprintf("WCS Effects Agent %d", suffix), runtimeID, fixture.userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority,context) VALUES ($1,$2,$3,'queued',0,'{}'::jsonb) RETURNING id`, agentID, runtimeID, fixture.issueID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, agentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, runtimeID)
	})

	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	result, err := handle.Delete(ctx, fixture.issueID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(result.Effects.CancelledTasks) != 1 || len(result.Effects.AffectedAgentIDs) != 1 {
		t.Fatalf("effects=%+v", result.Effects)
	}
	effect := result.Effects.CancelledTasks[0]
	if effect.ID.Bytes != taskID.Bytes || effect.WorkspaceID.Bytes != fixture.workspaceID.Bytes || effect.MetricsSource != "issue" || effect.RuntimeMode != "cloud" || effect.Provider != "wcs-test" {
		t.Fatalf("task effect=%+v", effect)
	}
	if err := handle.Finish(false); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestWorkCoordinationCancellationEffectsTolerateMissingAgentProjection(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	fixture := createWorkCoordinationFixture(t, pool)
	tasks := []db.AgentTaskQueue{
		{ID: fixture.workspaceID},
		{ID: fixture.issueID, AgentID: fixture.issueID},
	}
	effects, agentIDs, err := captureCancelledTaskEffects(context.Background(), db.New(pool), fixture.workspaceID, tasks)
	if err != nil {
		t.Fatalf("capture effects: %v", err)
	}
	if len(effects) != 2 || effects[0].RuntimeMode != "" || effects[1].RuntimeMode != "" {
		t.Fatalf("effects=%+v", effects)
	}
	if len(agentIDs) != 1 || agentIDs[0].Bytes != fixture.issueID.Bytes {
		t.Fatalf("agent IDs=%+v", agentIDs)
	}
}

func TestWorkCoordinationIssueDeletionGuardAndFinishOrdering(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	if _, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "delete-guard"}); err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	if _, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle); coordinationCode(err) != CoordinationDeleteBlocked {
		t.Fatalf("guard error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM coordination_scope WHERE workspace_id=$1`, fixture.workspaceID); err != nil {
		t.Fatalf("delete scope while retaining receipt: %v", err)
	}

	handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID, fixture.issueID, util.MustParseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")}, IssueDeletionBatch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	targets := handle.TargetIssueIDs()
	if len(targets) != 1 || !uuidEqual(targets[0], fixture.issueID) {
		t.Fatalf("actual targets=%v", targets)
	}
	result, err := handle.Delete(ctx, targets[0])
	if err != nil || result.Outcome != IssueDeletionDeleted || !uuidEqual(result.Effects.IssueID, fixture.issueID) {
		t.Fatalf("delete result=%+v err=%v", result, err)
	}

	key, _ := CoordinationWorkspaceAdvisoryKey(fixture.workspaceID)
	probeConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock probe: %v", err)
	}
	defer probeConn.Release()
	var available bool
	if err := probeConn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, key).Scan(&available); err != nil {
		t.Fatalf("try lock before finish: %v", err)
	}
	if available {
		_, _ = probeConn.Exec(ctx, `SELECT pg_advisory_unlock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, key)
		t.Fatal("session lock was released before Finish")
	}
	if err := handle.Finish(true); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := probeConn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, key).Scan(&available); err != nil || !available {
		t.Fatalf("lock not available after finish: available=%v err=%v", available, err)
	}
	_, _ = probeConn.Exec(ctx, `SELECT pg_advisory_unlock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, key)
	if err := handle.Finish(true); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("repeated finish error=%v", err)
	}
	if _, err := handle.Delete(ctx, fixture.issueID); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("repeated delete error=%v", err)
	}
	var issueExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&issueExists); err != nil || issueExists {
		t.Fatalf("issue exists=%v err=%v", issueExists, err)
	}
	var receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE workspace_id=$1`, fixture.workspaceID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
}

func TestWorkCoordinationBatchDeleteSavepointPartialSuccess(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	var restricted pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS restricted','member',$2,'none',2) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&restricted); err != nil {
		t.Fatalf("insert restricted issue: %v", err)
	}
	installIssueDeleteFailureTrigger(t, pool, restricted, "23503")

	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{restricted, fixture.issueID, fixture.issueID}, IssueDeletionBatch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	deleted := 0
	skipped := 0
	for _, targetID := range handle.TargetIssueIDs() {
		result, deleteErr := handle.Delete(ctx, targetID)
		if deleteErr != nil {
			t.Fatalf("delete %s: %v", util.UUIDToString(targetID), deleteErr)
		}
		switch result.Outcome {
		case IssueDeletionDeleted:
			deleted++
		case IssueDeletionSkippedRecoverable:
			skipped++
			if result.Phase != IssueDeletionPhaseEntityDelete || result.SafeCode != "target_restricted" || result.Effects.IssueID.Valid || len(result.Effects.CancelledTasks) != 0 || len(result.Effects.AttachmentURLs) != 0 {
				t.Fatalf("unsafe skipped result=%+v", result)
			}
		default:
			t.Fatalf("unknown outcome=%q", result.Outcome)
		}
	}
	if deleted != 1 || skipped != 1 {
		t.Fatalf("deleted=%d skipped=%d", deleted, skipped)
	}
	if err := handle.Finish(true); err != nil {
		t.Fatalf("finish: %v", err)
	}
	var ordinaryExists, restrictedExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1), EXISTS(SELECT 1 FROM issue WHERE id=$2)`, fixture.issueID, restricted).Scan(&ordinaryExists, &restrictedExists); err != nil {
		t.Fatalf("check remaining issues: %v", err)
	}
	if ordinaryExists || !restrictedExists {
		t.Fatalf("ordinary_exists=%v restricted_exists=%v", ordinaryExists, restrictedExists)
	}
}

func TestWorkCoordinationBatchFatalErrorRollsBackAllTargets(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	var second pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS second','member',$2,'none',2) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&second); err != nil {
		t.Fatalf("insert second issue: %v", err)
	}
	ids := []pgtype.UUID{fixture.issueID, second}
	sortUUIDs(ids)
	installIssueDeleteFailureTrigger(t, pool, ids[1], "40001")

	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, ids, IssueDeletionBatch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	first, err := handle.Delete(ctx, ids[0])
	if err != nil || first.Outcome != IssueDeletionDeleted {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	_, err = handle.Delete(ctx, ids[1])
	var fatal *IssueDeletionFatalError
	if !errors.As(err, &fatal) || fatal.Phase != IssueDeletionPhaseEntityDelete || fatal.Class != IssueDeletionFailureSerialization {
		t.Fatalf("fatal=%+v err=%v", fatal, err)
	}
	if err := handle.Finish(true); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("failed handle must reject commit and roll back: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id = ANY($1::uuid[])`, ids).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func TestWorkCoordinationBatchSavepointFailuresAreFatal(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failSQL       string
		restricted    bool
		expectedPhase IssueDeletionPhase
	}{
		{name: "create", failSQL: "SAVEPOINT coordination_issue_delete_target", expectedPhase: IssueDeletionPhaseSavepointCreate},
		{name: "release success", failSQL: "RELEASE SAVEPOINT coordination_issue_delete_target", expectedPhase: IssueDeletionPhaseSavepointRelease},
		{name: "rollback restricted", failSQL: "ROLLBACK TO SAVEPOINT coordination_issue_delete_target", restricted: true, expectedPhase: IssueDeletionPhaseSavepointRollback},
		{name: "release restricted", failSQL: "RELEASE SAVEPOINT coordination_issue_delete_target", restricted: true, expectedPhase: IssueDeletionPhaseSavepointRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			if tc.restricted {
				installIssueDeleteFailureTrigger(t, pool, fixture.issueID, "23503")
			}
			svc := NewCoordinationService(db.New(pool), pool)
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionBatch)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			handle.savepointExec = func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
				if sql == tc.failSQL {
					return pgconn.CommandTag{}, errors.New("injected savepoint failure")
				}
				return handle.lifecycle.tx.Exec(ctx, sql, args...)
			}
			_, err = handle.Delete(ctx, fixture.issueID)
			var fatal *IssueDeletionFatalError
			if !errors.As(err, &fatal) || fatal.Phase != tc.expectedPhase || fatal.Class != IssueDeletionFailureSavepoint {
				t.Fatalf("fatal=%+v err=%v", fatal, err)
			}
			if err := handle.Finish(false); err != nil {
				t.Fatalf("finish rollback: %v", err)
			}
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&exists); err != nil || !exists {
				t.Fatalf("issue exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestWorkCoordinationIssueHandleRejectsOutsideAndDuplicateTargets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		duplicate bool
	}{
		{name: "outside"},
		{name: "duplicate", duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			svc := NewCoordinationService(db.New(pool), pool)
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionBatch)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if tc.duplicate {
				if _, err := handle.Delete(ctx, fixture.issueID); err != nil {
					t.Fatalf("first delete: %v", err)
				}
				_, err = handle.Delete(ctx, fixture.issueID)
			} else {
				_, err = handle.Delete(ctx, util.MustParseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff"))
			}
			if coordinationCode(err) != CoordinationInternal {
				t.Fatalf("typed internal error=%v", err)
			}
			if err := handle.Finish(false); err != nil {
				t.Fatalf("finish rollback: %v", err)
			}
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&exists); err != nil || !exists {
				t.Fatalf("issue exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestWorkCoordinationSingleDeleteNeverSkipsRestriction(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	installIssueDeleteFailureTrigger(t, pool, fixture.issueID, "23503")
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_, err = handle.Delete(ctx, fixture.issueID)
	var fatal *IssueDeletionFatalError
	if !errors.As(err, &fatal) || fatal.Phase != IssueDeletionPhaseEntityDelete {
		t.Fatalf("single restriction was not fatal: %+v err=%v", fatal, err)
	}
	if err := handle.Finish(false); err != nil {
		t.Fatalf("finish rollback: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&exists); err != nil || !exists {
		t.Fatalf("issue exists=%v err=%v", exists, err)
	}
}

func TestWorkCoordinationCancelledDeleteContextIsFatalAndRollbackable(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireIssueDeletion(context.Background(), actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = handle.Delete(ctx, fixture.issueID)
	var fatal *IssueDeletionFatalError
	if !errors.As(err, &fatal) || fatal.Class != IssueDeletionFailureContext {
		t.Fatalf("cancelled delete fatal=%+v err=%v", fatal, err)
	}
	if err := handle.Finish(false); err != nil {
		t.Fatalf("finish rollback: %v", err)
	}
}

func TestWorkCoordinationCommitFailureReturnsNoUsableFinish(t *testing.T) {
	for _, sqlState := range []string{"40001", "40P01"} {
		t.Run(sqlState, func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			installDeferredIssueDeleteFailureTrigger(t, pool, fixture.issueID, sqlState)

			svc := NewCoordinationService(db.New(pool), pool)
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			result, err := handle.Delete(ctx, fixture.issueID)
			if err != nil || result.Outcome != IssueDeletionDeleted {
				t.Fatalf("delete result=%+v err=%v", result, err)
			}
			if err := handle.Finish(true); coordinationCode(err) != CoordinationInternal {
				t.Fatalf("commit failure code=%q err=%v", coordinationCode(err), err)
			}
			if handle.lifecycle.state != deleteStateReleased || handle.lifecycle.conn != nil {
				t.Fatalf("known commit failure state=%s conn=%v", handle.lifecycle.state, handle.lifecycle.conn)
			}
			if err := handle.Finish(true); coordinationCode(err) != CoordinationInternal {
				t.Fatalf("repeated finish error=%v", err)
			}
			var issueExists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&issueExists); err != nil || !issueExists {
				t.Fatalf("commit failure issue exists=%v err=%v", issueExists, err)
			}
		})
	}
}

func TestWorkCoordinationCommitSuccessUnlockFailureDiscardsAndReturnsError(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := handle.Delete(ctx, fixture.issueID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	unlocked, err := db.New(handle.lifecycle.conn).CoordinationAdvisorySessionUnlock(ctx, db.CoordinationAdvisorySessionUnlockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: handle.lifecycle.lockKey})
	if err != nil || !unlocked {
		t.Fatalf("pre-unlock: unlocked=%v err=%v", unlocked, err)
	}
	if err := handle.Finish(true); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("finish error=%v", err)
	}
	if handle.lifecycle.state != deleteStateDiscarded || handle.lifecycle.conn != nil {
		t.Fatalf("unlock failure state=%s conn=%v", handle.lifecycle.state, handle.lifecycle.conn)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&exists); err != nil || exists {
		t.Fatalf("commit should be durable despite finish error: exists=%v err=%v", exists, err)
	}
}

func TestWorkCoordinationWorkspaceFailureCannotBeCommitted(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireWorkspaceDeletion(ctx, actor, fixture.workspaceID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = handle.Delete(cancelledCtx)
	var fatal *WorkspaceDeletionFatalError
	if !errors.As(err, &fatal) || fatal.Class != IssueDeletionFailureContext {
		t.Fatalf("workspace fatal=%+v err=%v", fatal, err)
	}
	if err := handle.Finish(true); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("failed workspace handle must reject commit: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace WHERE id=$1)`, fixture.workspaceID).Scan(&exists); err != nil || !exists {
		t.Fatalf("workspace exists=%v err=%v", exists, err)
	}
}

func TestWorkCoordinationWorkspaceDeleteFailureRollsBackMembershipTeardown(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	installWorkspaceDeleteFailureTrigger(t, pool, fixture.workspaceID, "23503")
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	handle, err := svc.AcquireWorkspaceDeletion(ctx, actor, fixture.workspaceID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_, err = handle.Delete(ctx)
	var fatal *WorkspaceDeletionFatalError
	if !errors.As(err, &fatal) || fatal.Phase != IssueDeletionPhaseEntityDelete || fatal.Class != IssueDeletionFailureStatement {
		t.Fatalf("fatal=%+v err=%v", fatal, err)
	}
	if err := handle.Finish(false); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var workspaceExists, memberExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace WHERE id=$1), EXISTS(SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2)`, fixture.workspaceID, fixture.userID).Scan(&workspaceExists, &memberExists); err != nil {
		t.Fatalf("verify rollback: %v", err)
	}
	if !workspaceExists || !memberExists {
		t.Fatalf("workspace exists=%v member exists=%v", workspaceExists, memberExists)
	}
}

func TestWorkCoordinationWorkspaceDeletionGuardAndSuccess(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	if _, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "workspace-guard"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := svc.AcquireWorkspaceDeletion(ctx, actor, fixture.workspaceID); coordinationCode(err) != CoordinationDeleteBlocked {
		t.Fatalf("guard error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM coordination_receipt WHERE workspace_id=$1`, fixture.workspaceID); err != nil {
		t.Fatalf("delete receipts: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM coordination_scope WHERE workspace_id=$1`, fixture.workspaceID); err != nil {
		t.Fatalf("delete scopes: %v", err)
	}
	handle, err := svc.AcquireWorkspaceDeletion(ctx, actor, fixture.workspaceID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	effects, err := handle.Delete(ctx)
	if err != nil || !uuidEqual(effects.WorkspaceID, fixture.workspaceID) || len(effects.AffectedUserIDs) != 1 {
		t.Fatalf("effects=%+v err=%v", effects, err)
	}
	if err := handle.Finish(true); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := handle.Finish(false); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("repeated finish error=%v", err)
	}
	var workspaceExists, memberExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace WHERE id=$1), EXISTS(SELECT 1 FROM member WHERE workspace_id=$1)`, fixture.workspaceID).Scan(&workspaceExists, &memberExists); err != nil {
		t.Fatalf("verify delete: %v", err)
	}
	if workspaceExists || memberExists {
		t.Fatalf("workspace exists=%v member exists=%v", workspaceExists, memberExists)
	}
}

func installIssueDeleteFailureTrigger(t *testing.T, pool pgxBeginQueryExecer, issueID pgtype.UUID, sqlState string) {
	t.Helper()
	ctx := context.Background()
	fn := fmt.Sprintf("wcs_fail_delete_%s_%d", sqlState, time.Now().UnixNano())
	trigger := fn + "_trigger"
	quotedFn := pgx.Identifier{fn}.Sanitize()
	quotedTrigger := pgx.Identifier{trigger}.Sanitize()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced issue delete failure' USING ERRCODE='%s'; END $$`, quotedFn, sqlState)); err != nil {
		t.Fatalf("create failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON issue FOR EACH ROW WHEN (OLD.id = '%s'::uuid) EXECUTE FUNCTION %s()`, quotedTrigger, util.UUIDToString(issueID), quotedFn)); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON issue`, quotedTrigger))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFn))
	})
}

func installWorkspaceDeleteFailureTrigger(t *testing.T, pool pgxBeginQueryExecer, workspaceID pgtype.UUID, sqlState string) {
	t.Helper()
	ctx := context.Background()
	fn := fmt.Sprintf("wcs_fail_workspace_delete_%s_%d", sqlState, time.Now().UnixNano())
	trigger := fn + "_trigger"
	quotedFn := pgx.Identifier{fn}.Sanitize()
	quotedTrigger := pgx.Identifier{trigger}.Sanitize()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced workspace delete failure' USING ERRCODE='%s'; END $$`, quotedFn, sqlState)); err != nil {
		t.Fatalf("create workspace failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON workspace FOR EACH ROW WHEN (OLD.id = '%s'::uuid) EXECUTE FUNCTION %s()`, quotedTrigger, util.UUIDToString(workspaceID), quotedFn)); err != nil {
		t.Fatalf("create workspace failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON workspace`, quotedTrigger))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFn))
	})
}

func installDeferredIssueDeleteFailureTrigger(t *testing.T, pool pgxBeginQueryExecer, issueID pgtype.UUID, sqlState string) {
	t.Helper()
	ctx := context.Background()
	fn := fmt.Sprintf("wcs_fail_commit_%s_%d", sqlState, time.Now().UnixNano())
	trigger := fn + "_trigger"
	quotedFn := pgx.Identifier{fn}.Sanitize()
	quotedTrigger := pgx.Identifier{trigger}.Sanitize()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced commit failure' USING ERRCODE='%s'; END $$`, quotedFn, sqlState)); err != nil {
		t.Fatalf("create commit failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE CONSTRAINT TRIGGER %s AFTER DELETE ON issue DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (OLD.id = '%s'::uuid) EXECUTE FUNCTION %s()`, quotedTrigger, util.UUIDToString(issueID), quotedFn)); err != nil {
		t.Fatalf("create deferred failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON issue`, quotedTrigger))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFn))
	})
}

type pgxBeginQueryExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func coordinationCode(err error) CoordinationErrorCode {
	var target *CoordinationError
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func sortUUIDs(ids []pgtype.UUID) {
	if bytes.Compare(ids[0].Bytes[:], ids[1].Bytes[:]) > 0 {
		ids[0], ids[1] = ids[1], ids[0]
	}
}
