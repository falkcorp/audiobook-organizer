// file: internal/scheduler/silent_task_skip_test.go
// version: 1.0.0
// guid: 7c4e1b93-2a86-4d0f-9e57-3b1a8f6c204d
// last-edited: 2026-08-12

// Regression test for a scheduler that dropped tasks without saying so.
//
// Start() creates a ticker only when `IsEnabled() && GetInterval() > 0`. There
// was no else branch, so a task that was ENABLED but whose interval resolved to
// 0 produced no ticker, no log line, and no other evidence. From the outside it
// was identical to a task deliberately switched off.
//
// Measured on production 2026-08-12: every task under the `scheduled.*` config
// block had interval 0 — including library_scan, whose shipped defaults are
// enabled=true / interval=360 — because stored zero values were shadowing the
// viper defaults. library_scan is the only unattended discovery path for newly
// added books, so nothing had scanned automatically, and the sole symptom was a
// log line that was absent. Four unrelated tasks did get tickers, which made the
// scheduler look healthy.
//
// The test asserts the WARN names the task, so the next occurrence is greppable.

package scheduler

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// captureLogs swaps the default slog logger for one writing into a buffer, and
// restores it when the test ends.
func captureLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// startScheduler runs Start() with only the given tasks registered and returns
// the captured log output. Start spawns goroutines; shutting down immediately
// keeps the test fast and deterministic because every ticker branch logs
// synchronously before its goroutine does any work.
func startScheduler(t *testing.T, level slog.Level, defs ...TaskDefinition) string {
	t.Helper()
	buf := captureLogs(t, level)

	ts := &TaskScheduler{
		// Store must be non-nil: Start -> loadLastMaintenanceRun calls it
		// unconditionally. Returning a nil Store is the supported "DB not ready"
		// path and makes loadLastMaintenanceRun return early.
		deps:    SchedulerDeps{Store: func() database.Store { return nil }},
		tasks:   make(map[string]*TaskDefinition),
		lastRun: make(map[string]time.Time),
	}
	for _, d := range defs {
		ts.RegisterTask(d)
	}

	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	ts.Start(shutdown, &wg)
	close(shutdown)
	wg.Wait()

	return buf.String()
}

// TestEnabledTaskWithZeroIntervalIsReported is the core regression: an enabled
// task with no usable interval must produce a WARN naming it, not silence.
func TestEnabledTaskWithZeroIntervalIsReported(t *testing.T) {
	out := startScheduler(t, slog.LevelInfo, TaskDefinition{
		Name:        "library_scan",
		Description: "the only unattended discovery path for new books",
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 0 },
		RunOnStart:  func() bool { return false },
	})

	if !strings.Contains(out, "library_scan") {
		t.Fatalf("an enabled task that will NEVER run must be named in the logs; "+
			"this is the silent-skip defect. got:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("a task that is on but can never fire is a misconfiguration and "+
			"belongs at WARN, not below it; got:\n%s", out)
	}
	// The operator needs to know what to change, not merely that something is wrong.
	if !strings.Contains(out, "interval") {
		t.Errorf("the warning must point at the interval setting; got:\n%s", out)
	}
}

// TestScheduledTaskWithIntervalStillLogsNormally guards against the fix turning
// every healthy task into a warning — the discriminating half of the control.
func TestScheduledTaskWithIntervalStillLogsNormally(t *testing.T) {
	out := startScheduler(t, slog.LevelInfo, TaskDefinition{
		Name:        "purge_deleted",
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 6 * time.Hour },
		RunOnStart:  func() bool { return false },
	})

	if !strings.Contains(out, "Scheduled task interval") {
		t.Errorf("a correctly configured task should still log its interval; got:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("a correctly configured task must NOT warn; got:\n%s", out)
	}
}

// TestDisabledTaskDoesNotWarn pins the distinction the defect erased: OFF on
// purpose and ON-but-broken must not look the same, and OFF must stay quiet.
func TestDisabledTaskDoesNotWarn(t *testing.T) {
	out := startScheduler(t, slog.LevelDebug, TaskDefinition{
		Name:        "db_optimize",
		IsEnabled:   func() bool { return false },
		GetInterval: func() time.Duration { return 0 },
		RunOnStart:  func() bool { return false },
	})

	if strings.Contains(out, "level=WARN") {
		t.Errorf("a deliberately disabled task must not warn; got:\n%s", out)
	}
	if !strings.Contains(out, "db_optimize") {
		t.Errorf("the roster should still account for a disabled task at Debug "+
			"so 'why did nothing happen' is answerable; got:\n%s", out)
	}
}
