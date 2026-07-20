package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationInspectLifecycleReceiptWindowAndNoSideEffects(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2401, "inspect downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 2402, "inspect upstream")
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	svc := NewCoordinationService(db.New(pool), pool)
	before := captureCoordinationPassiveIssueSnapshots(t, pool, []pgtype.UUID{fixture.issueID, downstream, upstream})

	scopeResult, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-lifecycle", IdempotencyKey: "inspect-scope",
	})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	dependency, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scopeResult.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		IdempotencyKey: "inspect-dependency",
	})
	if err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	blocker, err := svc.AppendBlocker(ctx, actor, AppendBlockerInput{
		ScopeID: scopeResult.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		DependencyID: dependency.Dependency.ID, SchemaVersion: CoordinationBlockerSchemaV1,
		ReasonCode: CoordinationBlockerReasonWaitingOnIssue, EvidenceRefs: []CoordinationEvidenceRef{{Kind: "issue", ID: downstream}},
		IdempotencyKey: "inspect-blocker",
	})
	if err != nil {
		t.Fatalf("append blocker: %v", err)
	}

	initial, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, "")
	if err != nil {
		t.Fatalf("inspect initial state: %v", err)
	}
	if initial.ScopeRevision != 2 || initial.Scope.Revision != 2 || len(initial.ActiveDependencies) != 1 || len(initial.OpenBlockers) != 1 || len(initial.ReceiptRefs) != 3 || initial.NextReceiptCursor != "" {
		t.Fatalf("unexpected initial inspection: %+v", initial)
	}
	if !uuidEqual(initial.ActiveDependencies[0].ID, dependency.Dependency.ID) || !uuidEqual(initial.OpenBlockers[0].ID, blocker.Blocker.ID) || len(initial.OpenBlockers[0].CreateEvidenceRefs) != 1 {
		t.Fatalf("initial inspection facts=%+v", initial)
	}
	assertReceiptOrdinalsDescending(t, initial.ReceiptRefs)

	resolvedBlocker, err := svc.ResolveBlocker(ctx, actor, ResolveBlockerInput{
		ScopeID: scopeResult.Scope.ID, BlockerID: blocker.Blocker.ID, ExpectedRevision: 2, SchemaVersion: CoordinationBlockerSchemaV1,
		ResolutionCode: CoordinationBlockerResolutionNoLongerBlocking, IdempotencyKey: "inspect-resolve-blocker",
	})
	if err != nil || resolvedBlocker.ScopeRevision != 3 {
		t.Fatalf("resolve blocker=%+v err=%v", resolvedBlocker, err)
	}
	middle, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, "")
	if err != nil || middle.ScopeRevision != 3 || len(middle.ActiveDependencies) != 1 || len(middle.OpenBlockers) != 0 {
		t.Fatalf("inspect independent blocker resolution=%+v err=%v", middle, err)
	}

	resolvedDependency, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{
		ScopeID: scopeResult.Scope.ID, DependencyID: dependency.Dependency.ID, ExpectedRevision: 3,
		IdempotencyKey: "inspect-resolve-dependency",
	})
	if err != nil || resolvedDependency.ScopeRevision != 4 {
		t.Fatalf("resolve dependency=%+v err=%v", resolvedDependency, err)
	}
	finalFacts, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, "")
	if err != nil || finalFacts.ScopeRevision != 4 || len(finalFacts.ActiveDependencies) != 0 || len(finalFacts.OpenBlockers) != 0 {
		t.Fatalf("inspect final facts=%+v err=%v", finalFacts, err)
	}

	for index := 0; index < 105; index++ {
		result, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{
			ScopeID: scopeResult.Scope.ID, DependencyID: dependency.Dependency.ID, ExpectedRevision: 4,
			IdempotencyKey: fmt.Sprintf("inspect-receipt-noop-%03d", index),
		})
		if err != nil || result.Outcome != CoordinationOutcomeNoop || result.ScopeRevision != 4 {
			t.Fatalf("create no-op receipt %d result=%+v err=%v", index, result, err)
		}
	}
	if _, err := pool.Exec(ctx, `
UPDATE coordination_receipt
SET created_at = '2030-01-01T00:00:00Z'::timestamptz - receipt_ordinal * interval '1 second'
WHERE workspace_id=$1 AND coordination_scope_id=$2`, fixture.workspaceID, scopeResult.Scope.ID); err != nil {
		t.Fatalf("invert receipt timestamps: %v", err)
	}
	firstPage, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, "")
	if err != nil {
		t.Fatalf("inspect first receipt page: %v", err)
	}
	if firstPage.ScopeRevision != 4 || len(firstPage.ReceiptRefs) != CoordinationReceiptPageSize || firstPage.NextReceiptCursor == "" || firstPage.ReceiptRefs[0].ReceiptOrdinal != 110 || firstPage.ReceiptRefs[99].ReceiptOrdinal != 11 {
		t.Fatalf("first receipt page=%+v", firstPage)
	}
	assertReceiptOrdinalsDescending(t, firstPage.ReceiptRefs)
	decoded, err := decodeCoordinationReceiptCursor(firstPage.NextReceiptCursor)
	if err != nil || decoded.UpperOrdinal != 110 || decoded.LastOrdinal != 11 || decoded.ScopeRevision != 4 {
		t.Fatalf("first receipt cursor=%+v err=%v", decoded, err)
	}

	noOpAfterWindow, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{
		ScopeID: scopeResult.Scope.ID, DependencyID: dependency.Dependency.ID, ExpectedRevision: 4,
		IdempotencyKey: "inspect-receipt-after-window",
	})
	if err != nil || noOpAfterWindow.ScopeRevision != 4 || noOpAfterWindow.Receipt.ReceiptOrdinal != 111 {
		t.Fatalf("no-op after first page=%+v err=%v", noOpAfterWindow, err)
	}
	secondPage, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, firstPage.NextReceiptCursor)
	if err != nil || len(secondPage.ReceiptRefs) != 10 || secondPage.NextReceiptCursor != "" || secondPage.ReceiptRefs[0].ReceiptOrdinal != 10 || secondPage.ReceiptRefs[9].ReceiptOrdinal != 1 {
		t.Fatalf("second receipt page=%+v err=%v", secondPage, err)
	}
	assertReceiptOrdinalsDescending(t, secondPage.ReceiptRefs)
	seen := make(map[int64]struct{}, 110)
	for _, ref := range append(append([]ReceiptRef(nil), firstPage.ReceiptRefs...), secondPage.ReceiptRefs...) {
		if ref.ReceiptOrdinal == 111 {
			t.Fatal("new no-op receipt entered the prior receipt window")
		}
		if _, duplicate := seen[ref.ReceiptOrdinal]; duplicate {
			t.Fatalf("duplicate receipt ordinal %d", ref.ReceiptOrdinal)
		}
		seen[ref.ReceiptOrdinal] = struct{}{}
	}
	if len(seen) != 110 {
		t.Fatalf("receipt window size=%d", len(seen))
	}

	if _, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, "not-base64"); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("malformed cursor code=%q err=%v", coordinationCode(err), err)
	}
	foreignScopeID, _ := newPgUUID()
	foreignCursor, err := encodeCoordinationReceiptCursor(coordinationReceiptCursor{
		WorkspaceID: actor.WorkspaceID, ScopeID: foreignScopeID, ScopeRevision: 4, UpperOrdinal: 110, LastOrdinal: 11,
	})
	if err != nil {
		t.Fatalf("encode foreign cursor: %v", err)
	}
	if _, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, foreignCursor); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("foreign cursor code=%q err=%v", coordinationCode(err), err)
	}

	newDependency, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scopeResult.Scope.ID, ExpectedRevision: 4, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		IdempotencyKey: "inspect-revision-advance",
	})
	if err != nil || newDependency.ScopeRevision != 5 {
		t.Fatalf("advance scope revision=%+v err=%v", newDependency, err)
	}
	if _, err := svc.InspectScope(ctx, actor, scopeResult.Scope.ID, firstPage.NextReceiptCursor); coordinationCode(err) != CoordinationRevisionConflict {
		t.Fatalf("stale receipt cursor code=%q err=%v", coordinationCode(err), err)
	}

	after := captureCoordinationPassiveIssueSnapshots(t, pool, []pgtype.UUID{fixture.issueID, downstream, upstream})
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("coordination flow changed issue/task/autopilot state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestWorkCoordinationInspectResolvedDependencyKeepsOpenBlockerEvidence(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2406, "resolved-link downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 2407, "resolved-link upstream")
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	svc := NewCoordinationService(db.New(pool), pool)
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-resolved-link", IdempotencyKey: "inspect-resolved-link-scope",
	})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	dependency, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		IdempotencyKey: "inspect-resolved-link-dependency",
	})
	if err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	blocker, err := svc.AppendBlocker(ctx, actor, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		DependencyID: dependency.Dependency.ID, SchemaVersion: CoordinationBlockerSchemaV1,
		ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "inspect-resolved-link-blocker",
	})
	if err != nil {
		t.Fatalf("append blocker: %v", err)
	}
	resolved, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{
		ScopeID: scope.Scope.ID, DependencyID: dependency.Dependency.ID, ExpectedRevision: 2,
		IdempotencyKey: "inspect-resolved-link-resolve-dependency",
	})
	if err != nil || resolved.ScopeRevision != 3 {
		t.Fatalf("resolve dependency=%+v err=%v", resolved, err)
	}
	inspection, err := svc.InspectScope(ctx, actor, scope.Scope.ID, "")
	if err != nil || inspection.ScopeRevision != 3 || len(inspection.ActiveDependencies) != 0 || len(inspection.OpenBlockers) != 1 || !uuidEqual(inspection.OpenBlockers[0].ID, blocker.Blocker.ID) || !uuidEqual(inspection.OpenBlockers[0].DependencyID, dependency.Dependency.ID) {
		t.Fatalf("resolved dependency/open blocker inspection=%+v err=%v", inspection, err)
	}
}

