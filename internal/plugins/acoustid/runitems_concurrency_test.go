// file: internal/plugins/acoustid/runitems_concurrency_test.go
// version: 1.1.0
// guid: 0e8c4a72-5b1d-4f39-9a6e-c2d7b3f81a45
// last-edited: 2026-10-13

package acoustid

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// overlapProbe proves that a pool really ran items side by side. Each caller
// of enter parks until `want` callers are inside at once (or the timeout
// passes), so a sequential run never reaches `want` and maxInFlight stays 1.
// Parking only until the first time the quorum is met keeps the rest of the
// run fast.
type overlapProbe struct {
	want  int32
	in    atomic.Int32
	max   atomic.Int32
	ready chan struct{}
	once  sync.Once
}

func newOverlapProbe(want int) *overlapProbe {
	return &overlapProbe{want: int32(want), ready: make(chan struct{})}
}

func (o *overlapProbe) enter() (leave func()) {
	n := o.in.Add(1)
	for {
		m := o.max.Load()
		if n <= m || o.max.CompareAndSwap(m, n) {
			break
		}
	}
	if n >= o.want {
		o.once.Do(func() { close(o.ready) })
	}
	select {
	case <-o.ready:
	case <-time.After(250 * time.Millisecond):
	}
	return func() { o.in.Add(-1) }
}

func (o *overlapProbe) maxInFlight() int { return int(o.max.Load()) }

func needMultiCore(t *testing.T) {
	t.Helper()
	if runtime.NumCPU() < 2 {
		t.Skip("pool is sized to runtime.NumCPU(); one core cannot overlap items")
	}
}

// lastFrame returns the last progress message the reporter saw.
func lastFrame(frames []lshFrame) string {
	if len(frames) == 0 {
		return ""
	}
	return frames[len(frames)-1].message
}

// TestLSHBackfill_RunsItemsConcurrently: acoustid.lsh-backfill used to omit
// RunItemsOptions.Concurrency and ran on one core. With the pool, several
// rows are inside UpdateBookFile at once, and the tallies (atomics, read by
// the Label closure from worker goroutines) still add up. Run with -race.
func TestLSHBackfill_RunsItemsConcurrently(t *testing.T) {
	needMultiCore(t)
	const n = 24
	files := make([]database.BookFileCore, n)
	hydrate := make(map[string][]database.BookFile, n)
	for i := range files {
		bookID := fmt.Sprintf("b%02d", i)
		fileID := fmt.Sprintf("f%02d", i)
		files[i] = database.BookFileCore{ID: fileID, BookID: bookID, AcoustIDFingerprintDurationSec: 600}
		hydrate[bookID] = []database.BookFile{{ID: fileID, BookID: bookID, AcoustIDFingerprint: []byte{1, 2, 3}}}
	}

	probe := newOverlapProbe(2)
	var updates atomic.Int64
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetBookFileByIDFunc:     hydrateFromMap(hydrate),
		UpdateBookFileFunc: func(string, *database.BookFile) error {
			defer probe.enter()()
			updates.Add(1)
			return nil
		},
	}
	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("runLSHBackfill: %v", err)
	}
	if got := updates.Load(); got != n {
		t.Fatalf("UpdateBookFile calls = %d, want %d", got, n)
	}
	if got := probe.maxInFlight(); got < 2 {
		t.Fatalf("max rows in flight = %d, want >= 2: the op ran on one core", got)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if want := fmt.Sprintf("indexed=%d", n); !strings.Contains(lastFrame(r.frames), want) {
		t.Errorf("final summary %q does not report %s", lastFrame(r.frames), want)
	}
}

// TestResetAll_SlowPathRunsItemsConcurrently covers the per-row fallback of
// acoustid.reset-all: rows are cleared side by side, each from its own freshly
// hydrated copy, and the cleared tally is exact.
func TestResetAll_SlowPathRunsItemsConcurrently(t *testing.T) {
	needMultiCore(t)
	const n = 24
	files := make([]database.BookFileCore, n)
	for i := range files {
		files[i] = database.BookFileCore{ID: fmt.Sprintf("f%02d", i), BookID: fmt.Sprintf("b%02d", i)}
	}

	probe := newOverlapProbe(2)
	var updates atomic.Int64
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetBookFileByIDFunc: func(bookID, fileID string) (*database.BookFile, error) {
			// A fresh row per call: the op clears the copy it is handed.
			return &database.BookFile{ID: fileID, BookID: bookID, AcoustIDSeg2: "AQADsynthetic"}, nil
		},
		UpdateBookFileFunc: func(_ string, updated *database.BookFile) error {
			defer probe.enter()()
			if updated.AcoustIDSeg2 != "" {
				t.Errorf("row %s written with a segment still set", updated.ID)
			}
			updates.Add(1)
			return nil
		},
	}
	p := &Plugin{store: store}
	r := &resetAllTestReporter{}

	if err := p.runResetAll(context.Background(), nil, r); err != nil {
		t.Fatalf("runResetAll: %v", err)
	}
	if got := updates.Load(); got != n {
		t.Fatalf("UpdateBookFile calls = %d, want %d", got, n)
	}
	if got := probe.maxInFlight(); got < 2 {
		t.Fatalf("max rows in flight = %d, want >= 2: the slow path ran on one core", got)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if want := fmt.Sprintf("%d files cleared", n); !strings.Contains(lastFrame(r.frames), want) {
		t.Errorf("final summary %q does not report %q", lastFrame(r.frames), want)
	}
}

// TestResetAll_CandidatePassDropsInBatches drops more candidates than one
// batch holds through the real EmbeddingStore, and leaves a pinned manual
// candidate alone. The pass is deliberately NOT a worker pool: DeleteCandidate
// holds the store-wide candidate lock for its whole call, so a pool only
// queued on it. The saving is DeleteCandidates, one lock and one commit per
// chunk (see TestEmbeddingStore_DeleteCandidates in the database package).
func TestResetAll_CandidatePassDropsInBatches(t *testing.T) {
	emb := newTestResetAllEmbeddingStore(t)
	const n = 1100 // three chunks of 500
	for i := range n {
		if err := emb.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  fmt.Sprintf("a%04d", i),
			EntityBID:  fmt.Sprintf("b%04d", i),
			Layer:      "acoustid",
			Status:     "pending",
		}); err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	// A human pins an existing acoustid-layer pair: the row keeps its layer
	// and becomes a manual candidate.
	if err := emb.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "pin-a", EntityBID: "pin-b", Layer: "acoustid", Status: "pending",
	}); err != nil {
		t.Fatalf("seed pinned pair: %v", err)
	}
	if _, err := emb.EnqueueManualCandidate("book", "pin-a", "pin-b", "synthetic pin"); err != nil {
		t.Fatalf("pin candidate: %v", err)
	}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return nil, nil },
	}
	p := &Plugin{store: store, embeddingStore: emb}
	r := &resetAllTestReporter{}

	if err := p.runResetAll(context.Background(), nil, r); err != nil {
		t.Fatalf("runResetAll: %v", err)
	}
	remaining, _, err := emb.ListCandidates(database.CandidateFilter{Layer: "acoustid", Limit: 50})
	if err != nil {
		t.Fatalf("list remaining: %v", err)
	}
	if len(remaining) != 1 || !database.IsManualCandidate(remaining[0]) {
		t.Fatalf("remaining acoustid candidates = %+v, want only the pinned manual one", remaining)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if want := fmt.Sprintf("%d candidates dropped", n); !strings.Contains(lastFrame(r.frames), want) {
		t.Errorf("final summary %q does not report %q", lastFrame(r.frames), want)
	}
}
