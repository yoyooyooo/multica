package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationDependencyLifecycleAndOwnerScope(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2, "downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 3, "upstream")

	if _, err := pool.Exec(ctx, `
INSERT INTO issue_dependency (issue_id, depends_on_issue_id, type) VALUES
    ($1,$2,'blocks'), ($2,$1,'blocked_by'), ($1,$2,'related')`, downstream, upstream); err != nil {
		t.Fatalf("seed legacy dependencies: %v", err)
	}

	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scopeOne, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "v2-scope-one"})
	if err != nil {
		t.Fatalf("scope one: %v", err)
	}
	scopeTwo, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "review-loop", IdempotencyKey: "v2-scope-two"})
	if err != nil {
		t.Fatalf("scope two: %v", err)
	}

	addInput := AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "v2-add-one"}
	created, err := svc.AddDependency(ctx, actor, addInput)
	if err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	if created.Outcome != CoordinationOutcomeCreated || created.ScopeRevision != 1 || created.Receipt.ReceiptOrdinal != 2 || created.Dependency.Resolved {
		t.Fatalf("created=%+v", created)
	}
	_, err = svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: upstream, UpstreamIssueID: downstream, IdempotencyKey: addInput.IdempotencyKey})
	if coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("different-hash code=%q err=%v", coordinationCode(err), err)
	}
	_, err = svc.ResolveDependency(ctx, actor, ResolveDependencyInput{ScopeID: scopeOne.Scope.ID, DependencyID: created.Dependency.ID, ExpectedRevision: 1, IdempotencyKey: addInput.IdempotencyKey})
	if coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("different-operation code=%q err=%v", coordinationCode(err), err)
	}
	var otherUser pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('WCS dependency other',$1) RETURNING id`, fmt.Sprintf("wcs-dependency-other-%d@multica.ai", time.Now().UnixNano())).Scan(&otherUser); err != nil {
		t.Fatalf("insert other member user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, fixture.workspaceID, otherUser); err != nil {
		t.Fatalf("insert other member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, fixture.workspaceID, otherUser)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, otherUser)
	})
	otherActor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: otherUser}
	if _, err := svc.AddDependency(ctx, otherActor, addInput); coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("different-actor code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scopeOne.Scope.ID, 1)
	var conflictReceiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, scopeOne.Scope.ID).Scan(&conflictReceiptCount); err != nil || conflictReceiptCount != 2 {
		t.Fatalf("conflict receipt count=%d err=%v", conflictReceiptCount, err)
	}

	replay, err := svc.AddDependency(ctx, actor, addInput)
	if err != nil {
		t.Fatalf("add replay: %v", err)
	}
	if replay.Outcome != CoordinationOutcomeReplay || replay.ScopeRevision != 1 || replay.Receipt.ReceiptOrdinal != created.Receipt.ReceiptOrdinal || !uuidEqual(replay.Dependency.ID, created.Dependency.ID) {
		t.Fatalf("replay=%+v", replay)
	}

	noop, err := svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "v2-add-noop"})
	if err != nil {
		t.Fatalf("add noop: %v", err)
	}
	if noop.Outcome != CoordinationOutcomeNoop || noop.ScopeRevision != 1 || noop.Receipt.ReceiptOrdinal != 3 {
		t.Fatalf("noop=%+v", noop)
	}

	_, err = svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeTwo.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "v2-cross-scope"})
	if coordinationCode(err) != CoordinationDependencyScopeConflict {
		t.Fatalf("cross-scope code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scopeOne.Scope.ID, 1)
	assertCoordinationScopeRevision(t, pool, scopeTwo.Scope.ID, 0)

	page, err := svc.ListDependencies(ctx, actor, scopeOne.Scope.ID, "", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.ScopeRevision != 1 || len(page.Dependencies) != 1 || page.NextCursor != "" || !uuidEqual(page.Dependencies[0].ID, created.Dependency.ID) {
		t.Fatalf("page=%+v", page)
	}

	resolved, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{ScopeID: scopeOne.Scope.ID, DependencyID: created.Dependency.ID, ExpectedRevision: 1, IdempotencyKey: "v2-resolve-one"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Outcome != CoordinationOutcomeResolved || resolved.ScopeRevision != 2 || resolved.Receipt.ReceiptOrdinal != 4 || !resolved.Dependency.Resolved {
		t.Fatalf("resolved=%+v", resolved)
	}
	resolvedReplay, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{ScopeID: scopeOne.Scope.ID, DependencyID: created.Dependency.ID, ExpectedRevision: 1, IdempotencyKey: "v2-resolve-one"})
	if err != nil || resolvedReplay.Outcome != CoordinationOutcomeReplay || resolvedReplay.Receipt.ReceiptOrdinal != 4 || resolvedReplay.ScopeRevision != 2 {
		t.Fatalf("resolve replay=%+v err=%v", resolvedReplay, err)
	}
	resolvedNoop, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{ScopeID: scopeOne.Scope.ID, DependencyID: created.Dependency.ID, ExpectedRevision: 2, IdempotencyKey: "v2-resolve-noop"})
	if err != nil || resolvedNoop.Outcome != CoordinationOutcomeNoop || resolvedNoop.Receipt.ReceiptOrdinal != 5 || resolvedNoop.ScopeRevision != 2 {
		t.Fatalf("resolve noop=%+v err=%v", resolvedNoop, err)
	}

	page, err = svc.ListDependencies(ctx, actor, scopeOne.Scope.ID, "", 100)
	if err != nil || len(page.Dependencies) != 0 || page.ScopeRevision != 2 {
		t.Fatalf("resolved page=%+v err=%v", page, err)
	}

	second, err := svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 2, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "v2-add-again"})
	if err != nil || second.Outcome != CoordinationOutcomeCreated || second.ScopeRevision != 3 {
		t.Fatalf("second add=%+v err=%v", second, err)
	}
	_, err = svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 3, DownstreamIssueID: upstream, UpstreamIssueID: downstream, IdempotencyKey: "v2-cycle"})
	if coordinationCode(err) != CoordinationCycle {
		t.Fatalf("cycle code=%q err=%v", coordinationCode(err), err)
	}
	_, err = svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 3, DownstreamIssueID: downstream, UpstreamIssueID: downstream, IdempotencyKey: "v2-self"})
	if coordinationCode(err) != CoordinationSelfDependency {
		t.Fatalf("self code=%q err=%v", coordinationCode(err), err)
	}
	_, err = svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scopeOne.Scope.ID, ExpectedRevision: 2, DownstreamIssueID: upstream, UpstreamIssueID: fixture.issueID, IdempotencyKey: "v2-stale"})
	if coordinationCode(err) != CoordinationRevisionConflict {
		t.Fatalf("stale code=%q err=%v", coordinationCode(err), err)
	}

	var legacyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue_dependency WHERE issue_id=$1 AND depends_on_issue_id=$2`, downstream, upstream).Scan(&legacyCount); err != nil || legacyCount != 2 {
		t.Fatalf("legacy rows=%d err=%v", legacyCount, err)
	}
}