func TestWorkCoordinationInspectReceiptOrderIgnoresTransactionStartTime(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	svc := NewCoordinationService(db.New(pool), pool)
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-commit-order", IdempotencyKey: "inspect-order-scope",
	})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	earlyTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin early transaction: %v", err)
	}
	earlyDone := false
	defer func() {
		if !earlyDone {
			_ = earlyTx.Rollback(context.Background())
		}
	}()
	laterOpened, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-commit-order", IdempotencyKey: "inspect-order-later-opened",
	})
	if err != nil || laterOpened.Receipt.ReceiptOrdinal != 2 {
		t.Fatalf("later-opened receipt=%+v err=%v", laterOpened, err)
	}
	qtx := db.New(earlyTx)
	if err := lockWorkspace(ctx, qtx, fixture.workspaceID); err != nil {
		t.Fatalf("lock early transaction: %v", err)
	}
	if _, err := qtx.LockCoordinationScope(ctx, db.LockCoordinationScopeParams{WorkspaceID: fixture.workspaceID, ID: scope.Scope.ID}); err != nil {
		t.Fatalf("lock early scope: %v", err)
	}
	ordinal, err := qtx.AllocateCoordinationReceiptOrdinal(ctx, db.AllocateCoordinationReceiptOrdinalParams{WorkspaceID: fixture.workspaceID, ID: scope.Scope.ID})
	if err != nil || ordinal != 3 {
		t.Fatalf("allocate early transaction ordinal=%d err=%v", ordinal, err)
	}
	receiptID, _ := newPgUUID()
	if _, err := qtx.InsertCoordinationReceipt(ctx, db.InsertCoordinationReceiptParams{
		ID: receiptID, WorkspaceID: fixture.workspaceID, CoordinationScopeID: scope.Scope.ID,
		ReceiptOrdinal: ordinal, Operation: CoordinationOperationEnsureScope, IdempotencyKey: "inspect-order-early-opened",
		RequestHash: make([]byte, 32), ResourceType: CoordinationResourceScope, ResourceID: scope.Scope.ID,
		RevisionBefore: 0, RevisionAfter: 0, ResultSnapshot: []byte(`{}`),
		ActorType: CoordinationActorMember, ActorID: fixture.userID,
	}); err != nil {
		t.Fatalf("insert early transaction receipt: %v", err)
	}
	if err := earlyTx.Commit(ctx); err != nil {
		t.Fatalf("commit early transaction last: %v", err)
	}
	earlyDone = true
	inspection, err := svc.InspectScope(ctx, actor, scope.Scope.ID, "")
	if err != nil || len(inspection.ReceiptRefs) != 3 {
		t.Fatalf("inspect receipt commit order=%+v err=%v", inspection, err)
	}
	if inspection.ReceiptRefs[0].ReceiptOrdinal != 3 || inspection.ReceiptRefs[1].ReceiptOrdinal != 2 || inspection.ReceiptRefs[2].ReceiptOrdinal != 1 {
		t.Fatalf("receipt order=%+v", inspection.ReceiptRefs)
	}
}

