// file: internal/database/aggtest/aggtest.go
// version: 1.0.0
// guid: 2fac3bb3-d63b-433a-8d70-6be714066e1a
// last-edited: 2026-08-24

// Package aggtest counts how many times RecomputeBookAggregates ran, for tests
// that need to prove a write path coalesces its aggregate recomputes.
//
// WHY THIS IS A PACKAGE AND NOT A TEST HELPER. It started as unexported helpers
// in internal/database's own test files, which meant no other package could use
// it — and the write paths whose coalescing matters most live elsewhere
// (internal/plugins/maintenance's relink repair was measured at 92.1% of all
// attributed recomputes). Copying the helpers into each package would fork
// TerminalMarkers, whose whole contract is that it must be updated whenever
// RecomputeBookAggregates gains a new return path. One copy, imported.
package aggtest

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// Capture redirects the default logger into a buffer for the duration of one
// test and returns a reader for it.
//
// Counting log lines is the point, not a convenience. RecomputeBookAggregates is
// reached through notifyBookFileChange, which is unexported and has no return
// value, so there is no seam to inject a counter into. Its log lines are the only
// observable record of how many times it ran.
//
// A test that only checked the final Duration/FileSize would pass whether the
// recompute ran once or once per row — and "once per row" is precisely the O(N^2)
// this exists to detect. The final values cannot distinguish those two cases,
// because the last recompute of a run produces the same totals as the only one.
//
// The handler is pinned at LevelDebug because that distinction lives entirely in
// the Debug-level "no change needed" line: a redundant recompute produces no
// Info-level output at all. At the default level this capture would be blind to
// exactly the regression it exists to detect. See CountInvocations.
//
// slog.SetDefault is process-global, so a test using this must NOT call
// t.Parallel.
func Capture(t *testing.T) func() string {
	t.Helper()

	var mu sync.Mutex
	var buf bytes.Buffer

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(
		&syncWriter{mu: &mu, buf: &buf},
		&slog.HandlerOptions{Level: slog.LevelDebug},
	)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// syncWriter serialises writes from the logger, which RecomputeBookAggregates may
// reach from more than one goroutine in other suites.
type syncWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TerminalMarkers lists the mutually exclusive terminal outcomes of
// RecomputeBookAggregates. Every invocation reaches exactly one of them.
//
// WHY AN EXPLICIT LIST AND NOT A "RecomputeBookAggregates" SUBSTRING TEST: the
// obvious matcher over-counts, and it was measured doing so. One invocation can
// emit THREE lines carrying both that token and book_id — the two partial-data
// warnings ("no files have Duration", "no files have FileSize") and then the
// terminal "no change needed", because preserving both existing values means
// nothing changed. Seed a book with real aggregates, batch-upsert its row with
// Duration 0 and FileSize 0, and a substring matcher returned 3 for a single call.
//
// That is not hypothetical for every caller: the maintenance relink repair
// deliberately leaves per-file Duration at 0, so its books DO emit the
// no-Duration warning. Matching only terminal outcomes is what makes this usable
// from that package rather than only from a fixture where every file happens to
// carry a positive duration AND a positive size.
//
// Anything added to RecomputeBookAggregates that returns by another route must be
// added here too, or invocations will be under-counted — and an under-count reads
// exactly like the coalescing this is used to prove.
var TerminalMarkers = []string{
	"RecomputeBookAggregates updated",
	"RecomputeBookAggregates: no change needed",
	"RecomputeBookAggregates book not found, skipping",
	"notifyBookFileChange RecomputeBookAggregates failed",
}

// CountInvocations returns how many times RecomputeBookAggregates RAN for
// bookID, whether or not it changed anything.
//
// Invocations, not writes, are the quantity that matters, and the difference is
// the whole point of this function. Every invocation calls GetBookFiles and reads
// the book's ENTIRE file set; that read is the O(N^2) cost. But only the first
// invocation of a run finds anything to change — the rest compute the same sums,
// hit the "no change needed" early return, and never emit the "updated" line.
//
// Counting "updated" therefore reports 1 whether the recompute ran once or once
// per row. A mutant that dropped the per-book de-duplication and recomputed N
// times passed a version of this suite that counted writes.
func CountInvocations(logs, bookID string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "book_id="+bookID) {
			continue
		}
		for _, marker := range TerminalMarkers {
			if strings.Contains(line, marker) {
				n++
				break
			}
		}
	}
	return n
}

// CountWrites returns how many times an aggregate WRITE was logged for bookID —
// i.e. how often the sums actually changed. Distinct from CountInvocations; read
// the note there before choosing between them.
func CountWrites(logs, bookID string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "RecomputeBookAggregates updated") &&
			strings.Contains(line, "book_id="+bookID) {
			n++
		}
	}
	return n
}
