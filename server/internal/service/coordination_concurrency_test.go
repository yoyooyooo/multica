package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationAdvisoryLockModesShareOneKeySpace(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	workspaceA := pgtype.UUID{Bytes: [16]byte{15: 3}, Valid: true}
	workspaceB := pgtype.UUID{Bytes: [16]byte{15: 4}, Valid: true}
	keyA, err := CoordinationWorkspaceAdvisoryKey(workspaceA)
	if err != nil {
		t.Fatalf("workspace A key: %v", err)
	}
	keyB, err := CoordinationWorkspaceAdvisoryKey(workspaceB)
	if err != nil {
		t.Fatalf("workspace B key: %v", err)
	}
	if keyA == keyB {
		t.Fatal("lock fixture unexpectedly collided")
	}
	connA, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn A: %v", err)
	}
	defer connA.Release()
	connB, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn B: %v", err)
	}
	defer connB.Release()
	if err := db.New(connA).CoordinationAdvisorySessionLock(ctx, db.CoordinationAdvisorySessionLockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: keyA}); err != nil {
		t.Fatalf("session lock A: %v", err)
	}
	txB, err := connB.Begin(ctx)
	if err != nil {
		t.Fatalf("begin conn B: %v", err)
	}
	var gotSame, gotDifferent bool
	if err := txB.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, keyA).Scan(&gotSame); err != nil {
		t.Fatalf("try same xact lock: %v", err)
	}
	if err := txB.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, keyB).Scan(&gotDifferent); err != nil {
		t.Fatalf("try different xact lock: %v", err)
	}
	if gotSame || !gotDifferent {
		t.Fatalf("session/xact mutual exclusion same=%v different=%v", gotSame, gotDifferent)
	}
	if err := txB.Rollback(ctx); err != nil {
		t.Fatalf("rollback conn B: %v", err)
	}
	unlocked, err := db.New(connA).CoordinationAdvisorySessionUnlock(ctx, db.CoordinationAdvisorySessionUnlockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: keyA})
	if err != nil || !unlocked {
		t.Fatalf("unlock session A=%v err=%v", unlocked, err)
	}

	txA, err := connA.Begin(ctx)
	if err != nil {
		t.Fatalf("begin conn A: %v", err)
	}
	if err := db.New(txA).CoordinationAdvisoryXactLock(ctx, db.CoordinationAdvisoryXactLockParams{Namespace: CoordinationAdvisoryNamespace, WorkspaceKey: keyA}); err != nil {
		t.Fatalf("xact lock A: %v", err)
	}
	if err := connB.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, keyA).Scan(&gotSame); err != nil {
		t.Fatalf("try same session lock: %v", err)
	}
	if gotSame {
		_, _ = connB.Exec(ctx, `SELECT pg_advisory_unlock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, keyA)
		t.Fatal("session lock bypassed held xact lock")
	}
	if err := txA.Rollback(ctx); err != nil {
		t.Fatalf("rollback conn A: %v", err)
	}
	if err := connB.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, keyA).Scan(&gotSame); err != nil || !gotSame {
		t.Fatalf("session lock unavailable after rollback: got=%v err=%v", gotSame, err)
	}
	_, _ = connB.Exec(ctx, `SELECT pg_advisory_unlock($1::int4,$2::int4)`, CoordinationAdvisoryNamespace, keyA)
}

func TestWorkCoordinationNaturalConflictReloadUsesRecoveredSavepoint(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	input := EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "natural-conflict-seed"}
	created, err := svc.EnsureScope(ctx, actor, input)
	if err != nil {
		t.Fatalf("seed scope: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin outer transaction: %v", err)
	}
	defer tx.Rollback(context.Background())
	qtx := db.New(tx)
	if err := lockWorkspace(ctx, qtx, fixture.workspaceID); err != nil {
		t.Fatalf("lock workspace: %v", err)
	}
	params := db.GetActiveCoordinationScopeByRootParams{WorkspaceID: fixture.workspaceID, RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop"}
	row, outcome, err := svc.createScopeRowAfterMiss(ctx, tx, qtx, actor, input, params)
	if err != nil || outcome != CoordinationOutcomeNoop || !uuidEqual(row.ID, created.Scope.ID) {
		t.Fatalf("natural conflict reload row=%+v outcome=%q err=%v", row, outcome, err)
	}
	var transactionUsable int
	if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&transactionUsable); err != nil || transactionUsable != 1 {
		t.Fatalf("outer transaction remained aborted: value=%d err=%v", transactionUsable, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback outer transaction: %v", err)
	}
}

func TestWorkCoordinationConcurrentEnsureAndOrdinalRollback(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}

	const writers = 16
	results := make(chan EnsureScopeResult, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: fmt.Sprintf("concurrent-%02d", i)})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent ensure: %v", err)
	}
	var scopeID pgtype.UUID
	ordinals := make([]int, 0, writers)
	created := 0
	for result := range results {
		if !scopeID.Valid {
			scopeID = result.Scope.ID
		} else if !uuidEqual(scopeID, result.Scope.ID) {
			t.Fatalf("multiple scopes: %v and %v", scopeID, result.Scope.ID)
		}
		if result.Outcome == CoordinationOutcomeCreated {
			created++
		}
		ordinals = append(ordinals, int(result.Receipt.ReceiptOrdinal))
	}
	if created != 1 {
		t.Fatalf("created outcomes=%d, want 1", created)
	}
	sort.Ints(ordinals)
	for i, ordinal := range ordinals {
		if ordinal != i+1 {
			t.Fatalf("ordinals=%v", ordinals)
		}
	}
	var scopeCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_scope WHERE workspace_id=$1`, fixture.workspaceID).Scan(&scopeCount); err != nil || scopeCount != 1 {
		t.Fatalf("scope count=%d err=%v", scopeCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE workspace_id=$1`, fixture.workspaceID).Scan(&receiptCount); err != nil || receiptCount != writers {
		t.Fatalf("receipt count=%d err=%v", receiptCount, err)
	}

	// Allocation is transactional: a rolled-back ordinal must be reused by the
	// next committed receipt rather than leaving a pagination gap.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback allocation: %v", err)
	}
	qtx := db.New(tx)
	if err := lockWorkspace(ctx, qtx, fixture.workspaceID); err != nil {
		t.Fatalf("lock workspace: %v", err)
	}
	if _, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: fixture.workspaceID, ID: scopeID}); err != nil {
		t.Fatalf("lock scope: %v", err)
	}
	rolledBackOrdinal, err := qtx.AllocateCoordinationReceiptOrdinal(ctx, db.AllocateCoordinationReceiptOrdinalParams{WorkspaceID: fixture.workspaceID, ID: scopeID})
	if err != nil {
		t.Fatalf("allocate rollback ordinal: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback allocation: %v", err)
	}
	result, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "after-rollback"})
	if err != nil {
		t.Fatalf("ensure after rollback: %v", err)
	}
	if result.Receipt.ReceiptOrdinal != rolledBackOrdinal {
		t.Fatalf("ordinal after rollback=%d, want %d", result.Receipt.ReceiptOrdinal, rolledBackOrdinal)
	}

	rows, err := db.New(pool).ListCoordinationReceiptsByScope(ctx, db.ListCoordinationReceiptsByScopeParams{
		WorkspaceID: fixture.workspaceID, CoordinationScopeID: scopeID,
		BeforeOrdinal: pgtype.Int8{}, LimitRows: 100,
	})
	if err != nil || len(rows) != writers+1 {
		t.Fatalf("receipt page rows=%d err=%v", len(rows), err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].ReceiptOrdinal <= rows[i].ReceiptOrdinal {
			t.Fatalf("receipt page not descending at %d", i)
		}
	}
}