func TestWorkCoordinationInspectUsesRepeatableReadSnapshot(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2411, "snapshot downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 2412, "snapshot upstream")
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := NewCoordinationService(db.New(pool), pool).EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-snapshot", IdempotencyKey: "inspect-snapshot-scope",
	})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}

	blockerTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker transaction: %v", err)
	}
	blockerDone := false
	defer func() {
		if !blockerDone {
			_ = blockerTx.Rollback(context.Background())
		}
	}()
	if _, err := blockerTx.Exec(ctx, `LOCK TABLE coordination_dependency IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock dependency table: %v", err)
	}

	applicationName := fmt.Sprintf("wcs-inspect-%d", time.Now().UnixNano())
	inspectPool := openNamedWorkCoordinationPool(t, applicationName)
	inspectService := NewCoordinationService(db.New(inspectPool), inspectPool)
	type inspectResult struct {
		value ScopeInspection
		err   error
	}
	resultCh := make(chan inspectResult, 1)
	go func() {
		value, err := inspectService.InspectScope(context.Background(), actor, scope.Scope.ID, "")
		resultCh <- inspectResult{value: value, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_stat_activity
  WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%coordination_dependency%'
)`, applicationName).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked inspection: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inspection did not reach the dependency read barrier")
		}
		time.Sleep(20 * time.Millisecond)
	}

	dependencyID, _ := newPgUUID()
	if _, err := blockerTx.Exec(ctx, `
INSERT INTO coordination_dependency (
  id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,
  created_by_type,created_by_id,created_at
) VALUES ($1,$2,$3,$4,$5,'member',$6,clock_timestamp())`,
		dependencyID, fixture.workspaceID, scope.Scope.ID, downstream, upstream, fixture.userID); err != nil {
		t.Fatalf("insert concurrent dependency: %v", err)
	}
	if _, err := blockerTx.Exec(ctx, `UPDATE coordination_scope SET revision=1,updated_at=clock_timestamp() WHERE id=$1`, scope.Scope.ID); err != nil {
		t.Fatalf("advance concurrent scope revision: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent mutation: %v", err)
	}
	blockerDone = true

	select {
	case result := <-resultCh:
		if result.err != nil || result.value.ScopeRevision != 0 || len(result.value.ActiveDependencies) != 0 {
			t.Fatalf("repeatable-read inspection mixed revisions: value=%+v err=%v", result.value, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inspection remained blocked after concurrent commit")
	}
	fresh, err := inspectService.InspectScope(ctx, actor, scope.Scope.ID, "")
	if err != nil || fresh.ScopeRevision != 1 || len(fresh.ActiveDependencies) != 1 || !uuidEqual(fresh.ActiveDependencies[0].ID, dependencyID) {
		t.Fatalf("fresh inspection did not see committed mutation: value=%+v err=%v", fresh, err)
	}
}

func TestWorkCoordinationInspectFactBoundsOrderingAndReceiptAllowlist(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	svc := NewCoordinationService(db.New(pool), pool)
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-bounds", IdempotencyKey: "inspect-bounds-scope",
	})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO coordination_dependency (
  id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,
  created_by_type,created_by_id,created_at
)
SELECT gen_random_uuid(),$1,$2,gen_random_uuid(),gen_random_uuid(),'member',$3,'2026-01-01T00:00:00Z'::timestamptz
FROM generate_series(1,1000)`, fixture.workspaceID, scope.Scope.ID, fixture.userID); err != nil {
		t.Fatalf("seed dependency bound: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO coordination_record (
  id,workspace_id,coordination_scope_id,kind,schema_version,status,root_issue_id,
  downstream_issue_id,upstream_issue_id,reason_code,created_by_type,created_by_id,created_at
)
SELECT gen_random_uuid(),$1,$2,'blocker',1,'open',$3,gen_random_uuid(),gen_random_uuid(),
       'waiting_on_issue','member',$4,'2026-01-01T00:00:00Z'::timestamptz
FROM generate_series(1,1000)`, fixture.workspaceID, scope.Scope.ID, fixture.issueID, fixture.userID); err != nil {
		t.Fatalf("seed blocker bound: %v", err)
	}
	inspection, err := svc.InspectScope(ctx, actor, scope.Scope.ID, "")
	if err != nil || len(inspection.ActiveDependencies) != 1000 || len(inspection.OpenBlockers) != 1000 {
		t.Fatalf("inspect hard bounds dependencies=%d blockers=%d err=%v", len(inspection.ActiveDependencies), len(inspection.OpenBlockers), err)
	}
	for index := 1; index < len(inspection.ActiveDependencies); index++ {
		previous, current := inspection.ActiveDependencies[index-1], inspection.ActiveDependencies[index]
		if previous.CreatedAt.After(current.CreatedAt) || (previous.CreatedAt.Equal(current.CreatedAt) && bytes.Compare(previous.ID.Bytes[:], current.ID.Bytes[:]) >= 0) {
			t.Fatalf("dependency order broke at %d", index)
		}
	}
	for index := 1; index < len(inspection.OpenBlockers); index++ {
		previous, current := inspection.OpenBlockers[index-1], inspection.OpenBlockers[index]
		if previous.CreatedAt.Before(current.CreatedAt) || (previous.CreatedAt.Equal(current.CreatedAt) && bytes.Compare(previous.ID.Bytes[:], current.ID.Bytes[:]) <= 0) {
			t.Fatalf("blocker order broke at %d", index)
		}
	}

	var extraDependency pgtype.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO coordination_dependency (
  id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,created_by_type,created_by_id
) VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),gen_random_uuid(),'member',$3)
RETURNING id`, fixture.workspaceID, scope.Scope.ID, fixture.userID).Scan(&extraDependency); err != nil {
		t.Fatalf("seed dependency invariant violation: %v", err)
	}
	if _, err := svc.InspectScope(ctx, actor, scope.Scope.ID, ""); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("dependency invariant code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM coordination_dependency WHERE id=$1`, extraDependency); err != nil {
		t.Fatalf("remove dependency invariant violation: %v", err)
	}
	var extraBlocker pgtype.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO coordination_record (
  id,workspace_id,coordination_scope_id,kind,schema_version,status,root_issue_id,
  downstream_issue_id,upstream_issue_id,reason_code,created_by_type,created_by_id
) VALUES (gen_random_uuid(),$1,$2,'blocker',1,'open',$3,gen_random_uuid(),gen_random_uuid(),
          'waiting_on_issue','member',$4)
