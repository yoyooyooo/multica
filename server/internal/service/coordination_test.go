package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationGoldens(t *testing.T) {
	if CoordinationAdvisoryNamespace != int32(0x57435331) {
		t.Fatalf("namespace=%d want=%d", CoordinationAdvisoryNamespace, int32(0x57435331))
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "..", "tickets", "work-coordination-store", "README.md"))
	if err != nil {
		t.Fatalf("read lock SSoT: %v", err)
	}
	for _, golden := range []string{"1464030001", "927402239", "-1961171921", "SHA-256(canonical UUID 16 raw bytes)"} {
		if !strings.Contains(string(readme), golden) {
			t.Fatalf("lock SSoT is missing golden %q", golden)
		}
	}
	zero := util.MustParseUUID("00000000-0000-0000-0000-000000000000")
	three := util.MustParseUUID("00000000-0000-0000-0000-000000000003")
	if got, err := CoordinationWorkspaceAdvisoryKey(zero); err != nil || got != 927402239 {
		t.Fatalf("zero workspace key = %d, %v", got, err)
	}
	if got, err := CoordinationWorkspaceAdvisoryKey(three); err != nil || got != -1961171921 {
		t.Fatalf("three workspace key = %d, %v", got, err)
	}

	actor := CoordinationActor{
		WorkspaceID: zero,
		ActorType:   CoordinationActorMember,
		ActorID:     util.MustParseUUID("00000000-0000-0000-0000-000000000002"),
	}
	root := util.MustParseUUID("00000000-0000-0000-0000-000000000001")
	canonical, err := EnsureScopeCanonicalJSON(actor, root, "matt-loop")
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	wantCanonical := `{"actor":{"id":"00000000-0000-0000-0000-000000000002","task_id":null,"type":"member"},"hash_version":1,"operation":"ensure_scope","request":{"root_issue_id":"00000000-0000-0000-0000-000000000001","workflow_profile_key":"matt-loop"},"workspace_id":"00000000-0000-0000-0000-000000000000"}`
	if string(canonical) != wantCanonical {
		t.Fatalf("canonical mismatch\n got: %s\nwant: %s", canonical, wantCanonical)
	}
	hash, err := EnsureScopeRequestHash(actor, root, "matt-loop")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if got := hex.EncodeToString(hash); got != "d98699aa4465b9a91f590cf80c4f0151856f4f8b3d0eb0db3f82478da603f81e" {
		t.Fatalf("hash = %s", got)
	}

	upperRoot := util.MustParseUUID("00000000-0000-0000-0000-000000000001")
	upperHash, err := EnsureScopeRequestHash(actor, upperRoot, "matt-loop")
	if err != nil {
		t.Fatalf("upper hash: %v", err)
	}
	if hex.EncodeToString(upperHash) != hex.EncodeToString(hash) {
		t.Fatal("UUID textual case must normalize without changing digest")
	}
	otherHash, err := EnsureScopeRequestHash(actor, root, "matt.loop")
	if err != nil {
		t.Fatalf("other hash: %v", err)
	}
	if hex.EncodeToString(otherHash) == hex.EncodeToString(hash) {
		t.Fatal("profile changes must change digest")
	}
	agentActor := CoordinationActor{WorkspaceID: zero, ActorType: CoordinationActorAgent, ActorID: three, TaskID: root, TaskCredentialRef: util.MustParseUUID("00000000-0000-0000-0000-000000000004")}
	agentHashOne, err := EnsureScopeRequestHash(agentActor, root, "matt-loop")
	if err != nil {
		t.Fatalf("agent hash one: %v", err)
	}
	agentActor.TaskCredentialRef = util.MustParseUUID("00000000-0000-0000-0000-000000000005")
	agentHashTwo, err := EnsureScopeRequestHash(agentActor, root, "matt-loop")
	if err != nil {
		t.Fatalf("agent hash two: %v", err)
	}
	if !strings.EqualFold(hex.EncodeToString(agentHashOne), hex.EncodeToString(agentHashTwo)) {
		t.Fatal("exact credential ref must not enter the canonical request hash")
	}

	scopeID := util.MustParseUUID("00000000-0000-0000-0000-000000000006")
	downstreamID := util.MustParseUUID("00000000-0000-0000-0000-000000000007")
	upstreamID := util.MustParseUUID("00000000-0000-0000-0000-000000000008")
	dependencyID := util.MustParseUUID("00000000-0000-0000-0000-000000000009")
	addInput := AddDependencyInput{ScopeID: scopeID, ExpectedRevision: 0, DownstreamIssueID: downstreamID, UpstreamIssueID: upstreamID, IdempotencyKey: "golden-add"}
	addCanonical, err := AddDependencyCanonicalJSON(actor, addInput)
	if err != nil {
		t.Fatalf("add canonical: %v", err)
	}
	wantAddCanonical := `{"actor":{"id":"00000000-0000-0000-0000-000000000002","task_id":null,"type":"member"},"hash_version":1,"operation":"add_dependency","request":{"downstream_issue_id":"00000000-0000-0000-0000-000000000007","expected_revision":"0","scope_id":"00000000-0000-0000-0000-000000000006","upstream_issue_id":"00000000-0000-0000-0000-000000000008"},"workspace_id":"00000000-0000-0000-0000-000000000000"}`
	if string(addCanonical) != wantAddCanonical {
		t.Fatalf("add canonical mismatch\n got: %s\nwant: %s", addCanonical, wantAddCanonical)
	}
	addHash, err := AddDependencyRequestHash(actor, addInput)
	if err != nil || hex.EncodeToString(addHash) != "b5c87dec5b2fc12d37fbd16cbc30913548b3b7cd8271d14e191a37a4e0335f19" {
		t.Fatalf("add hash=%x err=%v", addHash, err)
	}
	resolveInput := ResolveDependencyInput{ScopeID: scopeID, DependencyID: dependencyID, ExpectedRevision: 1, IdempotencyKey: "golden-resolve"}
	resolveCanonical, err := ResolveDependencyCanonicalJSON(actor, resolveInput)
	if err != nil {
		t.Fatalf("resolve canonical: %v", err)
	}
	wantResolveCanonical := `{"actor":{"id":"00000000-0000-0000-0000-000000000002","task_id":null,"type":"member"},"hash_version":1,"operation":"resolve_dependency","request":{"dependency_id":"00000000-0000-0000-0000-000000000009","expected_revision":"1","scope_id":"00000000-0000-0000-0000-000000000006"},"workspace_id":"00000000-0000-0000-0000-000000000000"}`
	if string(resolveCanonical) != wantResolveCanonical {
		t.Fatalf("resolve canonical mismatch\n got: %s\nwant: %s", resolveCanonical, wantResolveCanonical)
	}
	resolveHash, err := ResolveDependencyRequestHash(actor, resolveInput)
	if err != nil || hex.EncodeToString(resolveHash) != "1c3e8f4919efb34a68c88190e442ad5ef625bcf005335a0cf8658ad13ec2af8f" {
		t.Fatalf("resolve hash=%x err=%v", resolveHash, err)
	}
}

