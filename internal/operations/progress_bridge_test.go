// file: internal/operations/progress_bridge_test.go
// version: 1.0.0
// guid: 6b1f0a72-3c9d-4e58-9a41-77c2e5d8b430
// last-edited: 2026-08-16

// LoggerFromReporter is the seam between an operation's work and the ops
// registry's liveness watchdog. It shipped as a stub that took the reporter and
// discarded it (`func LoggerFromReporter(_ ProgressReporter)`), returning a
// StandardLogger whose UpdateProgress has an empty body. The compiler was
// satisfied and every progress call in every op vanished.
//
// The registry watchdog cancels an op that goes ProgressTimeout (default 5m)
// without a progress stamp, falling back to StartedAt when progress was NEVER
// reported. An op that CANNOT report therefore accrues stuck time from birth and
// is killed at exactly five minutes no matter how healthy it is -- which is what
// happened to the 2026-08-16 library.scan, and to two earlier ops whose comments
// (internal/plugins/maintenance/dedupe_book_file_rows.go, internal/organizer/
// service.go) both describe "an operation that was in fact working the whole
// time".
//
// These tests assert the bridge carries the call. Verify them by reverting the
// bridge: they must go red.

package operations

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// recordingReporter captures what actually arrives at the ProgressReporter.
type recordingReporter struct {
	mu       sync.Mutex
	progress []progressCall
	canceled bool
}

type progressCall struct {
	current int
	total   int
	message string
}

func (r *recordingReporter) UpdateProgress(current, total int, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, progressCall{current, total, message})
	return nil
}

func (r *recordingReporter) Log(_, _ string, _ *string) error { return nil }

func (r *recordingReporter) IsCanceled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canceled
}

func (r *recordingReporter) calls() []progressCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]progressCall(nil), r.progress...)
}

// TestLoggerFromReporter_ForwardsProgress is the whole bug in one assertion.
func TestLoggerFromReporter_ForwardsProgress(t *testing.T) {
	rep := &recordingReporter{}
	log := LoggerFromReporter(rep)

	log.UpdateProgress(7, 100, "Scanning folder 1/3")

	calls := rep.calls()
	if len(calls) != 1 {
		t.Fatalf("reporter received %d progress calls, want 1 -- the logger is not wired to the reporter", len(calls))
	}
	if calls[0] != (progressCall{7, 100, "Scanning folder 1/3"}) {
		t.Errorf("reporter got %+v, want {7 100 \"Scanning folder 1/3\"}", calls[0])
	}
}

// TestLoggerFromReporter_WithPreservesTheBridge guards a trap specific to
// implementing this by embedding *logger.StandardLogger: the promoted With()
// returns a bare StandardLogger, silently dropping the reporter again.
//
// This is not hypothetical. internal/scanner/service.go:338 and :389 hand
// log.With("scanner") to ScanDirectoryParallel and ProcessBooksParallel -- the
// deepest, slowest part of a scan, and precisely the stretch during which the
// watchdog fires. A bridge that forgets to override With fixes the shallow calls
// and leaves the ones that matter broken.
func TestLoggerFromReporter_WithPreservesTheBridge(t *testing.T) {
	rep := &recordingReporter{}
	child := LoggerFromReporter(rep).With("scanner")

	child.UpdateProgress(42, 100, "from a child logger")

	calls := rep.calls()
	if len(calls) != 1 {
		t.Fatalf("child logger delivered %d progress calls, want 1 -- With() dropped the reporter", len(calls))
	}
	if calls[0].current != 42 || calls[0].message != "from a child logger" {
		t.Errorf("child logger delivered %+v", calls[0])
	}
}

// TestLoggerFromReporter_NilReporter keeps the no-reporter path a no-op rather
// than a panic: several callers build a logger before they have a reporter.
func TestLoggerFromReporter_NilReporter(t *testing.T) {
	log := LoggerFromReporter(nil)
	log.UpdateProgress(1, 10, "no reporter")     // must not panic
	log.With("child").UpdateProgress(1, 10, "x") // must not panic
	log.Info("still logs")
	if log.IsCanceled() {
		t.Error("a nil reporter reported cancellation")
	}
}

// TestLoggerFromReporter_PlainLoggingStillWorks confirms the bridge did not lose
// the ordinary Logger behaviour it wraps.
func TestLoggerFromReporter_PlainLoggingStillWorks(t *testing.T) {
	log := LoggerFromReporter(&recordingReporter{})
	log.Trace("trace")
	log.Debug("debug")
	log.Info("info")
	log.Warn("warn")
	log.Error("error")
	// ProgressReporter has no change-tracking method, so RecordChange keeps
	// StandardLogger's no-op behaviour. Asserted only to prove it does not panic.
	log.RecordChange(logger.Change{BookID: "1", ChangeType: "book_update", Summary: "s"})
}