RETURNING id`, fixture.workspaceID, scope.Scope.ID, fixture.issueID, fixture.userID).Scan(&extraBlocker); err != nil {
		t.Fatalf("seed blocker invariant violation: %v", err)
	}
	if _, err := svc.InspectScope(ctx, actor, scope.Scope.ID, ""); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("blocker invariant code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM coordination_record WHERE id=$1`, extraBlocker); err != nil {
		t.Fatalf("remove blocker invariant violation: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE coordination_receipt SET operation='future_operation' WHERE coordination_scope_id=$1`, scope.Scope.ID); err != nil {
		t.Fatalf("corrupt receipt operation: %v", err)
	}
	if _, err := svc.InspectScope(ctx, actor, scope.Scope.ID, ""); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("receipt allowlist code=%q err=%v", coordinationCode(err), err)
	}
}

func TestWorkCoordinationInspectCrossScopeOwnerIsolation(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2416, "cross-scope downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 2417, "cross-scope upstream")
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	svc := NewCoordinationService(db.New(pool), pool)
	scopeA, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-scope-a", IdempotencyKey: "inspect-scope-a",
	})
	if err != nil {
		t.Fatalf("ensure scope A: %v", err)
	}
	scopeB, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-scope-b", IdempotencyKey: "inspect-scope-b",
	})
	if err != nil {
		t.Fatalf("ensure scope B: %v", err)
	}
	owned, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scopeA.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		IdempotencyKey: "inspect-owned-pair",
	})
	if err != nil {
		t.Fatalf("add scope A pair: %v", err)
	}
	if _, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scopeB.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		IdempotencyKey: "inspect-foreign-pair",
	}); coordinationCode(err) != CoordinationDependencyScopeConflict {
		t.Fatalf("scope B duplicate pair code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{
		ScopeID: scopeB.Scope.ID, DependencyID: owned.Dependency.ID, ExpectedRevision: 0,
		IdempotencyKey: "inspect-foreign-resolve",
	}); coordinationCode(err) != CoordinationDependencyScopeConflict {
		t.Fatalf("scope B resolve code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scopeB.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: upstream, UpstreamIssueID: downstream,
		IdempotencyKey: "inspect-cross-scope-cycle",
	}); coordinationCode(err) != CoordinationCycle {
		t.Fatalf("workspace cycle code=%q err=%v", coordinationCode(err), err)
	}
	inspectionA, err := svc.InspectScope(ctx, actor, scopeA.Scope.ID, "")
	if err != nil || inspectionA.ScopeRevision != 1 || len(inspectionA.ActiveDependencies) != 1 {
		t.Fatalf("scope A inspection=%+v err=%v", inspectionA, err)
	}
	inspectionB, err := svc.InspectScope(ctx, actor, scopeB.Scope.ID, "")
	if err != nil || inspectionB.ScopeRevision != 0 || len(inspectionB.ActiveDependencies) != 0 {
		t.Fatalf("scope B inspection=%+v err=%v", inspectionB, err)
	}
	assertCoordinationScopeRevision(t, pool, scopeA.Scope.ID, 1)
	assertCoordinationScopeRevision(t, pool, scopeB.Scope.ID, 0)
}

