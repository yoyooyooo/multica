package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type prMergeConcurrencyFixture struct {
	workspaceID pgtype.UUID
	userID      pgtype.UUID
	row         db.WorkloadPrMergeDelegation
}

type prMergeConcurrencyResult struct {
	row db.WorkloadPrMergeDelegation
	err error
}

type prMergeSingleConnectionTxStarter struct {
	conn *pgxpool.Conn
}

func (starter prMergeSingleConnectionTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	return starter.conn.Begin(ctx)
}

func TestPRMergeDelegationPostgresCommitOrdering(t *testing.T) {
	if testPool == nil {
		t.Skip("requires test database")
	}
	t.Run("revoke-first", testPRMergeDelegationRevokeFirst)
	t.Run("consume-first", testPRMergeDelegationConsumeFirst)
	t.Run("expiry-crosses-lock-wait", testPRMergeDelegationExpiryCrossesLockWait)
	t.Run("workspace-delete-first", testPRMergeDelegationWorkspaceDeleteFirst)
	t.Run("consume-first-before-workspace-delete", testPRMergeDelegationConsumeFirstBeforeWorkspaceDelete)
}

func testPRMergeDelegationRevokeFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixture := seedPRMergeConcurrencyFixture(t, "10 minutes")
	blocker, waiter, blockerTx, waiterPID, blockerPID := beginPRMergeConcurrencyPair(t, ctx, fixture.workspaceID)
	defer blocker.Release()
	defer waiter.Release()
	q1 := testHandler.Queries.WithTx(blockerTx)
	revoked, err := q1.RevokePRMergeDelegationInWorkspace(ctx, db.RevokePRMergeDelegationInWorkspaceParams{
		RevokedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, RevokedByUserID: fixture.userID,
		RevocationReason: pgtype.Text{String: "concurrency test", Valid: true}, ID: fixture.row.ID, WorkspaceID: fixture.workspaceID,
	})
	if err != nil {
		t.Fatalf("revoke in first transaction: %v", err)
	}
	if err := createPRMergeDelegationEvent(ctx, q1, revoked, "revoked", "human", uuidToString(fixture.userID), pgtype.UUID{}, map[string]any{}); err != nil {
		t.Fatalf("write revoke event: %v", err)
	}
	result := make(chan prMergeConcurrencyResult, 1)
	go runPRMergeWaiter(ctx, waiter, fixture.workspaceID, result, func(q *db.Queries) (db.WorkloadPrMergeDelegation, error) {
		return q.ConsumePRMergeDelegation(ctx, consumeParamsForConcurrency(fixture.row.ID))
	})
	waitForPRMergeBlock(t, ctx, blockerTx, waiterPID, blockerPID)
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("commit revoke-first: %v", err)
	}
	got := awaitPRMergeResult(t, ctx, result)
	if !errors.Is(got.err, pgx.ErrNoRows) {
		t.Fatalf("consume after committed revoke err=%v row=%#v", got.err, got.row)
	}
	assertPRMergeConcurrencyState(t, fixture.row.ID, "revoked", 1, 0, false)
}

func testPRMergeDelegationConsumeFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixture := seedPRMergeConcurrencyFixture(t, "10 minutes")
	blocker, waiter, blockerTx, waiterPID, blockerPID := beginPRMergeConcurrencyPair(t, ctx, fixture.workspaceID)
	defer blocker.Release()
	defer waiter.Release()
	q1 := testHandler.Queries.WithTx(blockerTx)
	consumed, err := q1.ConsumePRMergeDelegation(ctx, consumeParamsForConcurrency(fixture.row.ID))
	if err != nil {
		t.Fatalf("consume in first transaction: %v", err)
	}
	if err := createPRMergeDelegationEvent(ctx, q1, consumed, "consumed", "ags_service", "mini", consumed.ConsumerIntentID, map[string]any{}); err != nil {
		t.Fatalf("write consume event: %v", err)
	}
	result := make(chan prMergeConcurrencyResult, 1)
	go runPRMergeWaiter(ctx, waiter, fixture.workspaceID, result, func(q *db.Queries) (db.WorkloadPrMergeDelegation, error) {
		return q.RevokePRMergeDelegationInWorkspace(ctx, db.RevokePRMergeDelegationInWorkspaceParams{
			RevokedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, RevokedByUserID: fixture.userID,
			RevocationReason: pgtype.Text{String: "concurrency test", Valid: true}, ID: fixture.row.ID, WorkspaceID: fixture.workspaceID,
		})
	})
	waitForPRMergeBlock(t, ctx, blockerTx, waiterPID, blockerPID)
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("commit consume-first: %v", err)
	}
	got := awaitPRMergeResult(t, ctx, result)
	if !errors.Is(got.err, pgx.ErrNoRows) {
		t.Fatalf("revoke after committed consume err=%v row=%#v", got.err, got.row)
	}
	assertPRMergeConcurrencyState(t, fixture.row.ID, "consumed", 0, 1, true)
}

func testPRMergeDelegationExpiryCrossesLockWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixture := seedPRMergeConcurrencyFixture(t, "600 milliseconds")
	blocker, waiter, blockerTx, waiterPID, blockerPID := beginPRMergeConcurrencyPair(t, ctx, fixture.workspaceID)
	defer blocker.Release()
	defer waiter.Release()
	t.Setenv("MULTICA_DELEGATED_PR_MERGE_ENABLED", "1")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "expiry-race-service-token")
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_INSTANCE_ID", "mini")
	handler := *testHandler
	handler.TxStarter = prMergeSingleConnectionTxStarter{conn: waiter}
	// If application time ever regains authority, this deliberately stale value
	// would authorize the consume. The database linearization point must win.
	handler.workloadAssertionNow = func() time.Time { return fixture.row.NotAfter.Time.Add(-time.Hour) }
	body := prMergeDelegationServiceRequest{
		AuthorityRevision: uuidToString(fixture.row.AuthorityRevision), FactsDigest: fixture.row.FactsDigest,
		TargetInstance: fixture.row.TargetInstance, CanonicalRepositoryID: fixture.row.CanonicalRepositoryID,
		CanonicalRepository: fixture.row.CanonicalRepository, ProviderBindingID: fixture.row.ProviderBindingID,
		ProviderBindingRevision: fixture.row.ProviderBindingRevision, ProviderRepository: fixture.row.ProviderRepository,
		AGSPRNumber: fixture.row.AgsPrNumber, ProviderPRNumber: fixture.row.ProviderPrNumber,
		ExpectedHeadSHA: fixture.row.ExpectedHeadSha, ExpectedBaseSHA: fixture.row.ExpectedBaseSha,
		BaseRef: fixture.row.BaseRef, MergeMethod: fixture.row.MergeMethod, ProjectionFactsRevision: fixture.row.ProjectionFactsRevision,
		TaskID: uuidToString(fixture.row.TaskID), RunID: uuidToString(fixture.row.ExecutionID),
		SessionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", IntentID: uuid.NewString(), Phase: "pre_effect",
	}
	response := make(chan struct {
		code int
		body string
	}, 1)
	go func() {
		req := newRequest(http.MethodPost, "/", body)
		req.Header.Set("Authorization", "Bearer expiry-race-service-token")
		route := chi.NewRouteContext()
		route.URLParams.Add("delegationId", uuidToString(fixture.row.ID))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		recorder := httptest.NewRecorder()
		handler.ConsumePRMergeDelegation(recorder, req)
		response <- struct {
			code int
			body string
		}{recorder.Code, recorder.Body.String()}
	}()
	waitForPRMergeBlock(t, ctx, blockerTx, waiterPID, blockerPID)
	for {
		var expired bool
		if err := blockerTx.QueryRow(ctx, `SELECT clock_timestamp() >= $1`, fixture.row.NotAfter).Scan(&expired); err != nil {
			t.Fatalf("read DB expiry clock: %v", err)
		}
		if expired {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release lock after expiry: %v", err)
	}
	select {
	case got := <-response:
		if got.code != http.StatusConflict || !strings.Contains(got.body, "not active") {
			t.Fatalf("expiry-crossing consume status=%d body=%s", got.code, got.body)
		}
	case <-ctx.Done():
		t.Fatalf("expiry-crossing handler timed out (possible deadlock): %v", ctx.Err())
	}
	assertPRMergeConcurrencyState(t, fixture.row.ID, "expired", 0, 0, false)
	var expiredEvents int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM workload_pr_merge_delegation_event WHERE delegation_id=$1 AND event_type='expired'`, fixture.row.ID).Scan(&expiredEvents); err != nil || expiredEvents != 1 {
		t.Fatalf("expiry event count=%d err=%v", expiredEvents, err)
	}
}

func testPRMergeDelegationWorkspaceDeleteFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixture := seedPRMergeConcurrencyFixture(t, "10 minutes")
	blocker, waiter, blockerTx, waiterPID, blockerPID := beginPRMergeConcurrencyPair(t, ctx, fixture.workspaceID)
	defer blocker.Release()
	defer waiter.Release()
	q1 := testHandler.Queries.WithTx(blockerTx)
	deletePRMergeConcurrencyWorkspace(ctx, t, blockerTx, q1, fixture.workspaceID)
	result := make(chan prMergeConcurrencyResult, 1)
	go runPRMergeWaiter(ctx, waiter, fixture.workspaceID, result, func(q *db.Queries) (db.WorkloadPrMergeDelegation, error) {
		return q.ConsumePRMergeDelegation(ctx, consumeParamsForConcurrency(fixture.row.ID))
	})
	waitForPRMergeBlock(t, ctx, blockerTx, waiterPID, blockerPID)
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("commit workspace-delete-first: %v", err)
	}
	got := awaitPRMergeResult(t, ctx, result)
	if !errors.Is(got.err, pgx.ErrNoRows) {
		t.Fatalf("consume after workspace delete err=%v row=%#v", got.err, got.row)
	}
	assertPRMergeConcurrencyNoOrphans(t, fixture)
}

func testPRMergeDelegationConsumeFirstBeforeWorkspaceDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixture := seedPRMergeConcurrencyFixture(t, "10 minutes")
	blocker, waiter, blockerTx, waiterPID, blockerPID := beginPRMergeConcurrencyPair(t, ctx, fixture.workspaceID)
	defer blocker.Release()
	defer waiter.Release()
	q1 := testHandler.Queries.WithTx(blockerTx)
	consumed, err := q1.ConsumePRMergeDelegation(ctx, consumeParamsForConcurrency(fixture.row.ID))
	if err != nil || !consumed.ConsumptionReceiptID.Valid {
		t.Fatalf("consume before workspace delete row=%#v err=%v", consumed, err)
	}
	if err := createPRMergeDelegationEvent(ctx, q1, consumed, "consumed", "ags_service", "mini", consumed.ConsumerIntentID, map[string]any{}); err != nil {
		t.Fatalf("write consume event: %v", err)
	}
	result := make(chan prMergeConcurrencyResult, 1)
	go func() {
		deleteTx, beginErr := waiter.Begin(ctx)
		if beginErr != nil {
			result <- prMergeConcurrencyResult{err: beginErr}
			return
		}
		defer deleteTx.Rollback(ctx)
		if err := setPRMergeConcurrencyTimeouts(ctx, deleteTx); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		if err := lockProviderWorkspaces(ctx, deleteTx, []pgtype.UUID{fixture.workspaceID}); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		q := testHandler.Queries.WithTx(deleteTx)
		if err := q.DeleteWorkspacePRMergeDelegationEvents(ctx, fixture.workspaceID); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		if err := q.DeleteWorkspacePRMergeDelegations(ctx, fixture.workspaceID); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		if _, err := deleteTx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, fixture.workspaceID); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		if err := q.DeleteWorkspaceWorkloadAuthority(ctx, fixture.workspaceID); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		if err := deleteTx.Commit(ctx); err != nil {
			result <- prMergeConcurrencyResult{err: err}
			return
		}
		result <- prMergeConcurrencyResult{}
	}()
	waitForPRMergeBlock(t, ctx, blockerTx, waiterPID, blockerPID)
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("commit consume before workspace delete: %v", err)
	}
	if got := awaitPRMergeResult(t, ctx, result); got.err != nil {
		t.Fatalf("workspace delete after consume: %v", got.err)
	}
	assertPRMergeConcurrencyNoOrphans(t, fixture)
}

func seedPRMergeConcurrencyFixture(t *testing.T, notAfterInterval string) prMergeConcurrencyFixture {
	t.Helper()
	ctx := context.Background()
	userID, workspaceID, runtimeID, agentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	issueID, taskID, executionID, linkID, delegationID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := testPool.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'PR merge concurrency',$2)`, userID, "pr-merge-concurrency-"+userID.String()+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO workspace (id,name,slug,issue_prefix) VALUES ($1,'PR merge concurrency',$2,'PMC')`, workspaceID, "pr-merge-concurrency-"+workspaceID.String()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_runtime (id,workspace_id,name,runtime_mode,provider,status,device_info,metadata,owner_id,last_seen_at) VALUES ($1,$2,'PR merge runtime','cloud','test','online','test','{}',$3,clock_timestamp())`, runtimeID, workspaceID, userID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent (id,workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,permission_mode,max_concurrent_tasks,owner_id) VALUES ($1,$2,'PR merge agent','','cloud','{}',$3,'workspace','public_to',1,$4)`, agentID, workspaceID, runtimeID, userID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO issue (id,workspace_id,title,status,creator_type,creator_id,number) VALUES ($1,$2,'PR merge race','in_review','member',$3,1)`, issueID, workspaceID, userID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_task_queue (id,agent_id,runtime_id,issue_id,status,priority,started_at,execution_id) VALUES ($1,$2,$3,$4,'running',0,clock_timestamp(),$5)`, taskID, agentID, runtimeID, issueID, executionID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO external_pull_request_link (id,workspace_id,issue_id,provider,external_repo,external_number,merge_provider,merge_repo,merge_number,link_confidence,completion_intent,state,target_instance,canonical_repository_id,canonical_repository,provider_binding_id,provider_binding_revision,provider_repository,expected_head_sha,expected_base_sha,base_ref,delegated_merge_method,projection_facts_revision) VALUES ($1,$2,$3,'ags','ux/smip',41,'forgejo','ux/smip',52,'authoritative',false,'open','mini',$4,'ux/smip',$5,$6,'ux/smip',$7,$8,'main','rebase',$9)`,
		linkID, workspaceID, issueID,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222",
		"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); err != nil {
		t.Fatalf("seed projection: %v", err)
	}
	row := db.WorkloadPrMergeDelegation{
		ID: pgtype.UUID{Bytes: delegationID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: workspaceID, Valid: true},
		IssueID: pgtype.UUID{Bytes: issueID, Valid: true}, ExternalPrLinkID: pgtype.UUID{Bytes: linkID, Valid: true},
		TaskID: pgtype.UUID{Bytes: taskID, Valid: true}, ExecutionID: pgtype.UUID{Bytes: executionID, Valid: true},
		RuntimeID: pgtype.UUID{Bytes: runtimeID, Valid: true}, Operation: "pr.merge", TargetInstance: "mini",
		CanonicalRepositoryID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CanonicalRepository: "ux/smip",
		Provider: "forgejo", ProviderBindingID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ProviderBindingRevision: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ProviderRepository: "ux/smip",
		AgsPrNumber: 41, ProviderPrNumber: 52, ExpectedHeadSha: "1111111111111111111111111111111111111111",
		ExpectedBaseSha: "2222222222222222222222222222222222222222", BaseRef: "main", MergeMethod: "rebase",
		ProjectionFactsRevision: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	row.FactsDigest = digestPRMergeDelegationFacts(prMergeDelegationBindingFactsFromRow(row))
	if _, err := testPool.Exec(ctx, `INSERT INTO workload_pr_merge_delegation (id,workspace_id,issue_id,external_pr_link_id,task_id,execution_id,runtime_id,target_instance,canonical_repository_id,canonical_repository,provider_binding_id,provider_binding_revision,provider_repository,ags_pr_number,provider_pr_number,expected_head_sha,expected_base_sha,base_ref,merge_method,projection_facts_revision,facts_digest,state,approved_at,approved_by_user_id,not_after) VALUES ($1,$2,$3,$4,$5,$6,$7,'mini',$8,'ux/smip',$9,$10,'ux/smip',41,52,$11,$12,'main','rebase',$13,$14,'approved',clock_timestamp(),$15,clock_timestamp()+$16::interval)`,
		delegationID, workspaceID, issueID, linkID, taskID, executionID, runtimeID,
		row.CanonicalRepositoryID, row.ProviderBindingID, row.ProviderBindingRevision, row.ExpectedHeadSha, row.ExpectedBaseSha,
		row.ProjectionFactsRevision, row.FactsDigest, userID, notAfterInterval); err != nil {
		t.Fatalf("seed approved delegation: %v", err)
	}
	stored, err := testHandler.Queries.GetPRMergeDelegationByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("read seeded delegation: %v", err)
	}
	fixture := prMergeConcurrencyFixture{workspaceID: row.WorkspaceID, userID: pgtype.UUID{Bytes: userID, Valid: true}, row: stored}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation_event WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workload_pr_merge_delegation WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace_workload_authority WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, userID)
	})
	return fixture
}

