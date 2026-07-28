//go:build unix

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// piCancelFakeScript returns a POSIX-sh script that impersonates a long-running
// `pi`: it spawns a background grandchild, records both its own
// (process-group-leader) pid and the grandchild pid in the file named by
// PI_PID_FILE, then streams JSON events in a tight loop forever. This is the
// shape that orphans and spins on EPIPE when the daemon closes stdout while
// the process is still alive. When ignoreTerm is true the whole group ignores
// SIGTERM, forcing the SIGKILL escalation path.
func piCancelFakeScript(ignoreTerm bool) string {
	trap := "trap 'exit 0' TERM\n"
	if ignoreTerm {
		trap = "trap '' TERM\n"
	}
	return "#!/bin/sh\n" + trap +
		`# Background grandchild so the test can assert the *whole* group is
# terminated on cancellation, not just the direct child.
( sleep 300 ) &
child=$!
if [ -n "$PI_PID_FILE" ]; then
  printf '%s %s\n' "$$" "$child" > "$PI_PID_FILE"
fi
printf '{"type":"agent_start"}\n'
while true; do
  printf '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"tick"}}\n'
  sleep 0.1
done
`
}

// TestPiCancellationTerminatesProcessGroupGraceful verifies that
// cancelling a run terminates a SIGTERM-respecting pi and its whole
// process group, returns an "aborted" result without hanging, and leaves no
// orphaned descendant.
func TestPiCancellationTerminatesProcessGroupGraceful(t *testing.T) {
	runPiCancellationTest(t, piCancelFakeScript(false))
}

// TestPiCancellationEscalatesToSIGKILL verifies that when pi (and its children)
// ignore SIGTERM, cancellation must escalate to a group SIGKILL, still return
// promptly, and still reap the whole group — without deadlocking on the stdout
// scanner or closing the pipe under a live writer.
func TestPiCancellationEscalatesToSIGKILL(t *testing.T) {
	piTerminateGraceNanos.Store(int64(300 * time.Millisecond))
	t.Cleanup(func() { piTerminateGraceNanos.Store(0) })
	runPiCancellationTest(t, piCancelFakeScript(true))
}