func TestWorkCoordinationInspectAgentRootAuthorityAndRevocation(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	agentFixture := createWorkCoordinationAgentFixture(t, pool, fixture)
	memberActor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	agentActor := CoordinationActor{
		WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorAgent, ActorID: agentFixture.agentID,
		TaskID: agentFixture.taskID, TaskCredentialRef: agentFixture.tokenOneID,
	}
	svc := NewCoordinationService(db.New(pool), pool)
	scope, err := svc.EnsureScope(ctx, memberActor, EnsureScopeInput{
		RootIssueID: fixture.issueID, WorkflowProfileKey: "inspect-agent", IdempotencyKey: "inspect-agent-scope",
	})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	if _, err := svc.InspectScope(ctx, agentActor, scope.Scope.ID, ""); err != nil {
		t.Fatalf("agent inspect with matching actual root: %v", err)
	}
	var otherRoot pgtype.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number)
VALUES ($1,'WCS other inspect root','member',$2,'none',2421) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&otherRoot); err != nil {
		t.Fatalf("insert other root: %v", err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, otherRoot) }()
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=$1 WHERE id=$2`, otherRoot, agentFixture.taskID); err != nil {
		t.Fatalf("move task to other root: %v", err)
	}
	if _, err := svc.InspectScope(ctx, agentActor, scope.Scope.ID, ""); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("other-root inspect code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=$1 WHERE id=$2`, fixture.issueID, agentFixture.taskID); err != nil {
		t.Fatalf("restore task root: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM task_token WHERE id=$1`, agentFixture.tokenOneID); err != nil {
		t.Fatalf("revoke task credential: %v", err)
	}
	if _, err := svc.InspectScope(ctx, agentActor, scope.Scope.ID, ""); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("revoked inspect code=%q err=%v", coordinationCode(err), err)
	}
}

