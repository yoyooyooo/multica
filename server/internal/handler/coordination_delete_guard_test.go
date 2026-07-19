package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestWorkCoordinationIssueDeletionCallerGuardPanicMatrix(t *testing.T) {
	for _, stage := range []string{"before delete", "during delete", "after delete"} {
		t.Run(stage, func(t *testing.T) {
			handle := newGuardIssueHandle(1)
			switch stage {
			case "before delete":
				handle.targetPanic = true
			case "during delete":
				handle.deletePanicAt = 1
			}
			var after func()
			if stage == "after delete" {
				after = func() { panic("injected post-delete panic") }
			}
			assertPanics(t, func() {
				_, _, _ = runIssueDeletionHandle(context.Background(), handle, after)
			})
			assertFinishCalls(t, handle.finishCalls, false)
			if !handle.terminal || handle.effectsApplied != 0 {
				t.Fatalf("terminal=%v effects=%d", handle.terminal, handle.effectsApplied)
			}
		})
	}
}

func TestWorkCoordinationIssueDeletionCallerDisarmsBeforeExplicitFinish(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deleteErr   error
		finishError error
		finishPanic bool
		wantCommit  bool
	}{
		{name: "Finish true error", finishError: errors.New("commit unknown"), wantCommit: true},
		{name: "Finish true panic", finishPanic: true, wantCommit: true},
		{name: "Finish false error", deleteErr: errors.New("fatal delete"), finishError: errors.New("rollback failed")},
		{name: "Finish false panic", deleteErr: errors.New("fatal delete"), finishPanic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handle := newGuardIssueHandle(1)
			handle.deleteErrAt = 1
			handle.deleteErr = tc.deleteErr
			handle.finishErr = tc.finishError
			handle.finishPanic = tc.finishPanic
			invoke := func() {
				deleted, effects, _ := runIssueDeletionHandle(context.Background(), handle, nil)
				if deleted != 0 || len(effects) != 0 {
					t.Fatalf("failed finish exposed committed result: deleted=%d effects=%d", deleted, len(effects))
				}
			}
			if tc.finishPanic {
				assertPanics(t, invoke)
			} else {
				invoke()
			}
			assertFinishCalls(t, handle.finishCalls, tc.wantCommit)
			if !handle.terminal || handle.effectsApplied != 0 {
				t.Fatalf("terminal=%v effects=%d", handle.terminal, handle.effectsApplied)
			}
		})
	}
}

func TestWorkCoordinationIssueDeletionEffectsPanicDoesNotRefinish(t *testing.T) {
	handle := newGuardIssueHandle(1)
	deleted, effects, err := runIssueDeletionHandle(context.Background(), handle, nil)
	if err != nil || deleted != 1 || len(effects) != 1 {
		t.Fatalf("deleted=%d effects=%d err=%v", deleted, len(effects), err)
	}
	assertFinishCalls(t, handle.finishCalls, true)
	assertPanics(t, func() {
		handle.effectsApplied++
		panic("injected effects panic")
	})
	assertFinishCalls(t, handle.finishCalls, true)
}

func TestWorkCoordinationBatchFatalAbortsWholeCallerResult(t *testing.T) {
	handle := newGuardIssueHandle(2)
	handle.deleteErrAt = 2
	handle.deleteErr = errors.New("second target fatal")
	deleted, effects, err := runIssueDeletionHandle(context.Background(), handle, nil)
	if err == nil || deleted != 0 || len(effects) != 0 || handle.deleteCalls != 2 {
		t.Fatalf("deleted=%d effects=%d delete_calls=%d err=%v", deleted, len(effects), handle.deleteCalls, err)
	}
	assertFinishCalls(t, handle.finishCalls, false)
	if !handle.terminal || handle.effectsApplied != 0 {
		t.Fatalf("terminal=%v effects=%d", handle.terminal, handle.effectsApplied)
	}
}

