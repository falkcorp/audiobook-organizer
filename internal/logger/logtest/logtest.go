// file: internal/logger/logtest/logtest.go
// version: 1.0.0
// guid: db0bc5ed-6765-4330-b2c4-7fd8faffd328
// last-edited: 2026-10-06

// Package logtest captures slog output in tests.
//
// Tests that assert on a log line have to swap slog.Default, because the code
// under test logs through logger.New, which writes to slog.Default. That swap
// has two hazards this package closes:
//
//   - The capture target is shared with every goroutine still running in the
//     test binary, including ones leaked by earlier tests. A plain
//     bytes.Buffer written by such a goroutine while the test reads it is a
//     data race (TestResolveVectorBackend_HNSWIsSilent failed -race this way
//     when a leaked SQLite activity checkpointer logged into it). Buffer and
//     Recorder are mutex-guarded.
//   - slog.Default is process-global, so two tests capturing at once see each
//     other's lines. Capture and CaptureRecords call t.Setenv, which panics if
//     the test or any parent called t.Parallel, and makes a later t.Parallel
//     call panic too. The rule is enforced, not just documented.
//
// It lives under internal/logger so it is covered by that prefix's exemption
// in the slog guard (it must call slog.Default and slog.SetDefault).
package logtest

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// serialEnv is set by every capture so testing panics if the test is, or
// becomes, parallel.
const serialEnv = "LOGTEST_SLOG_DEFAULT_CAPTURED"

// Buffer is an io.Writer safe for concurrent writers and readers.
type Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p under the lock.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset discards everything written so far.
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// Lines returns the captured lines that contain substr.
func (b *Buffer) Lines(substr string) []string {
	var out []string
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

// swapDefault installs l as slog.Default for the rest of the test.
func swapDefault(t testing.TB, l *slog.Logger) {
	t.Helper()
	t.Setenv(serialEnv, "1") // panics under t.Parallel; see package doc
	prev := slog.Default()
	slog.SetDefault(l)
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// Capture routes slog.Default, at level and above, as text into a Buffer until
// the test ends. The test must not be parallel.
func Capture(t testing.TB, level slog.Level) *Buffer {
	t.Helper()
	buf := &Buffer{}
	swapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	return buf
}

// Recorder is a slog.Handler that keeps every record, for tests that assert on
// levels or attributes rather than formatted text. Attributes and groups added
// through WithAttrs/WithGroup are not retained.
type Recorder struct {
	mu      sync.Mutex
	records []slog.Record
}

// Enabled reports true for every level.
func (r *Recorder) Enabled(context.Context, slog.Level) bool { return true }

// Handle stores a copy of rec.
func (r *Recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

// WithAttrs returns r unchanged.
func (r *Recorder) WithAttrs([]slog.Attr) slog.Handler { return r }

// WithGroup returns r unchanged.
func (r *Recorder) WithGroup(string) slog.Handler { return r }

// Records returns a snapshot of the records handled so far.
func (r *Recorder) Records() []slog.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]slog.Record(nil), r.records...)
}

// CaptureRecords routes slog.Default into a Recorder until the test ends. The
// test must not be parallel.
func CaptureRecords(t testing.TB) *Recorder {
	t.Helper()
	r := &Recorder{}
	swapDefault(t, slog.New(r))
	return r
}