func TestWorkCoordinationReceiptCursorStrictShape(t *testing.T) {
	workspaceID := util.MustParseUUID("00000000-0000-0000-0000-000000000001")
	scopeID := util.MustParseUUID("00000000-0000-0000-0000-000000000002")
	encoded, err := encodeCoordinationReceiptCursor(coordinationReceiptCursor{
		WorkspaceID: workspaceID, ScopeID: scopeID, ScopeRevision: 7, UpperOrdinal: 200, LastOrdinal: 101,
	})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	decoded, err := decodeCoordinationReceiptCursor(encoded)
	if err != nil || decoded.ScopeRevision != 7 || decoded.UpperOrdinal != 200 || decoded.LastOrdinal != 101 || !uuidEqual(decoded.WorkspaceID, workspaceID) || !uuidEqual(decoded.ScopeID, scopeID) {
		t.Fatalf("decoded cursor=%+v err=%v", decoded, err)
	}
	invalidJSON := []string{
		`{"v":1,"v":1,"collection":"receipt","workspace_id":"00000000-0000-0000-0000-000000000001","scope_id":"00000000-0000-0000-0000-000000000002","scope_revision":7,"upper_ordinal":200,"last_ordinal":101}`,
		`{"v":1,"collection":"dependency","workspace_id":"00000000-0000-0000-0000-000000000001","scope_id":"00000000-0000-0000-0000-000000000002","scope_revision":7,"upper_ordinal":200,"last_ordinal":101}`,
		`{"v":1,"collection":"receipt","workspace_id":"00000000-0000-0000-0000-000000000001","scope_id":"00000000-0000-0000-0000-000000000002","scope_revision":7,"upper_ordinal":100,"last_ordinal":101}`,
		`{"v":1,"collection":"receipt","workspace_id":"00000000-0000-0000-0000-000000000001","scope_id":"00000000-0000-0000-0000-000000000002","scope_revision":7,"upper_ordinal":200,"last_ordinal":101,"unknown":true}`,
	}
	for _, value := range invalidJSON {
		if _, err := decodeCoordinationReceiptCursor(base64RawURL(value)); err == nil {
			t.Fatalf("accepted invalid cursor %s", value)
		}
	}
}