func runPiCancellationTest(t *testing.T, script string) {
	t.Helper()

	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "pids")
	fakePath := filepath.Join(tempDir, "pi")
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("pi", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"PI_PID_FILE": pidFile},
	})
	if err != nil {
		t.Fatalf("new pi backend: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{Cwd: tempDir})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Drain streamed messages so processEvents never blocks on a full channel.
	go func() {
		for range session.Messages {
		}
	}()

	pids := waitPiPids(t, pidFile)

	cancel() // user cancels the task

	select {
	case res := <-session.Result:
		if res.Status != "aborted" {
			t.Errorf("status = %q, want aborted", res.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return after cancellation (possible scanner deadlock or unkilled process)")
	}

	// The leader and the grandchild must both be gone — cancellation reaped the
	// whole group, leaving no orphan spinning.
	for _, pid := range pids {
		waitProcessGone(t, pid)
	}
}

// TestPiTimeoutUsesProcessGroupCleanup verifies that a hard timeout follows
// the same TERM→KILL process-group cleanup path as user cancellation,
// terminates the whole owned tree and returns a "timeout" status.
func TestPiTimeoutUsesProcessGroupCleanup(t *testing.T) {
	piTerminateGraceNanos.Store(int64(300 * time.Millisecond))
	t.Cleanup(func() { piTerminateGraceNanos.Store(0) })

	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "pids")
	fakePath := filepath.Join(tempDir, "pi")
	writeTestExecutable(t, fakePath, []byte(piCancelFakeScript(false)))

	backend, err := New("pi", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"PI_PID_FILE": pidFile},
	})
	if err != nil {
		t.Fatalf("new pi backend: %v", err)
	}

	ctx := context.Background()
	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{
		Cwd:     tempDir,
		Timeout: 500 * time.Millisecond, // short timeout triggers deadline
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	go func() {
		for range session.Messages {
		}
	}()

	pids := waitPiPids(t, pidFile)

	select {
	case res := <-session.Result:
		if res.Status != "timeout" {
			t.Errorf("status = %q, want timeout", res.Status)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return after timeout (possible deadlock)")
	}

	// The whole process group must be terminated.
	for _, pid := range pids {
		waitProcessGone(t, pid)
	}
}

// TestPiConcurrentNormalExitAndCancel verifies race safety: when the process
// exits normally just as cancellation fires, the result must be either
// "aborted" or "completed" (whichever wins) and cleanup must be idempotent
// (no panic, no double-signal to a dead pid, no hang).
func TestPiConcurrentNormalExitAndCancel(t *testing.T) {
	piTerminateGraceNanos.Store(int64(300 * time.Millisecond))
	t.Cleanup(func() { piTerminateGraceNanos.Store(0) })

	// A script that emits one event and exits quickly.
	script := "#!/bin/sh\n" +
		`printf '{"type":"agent_start"}\n'` + "\n" +
		`printf '{"type":"turn_end","message":{"role":"assistant","model":"test","usage":{"input":1,"output":1}}}\n'` + "\n" +
		"exit 0\n"

	tempDir := t.TempDir()
	fakePath := filepath.Join(tempDir, "pi")
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("pi", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatalf("new pi backend: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{Cwd: tempDir})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	go func() {
		for range session.Messages {
		}
	}()

	// Give the process a moment to start, then cancel concurrently
	// with its exit.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case res := <-session.Result:
		// Either "completed" (normal exit won the race) or "aborted"
		// (cancellation won). Neither should be a failure or hang.
		if res.Status != "completed" && res.Status != "aborted" {
			t.Errorf("status = %q, want completed or aborted (race-safe)", res.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return after concurrent exit (possible deadlock)")
	}
}

// TestPiCancellationCleanupReceipt verifies the result structure when
// cancellation fires: the receipt must contain process/result/timing
// identifiers and no command environment, credential or secret material.
func TestPiCancellationCleanupReceipt(t *testing.T) {
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "pids")
	fakePath := filepath.Join(tempDir, "pi")
	writeTestExecutable(t, fakePath, []byte(piCancelFakeScript(false)))

	backend, err := New("pi", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"PI_PID_FILE": pidFile},
	})
	if err != nil {
		t.Fatalf("new pi backend: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{Cwd: tempDir})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	go func() {
		for range session.Messages {
		}
	}()

	// Wait for the fake to start, then cancel.
	_ = waitPiPids(t, pidFile)
	cancel()

	select {
	case res := <-session.Result:
		if res.Error == "" {
			t.Error("aborted result should have a non-empty Error field (cancellation reason)")
		}
		if res.DurationMs <= 0 {
			t.Errorf("DurationMs = %d, want positive", res.DurationMs)
		}
		if res.SessionID == "" {
			t.Error("SessionID should be set on aborted results")
		}
		// Sanity: output up to the cancel point is fine and may contain ticks.
		if len(res.Output) > 0 {
			if strings.Contains(res.Output, "PI_PID_FILE") ||
				strings.Contains(res.Output, tempDir) ||
				strings.Contains(res.Output, "/tmp/") {
				t.Error("cleanup receipt leaked command environment or path information")
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

// waitPiPids polls pidFile until it contains the space-separated pids the
// fake recorded, then returns them.
func waitPiPids(t *testing.T, pidFile string) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) >= 2 {
				pids := make([]int, 0, len(fields))
				ok := true
				for _, f := range fields {
					n, perr := strconv.Atoi(f)
					if perr != nil || n <= 0 {
						ok = false
						break
					}
					pids = append(pids, n)
				}
				if ok {
					return pids
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake pi never recorded its pids in %s", pidFile)
	return nil
}
