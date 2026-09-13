// file: internal/plugins/maintenance/tag_backfill_liveness_test.go
// version: 1.0.1
// guid: 4c8f0282-d1ad-446a-af3d-d36cea86888b
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// livenessReporter is a fakeReporter that also implements
// registry.LivenessToucher and counts every liveness-relevant call, so a test
// can see whether the op stamped the watchdog's clock mid-book.
type livenessReporter struct {
	fakeReporter
	touches  atomic.Int64
	progress atomic.Int64
	items    atomic.Int64
}

func (r *livenessReporter) UpdateProgress(_, _ int, _ string) error { r.progress.Add(1); return nil }
func (r *livenessReporter) TouchLiveness()                          { r.touches.Add(1) }
func (r *livenessReporter) SetCurrentItem(_ string)                 { r.items.Add(1) }

var _ registry.LivenessToucher = (*livenessReporter)(nil)

// blockingTagExtractor delegates to a mapTagExtractor except for the files in
// block, which hang until release is closed -- a stand-in for a TagLib WASM read
// that never returns.
type blockingTagExtractor struct {
	inner   *mapTagExtractor
	block   map[string]bool
	release chan struct{}
}

func (e *blockingTagExtractor) ExtractMetadata(filePath string) (metadata.Metadata, error) {
	base := filePath[strings.LastIndex(filePath, "/")+1:]
	if e.block[base] {
		<-e.release
	}
	return e.inner.ExtractMetadata(filePath)
}

// installBlockingExtractor swaps in a blocking extractor with a short read bound
// and, at cleanup, releases the abandoned reads and waits for them to return so
// no goroutine outlives the test.
func installBlockingExtractor(t *testing.T, fx *tagFixture, bound time.Duration, block ...string) {
	t.Helper()
	e := &blockingTagExtractor{inner: fx.extractor, block: map[string]bool{}, release: make(chan struct{})}
	for _, b := range block {
		e.block[b] = true
	}
	metadata.SetMetadataExtractor(e)
	oldBound, oldCap := tagReadTimeout, tagReadMaxAbandoned
	tagReadTimeout = bound
	t.Cleanup(func() {
		close(e.release)
		drainAbandonedTagReads(t)
		tagReadTimeout, tagReadMaxAbandoned = oldBound, oldCap
	})
}

func runWith(t *testing.T, fx *tagFixture, rep *livenessReporter, params tagBackfillParams) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return New(fakeDeps{store: fx.store}).runTagBackfill(context.Background(), raw, rep)
}

// A read that never returns is counted as a read error after the bound, its
// book is still judged and the rest of the run completes.
func TestTagBackfill_HungReadCountsAsReadErrAndBookFinishes(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "h1", BookID: "H", TrackNumber: 1},
		{ID: "h2", BookID: "H", TrackNumber: 2},
		{ID: "h3", BookID: "H", TrackNumber: 3},
		{ID: "o1", BookID: "O", TrackNumber: 1},
	}, map[string]metadata.Metadata{
		"h1.mp3": tagMeta(1, 3, 0, 0), "h2.mp3": tagMeta(2, 3, 0, 0), "h3.mp3": tagMeta(3, 3, 0, 0),
		"o1.mp3": tagMeta(1, 1, 0, 0),
	})
	installBlockingExtractor(t, fx, 50*time.Millisecond, "h2.mp3")

	rep := &livenessReporter{}
	start := time.Now()
	if err := runWith(t, fx, rep, tagBackfillParams{DryRun: true}); err != nil {
		t.Fatalf("op failed: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("op took %v; the hung read was waited on, not bounded", d)
	}
	summary := rep.logs[len(rep.logs)-1]
	// h1+h3 read (book H refused: h2 has no reading), o1 read: 3 judged rows.
	assertSummaryHas(t, summary, "read-errors=1", "judged-rows=3", "missing-on-disk=0")
	var warned bool
	for _, l := range rep.logs {
		if strings.Contains(l, "h2.mp3") && strings.Contains(l, "exceeded") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no WARN naming the timed-out file; logs: %q", rep.logs)
	}
}

