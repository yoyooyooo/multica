package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationDeletionEffectsDeduplicateAgentRefs(t *testing.T) {
	agentA := util.MustParseUUID("00000000-0000-0000-0000-00000000000a")
	agentB := util.MustParseUUID("00000000-0000-0000-0000-00000000000b")
	got := affectedAgentIDs([]db.AgentTaskQueue{{AgentID: agentB}, {AgentID: agentA}, {AgentID: agentB}})
	if len(got) != 2 || got[0].Bytes != agentA.Bytes || got[1].Bytes != agentB.Bytes {
		t.Fatalf("affected agents=%v", got)
	}
}

func TestWorkCoordinationFinishTerminalizesPanicsAndUnknownState(t *testing.T) {
	cases := []struct {
		name     string
		commit   bool
		commitFn func(context.Context) error
		rollFn   func(context.Context) error
	}{
		{
			name:   "commit panic",
			commit: true,
			commitFn: func(context.Context) error {
				panic("injected commit panic")
			},
		},
		{
			name:     "commit outcome unknown",
			commit:   true,
			commitFn: func(context.Context) error { return context.DeadlineExceeded },
		},
		{
			name:   "rollback error",
			commit: false,
			rollFn: func(context.Context) error { return errors.New("injected rollback failure") },
		},
		{
			name:   "rollback panic",
			commit: false,
			rollFn: func(context.Context) error {
				panic("injected rollback panic")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := coordinationDeleteLifecycle{
				tx:    &finishTestTx{commitFn: tc.commitFn, rollbackFn: tc.rollFn},
				state: deleteStateReady,
			}
			if err := lifecycle.finish(tc.commit); coordinationCode(err) != CoordinationInternal {
				t.Fatalf("finish error=%v", err)
			}
			if lifecycle.state != deleteStateDiscarded {
				t.Fatalf("state=%s", lifecycle.state)
			}
		})
	}
}

type finishTestTx struct {
	commitFn   func(context.Context) error
	rollbackFn func(context.Context) error
}

func (tx *finishTestTx) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("unused") }
func (tx *finishTestTx) Commit(ctx context.Context) error {
	if tx.commitFn != nil {
		return tx.commitFn(ctx)
	}
	return nil
}
func (tx *finishTestTx) Rollback(ctx context.Context) error {
	if tx.rollbackFn != nil {
		return tx.rollbackFn(ctx)
	}
	return nil
}
func (tx *finishTestTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unused")
}
func (tx *finishTestTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (tx *finishTestTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (tx *finishTestTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("unused")
}
func (tx *finishTestTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unused")
}
func (tx *finishTestTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (tx *finishTestTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (tx *finishTestTx) Conn() *pgx.Conn                                  { return nil }