type coordinationPassiveIssueSnapshot struct {
	Status        string
	AssigneeType  string
	AssigneeID    string
	UpdatedAt     time.Time
	Metadata      string
	CommentCount  int64
	ActiveTasks   int64
	TotalTasks    int64
	AutopilotRuns int64
}

func captureCoordinationPassiveIssueSnapshots(t *testing.T, pool *pgxpool.Pool, issueIDs []pgtype.UUID) map[string]coordinationPassiveIssueSnapshot {
	t.Helper()
	result := make(map[string]coordinationPassiveIssueSnapshot, len(issueIDs))
	for _, issueID := range issueIDs {
		var snapshot coordinationPassiveIssueSnapshot
		if err := pool.QueryRow(context.Background(), `
SELECT i.status,
       COALESCE(i.assignee_type,''),
       COALESCE(i.assignee_id::text,''),
       i.updated_at,
       COALESCE(i.metadata::text,'null'),
       (SELECT count(*) FROM comment c WHERE c.issue_id=i.id),
       (SELECT count(*) FROM agent_task_queue t WHERE t.issue_id=i.id AND t.status IN ('queued','dispatched','running','waiting_local_directory','deferred')),
       (SELECT count(*) FROM agent_task_queue t WHERE t.issue_id=i.id),
       (SELECT count(*) FROM autopilot_run r WHERE r.issue_id=i.id)
FROM issue i
WHERE i.id=$1`, issueID).Scan(
			&snapshot.Status, &snapshot.AssigneeType, &snapshot.AssigneeID, &snapshot.UpdatedAt, &snapshot.Metadata,
			&snapshot.CommentCount, &snapshot.ActiveTasks, &snapshot.TotalTasks, &snapshot.AutopilotRuns,
		); err != nil {
			t.Fatalf("capture passive issue snapshot: %v", err)
		}
		result[util.UUIDToString(issueID)] = snapshot
	}
	return result
}

func assertReceiptOrdinalsDescending(t *testing.T, refs []ReceiptRef) {
	t.Helper()
	for index, ref := range refs {
		if ref.ActorType != CoordinationActorMember {
			t.Fatalf("receipt %d actor type=%q", index, ref.ActorType)
		}
		if index > 0 && refs[index-1].ReceiptOrdinal <= ref.ReceiptOrdinal {
			t.Fatalf("receipt order at %d: %d then %d", index, refs[index-1].ReceiptOrdinal, ref.ReceiptOrdinal)
		}
	}
}

func openNamedWorkCoordinationPool(t *testing.T, applicationName string) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse database config: %v", err)
	}
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open named database pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping named database pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func base64RawURL(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
