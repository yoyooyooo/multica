package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func allIssueDeletionOperationPhases() []IssueDeletionPhase {
	return []IssueDeletionPhase{
		IssueDeletionPhaseTaskCancel,
		IssueDeletionPhaseTaskTokenCleanup,
		IssueDeletionPhaseAutopilotFail,
		IssueDeletionPhaseAttachmentCensus,
		IssueDeletionPhaseEntityDelete,
	}
}

func TestWorkCoordinationIssueDeletionOperationFaultsUseProductionPhasesAndRollbackBatch(t *testing.T) {
	for _, targetPhase := range allIssueDeletionOperationPhases() {
		t.Run(string(targetPhase), func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			var secondID pgtype.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS phase second','member',$2,'none',2) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&secondID); err != nil {
				t.Fatalf("insert second issue: %v", err)
			}
			issueIDs := []pgtype.UUID{fixture.issueID, secondID}
			sortUUIDs(issueIDs)
			operationFixture := createIssueDeletionOperationFixture(t, pool, fixture, issueIDs)

			svc := NewCoordinationService(db.New(pool), pool)
			var reached []IssueDeletionPhase
			phaseFault := func(_ context.Context, phase IssueDeletionPhase, issue db.Issue) error {
				if issue.ID.Bytes != issueIDs[1].Bytes {
					return nil
				}
				reached = append(reached, phase)
				if phase == targetPhase {
					return errors.New("injected operation-boundary failure")
				}
				return nil
			}
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, issueIDs, IssueDeletionBatch)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			handle.phaseFault = phaseFault
			first, err := handle.Delete(ctx, issueIDs[0])
			if err != nil || first.Outcome != IssueDeletionDeleted || !first.Effects.IssueID.Valid {
				t.Fatalf("first production deletion result=%+v err=%v", first, err)
			}
			result, err := handle.Delete(ctx, issueIDs[1])
			var fatal *IssueDeletionFatalError
			if !errors.As(err, &fatal) || fatal.Phase != targetPhase || fatal.Class != IssueDeletionFailureUnknown {
				t.Fatalf("fatal=%+v err=%v", fatal, err)
			}
			if result.Outcome != "" || result.Effects.IssueID.Valid || len(result.Effects.CancelledTasks) != 0 || len(result.Effects.AttachmentURLs) != 0 {
				t.Fatalf("fatal operation exposed executable effects: %+v", result)
			}
			assertReachedIssueDeletionPhases(t, reached, targetPhase)
			if err := handle.Finish(false); err != nil {
				t.Fatalf("finish whole-batch rollback: %v", err)
			}
			assertIssueDeletionOperationFixtureUnchanged(t, pool, issueIDs, operationFixture)
		})
	}
}

func TestWorkCoordinationBatchRecoverabilityIsProductionEntityDelete23503Only(t *testing.T) {
	for _, targetPhase := range allIssueDeletionOperationPhases() {
		t.Run(string(targetPhase), func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			operationFixture := createIssueDeletionOperationFixture(t, pool, fixture, []pgtype.UUID{fixture.issueID})
			svc := NewCoordinationService(db.New(pool), pool)
			var reached []IssueDeletionPhase
			phaseFault := func(_ context.Context, phase IssueDeletionPhase, issue db.Issue) error {
				if issue.ID.Bytes != fixture.issueID.Bytes {
					return nil
				}
				reached = append(reached, phase)
				if phase == targetPhase {
					return &pgconn.PgError{Code: "23503"}
				}
				return nil
			}
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionBatch)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			handle.phaseFault = phaseFault
			result, err := handle.Delete(ctx, fixture.issueID)
			if targetPhase == IssueDeletionPhaseEntityDelete {
				if err != nil || result.Outcome != IssueDeletionSkippedRecoverable || result.Phase != IssueDeletionPhaseEntityDelete || result.SafeCode != "target_restricted" || result.Effects.IssueID.Valid {
					t.Fatalf("recoverable result=%+v err=%v", result, err)
				}
				if err := handle.Finish(true); err != nil {
					t.Fatalf("commit recovered batch: %v", err)
				}
			} else {
				var fatal *IssueDeletionFatalError
				if !errors.As(err, &fatal) || fatal.Phase != targetPhase || fatal.Class != IssueDeletionFailureStatement || result.Outcome != "" {
					t.Fatalf("non-entity 23503 fatal=%+v result=%+v err=%v", fatal, result, err)
				}
				if err := handle.Finish(false); err != nil {
					t.Fatalf("rollback fatal batch: %v", err)
				}
			}
			assertReachedIssueDeletionPhases(t, reached, targetPhase)
			assertIssueDeletionOperationFixtureUnchanged(t, pool, []pgtype.UUID{fixture.issueID}, operationFixture)
		})
	}
}