func TestWorkCoordinationDependencyEndpointFailures(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	child := createWorkCoordinationChildIssue(t, pool, fixture, 2, "endpoint-child")
	otherWorkspace := createWorkCoordinationFixture(t, pool)
	var otherRoot pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS unrelated root','member',$2,'none',99) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&otherRoot); err != nil {
		t.Fatalf("insert unrelated root: %v", err)
	}
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "endpoint-failure-scope"})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	missing, err := newPgUUID()
	if err != nil {
		t.Fatalf("missing uuid: %v", err)
	}
	cases := []struct {
		name       string
		upstreamID pgtype.UUID
		key        string
		wantCode   CoordinationErrorCode
	}{
		{name: "missing", upstreamID: missing, key: "endpoint-missing", wantCode: CoordinationNotFound},
		{name: "cross workspace", upstreamID: otherWorkspace.issueID, key: "endpoint-cross-workspace", wantCode: CoordinationCrossWorkspace},
		{name: "outside scope", upstreamID: otherRoot, key: "endpoint-outside-scope", wantCode: CoordinationForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: child, UpstreamIssueID: tc.upstreamID, IdempotencyKey: tc.key})
			if coordinationCode(err) != tc.wantCode {
				t.Fatalf("code=%q want=%q err=%v", coordinationCode(err), tc.wantCode, err)
			}
		})
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 0)
	var dependencies, receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_dependency WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&dependencies); err != nil || dependencies != 0 {
		t.Fatalf("dependency count=%d err=%v", dependencies, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
}

func TestWorkCoordinationDependencyPaginationAndRevisionCursor(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2, "cursor-downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 3, "cursor-upstream")
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "cursor-scope"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO coordination_dependency (
    id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,
    created_by_type,created_by_id,created_at
)
SELECT
    md5('id-'||g)::uuid,$1,$2,md5('down-'||g)::uuid,md5('up-'||g)::uuid,
    'member',$3,'2026-01-01T00:00:00Z'::timestamptz
FROM generate_series(1,205) g`, fixture.workspaceID, scope.Scope.ID, fixture.userID); err != nil {
		t.Fatalf("seed dependencies: %v", err)
	}

	seen := map[[16]byte]struct{}{}
	cursor := ""
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		page, err := svc.ListDependencies(ctx, actor, scope.Scope.ID, cursor, 100)
		if err != nil {
			t.Fatalf("page %d: %v", pageNumber, err)
		}
		want := 100
		if pageNumber == 2 {
			want = 5
		}
		if len(page.Dependencies) != want || page.ScopeRevision != 0 {
			t.Fatalf("page %d len=%d revision=%d", pageNumber, len(page.Dependencies), page.ScopeRevision)
		}
		for _, dependency := range page.Dependencies {
			if _, duplicate := seen[dependency.ID.Bytes]; duplicate {
				t.Fatalf("duplicate dependency %v", dependency.ID)
			}
			seen[dependency.ID.Bytes] = struct{}{}
		}
		cursor = page.NextCursor
		if pageNumber < 2 && cursor == "" {
			t.Fatalf("page %d missing cursor", pageNumber)
		}
	}
	if len(seen) != 205 || cursor != "" {
		t.Fatalf("seen=%d final cursor=%q", len(seen), cursor)
	}

	first, err := svc.ListDependencies(ctx, actor, scope.Scope.ID, "", 100)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	if _, err := svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "cursor-mutation"}); err != nil {
		t.Fatalf("mutate between pages: %v", err)
	}
	if _, err := svc.ListDependencies(ctx, actor, scope.Scope.ID, first.NextCursor, 100); coordinationCode(err) != CoordinationRevisionConflict {
		t.Fatalf("stale cursor code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.ListDependencies(ctx, actor, scope.Scope.ID, "not-base64", 100); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("malformed cursor code=%q err=%v", coordinationCode(err), err)
	}
	decoded, err := decodeDependencyCursor(first.NextCursor)
	if err != nil || decoded == nil {
		t.Fatalf("decode first cursor: %+v err=%v", decoded, err)
	}
	foreignWorkspace, err := newPgUUID()
	if err != nil {
		t.Fatalf("foreign workspace id: %v", err)
	}
	decoded.WorkspaceID = foreignWorkspace
	foreignCursor, err := encodeDependencyCursor(*decoded)
	if err != nil {
		t.Fatalf("encode foreign cursor: %v", err)
	}
	if _, err := svc.ListDependencies(ctx, actor, scope.Scope.ID, foreignCursor, 100); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("foreign cursor code=%q err=%v", coordinationCode(err), err)
	}
}

func TestWorkCoordinationDependencyCapacityBoundary(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2, "capacity-downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 3, "capacity-upstream")
	other := createWorkCoordinationChildIssue(t, pool, fixture, 4, "capacity-other")
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "capacity-scope"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO coordination_dependency (
    id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,
    created_by_type,created_by_id
)
SELECT md5('cap-id-'||g)::uuid,$1,$2,md5('cap-down-'||g)::uuid,md5('cap-up-'||g)::uuid,'member',$3
FROM generate_series(1,999) g`, fixture.workspaceID, scope.Scope.ID, fixture.userID); err != nil {
		t.Fatalf("seed capacity: %v", err)
	}
	thousandth, err := svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "capacity-1000"})
	if err != nil || thousandth.ScopeRevision != 1 {
		t.Fatalf("thousandth=%+v err=%v", thousandth, err)
	}
	_, err = svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: downstream, UpstreamIssueID: other, IdempotencyKey: "capacity-1001"})
	if coordinationCode(err) != CoordinationCapacityExceeded {
		t.Fatalf("capacity code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 1)
	var dependencyCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_dependency WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&dependencyCount); err != nil || dependencyCount != 1000 {
		t.Fatalf("dependency count=%d err=%v", dependencyCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatalf("receipt count=%d err=%v", receiptCount, err)
	}
}

func TestWorkCoordinationDependencyDenseDAGReachabilityIsNodeBounded(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2, "dense-downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 3, "dense-upstream")
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "dense-scope"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
WITH nodes AS (
    SELECT g, CASE WHEN g=1 THEN $4::uuid ELSE md5('wcs-dense-node-'||g)::uuid END AS id
    FROM generate_series(1,45) g
)
INSERT INTO coordination_dependency (
    id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,
    created_by_type,created_by_id
)
SELECT md5('wcs-dense-edge-'||left_node.g||'-'||right_node.g)::uuid,$1,$2,left_node.id,right_node.id,'member',$3
FROM nodes left_node
JOIN nodes right_node ON left_node.g < right_node.g`, fixture.workspaceID, scope.Scope.ID, fixture.userID, upstream); err != nil {
		t.Fatalf("seed dense DAG: %v", err)
	}
	var seeded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_dependency WHERE coordination_scope_id=$1 AND resolved_at IS NULL`, scope.Scope.ID).Scan(&seeded); err != nil || seeded != 990 {
		t.Fatalf("seeded=%d err=%v", seeded, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bounded reachability: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '750ms'`); err != nil {
		t.Fatalf("set statement timeout: %v", err)
	}
	reachable, err := db.New(tx).CoordinationDependencyPathExists(ctx, db.CoordinationDependencyPathExistsParams{WorkspaceID: fixture.workspaceID, StartIssueID: upstream, TargetIssueID: downstream})
	if err != nil || reachable {
		t.Fatalf("dense reachability=%v err=%v", reachable, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback bounded reachability: %v", err)
	}

	created, err := svc.AddDependency(ctx, actor, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "dense-add"})
	if err != nil || created.Outcome != CoordinationOutcomeCreated || created.ScopeRevision != 1 {
		t.Fatalf("dense add=%+v err=%v", created, err)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_dependency WHERE coordination_scope_id=$1 AND resolved_at IS NULL`, scope.Scope.ID).Scan(&active); err != nil || active != 991 {
		t.Fatalf("active=%d err=%v", active, err)
	}
}

func TestWorkCoordinationDependencyConcurrentReverseEdges(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	left := createWorkCoordinationChildIssue(t, pool, fixture, 2, "left")
	right := createWorkCoordinationChildIssue(t, pool, fixture, 3, "right")
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "reverse-scope"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	inputs := []AddDependencyInput{
		{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: left, UpstreamIssueID: right, IdempotencyKey: "reverse-left"},
		{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: right, UpstreamIssueID: left, IdempotencyKey: "reverse-right"},
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.AddDependency(context.Background(), actor, input)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
			continue
		}
		if code := coordinationCode(err); code != CoordinationRevisionConflict && code != CoordinationCycle {
			t.Fatalf("unexpected loser error=%v", err)
		}
	}
	if success != 1 {
		t.Fatalf("successes=%d want=1", success)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_dependency WHERE coordination_scope_id=$1 AND resolved_at IS NULL`, scope.Scope.ID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active=%d err=%v", active, err)
	}
}

func TestWorkCoordinationDependencyAgentEndpointAndReplayAuthority(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	child := createWorkCoordinationChildIssue(t, pool, fixture, 2, "agent-child")
	other := createWorkCoordinationChildIssue(t, pool, fixture, 3, "agent-other")
	agentFixture := createWorkCoordinationAgentFixture(t, pool, fixture)
	svc := NewCoordinationService(db.New(pool), pool)
	member := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	scope, err := svc.EnsureScope(ctx, member, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "agent-dependency-scope"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	agent := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorAgent, ActorID: agentFixture.agentID, TaskID: agentFixture.taskID, TaskCredentialRef: agentFixture.tokenOneID}
	input := AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: fixture.issueID, UpstreamIssueID: child, IdempotencyKey: "agent-dependency-add"}
	if _, err := svc.AddDependency(ctx, agent, input); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	if _, err := svc.AddDependency(ctx, agent, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: child, UpstreamIssueID: other, IdempotencyKey: "agent-not-endpoint"}); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("non-endpoint code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.AddDependency(ctx, member, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: fixture.issueID, UpstreamIssueID: other, IdempotencyKey: "member-visible-agent-edge"}); err != nil {
		t.Fatalf("add second visible edge: %v", err)
	}
	if _, err := svc.AddDependency(ctx, member, AddDependencyInput{ScopeID: scope.Scope.ID, ExpectedRevision: 2, DownstreamIssueID: child, UpstreamIssueID: other, IdempotencyKey: "member-unrelated-agent-edge"}); err != nil {
		t.Fatalf("add unrelated edge: %v", err)
	}
	memberPage, err := svc.ListDependencies(ctx, member, scope.Scope.ID, "", 100)
	if err != nil || len(memberPage.Dependencies) != 3 {
		t.Fatalf("member page len=%d err=%v", len(memberPage.Dependencies), err)
	}
	agentPage, err := svc.ListDependencies(ctx, agent, scope.Scope.ID, "", 100)
	if err != nil || len(agentPage.Dependencies) != 2 {
		t.Fatalf("agent page len=%d err=%v", len(agentPage.Dependencies), err)
	}
	for _, dependency := range agentPage.Dependencies {
		if !uuidEqual(dependency.DownstreamIssueID, fixture.issueID) && !uuidEqual(dependency.UpstreamIssueID, fixture.issueID) {
			t.Fatalf("agent saw unrelated dependency: %+v", dependency)
		}
	}
	firstAgentPage, err := svc.ListDependencies(ctx, agent, scope.Scope.ID, "", 1)
	if err != nil || len(firstAgentPage.Dependencies) != 1 || firstAgentPage.NextCursor == "" {
		t.Fatalf("first agent page=%+v err=%v", firstAgentPage, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM task_token WHERE id=$1`, agentFixture.tokenOneID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := svc.ListDependencies(ctx, agent, scope.Scope.ID, firstAgentPage.NextCursor, 1); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("list after revoke code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.AddDependency(ctx, agent, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("replay after revoke code=%q err=%v", coordinationCode(err), err)
	}
}

func createWorkCoordinationChildIssue(t *testing.T, pool *pgxpool.Pool, fixture workCoordinationFixture, number int, title string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number,parent_issue_id)
VALUES ($1,$2,'member',$3,'none',$4,$5)
RETURNING id`, fixture.workspaceID, fmt.Sprintf("WCS %s %d", title, time.Now().UnixNano()), fixture.userID, number, fixture.issueID).Scan(&id); err != nil {
		t.Fatalf("insert child issue: %v", err)
	}
	return id
}

func assertCoordinationScopeRevision(t *testing.T, pool *pgxpool.Pool, scopeID pgtype.UUID, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), `SELECT revision FROM coordination_scope WHERE id=$1`, scopeID).Scan(&got); err != nil || got != want {
		t.Fatalf("scope revision=%d want=%d err=%v", got, want, err)
	}
}
