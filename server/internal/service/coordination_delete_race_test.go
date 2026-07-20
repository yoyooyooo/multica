package service

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationEnsureRacesWithIssueAndBatchDeletion(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		mode := IssueDeletionSingle
		if batch {
			name = "batch"
			mode = IssueDeletionBatch
		}
		t.Run(name, func(t *testing.T) {
			pool := openWorkCoordinationPool(t)
			ctx := context.Background()
			fixture := createWorkCoordinationFixture(t, pool)
			ids := []pgtype.UUID{fixture.issueID}
			if batch {
				var second pgtype.UUID
				if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,priority,number) VALUES ($1,'WCS race second','member',$2,'none',2) RETURNING id`, fixture.workspaceID, fixture.userID).Scan(&second); err != nil {
					t.Fatalf("insert second issue: %v", err)
				}
				ids = append(ids, second)
			}
			svc := NewCoordinationService(db.New(pool), pool)
			actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
			start := make(chan struct{})
			var ensureErr, deleteErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, ensureErr = svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "race-delete"})
			}()
			go func() {
				defer wg.Done()
				<-start
				handle, err := svc.AcquireIssueDeletion(ctx, actor, fixture.workspaceID, ids, mode)
				if err != nil {
					deleteErr = err
					return
				}
				for _, targetID := range handle.TargetIssueIDs() {
					if _, err = handle.Delete(ctx, targetID); err != nil {
						break
					}
				}
				if err == nil {
					err = handle.Finish(true)
				} else {
					_ = handle.Finish(false)
				}
				deleteErr = err
			}()
			close(start)
			wg.Wait()

			switch {
			case ensureErr == nil && coordinationCode(deleteErr) == CoordinationDeleteBlocked:
				var issueCount, orphanCount int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=ANY($1::uuid[])`, ids).Scan(&issueCount); err != nil || issueCount != len(ids) {
					t.Fatalf("ensure-won issue count=%d err=%v", issueCount, err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_scope s WHERE s.workspace_id=$1 AND NOT EXISTS(SELECT 1 FROM issue i WHERE i.workspace_id=s.workspace_id AND i.id=s.root_issue_id)`, fixture.workspaceID).Scan(&orphanCount); err != nil || orphanCount != 0 {
					t.Fatalf("ensure-won orphan count=%d err=%v", orphanCount, err)
				}
			case deleteErr == nil && coordinationCode(ensureErr) == CoordinationNotFound:
				var issueCount, scopeCount int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=ANY($1::uuid[])`, ids).Scan(&issueCount); err != nil || issueCount != 0 {
					t.Fatalf("delete-won issue count=%d err=%v", issueCount, err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_scope WHERE workspace_id=$1`, fixture.workspaceID).Scan(&scopeCount); err != nil || scopeCount != 0 {
					t.Fatalf("delete-won scope count=%d err=%v", scopeCount, err)
				}
			default:
				t.Fatalf("unexpected race outcome ensure=%v delete=%v", ensureErr, deleteErr)
			}
		})
	}
}

func TestWorkCoordinationEnsureRacesWithWorkspaceDeletion(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	start := make(chan struct{})
	var ensureErr, deleteErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, ensureErr = svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "matt-loop", IdempotencyKey: "race-workspace-delete"})
	}()
	go func() {
		defer wg.Done()
		<-start
		handle, err := svc.AcquireWorkspaceDeletion(ctx, actor, fixture.workspaceID)
		if err != nil {
			deleteErr = err
			return
		}
		if _, err = handle.Delete(ctx); err == nil {
			err = handle.Finish(true)
		} else {
			_ = handle.Finish(false)
		}
		deleteErr = err
	}()
	close(start)
	wg.Wait()

	var workspaceExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace WHERE id=$1)`, fixture.workspaceID).Scan(&workspaceExists); err != nil {
		t.Fatalf("check workspace: %v", err)
	}
	switch {
	case ensureErr == nil && coordinationCode(deleteErr) == CoordinationDeleteBlocked && workspaceExists:
		var orphanCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_scope s WHERE s.workspace_id=$1 AND NOT EXISTS(SELECT 1 FROM issue i WHERE i.workspace_id=s.workspace_id AND i.id=s.root_issue_id)`, fixture.workspaceID).Scan(&orphanCount); err != nil || orphanCount != 0 {
			t.Fatalf("ensure-won orphan count=%d err=%v", orphanCount, err)
		}
	case deleteErr == nil && coordinationCode(ensureErr) == CoordinationForbidden && !workspaceExists:
		var scopeCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_scope WHERE workspace_id=$1`, fixture.workspaceID).Scan(&scopeCount); err != nil || scopeCount != 0 {
			t.Fatalf("delete-won scope count=%d err=%v", scopeCount, err)
		}
	default:
		t.Fatalf("unexpected workspace race outcome exists=%v ensure=%v delete=%v", workspaceExists, ensureErr, deleteErr)
	}
}