func TestWorkCoordinationIssueDeletionFailureClassesThroughProductionBoundary(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want IssueDeletionFailureClass
	}{
		{name: "unknown SQLSTATE", err: &pgconn.PgError{Code: "ZZ999"}, want: IssueDeletionFailureStatement},
		{name: "row count", err: errDeletionRowCount, want: IssueDeletionFailureRowCount},
		{name: "serialization", err: &pgconn.PgError{Code: "40001"}, want: IssueDeletionFailureSerialization},
		{name: "deadlock", err: &pgconn.PgError{Code: "40P01"}, want: IssueDeletionFailureDeadlock},
		{name: "context cancelled", err: context.Canceled, want: IssueDeletionFailureContext},
		{name: "context deadline", err: context.DeadlineExceeded, want: IssueDeletionFailureContext},
		{name: "connection protocol", err: retryableDeletionError{}, want: IssueDeletionFailureConnection},
		{name: "unknown transaction state", err: errors.New("transaction status unavailable"), want: IssueDeletionFailureUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			operationFixture := createIssueDeletionOperationFixture(t, pool, fixture, []pgtype.UUID{fixture.issueID})
			svc := NewCoordinationService(db.New(pool), pool)
			phaseFault := func(_ context.Context, phase IssueDeletionPhase, issue db.Issue) error {
				if issue.ID.Bytes == fixture.issueID.Bytes && phase == IssueDeletionPhaseEntityDelete {
					return tc.err
				}
				return nil
			}
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionBatch)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			handle.phaseFault = phaseFault
			result, err := handle.Delete(ctx, fixture.issueID)
			var fatal *IssueDeletionFatalError
			if !errors.As(err, &fatal) || fatal.Phase != IssueDeletionPhaseEntityDelete || fatal.Class != tc.want {
				t.Fatalf("fatal=%+v err=%v", fatal, err)
			}
			if result.Outcome != "" || result.Effects.IssueID.Valid {
				t.Fatalf("classified failure exposed effects: %+v", result)
			}
			if err := handle.Finish(false); err != nil {
				t.Fatalf("rollback classified failure: %v", err)
			}
			assertIssueDeletionOperationFixtureUnchanged(t, pool, []pgtype.UUID{fixture.issueID}, operationFixture)
		})
	}
}

