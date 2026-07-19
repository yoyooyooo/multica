package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationIssueDeletionFatalPhaseMatrix(t *testing.T) {
	phases := []IssueDeletionPhase{
		IssueDeletionPhaseTaskCancel,
		IssueDeletionPhaseTaskTokenCleanup,
		IssueDeletionPhaseAutopilotFail,
		IssueDeletionPhaseAttachmentCensus,
		IssueDeletionPhaseEntityDelete,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			handle, tx, terminal := newDeletionMatrixHandle(IssueDeletionBatch)
			handle.phaseRunner = func(context.Context, db.Issue) (IssueDeletionEffects, IssueDeletionPhase, error) {
				return IssueDeletionEffects{IssueID: matrixIssueID()}, phase, errors.New("injected phase failure")
			}

			result, err := handle.Delete(context.Background(), matrixIssueID())
			var fatal *IssueDeletionFatalError
			if !errors.As(err, &fatal) || fatal.Phase != phase || fatal.Class != IssueDeletionFailureUnknown {
				t.Fatalf("fatal=%+v err=%v", fatal, err)
			}
			if result.Outcome != "" || result.Effects.IssueID.Valid {
				t.Fatalf("fatal phase returned effects: %+v", result)
			}
			if err := handle.Finish(false); err != nil {
				t.Fatalf("finish rollback: %v", err)
			}
			if tx.rollbacks != 1 || tx.commits != 0 || terminal.releases != 1 || terminal.discards != 0 || handle.lifecycle.state != deleteStateReleased {
				t.Fatalf("terminal tx=%+v terminal=%+v state=%s", tx, terminal, handle.lifecycle.state)
			}
		})
	}
}

func TestWorkCoordinationIssueDeletionFailureClassificationMatrix(t *testing.T) {
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
			class := classifyIssueDeletionFailure(tc.err, false)
			if class != tc.want {
				t.Fatalf("class=%q want=%q", class, tc.want)
			}
			var fatal *IssueDeletionFatalError
			if err := newIssueDeletionFatal(IssueDeletionPhaseTaskCancel, class, tc.err); !errors.As(err, &fatal) || fatal.Class != tc.want || fatal.Phase != IssueDeletionPhaseTaskCancel {
				t.Fatalf("typed fatal=%+v err=%v", fatal, err)
			}
		})
	}
}

func TestWorkCoordinationBatchRecoverabilityIsEntityDelete23503Only(t *testing.T) {
	phases := []IssueDeletionPhase{
		IssueDeletionPhaseTaskCancel,
		IssueDeletionPhaseTaskTokenCleanup,
		IssueDeletionPhaseAutopilotFail,
		IssueDeletionPhaseAttachmentCensus,
		IssueDeletionPhaseEntityDelete,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			handle, tx, terminal := newDeletionMatrixHandle(IssueDeletionBatch)
			handle.phaseRunner = func(context.Context, db.Issue) (IssueDeletionEffects, IssueDeletionPhase, error) {
				return IssueDeletionEffects{}, phase, &pgconn.PgError{Code: "23503"}
			}
			result, err := handle.Delete(context.Background(), matrixIssueID())
			if phase == IssueDeletionPhaseEntityDelete {
				if err != nil || result.Outcome != IssueDeletionSkippedRecoverable || result.SafeCode != "target_restricted" || result.Effects.IssueID.Valid {
					t.Fatalf("recoverable result=%+v err=%v", result, err)
				}
			} else {
				var fatal *IssueDeletionFatalError
				if !errors.As(err, &fatal) || fatal.Phase != phase || fatal.Class != IssueDeletionFailureStatement || result.Outcome != "" {
					t.Fatalf("non-entity 23503 fatal=%+v result=%+v err=%v", fatal, result, err)
				}
			}
			if err := handle.Finish(false); err != nil {
				t.Fatalf("finish rollback: %v", err)
			}
			if tx.rollbacks != 1 || terminal.releases != 1 || handle.lifecycle.state != deleteStateReleased {
				t.Fatalf("terminal tx=%+v terminal=%+v state=%s", tx, terminal, handle.lifecycle.state)
			}
		})
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

func newDeletionMatrixHandle(mode IssueDeletionMode) (*IssueDeletionHandle, *deletionCountingTx, *deletionTerminalCounts) {
	id := matrixIssueID()
	tx := &deletionCountingTx{}
	terminal := &deletionTerminalCounts{}
	handle := &IssueDeletionHandle{
		lifecycle: coordinationDeleteLifecycle{
			tx:       tx,
			state:    deleteStateReady,
			lockHeld: true,
			sessionUnlock: func(context.Context, int32) (bool, error) {
				terminal.unlocks++
				return true, nil
			},
			releaseConn: func() { terminal.releases++ },
			discardConn: func() { terminal.discards++ },
		},
		mode:      mode,
		issues:    map[[16]byte]db.Issue{id.Bytes: {ID: id, WorkspaceID: pgtype.UUID{Bytes: [16]byte{15: 2}, Valid: true}}},
		targetIDs: []pgtype.UUID{id},
		attempted: make(map[[16]byte]struct{}),
		savepointExec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}
	return handle, tx, terminal
}

func matrixIssueID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{15: 1}, Valid: true}
}