func TestWorkCoordinationServiceEnsureScopeDB(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	q := db.New(pool)
	fixture := createWorkCoordinationFixture(t, pool)

	svc := NewCoordinationService(q, pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	input := EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "ensure-1"}
	created, err := svc.EnsureScope(ctx, actor, input)
	if err != nil {
		t.Fatalf("ensure create: %v", err)
	}
	if created.Outcome != CoordinationOutcomeCreated || created.Scope.Revision != 0 || created.Receipt.ReceiptOrdinal != 1 {
		t.Fatalf("unexpected create result: %+v", created)
	}
	if _, err := pool.Exec(ctx, `UPDATE coordination_scope SET updated_at = updated_at + interval '1 hour' WHERE id = $1`, created.Scope.ID); err != nil {
		t.Fatalf("advance current scope timestamp: %v", err)
	}
	replay, err := svc.EnsureScope(ctx, actor, input)
	if err != nil {
		t.Fatalf("ensure replay: %v", err)
	}
	if replay.Outcome != CoordinationOutcomeReplay || replay.Receipt.ReceiptOrdinal != created.Receipt.ReceiptOrdinal || !uuidEqual(replay.Scope.ID, created.Scope.ID) {
		t.Fatalf("unexpected replay result: %+v", replay)
	}
	if !replay.Scope.UpdatedAt.Equal(created.Scope.UpdatedAt) {
		t.Fatalf("replay must restore saved scope representation: got %s want %s", replay.Scope.UpdatedAt, created.Scope.UpdatedAt)
	}
	noop, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "ensure-2"})
	if err != nil {
		t.Fatalf("ensure noop: %v", err)
	}
	if noop.Outcome != CoordinationOutcomeNoop || noop.Receipt.ReceiptOrdinal != 2 || !uuidEqual(noop.Scope.ID, created.Scope.ID) {
		t.Fatalf("unexpected noop result: %+v", noop)
	}
	_, err = svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt.loop", IdempotencyKey: "ensure-1"})
	var coordErr *CoordinationError
	if !errors.As(err, &coordErr) || coordErr.Code != CoordinationIdempotencyConflict {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	for column, value := range map[string]string{"operation": "unknown_operation", "resource_type": "unknown_resource"} {
		query := fmt.Sprintf("UPDATE coordination_receipt SET %s=$1 WHERE workspace_id=$2 AND idempotency_key='ensure-1'", column)
		if _, err := pool.Exec(ctx, query, value, fixture.workspaceID); err != nil {
			t.Fatalf("poison receipt %s: %v", column, err)
		}
		if _, err := svc.EnsureScope(ctx, actor, input); coordinationCode(err) != CoordinationIdempotencyConflict {
			t.Fatalf("unknown %s code=%q err=%v", column, coordinationCode(err), err)
		}
		restore := CoordinationOperationEnsureScope
		if column == "resource_type" {
			restore = CoordinationResourceScope
		}
		if _, err := pool.Exec(ctx, query, restore, fixture.workspaceID); err != nil {
			t.Fatalf("restore receipt %s: %v", column, err)
		}
	}

	got, err := svc.GetScope(ctx, actor, created.Scope.ID)
	if err != nil || !uuidEqual(got.ID, created.Scope.ID) {
		t.Fatalf("get scope = %+v, %v", got, err)
	}
	got, err = svc.GetScopeByRoot(ctx, actor, fixture.issueID, "matt-loop")
	if err != nil || !uuidEqual(got.ID, created.Scope.ID) {
		t.Fatalf("get by root = %+v, %v", got, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE coordination_scope SET next_receipt_ordinal=9223372036854775807 WHERE id=$1`, created.Scope.ID); err != nil {
		t.Fatalf("set receipt capacity: %v", err)
	}
	if _, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "at-capacity"}); coordinationCode(err) != CoordinationCapacityExceeded {
		t.Fatalf("capacity code=%q err=%v", coordinationCode(err), err)
	}
	var receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, created.Scope.ID).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatalf("capacity failure receipt count=%d err=%v", receiptCount, err)
	}
}

type workCoordinationFixture struct {
	userID      pgtype.UUID
	workspaceID pgtype.UUID
	issueID     pgtype.UUID
}

func openWorkCoordinationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		if os.Getenv("WORK_COORDINATION_DB_REQUIRED") == "1" {
			t.Fatalf("connect database: %v", err)
		}
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		if os.Getenv("WORK_COORDINATION_DB_REQUIRED") == "1" {
			t.Fatalf("ping database: %v", err)
		}
		t.Skipf("database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func createWorkCoordinationFixture(t *testing.T, pool *pgxpool.Pool) workCoordinationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var f workCoordinationFixture
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "WCS Test", "wcs-"+suffix+"@multica.ai").Scan(&f.userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`, "WCS Test", "wcs-"+suffix).Scan(&f.workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, priority, number)
		VALUES ($1, 'WCS root', 'member', $2, 'none', 1)
		RETURNING id`, f.workspaceID, f.userID).Scan(&f.issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordination_record_issue_ref WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordination_record WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordination_dependency WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, f.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, f.userID)
	})
	return f
}