func TestWorkCoordinationAcquireReadyBoundaryPanicCleansRealTransactionAndLock(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	beforeAcquired := pool.Stat().AcquiredConns()
	svc := NewCoordinationService(db.New(pool), pool)
	boundaryCalls := 0
	svc.deletionReadyBoundary = func() {
		boundaryCalls++
		panic("injected acquire ready-boundary panic")
	}
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, []pgtype.UUID{fixture.issueID}, IssueDeletionSingle)
	}()
	if recovered == nil || boundaryCalls != 1 {
		t.Fatalf("ready boundary recovered=%v calls=%d", recovered, boundaryCalls)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != beforeAcquired {
		t.Fatalf("Acquire panic leaked pooled connection: before=%d after=%d", beforeAcquired, acquired)
	}

	key, err := CoordinationWorkspaceAdvisoryKey(fixture.workspaceID)
	if err != nil {
		t.Fatalf("lock key: %v", err)
	}
	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire independent probe: %v", err)
	}
	defer probe.Release()
	var available bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, key).Scan(&available); err != nil || !available {
		t.Fatalf("ready panic left advisory lock held: available=%v err=%v", available, err)
	}
	defer func() {
		_, _ = probe.Exec(context.Background(), `SELECT pg_advisory_unlock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, key)
	}()
	var issueExists bool
	if err := probe.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1)`, fixture.issueID).Scan(&issueExists); err != nil || !issueExists {
		t.Fatalf("ready panic changed issue state: exists=%v err=%v", issueExists, err)
	}
	if tag, err := probe.Exec(ctx, `UPDATE issue SET title=title WHERE id=$1`, fixture.issueID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("ready panic left issue row lock: rows=%d err=%v", tag.RowsAffected(), err)
	}
}

type issueDeletionOperationFixture struct {
	taskIDs  []pgtype.UUID
	tokenIDs []pgtype.UUID
	runIDs   []pgtype.UUID
}

