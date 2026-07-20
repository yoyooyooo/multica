package handler

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkCoordinationPostCommitEffectsRunAfterSessionLockIsTerminal(t *testing.T) {
	ctx := context.Background()
	workspaceID := mustHandlerUUID(t, testWorkspaceID)
	userID := mustHandlerUUID(t, testUserID)
	var deleteIssueID, reentrantRootID pgtype.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS effect delete','member',$2,'none',990201) RETURNING id`, workspaceID, userID).Scan(&deleteIssueID); err != nil {
		t.Fatalf("insert delete issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS effect reentrant','member',$2,'none',990202) RETURNING id`, workspaceID, userID).Scan(&reentrantRootID); err != nil {
		t.Fatalf("insert reentrant root: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO attachment (id,workspace_id,issue_id,uploader_type,uploader_id,filename,url,content_type,size_bytes) VALUES (gen_random_uuid(),$1,$2,'member',$3,'proof.txt','https://storage.test/wcs/proof.txt','text/plain',5)`, workspaceID, deleteIssueID, userID); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_receipt WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM coordination_scope WHERE workspace_id=$1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=ANY($1::uuid[])`, []pgtype.UUID{deleteIssueID, reentrantRootID})
	})

	key, err := service.CoordinationWorkspaceAdvisoryKey(workspaceID)
	if err != nil {
		t.Fatalf("lock key: %v", err)
	}
	storageLockWasFree := false
	fakeStorage := &coordinationEffectStorage{onDeleteKeys: func(keys []string) {
		if len(keys) != 1 || keys[0] != "wcs/proof.txt" {
			t.Errorf("storage keys=%v", keys)
			return
		}
		conn, err := testPool.Acquire(ctx)
		if err != nil {
			t.Errorf("acquire lock probe connection: %v", err)
			return
		}
		defer conn.Release()
		var available bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4,$2::int4)`, service.CoordinationAdvisoryNamespace, key).Scan(&available); err != nil {
			t.Errorf("try lock in storage effect: %v", err)
			return
		}
		storageLockWasFree = available
		if available {
			_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1::int4,$2::int4)`, service.CoordinationAdvisoryNamespace, key)
		}
	}}
	bus := events.New()
	reentrantCalled := false
	bus.Subscribe(protocol.EventIssueDeleted, func(event events.Event) {
		reentrantCalled = true
		time.Sleep(10 * time.Millisecond)
		actor := service.CoordinationActor{WorkspaceID: workspaceID, ActorType: service.CoordinationActorMember, ActorID: userID}
		if _, err := testHandler.CoordinationService.EnsureScope(context.Background(), actor, service.EnsureScopeInput{RootIssueID: reentrantRootID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "post-commit-reentrant"}); err != nil {
			t.Errorf("reentrant ensure after issue event: %v", err)
		}
	})

	h := *testHandler
	h.Bus = bus
	h.Storage = fakeStorage
	actor := service.CoordinationActor{WorkspaceID: workspaceID, ActorType: service.CoordinationActorMember, ActorID: userID}
	deleted, effects, err := h.performIssueDeletion(ctx, actor, workspaceID, []pgtype.UUID{deleteIssueID}, service.IssueDeletionSingle)
	if err != nil || deleted != 1 {
		t.Fatalf("perform deletion count=%d err=%v", deleted, err)
	}
	if len(effects) != 1 {
		t.Fatalf("effects=%+v", effects)
	}
	h.applyIssueDeletionEffects(ctx, actor, effects)
	if !storageLockWasFree || !reentrantCalled {
		t.Fatalf("post-commit effects storage_lock_free=%v reentrant_called=%v", storageLockWasFree, reentrantCalled)
	}
}

type coordinationEffectStorage struct {
	onDeleteKeys func([]string)
}

func (s *coordinationEffectStorage) Upload(context.Context, string, []byte, string, string) (string, error) {
	return "", nil
}
func (s *coordinationEffectStorage) Delete(context.Context, string) {}
func (s *coordinationEffectStorage) DeleteKeys(_ context.Context, keys []string) {
	if s.onDeleteKeys != nil {
		s.onDeleteKeys(keys)
	}
}
func (s *coordinationEffectStorage) KeyFromURL(rawURL string) string {
	return strings.TrimPrefix(rawURL, "https://storage.test/")
}
func (s *coordinationEffectStorage) CdnDomain() string { return "storage.test" }
func (s *coordinationEffectStorage) GetReader(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func mustHandlerUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