func beginPRMergeConcurrencyPair(t *testing.T, ctx context.Context, workspaceID pgtype.UUID) (*pgxpool.Conn, *pgxpool.Conn, pgx.Tx, int32, int32) {
	t.Helper()
	blocker, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker connection: %v", err)
	}
	waiter, err := testPool.Acquire(ctx)
	if err != nil {
		blocker.Release()
		t.Fatalf("acquire waiter connection: %v", err)
	}
	var blockerPID, waiterPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if err := waiter.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&waiterPID); err != nil {
		t.Fatal(err)
	}
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker transaction: %v", err)
	}
	if err := setPRMergeConcurrencyTimeouts(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{workspaceID}); err != nil {
		t.Fatalf("lock blocker provider workspace: %v", err)
	}
	return blocker, waiter, tx, waiterPID, blockerPID
}

func runPRMergeWaiter(ctx context.Context, conn *pgxpool.Conn, workspaceID pgtype.UUID, result chan<- prMergeConcurrencyResult, operation func(*db.Queries) (db.WorkloadPrMergeDelegation, error)) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		result <- prMergeConcurrencyResult{err: err}
		return
	}
	defer tx.Rollback(ctx)
	if err := setPRMergeConcurrencyTimeouts(ctx, tx); err != nil {
		result <- prMergeConcurrencyResult{err: err}
		return
	}
	if err := lockProviderWorkspaces(ctx, tx, []pgtype.UUID{workspaceID}); err != nil {
		result <- prMergeConcurrencyResult{err: err}
		return
	}
	row, operationErr := operation(testHandler.Queries.WithTx(tx))
	if operationErr != nil && !errors.Is(operationErr, pgx.ErrNoRows) {
		result <- prMergeConcurrencyResult{row: row, err: operationErr}
		return
	}
	if err := tx.Commit(ctx); err != nil {
		result <- prMergeConcurrencyResult{row: row, err: err}
		return
	}
	result <- prMergeConcurrencyResult{row: row, err: operationErr}
}