func createIssueDeletionOperationFixture(t *testing.T, pool *pgxpool.Pool, fixture workCoordinationFixture, issueIDs []pgtype.UUID) issueDeletionOperationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	var runtimeID, agentID, autopilotID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata,last_seen_at,visibility,owner_id) VALUES ($1,$2,$3,'cloud','wcs-phase','online','test','{}'::jsonb,now(),'private',$4) RETURNING id`, fixture.workspaceID, fmt.Sprintf("wcs-phase-daemon-%d", suffix), fmt.Sprintf("WCS Phase Runtime %d", suffix), fixture.userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert phase runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id) VALUES ($1,$2,'','cloud','{}'::jsonb,$3,'private',$4,$5) RETURNING id`, fixture.workspaceID, fmt.Sprintf("WCS Phase Agent %d", suffix), runtimeID, int32(len(issueIDs)), fixture.userID).Scan(&agentID); err != nil {
		t.Fatalf("insert phase agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO autopilot (workspace_id,title,assignee_id,execution_mode,created_by_type,created_by_id) VALUES ($1,$2,$3,'run_only','member',$4) RETURNING id`, fixture.workspaceID, fmt.Sprintf("WCS Phase Autopilot %d", suffix), agentID, fixture.userID).Scan(&autopilotID); err != nil {
		t.Fatalf("insert phase autopilot: %v", err)
	}
	out := issueDeletionOperationFixture{}
	for i, issueID := range issueIDs {
		var taskID, tokenID, runID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority,context) VALUES ($1,$2,$3,'queued',0,'{}'::jsonb) RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("insert phase task: %v", err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2,$3,$4,$5,now()+interval '1 hour') RETURNING id`, fmt.Sprintf("wcs-phase-token-%d-%d", suffix, i), taskID, agentID, fixture.workspaceID, fixture.userID).Scan(&tokenID); err != nil {
			t.Fatalf("insert phase token: %v", err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO autopilot_run (autopilot_id,source,status,issue_id) VALUES ($1,'manual','running',$2) RETURNING id`, autopilotID, issueID).Scan(&runID); err != nil {
			t.Fatalf("insert phase autopilot run: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO attachment (id,workspace_id,issue_id,uploader_type,uploader_id,filename,url,content_type,size_bytes) VALUES (gen_random_uuid(),$1,$2,'member',$3,$4,$5,'text/plain',5)`, fixture.workspaceID, issueID, fixture.userID, fmt.Sprintf("phase-%d.txt", i), fmt.Sprintf("https://storage.test/wcs/phase-%d-%d.txt", suffix, i)); err != nil {
			t.Fatalf("insert phase attachment: %v", err)
		}
		out.taskIDs = append(out.taskIDs, taskID)
		out.tokenIDs = append(out.tokenIDs, tokenID)
		out.runIDs = append(out.runIDs, runID)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE id=ANY($1::uuid[])`, out.runIDs)
		_, _ = pool.Exec(context.Background(), `DELETE FROM autopilot WHERE id=$1`, autopilotID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_token WHERE id=ANY($1::uuid[])`, out.tokenIDs)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=ANY($1::uuid[])`, out.taskIDs)
		_, _ = pool.Exec(context.Background(), `DELETE FROM attachment WHERE issue_id=ANY($1::uuid[])`, issueIDs)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, agentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, runtimeID)
	})
	return out
}

func assertReachedIssueDeletionPhases(t *testing.T, got []IssueDeletionPhase, target IssueDeletionPhase) {
	t.Helper()
	phases := allIssueDeletionOperationPhases()
	want := make([]IssueDeletionPhase, 0, len(phases))
	for _, phase := range phases {
		want = append(want, phase)
		if phase == target {
			break
		}
	}
	if len(got) != len(want) {
		t.Fatalf("production phases reached=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("production phases reached=%v want=%v", got, want)
		}
	}
}

func assertIssueDeletionOperationFixtureUnchanged(t *testing.T, pool *pgxpool.Pool, issueIDs []pgtype.UUID, fixture issueDeletionOperationFixture) {
	t.Helper()
	ctx := context.Background()
	var issueCount, queuedTasks, tokenCount, runningRuns, attachmentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=ANY($1::uuid[])`, issueIDs).Scan(&issueCount); err != nil {
		t.Fatalf("count issues after rollback: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE id=ANY($1::uuid[]) AND status='queued'`, fixture.taskIDs).Scan(&queuedTasks); err != nil {
		t.Fatalf("count queued tasks after rollback: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE id=ANY($1::uuid[])`, fixture.tokenIDs).Scan(&tokenCount); err != nil {
		t.Fatalf("count task tokens after rollback: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM autopilot_run WHERE id=ANY($1::uuid[]) AND status='running'`, fixture.runIDs).Scan(&runningRuns); err != nil {
		t.Fatalf("count autopilot runs after rollback: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachment WHERE issue_id=ANY($1::uuid[])`, issueIDs).Scan(&attachmentCount); err != nil {
		t.Fatalf("count attachments after rollback: %v", err)
	}
	if issueCount != len(issueIDs) || queuedTasks != len(fixture.taskIDs) || tokenCount != len(fixture.tokenIDs) || runningRuns != len(fixture.runIDs) || attachmentCount != len(issueIDs) {
		t.Fatalf("rollback state issues=%d/%d queued_tasks=%d/%d tokens=%d/%d running_runs=%d/%d attachments=%d/%d", issueCount, len(issueIDs), queuedTasks, len(fixture.taskIDs), tokenCount, len(fixture.tokenIDs), runningRuns, len(fixture.runIDs), attachmentCount, len(issueIDs))
	}
}

func TestWorkCoordinationDeletionLockAndFinishTerminalMatrix(t *testing.T) {
	t.Run("lock acquire error discards", func(t *testing.T) {
		terminal := &deletionTerminalCounts{}
		lifecycle := coordinationDeleteLifecycle{
			sessionLock: func(context.Context, int32) error { return retryableDeletionError{} },
			discardConn: func() { terminal.discards++ },
			state:       deleteStateAcquiring,
		}
		if err := lifecycle.acquireSessionLock(context.Background(), pgtype.UUID{Bytes: [16]byte{15: 1}, Valid: true}); coordinationCode(err) != CoordinationInternal {
			t.Fatalf("lock error=%v", err)
		}
		if lifecycle.state != deleteStateDiscarded || terminal.discards != 1 {
			t.Fatalf("state=%s terminal=%+v", lifecycle.state, terminal)
		}
	})

	cases := []struct {
		name              string
		commit            bool
		commitErr         error
		rollbackErr       error
		unlockResult      bool
		unlockErr         error
		wantState         deleteHandleState
		wantCommits       int
		wantRollbacks     int
		wantReleases      int
		wantDiscards      int
		unknownNoRollback bool
	}{
		{name: "commit success", commit: true, unlockResult: true, wantState: deleteStateReleased, wantCommits: 1, wantReleases: 1},
		{name: "definite commit failure", commit: true, commitErr: &pgconn.PgError{Code: "40001"}, unlockResult: true, wantState: deleteStateReleased, wantCommits: 1, wantRollbacks: 1, wantReleases: 1},
		{name: "server side rollback", commit: true, commitErr: pgx.ErrTxCommitRollback, rollbackErr: pgx.ErrTxClosed, unlockResult: true, wantState: deleteStateReleased, wantCommits: 1, wantRollbacks: 1, wantReleases: 1},
		{name: "commit response unknown", commit: true, commitErr: context.DeadlineExceeded, unlockResult: true, wantState: deleteStateDiscarded, wantCommits: 1, wantDiscards: 1, unknownNoRollback: true},
		{name: "commit success unlock false", commit: true, unlockResult: false, wantState: deleteStateDiscarded, wantCommits: 1, wantDiscards: 1},
		{name: "rollback success", commit: false, unlockResult: true, wantState: deleteStateReleased, wantRollbacks: 1, wantReleases: 1},
		{name: "rollback unlock error", commit: false, unlockErr: errors.New("unlock failed"), wantState: deleteStateDiscarded, wantRollbacks: 1, wantDiscards: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &deletionCountingTx{commitErr: tc.commitErr, rollbackErr: tc.rollbackErr}
			terminal := &deletionTerminalCounts{}
			lifecycle := coordinationDeleteLifecycle{
				tx:       tx,
				state:    deleteStateReady,
				lockHeld: true,
				sessionUnlock: func(context.Context, int32) (bool, error) {
					terminal.unlocks++
					return tc.unlockResult, tc.unlockErr
				},
				releaseConn: func() { terminal.releases++ },
				discardConn: func() { terminal.discards++ },
			}
			err := lifecycle.finish(tc.commit)
			if tc.name == "commit success" || tc.name == "rollback success" {
				if err != nil {
					t.Fatalf("finish: %v", err)
				}
			} else if coordinationCode(err) != CoordinationInternal {
				t.Fatalf("typed finish error=%v", err)
			}
			if lifecycle.state != tc.wantState || tx.commits != tc.wantCommits || tx.rollbacks != tc.wantRollbacks || terminal.releases != tc.wantReleases || terminal.discards != tc.wantDiscards {
				t.Fatalf("state=%s tx=%+v terminal=%+v", lifecycle.state, tx, terminal)
			}
			if tc.unknownNoRollback && (tx.rollbacks != 0 || terminal.unlocks != 0 || terminal.releases != 0) {
				t.Fatalf("unknown outcome claimed rollback/unlock/release: tx=%+v terminal=%+v", tx, terminal)
			}
		})
	}
}

type retryableDeletionError struct{}

func (retryableDeletionError) Error() string     { return "connection protocol failure" }
func (retryableDeletionError) SafeToRetry() bool { return true }

type deletionTerminalCounts struct {
	unlocks  int
	releases int
	discards int
}

type deletionCountingTx struct {
	commitErr   error
	rollbackErr error
	commits     int
	rollbacks   int
}

func (tx *deletionCountingTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unused")
}
func (tx *deletionCountingTx) Commit(context.Context) error {
	tx.commits++
	return tx.commitErr
}
func (tx *deletionCountingTx) Rollback(context.Context) error {
	tx.rollbacks++
	return tx.rollbackErr
}
func (tx *deletionCountingTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unused")
}
func (tx *deletionCountingTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (tx *deletionCountingTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (tx *deletionCountingTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("unused")
}
func (tx *deletionCountingTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (tx *deletionCountingTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (tx *deletionCountingTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (tx *deletionCountingTx) Conn() *pgx.Conn                                  { return nil }