func TestWorkCoordinationWorkspaceDeletionCallerGuardAtMostOnce(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deletePanic bool
		deleteErr   error
		finishPanic bool
		finishErr   error
		wantCommit  bool
	}{
		{name: "delete panic rolls back", deletePanic: true},
		{name: "rollback error is not retried", deleteErr: errors.New("delete failed"), finishErr: errors.New("rollback failed")},
		{name: "rollback panic is not retried", deleteErr: errors.New("delete failed"), finishPanic: true},
		{name: "commit error is not rolled back twice", finishErr: errors.New("commit unknown"), wantCommit: true},
		{name: "commit panic is not retried", finishPanic: true, wantCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handle := &guardWorkspaceHandle{deletePanic: tc.deletePanic, deleteErr: tc.deleteErr, finishPanic: tc.finishPanic, finishErr: tc.finishErr}
			h := &Handler{acquireWorkspaceDelete: func(context.Context, service.CoordinationActor, pgtype.UUID) (workspaceDeletionHandle, error) {
				return handle, nil
			}}
			invoke := func() {
				_, _ = h.performWorkspaceDeletion(context.Background(), service.CoordinationActor{}, pgtype.UUID{})
			}
			if tc.deletePanic || tc.finishPanic {
				assertPanics(t, invoke)
			} else {
				invoke()
			}
			assertFinishCalls(t, handle.finishCalls, tc.wantCommit)
			if !handle.terminal || handle.effectsApplied != 0 {
				t.Fatalf("terminal=%v effects=%d", handle.terminal, handle.effectsApplied)
			}
		})
	}
}

type guardIssueHandle struct {
	targets        []pgtype.UUID
	targetPanic    bool
	deleteCalls    int
	deletePanicAt  int
	deleteErrAt    int
	deleteErr      error
	finishCalls    []bool
	finishErr      error
	finishPanic    bool
	terminal       bool
	effectsApplied int
}

func newGuardIssueHandle(count int) *guardIssueHandle {
	targets := make([]pgtype.UUID, count)
	for i := range targets {
		targets[i] = pgtype.UUID{Bytes: [16]byte{15: byte(i + 1)}, Valid: true}
	}
	return &guardIssueHandle{targets: targets}
}

func (h *guardIssueHandle) TargetIssueIDs() []pgtype.UUID {
	if h.targetPanic {
		panic("injected pre-delete panic")
	}
	return append([]pgtype.UUID(nil), h.targets...)
}

func (h *guardIssueHandle) Delete(_ context.Context, id pgtype.UUID) (service.IssueDeletionResult, error) {
	h.deleteCalls++
	if h.deletePanicAt == h.deleteCalls {
		panic("injected delete panic")
	}
	if h.deleteErrAt == h.deleteCalls && h.deleteErr != nil {
		return service.IssueDeletionResult{}, h.deleteErr
	}
	return service.IssueDeletionResult{
		Outcome: service.IssueDeletionDeleted,
		Effects: service.IssueDeletionEffects{IssueID: id},
	}, nil
}

func (h *guardIssueHandle) Finish(commit bool) error {
	h.finishCalls = append(h.finishCalls, commit)
	h.terminal = true
	if h.finishPanic {
		panic("injected finish panic")
	}
	return h.finishErr
}

type guardWorkspaceHandle struct {
	deletePanic    bool
	deleteErr      error
	finishPanic    bool
	finishErr      error
	finishCalls    []bool
	terminal       bool
	effectsApplied int
}

func (h *guardWorkspaceHandle) Delete(context.Context) (service.WorkspaceDeletionEffects, error) {
	if h.deletePanic {
		panic("injected workspace delete panic")
	}
	return service.WorkspaceDeletionEffects{}, h.deleteErr
}

func (h *guardWorkspaceHandle) Finish(commit bool) error {
	h.finishCalls = append(h.finishCalls, commit)
	h.terminal = true
	if h.finishPanic {
		panic("injected workspace finish panic")
	}
	return h.finishErr
}

func assertFinishCalls(t *testing.T, got []bool, want bool) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Finish calls=%v want=[%v]", got, want)
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}