// Past tagReadMaxAbandoned outstanding abandoned reads the op fails loudly
// instead of abandoning more goroutines.
func TestTagBackfill_TooManyHungReadsFailsOp(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "x1", BookID: "X", TrackNumber: 1},
		{ID: "x2", BookID: "X", TrackNumber: 2},
		{ID: "x3", BookID: "X", TrackNumber: 3},
	}, map[string]metadata.Metadata{
		"x1.mp3": tagMeta(1, 3, 0, 0), "x2.mp3": tagMeta(2, 3, 0, 0), "x3.mp3": tagMeta(3, 3, 0, 0),
	})
	installBlockingExtractor(t, fx, 20*time.Millisecond, "x1.mp3", "x2.mp3", "x3.mp3")
	tagReadMaxAbandoned = 2

	err := runWith(t, fx, &livenessReporter{}, tagBackfillParams{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "of this run's timed-out tag reads are still running") {
		t.Fatalf("err = %v, want the abandoned-read cap error", err)
	}
}

// The watchdog's clock is stamped while a long book is still being read, not
// only when the book returns. RunItems' own UpdateProgress fires once per book,
// so without the mid-book stamp a book of thousands of files is silent for the
// whole read -- the 2026-09-13 prod kill.
func TestTagBackfill_TouchesLivenessWithinABook(t *testing.T) {
	const n = 12
	files := make([]database.BookFile, n)
	tags := map[string]metadata.Metadata{}
	for i := range files {
		id := fmt.Sprintf("l%02d", i)
		files[i] = database.BookFile{ID: id, BookID: "L", TrackNumber: i + 1}
		tags[id+".mp3"] = tagMeta(i+1, n, 0, 0)
	}
	fx := newTagFixture(t, files, tags)

	rep := &livenessReporter{}
	var mu sync.Mutex
	var touchesAtRead, progressAtRead []int64
	fx.extractor.hook = func(string) {
		mu.Lock()
		touchesAtRead = append(touchesAtRead, rep.touches.Load())
		progressAtRead = append(progressAtRead, rep.progress.Load())
		mu.Unlock()
	}
	if err := runWith(t, fx, rep, tagBackfillParams{DryRun: true}); err != nil {
		t.Fatalf("op failed: %v", err)
	}
	if len(touchesAtRead) != n {
		t.Fatalf("reads = %d, want %d", len(touchesAtRead), n)
	}
	for k := range touchesAtRead {
		// Only the op's initial 0/N UpdateProgress has fired: the book is in
		// flight, so any liveness here must come from the mid-book stamp.
		if progressAtRead[k] != 1 {
			t.Fatalf("read %d: UpdateProgress calls = %d, want 1 (book still in flight)", k, progressAtRead[k])
		}
		if touchesAtRead[k] < int64(k) {
			t.Errorf("read %d: liveness touches so far = %d, want >= %d (one per finished file)", k, touchesAtRead[k], k)
		}
	}
	if got := rep.items.Load(); got < n {
		t.Errorf("SetCurrentItem calls = %d, want >= %d (one per file)", got, n)
	}
}

// Reads stuck from EARLIER runs must not count against a new run's cap. A read
// that never returns never decrements the process-wide gauge, so a process-wide
// cap would fail every later run on its first timeout until a restart.
func TestTagBackfill_EarlierStuckReadsDoNotPoisonLaterRuns(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "p1", BookID: "P", TrackNumber: 1},
		{ID: "p2", BookID: "P", TrackNumber: 2},
	}, map[string]metadata.Metadata{"p1.mp3": tagMeta(1, 2, 0, 0), "p2.mp3": tagMeta(2, 2, 0, 0)})
	installBlockingExtractor(t, fx, 20*time.Millisecond, "p2.mp3")
	// Simulate reads left stuck by earlier runs, past the cap. Registered after
	// installBlockingExtractor so it is undone before the drain runs (LIFO).
	const leftover = 20
	tagReadsAbandoned.Add(leftover)
	t.Cleanup(func() { tagReadsAbandoned.Add(-leftover) })

	rep := &livenessReporter{}
	if err := runWith(t, fx, rep, tagBackfillParams{DryRun: true}); err != nil {
		t.Fatalf("a run failed on reads abandoned by earlier runs: %v", err)
	}
	assertSummaryHas(t, rep.logs[len(rep.logs)-1], "read-errors=1")
	if !strings.Contains(strings.Join(rep.logs, "\n"), fmt.Sprintf("%d tag reads abandoned by earlier runs", leftover)) {
		t.Errorf("no start-of-run WARN reporting the %d leftover reads; logs: %q", leftover, rep.logs)
	}
}
