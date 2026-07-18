// file: internal/operations/registry/reporter_terminal_test.go
// version: 1.0.0
// guid: 9b1c4e7a-3d52-4a68-b0f1-6c2d3e4f5a6b
// last-edited: 2026-07-18

package registry

// White-box tests for R-3: the abandoned-reporter log-buffer guards. Kept in
// package registry (not registry_test) so they can construct a bare dbReporter
// and reach the unexported markTerminal / droppedLogCount without a store or
// flush goroutine.

import (
	"context"
	"log/slog"
	"testing"
)

// newBareReporter builds a dbReporter with no store, bus, activity recorder, or
// flushLoop goroutine — Log on it touches only in-memory state at info level.
func newBareReporter() *dbReporter {
	return &dbReporter{
		opID:    "op-r3",
		defID:   "test.r3",
		flushCh: make(chan struct{}, 1),
		runCtx:  context.Background(),
	}
}

// TestReporterR3_LogNoOpAfterTerminal verifies that once markTerminal is called
// (the abandonment path) Log stops buffering — the fix that stops logBuf from
// growing unbounded behind a wedged goroutine.
func TestReporterR3_LogNoOpAfterTerminal(t *testing.T) {
	r := newBareReporter()

	// A few pre-terminal lines buffer normally.
	for i := 0; i < 5; i++ {
		_ = r.Log(slog.LevelInfo, "pre-terminal")
	}
	r.logMu.Lock()
	before := len(r.logBuf)
	r.logMu.Unlock()
	if before != 5 {
		t.Fatalf("expected 5 buffered entries pre-terminal, got %d", before)
	}

	r.markTerminal()

	// Post-terminal lines must be dropped without touching the buffer.
	for i := 0; i < 10_000; i++ {
		_ = r.Log(slog.LevelInfo, "post-terminal — wedged goroutine still logging")
	}
	r.logMu.Lock()
	after := len(r.logBuf)
	r.logMu.Unlock()
	if after != before {
		t.Errorf("logBuf grew after markTerminal: before=%d after=%d (want no growth)", before, after)
	}
	if !r.terminated.Load() {
		t.Error("terminated flag not set after markTerminal")
	}
}

// TestReporterR3_BufferCapDropsOldest verifies that even before termination the
// buffer is bounded: past the cap, oldest entries are dropped and counted, so a
// run whose flushLoop has stalled cannot grow logBuf without limit.
func TestReporterR3_BufferCapDropsOldest(t *testing.T) {
	r := newBareReporter()

	const extra = 500
	total := maxBufferedLogEntries + extra
	for i := 0; i < total; i++ {
		_ = r.Log(slog.LevelInfo, "line")
	}

	r.logMu.Lock()
	got := len(r.logBuf)
	r.logMu.Unlock()
	if got != maxBufferedLogEntries {
		t.Errorf("logBuf len: got %d want cap %d", got, maxBufferedLogEntries)
	}
	if dropped := r.droppedLogCount(); dropped != extra {
		t.Errorf("droppedLogCount: got %d want %d", dropped, extra)
	}
}

// TestReporterR3_MarkTerminalIdempotent verifies markTerminal is safe to call
// more than once (both shutdown-abandon and genuine-abandon paths may hit it).
func TestReporterR3_MarkTerminalIdempotent(t *testing.T) {
	r := newBareReporter()
	r.markTerminal()
	r.markTerminal() // must not panic or flip anything back
	if !r.terminated.Load() {
		t.Error("terminated flag cleared by second markTerminal")
	}
	if err := r.Log(slog.LevelInfo, "x"); err != nil {
		t.Errorf("Log after terminal returned error: %v", err)
	}
}
