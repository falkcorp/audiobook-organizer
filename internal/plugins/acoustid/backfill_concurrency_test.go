// file: internal/plugins/acoustid/backfill_concurrency_test.go
// version: 2.2.1
// guid: 4b1c7e92-6d05-4a38-9f71-2c8ab6d34e50
// last-edited: 2026-09-12

package acoustid

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// captureReporter is the minimum registry.Reporter (== sdk.Reporter) needed to
// drive RunItems, recording every checkpoint so the cursor can be inspected.
type captureReporter struct {
	mu          sync.Mutex
	checkpoints []BackfillParams
}

func (r *captureReporter) UpdateProgress(int, int, string) error      { return nil }
func (r *captureReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *captureReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (r *captureReporter) IsCanceled() bool                           { return false }
func (r *captureReporter) Trigger(context.Context, string, any) error { return nil }
func (r *captureReporter) SetCurrentItem(string)                      {}

func (r *captureReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

func (r *captureReporter) Checkpoint(state any) error {
	cp, ok := state.(BackfillParams)
	if !ok {
		return fmt.Errorf("checkpoint state is %T, want BackfillParams", state)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints = append(r.checkpoints, cp)
	return nil
}

func (r *captureReporter) saved() []BackfillParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]BackfillParams(nil), r.checkpoints...)
}

// pagedFixture is a store whose GetAllBooksFullFrom behaves like the memdb
// path: ID-ordered, seeking to the first ID strictly greater than afterID
// whether or not afterID still exists. It records every page request and how
// often each book's files were listed. beforePage runs ahead of each page
// request (1-based call number) and maxRows can shorten a page, so tests can
// delete books mid-run or return a short page mid-table.
type pagedFixture struct {
	books []database.Book
	files map[string][]database.BookFile
	store *database.MockStore

	mu     sync.Mutex
	limits []int
	visits map[string]int

	beforePage func(call int, afterID string)
	maxRows    func(call int) int
}

// deleteBook removes a book the way a mid-run merge or delete would.
func (fx *pagedFixture) deleteBook(id string) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.books = slices.DeleteFunc(fx.books, func(b database.Book) bool { return b.ID == id })
}

func newPagedFixture(nBooks, filesPerBook int, file func(bookID string, j int) database.BookFile) *pagedFixture {
	fx := &pagedFixture{files: map[string][]database.BookFile{}, visits: map[string]int{}}
	for i := 0; i < nBooks; i++ {
		id := fmt.Sprintf("book-%05d", i)
		sig := "signature-present"
		fx.books = append(fx.books, database.Book{ID: id, BookSigV1: &sig})
		for j := 0; j < filesPerBook; j++ {
			fx.files[id] = append(fx.files[id], file(id, j))
		}
	}
	fx.store = &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return nBooks, nil },
		GetAllBooksFullFromFunc: func(afterID string, limit int) ([]database.Book, error) {
			fx.mu.Lock()
			fx.limits = append(fx.limits, limit)
			call := len(fx.limits)
			fx.mu.Unlock()
			if fx.beforePage != nil {
				fx.beforePage(call, afterID)
			}
			fx.mu.Lock()
			defer fx.mu.Unlock()
			start := sort.Search(len(fx.books), func(i int) bool { return fx.books[i].ID > afterID })
			end := len(fx.books)
			if limit > 0 && start+limit < end {
				end = start + limit
			}
			if fx.maxRows != nil && start+fx.maxRows(call) < end {
				end = start + fx.maxRows(call)
			}
			if start >= end {
				return nil, nil
			}
			return slices.Clone(fx.books[start:end]), nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			fx.mu.Lock()
			fx.visits[bookID]++
			fx.mu.Unlock()
			return fx.files[bookID], nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			for i := range fx.books {
				if fx.books[i].ID == id {
					b := fx.books[i]
					return &b, nil
				}
			}
			return nil, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) { return b, nil },
	}
	return fx
}

// doneFile carries the raw-print proxy, so eligibility stops at "already
// fingerprinted" without touching the filesystem or fpcalc.
func doneFile(bookID string, j int) database.BookFile {
	return database.BookFile{
		ID: fmt.Sprintf("%s-file-%d", bookID, j), BookID: bookID,
		FilePath:                       fmt.Sprintf("/does/not/exist/%s-%d.m4b", bookID, j),
		AcoustIDFingerprintDurationSec: 60,
	}
}

// eligibleFiles returns a file factory whose rows pass every eligibility check:
// a real .mp3 path and no fingerprint of any kind.
func eligibleFiles(t *testing.T) func(string, int) database.BookFile {
	dir := t.TempDir()
	return func(bookID string, j int) database.BookFile {
		path := filepath.Join(dir, fmt.Sprintf("%s-%d.mp3", bookID, j))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return database.BookFile{ID: fmt.Sprintf("%s-file-%d", bookID, j), BookID: bookID, FilePath: path}
	}
}