func setPRMergeConcurrencyTimeouts(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SET LOCAL lock_timeout='4s'; SET LOCAL statement_timeout='5s'`)
	return err
}

func waitForPRMergeBlock(t *testing.T, ctx context.Context, blockerTx pgx.Tx, waiterPID, blockerPID int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := blockerTx.QueryRow(ctx, `SELECT $1::int = ANY(pg_blocking_pids($2::int))`, blockerPID, waiterPID).Scan(&blocked); err != nil {
			t.Fatalf("inspect PostgreSQL blocking graph: %v", err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiter backend %d did not block on backend %d", waiterPID, blockerPID)
}

func awaitPRMergeResult(t *testing.T, ctx context.Context, result <-chan prMergeConcurrencyResult) prMergeConcurrencyResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-ctx.Done():
		t.Fatalf("PostgreSQL concurrency case timed out (possible deadlock): %v", ctx.Err())
		return prMergeConcurrencyResult{}
	}
}

func consumeParamsForConcurrency(id pgtype.UUID) db.ConsumePRMergeDelegationParams {
	return db.ConsumePRMergeDelegationParams{
		ConsumerInstanceID:   pgtype.Text{String: "mini", Valid: true},
		ConsumerIntentID:     pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ConsumeRequestDigest: pgtype.Text{String: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Valid: true},
		ConsumptionReceiptID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, ID: id,
	}
}

func assertPRMergeConcurrencyState(t *testing.T, id pgtype.UUID, state string, revokedEvents, consumedEvents int, receipt bool) {
	t.Helper()
	var gotState string
	var gotReceipt pgtype.UUID
	var gotRevoked, gotConsumed int
	if err := testPool.QueryRow(context.Background(), `SELECT state, consumption_receipt_id FROM workload_pr_merge_delegation WHERE id=$1`, id).Scan(&gotState, &gotReceipt); err != nil {
		t.Fatalf("read final delegation: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE event_type='revoked'), count(*) FILTER (WHERE event_type='consumed') FROM workload_pr_merge_delegation_event WHERE delegation_id=$1`, id).Scan(&gotRevoked, &gotConsumed); err != nil {
		t.Fatalf("read final events: %v", err)
	}
	if gotState != state || gotRevoked != revokedEvents || gotConsumed != consumedEvents || gotReceipt.Valid != receipt {
		t.Fatalf("terminal state=%s revoked_events=%d consumed_events=%d receipt_valid=%v", gotState, gotRevoked, gotConsumed, gotReceipt.Valid)
	}
}

func deletePRMergeConcurrencyWorkspace(ctx context.Context, t *testing.T, tx pgx.Tx, q *db.Queries, workspaceID pgtype.UUID) {
	t.Helper()
	if err := q.DeleteWorkspacePRMergeDelegationEvents(ctx, workspaceID); err != nil {
		t.Fatalf("delete delegation events: %v", err)
	}
	if err := q.DeleteWorkspacePRMergeDelegations(ctx, workspaceID); err != nil {
		t.Fatalf("delete delegations: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, workspaceID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if err := q.DeleteWorkspaceWorkloadAuthority(ctx, workspaceID); err != nil {
		t.Fatalf("delete workspace authority: %v", err)
	}
}

func assertPRMergeConcurrencyNoOrphans(t *testing.T, fixture prMergeConcurrencyFixture) {
	t.Helper()
	for table, column := range map[string]string{
		"workspace": "id", "workspace_workload_authority": "workspace_id",
		"workload_pr_merge_delegation": "workspace_id", "workload_pr_merge_delegation_event": "workspace_id",
	} {
		var count int
		query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s=$1`, table, column)
		if err := testPool.QueryRow(context.Background(), query, fixture.workspaceID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s orphan count=%d err=%v", table, count, err)
		}
	}
}