// inFlightFake replaces fpcalc with a sleep that records the peak number of
// concurrent invocations.
func inFlightFake(t *testing.T) *atomic.Int64 {
	t.Helper()
	var cur, peak atomic.Int64
	orig := fingerprintFileFn
	fingerprintFileFn = func(pluginStore, database.BookFile, bool) fingerprintFileOutcome {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return fingerprintOutcomeFingerprinted
	}
	t.Cleanup(func() { fingerprintFileFn = orig })
	return &peak
}

func setWorkers(t *testing.T, n int) {
	t.Helper()
	orig := config.AppConfig.FPParallelWorkers
	config.AppConfig.FPParallelWorkers = n
	t.Cleanup(func() { config.AppConfig.FPParallelWorkers = orig })
}

// TestBackfillPages_LoadsInPages is fix (b). The op used to call
// GetAllBooksFullFrom("", 0) — the whole book table — which held ~862 MB in
// production. Every request must now be a bounded page, every book must be
// visited exactly once across pages, and the cursor must be checkpointed at the
// last book of each drained page.
func TestBackfillPages_LoadsInPages(t *testing.T) {
	const n = 2*backfillPageSize + 234
	fx := newPagedFixture(n, 1, doneFile)
	p := New(nil, fx.store, nil)
	rep := &captureReporter{}
	var tally backfillTally

	if err := p.backfillPages(context.Background(), rep, BackfillParams{}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}

	for i, l := range fx.limits {
		if l <= 0 || l > backfillPageSize {
			t.Fatalf("page request %d asked for limit=%d; a limit of 0 loads the whole book table", i, l)
		}
	}
	// 500 + 500 + 234, then one empty page: only an empty page ends the walk.
	if got := len(fx.limits); got != 4 {
		t.Errorf("page requests = %d, want 4 (500 + 500 + 234 + the empty page that ends the walk)", got)
	}
	for _, b := range fx.books {
		if fx.visits[b.ID] != 1 {
			t.Fatalf("book %s visited %d times, want exactly 1", b.ID, fx.visits[b.ID])
		}
	}
	if got := tally.skipped.Load(); got != n {
		t.Errorf("skipped = %d, want %d", got, n)
	}
	want := []string{fx.books[backfillPageSize-1].ID, fx.books[2*backfillPageSize-1].ID, fx.books[n-1].ID}
	var got []string
	for _, cp := range rep.saved() {
		got = append(got, cp.AfterBookID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("checkpoint cursors = %v, want %v", got, want)
	}
}

func TestBackfillPages_ResumesAfterCursor(t *testing.T) {
	fx := newPagedFixture(1200, 1, doneFile)
	p := New(nil, fx.store, nil)
	var tally backfillTally
	state := BackfillParams{AfterBookID: fx.books[599].ID}

	if err := p.backfillPages(context.Background(), &captureReporter{}, state, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	for i, b := range fx.books {
		want := 0
		if i >= 600 {
			want = 1
		}
		if fx.visits[b.ID] != want {
			t.Fatalf("book %d visited %d times, want %d", i, fx.visits[b.ID], want)
		}
	}
}

// A resumed run whose first page is empty and whose cursor no longer exists
// restarts from the top rather than reporting success having fingerprinted
// nothing. "book-deleted" sorts after every fixture ID, so the seek returns
// nothing and only the restart reaches the books.
func TestBackfillPages_StaleCursorRestartsFromTheTop(t *testing.T) {
	fx := newPagedFixture(30, 1, doneFile)
	p := New(nil, fx.store, nil)
	var tally backfillTally

	if err := p.backfillPages(context.Background(), &captureReporter{}, BackfillParams{AfterBookID: "book-deleted"}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	for _, b := range fx.books {
		if fx.visits[b.ID] != 1 {
			t.Fatalf("book %s visited %d times after a stale cursor, want 1", b.ID, fx.visits[b.ID])
		}
	}
}

// A resume whose cursor is the last book has nothing left to do. It must end
// cleanly: before 2026-09-12 the empty first page sent it back to the top to
// re-list and re-check the whole library.
func TestBackfillPages_ResumeAtLastBookFinishesWithoutRestart(t *testing.T) {
	fx := newPagedFixture(30, 1, doneFile)
	p := New(nil, fx.store, nil)
	var tally backfillTally

	if err := p.backfillPages(context.Background(), &captureReporter{}, BackfillParams{AfterBookID: fx.books[29].ID}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	for _, b := range fx.books {
		if fx.visits[b.ID] != 0 {
			t.Fatalf("book %s visited %d times; a resume at the last book restarted from the top", b.ID, fx.visits[b.ID])
		}
	}
	if got := len(fx.limits); got != 1 {
		t.Errorf("page requests = %d, want 1 (no re-list from the beginning)", got)
	}
}

// The cursor book is merged or deleted after its page drained. The next page
// request names a cursor that no longer exists; the run must still cover every
// book after it.
func TestBackfillPages_CursorBookDeletedMidRun(t *testing.T) {
	const n = 2*backfillPageSize + 10
	fx := newPagedFixture(n, 1, doneFile)
	deleted := fx.books[backfillPageSize-1].ID
	fx.beforePage = func(call int, afterID string) {
		if call == 2 {
			if afterID != deleted {
				t.Errorf("second page cursor = %q, want %q", afterID, deleted)
			}
			fx.deleteBook(afterID)
		}
	}
	all := slices.Clone(fx.books)
	p := New(nil, fx.store, nil)
	var tally backfillTally

	if err := p.backfillPages(context.Background(), &captureReporter{}, BackfillParams{}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	for _, b := range all {
		if fx.visits[b.ID] != 1 {
			t.Fatalf("book %s visited %d times, want 1; the walk stopped at the deleted cursor", b.ID, fx.visits[b.ID])
		}
	}
}

// A page shorter than backfillPageSize is not the end of the table: the memdb
// store can return fewer rows than asked for. Only an empty page ends the run.
func TestBackfillPages_ShortPageMidTableDoesNotEndRun(t *testing.T) {
	const n = backfillPageSize + 40
	fx := newPagedFixture(n, 1, doneFile)
	fx.maxRows = func(call int) int {
		if call == 1 {
			return 7
		}
		return n
	}
	p := New(nil, fx.store, nil)
	var tally backfillTally

	if err := p.backfillPages(context.Background(), &captureReporter{}, BackfillParams{}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	for _, b := range fx.books {
		if fx.visits[b.ID] != 1 {
			t.Fatalf("book %s visited %d times, want 1; a short first page ended the run", b.ID, fx.visits[b.ID])
		}
	}
}

// Older checkpoint formats each name a book with every earlier book done, so
// each maps onto the cursor rather than being discarded.
func TestResumeCursor_ReadsEveryCheckpointFormat(t *testing.T) {
	cases := []struct {
		state BackfillParams
		want  string
	}{
		{BackfillParams{}, ""},
		{BackfillParams{LastProcessedBookID: "legacy"}, "legacy"},
		{BackfillParams{Watermark: 4, WatermarkBookID: "wm"}, "wm"},
		{BackfillParams{AfterBookID: "new", WatermarkBookID: "wm", LastProcessedBookID: "legacy"}, "new"},
	}
	for _, c := range cases {
		if got := resumeCursor(c.state); got != c.want {
			t.Errorf("resumeCursor(%+v) = %q, want %q", c.state, got, c.want)
		}
	}
}

// TestBackfillBook_FansOutFilesOfOneBook is fix (c). Before 2026-09-12 a book's
// files ran one after another inside a single worker, so a many-chapter book
// ran at one fpcalc no matter how many workers were configured. One book with
// sixteen eligible files must now keep several fpcalc calls in flight, and
// never more than the semaphore allows.
func TestBackfillBook_FansOutFilesOfOneBook(t *testing.T) {
	peak := inFlightFake(t)
	fx := newPagedFixture(1, 16, eligibleFiles(t))
	p := New(nil, fx.store, nil)
	var tally backfillTally
	sem := make(chan struct{}, 8)

	if err := p.backfillBook(context.Background(), fx.books[0], &tally, sem, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("backfillBook: %v", err)
	}
	if got := peak.Load(); got < 2 {
		t.Errorf("peak in-flight fpcalc for one 16-file book = %d; its files ran sequentially", got)
	}
	if got := peak.Load(); got > 8 {
		t.Errorf("peak in-flight = %d exceeds the semaphore of 8", got)
	}
	if got := tally.fingerprinted.Load(); got != 16 {
		t.Errorf("fingerprinted = %d, want 16", got)
	}
}

// The semaphore is shared across every book in the run, so book-level and
// file-level parallelism together never exceed FP_PARALLEL_WORKERS.
func TestBackfillPages_SharedSemaphoreBoundsTheWholeRun(t *testing.T) {
	setWorkers(t, 3)
	peak := inFlightFake(t)
	fx := newPagedFixture(6, 6, eligibleFiles(t))
	p := New(nil, fx.store, nil)
	var tally backfillTally

	if err := p.backfillPages(context.Background(), &captureReporter{}, BackfillParams{}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	if got := peak.Load(); got > 3 {
		t.Errorf("peak in-flight fpcalc = %d with FP_PARALLEL_WORKERS=3", got)
	}
	if got := tally.fingerprinted.Load(); got != 36 {
		t.Errorf("fingerprinted = %d, want 36", got)
	}
}

// TestBackfillTally_IsExactUnderConcurrency runs the real page options with
// the pool on and checks no outcome went missing to a lost update.
func TestBackfillTally_IsExactUnderConcurrency(t *testing.T) {
	const nBooks, filesPerBook = 240, 3
	fx := newPagedFixture(nBooks, filesPerBook, doneFile)
	p := New(nil, fx.store, nil)
	var tally backfillTally

	if opts := backfillRunOptions(0, nBooks, &tally); opts.Concurrency < 2 {
		t.Fatalf("backfillRunOptions produced Concurrency=%d; the op is sequential and this test is vacuous", opts.Concurrency)
	}
	if err := p.backfillPages(context.Background(), &captureReporter{}, BackfillParams{}, &tally); err != nil {
		t.Fatalf("backfillPages: %v", err)
	}
	if got, want := tally.skipped.Load(), int64(nBooks*filesPerBook); got != want {
		t.Errorf("skipped tally = %d, want %d — %d outcomes were lost to concurrent increments", got, want, want-got)
	}
}

// Ineligible outcomes used to fall through backfillBook's switch uncounted.
func TestBackfillTally_RecordsIneligibleReasons(t *testing.T) {
	fx := newPagedFixture(1, 3, func(bookID string, j int) database.BookFile {
		f := database.BookFile{ID: fmt.Sprintf("f%d", j), BookID: bookID, FilePath: "/x/a.mp3"}
		switch j {
		case 0:
			f.Missing = true
		case 1:
			f.FilePath = "/x/a.pdf"
		case 2:
			f.AcoustIDFingerprintDurationSec = 1
		}
		return f
	})
	p := New(nil, fx.store, nil)
	var tally backfillTally
	if err := p.backfillBook(context.Background(), fx.books[0], &tally, make(chan struct{}, 2), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if got := tally.ineligible.Load(); got != 2 {
		t.Errorf("ineligible = %d, want 2", got)
	}
	want := map[string]int64{"marked_missing": 1, "non_audio_ext": 1}
	if got := tally.reasonCounts(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
}

func TestBackfillWorkers_ReadsTheSharedKnob(t *testing.T) {
	setWorkers(t, 12)
	if got := backfillWorkers(); got != 12 {
		t.Errorf("backfillWorkers() = %d, want 12", got)
	}
	for _, bad := range []int{0, -1, 33} {
		config.AppConfig.FPParallelWorkers = bad
		if got := backfillWorkers(); got != 4 {
			t.Errorf("backfillWorkers() with FPParallelWorkers=%d = %d, want the default 4", bad, got)
		}
	}
}

// fix (e): an unconfigured deployment must keep fpcalc's 120 s window, the
// one every stored fingerprint was made with.
func TestFingerprintLengthSec_DefaultReproducesStoredPrints(t *testing.T) {
	orig := config.AppConfig.FingerprintLengthSec
	t.Cleanup(func() { config.AppConfig.FingerprintLengthSec = orig })

	// A negative value must NOT reach whole-file mode (-length 0): that is
	// ~80x the work and an owner decision, not a config typo.
	// Above the cap is clamped to it, so a typo cannot reach near-whole-file
	// decode work either.
	for cfg, want := range map[int]int{0: 120, 120: 120, 300: 300, 600: 600, 601: 600, 12000: 600, -1: fingerprint.DefaultAnalysisLengthSec} {
		config.AppConfig.FingerprintLengthSec = cfg
		if got := fingerprintLengthSec(); got != want {
			t.Errorf("fingerprint_length_sec=%d → -length %d, want %d", cfg, got, want)
		}
	}
}

// fix (d): the def no longer carries the cron string nothing read, but keeps
// the one effect it had — EnqueueOp deduping a second request on def id.
func TestBackfillDef_NoDeadScheduleNoSilentDedupe(t *testing.T) {
	def := (&Plugin{}).backfillDef()
	if def.Schedule != nil {
		t.Errorf("Schedule = %q; OperationDef.Schedule is never evaluated — schedule via the TaskScheduler", *def.Schedule)
	}
	if def.DedupeQueuedRuns {
		t.Error("DedupeQueuedRuns is an audited opt-in (op_dedupe_decision_test.go); acoustid.backfill is not on that list")
	}
	if def.ConcurrencyKey == "" {
		t.Error("ConcurrencyKey is empty: a second backfill would run concurrently with the first")
	}
}
