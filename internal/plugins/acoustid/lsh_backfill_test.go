// file: internal/plugins/acoustid/lsh_backfill_test.go
// version: 1.3.0
// guid: 3d5e7f91-4c6b-5a0d-ac2e-8f9a1b3c5d7e
// last-edited: 2026-08-21

package acoustid

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- test reporter --------------------------------------------------------

type lshFrame struct {
	current int
	total   int
	message string
}

type lshTestReporter struct {
	mu     sync.Mutex
	frames []lshFrame
}

func (r *lshTestReporter) UpdateProgress(current, total int, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, lshFrame{current, total, message})
	return nil
}
func (r *lshTestReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *lshTestReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *lshTestReporter) Checkpoint(any) error                       { return nil }
func (r *lshTestReporter) IsCanceled() bool                           { return false }
func (r *lshTestReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, r)
}
func (r *lshTestReporter) Trigger(context.Context, string, any) error { return nil }
func (r *lshTestReporter) SetCurrentItem(string)                      {}

// --- store with optional HasLSHIndex --------------------------------------

// indexableMockStore wraps a MockStore and also implements the
// lshIndexChecker interface — used to exercise the fast-skip path.
type indexableMockStore struct {
	*database.MockStore
	indexed map[string]bool
}

func (i *indexableMockStore) HasLSHIndex(id string) bool {
	return i.indexed[id]
}

// unwrappingStore is a minimal StoreUnwrapper-implementing decorator around
// pluginStore — it forwards every method to the wrapped store and only opts
// into the database.AsCapability lookup via Unwrap(). Models a real
// production decorator (e.g. the search-index store) sitting in front of
// p.store.
type unwrappingStore struct {
	pluginStore
	inner database.Store
}

func (u *unwrappingStore) Unwrap() database.Store { return u.inner }

// --- tests ---------------------------------------------------------------

// TestLSHBackfill_FiltersAndUpdates verifies that the op processes only the
// rows with a stored AcoustIDFingerprint and skips the rest. Five book files:
// three with fingerprints, two without — exactly three UpdateBookFile calls
// should fire.
func TestLSHBackfill_FiltersAndUpdates(t *testing.T) {
	files := []database.BookFileCore{
		{ID: "f1", BookID: "b1", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f2", BookID: "b2"}, // no fp
		{ID: "f3", BookID: "b3", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f4", BookID: "b4"}, // no fp
		{ID: "f5", BookID: "b5", AcoustIDFingerprintDurationSec: 1800},
	}
	// hydrateFiles simulates GetBookFiles(bookID) — the full-row read the op
	// now hydrates from before writing back (see the writeback-wipe audit
	// doc). Keyed by BookID.
	hydrateFiles := map[string][]database.BookFile{
		"b1": {{ID: "f1", BookID: "b1", AcoustIDFingerprint: []byte{0xde, 0xad, 0xbe, 0xef}}},
		"b3": {{ID: "f3", BookID: "b3", AcoustIDFingerprint: []byte{0xfe, 0xed, 0xfa, 0xce}}},
		"b5": {{ID: "f5", BookID: "b5", AcoustIDFingerprint: []byte{0xca, 0xfe, 0xba, 0xbe}}},
	}

	var (
		mu      sync.Mutex
		updates []string
	)
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return hydrateFiles[bookID], nil
		},
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, id)
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("runLSHBackfill returned error: %v", err)
	}

	if got, want := len(updates), 3; got != want {
		t.Fatalf("UpdateBookFile calls = %d, want %d (%v)", got, want, updates)
	}
	wantIDs := map[string]bool{"f1": true, "f3": true, "f5": true}
	for _, id := range updates {
		if !wantIDs[id] {
			t.Errorf("unexpected update for id %q", id)
		}
	}

	// Progress invariants: at least Start + Done frames; final frame at
	// (total, total) where total is n+2. We never want a 0/0 frame.
	if len(r.frames) < 2 {
		t.Fatalf("expected at least 2 progress frames, got %d", len(r.frames))
	}
	last := r.frames[len(r.frames)-1]
	if last.total == 0 || last.current != last.total {
		t.Errorf("final frame not Done: %+v", last)
	}
	for _, f := range r.frames {
		if f.total == 0 {
			t.Errorf("0/0 progress frame leaked: %+v", f)
		}
	}
}

// TestLSHBackfill_IdempotentWithHasLSHIndex verifies that when the store
// reports rows are already indexed, the op makes zero UpdateBookFile calls.
// Models the second-run case after a previous successful backfill.
func TestLSHBackfill_IdempotentWithHasLSHIndex(t *testing.T) {
	files := []database.BookFileCore{
		{ID: "f1", BookID: "b1", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f2", BookID: "b2", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f3", BookID: "b3", AcoustIDFingerprintDurationSec: 1800},
	}

	updateCalls := 0
	mock := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(string, *database.BookFile) error {
			updateCalls++
			return nil
		},
	}
	store := &indexableMockStore{
		MockStore: mock,
		indexed:   map[string]bool{"f1": true, "f2": true, "f3": true},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if updateCalls != 0 {
		t.Fatalf("idempotent run still called UpdateBookFile %d times", updateCalls)
	}

	// Run twice — should still be zero.
	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if updateCalls != 0 {
		t.Fatalf("second idempotent run called UpdateBookFile %d times", updateCalls)
	}
}

// TestLSHBackfill_PartialIndex verifies that with HasLSHIndex returning true
// for some rows and false for others, only the unindexed rows with a stored
// fp are updated.
func TestLSHBackfill_PartialIndex(t *testing.T) {
	files := []database.BookFileCore{
		{ID: "f1", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f2", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f3", AcoustIDFingerprintDurationSec: 1800},
		{ID: "f4"}, // no fp at all
	}
	// hydrateFiles simulates GetBookFiles(bookID="") — none of the files set
	// BookID, so they all share the empty-string bucket; the op matches by
	// file ID within the returned slice.
	hydrateFiles := map[string][]database.BookFile{
		"": {
			{ID: "f2", AcoustIDFingerprint: []byte{2}},
			{ID: "f3", AcoustIDFingerprint: []byte{3}},
		},
	}
	var updates []string
	mock := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return hydrateFiles[bookID], nil
		},
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			updates = append(updates, id)
			return nil
		},
	}
	store := &indexableMockStore{
		MockStore: mock,
		indexed:   map[string]bool{"f1": true}, // only f1 already indexed
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("err: %v", err)
	}
	if got, want := len(updates), 2; got != want {
		t.Fatalf("updates = %v (want 2: f2,f3)", updates)
	}
}

// TestLSHBackfill_CancelMidRun verifies that cancelling the context part-way
// returns ctx.Err() and stops further updates.
func TestLSHBackfill_CancelMidRun(t *testing.T) {
	files := make([]database.BookFileCore, 100)
	hydrateFiles := make([]database.BookFile, 100)
	for i := range files {
		id := string(rune('a'+i%26)) + "-" + string(rune('0'+i%10))
		files[i] = database.BookFileCore{
			ID:                             id,
			AcoustIDFingerprintDurationSec: 1800,
		}
		hydrateFiles[i] = database.BookFile{
			ID:                  id,
			AcoustIDFingerprint: []byte{byte(i)},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	var updates int
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		// None of the files set BookID, so every hydrate call shares the
		// same full slice; the op matches by file ID within it.
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) { return hydrateFiles, nil },
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			updates++
			if updates == 5 {
				cancel()
			}
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	err := p.runLSHBackfill(ctx, nil, r)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if updates < 5 {
		t.Errorf("expected at least 5 updates before cancel, got %d", updates)
	}
	if updates >= len(files) {
		t.Errorf("expected updates < total after cancel, got %d/%d", updates, len(files))
	}
}

// TestLSHBackfill_MemdbSlimRowStillTriggersUpdate is the regression test for
// the memdb-slim no-op bug: GetAllBookFilesCore returns BookFileCore, which
// never carries the raw AcoustIDFingerprint bytes at all (heavy field,
// stripped on both the memdb and Pebble-direct paths); DurationSec is the
// KEPT proxy. Before the fix, the gate was len(AcoustIDFingerprint)==0 —
// always true — so this op silently skipped every fingerprinted file in
// prod. This test supplies exactly that shape (DurationSec>0, no blob to
// even check) and asserts UpdateBookFile still fires against the row
// hydrated via GetBookFiles.
func TestLSHBackfill_MemdbSlimRowStillTriggersUpdate(t *testing.T) {
	files := []database.BookFileCore{
		// Memdb-slim shape: duration proxy survives.
		{ID: "f1", BookID: "b1", AcoustIDFingerprintDurationSec: 1800},
		// Genuinely unfingerprinted: no duration.
		{ID: "f2", BookID: "b2"},
	}
	hydrateFiles := map[string][]database.BookFile{
		"b1": {{ID: "f1", BookID: "b1", AcoustIDFingerprint: []byte{0xAA, 0xBB}}},
	}

	var updates []string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return hydrateFiles[bookID], nil
		},
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			updates = append(updates, id)
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("runLSHBackfill: %v", err)
	}

	if got, want := len(updates), 1; got != want {
		t.Fatalf("UpdateBookFile calls = %d, want %d (%v)", got, want, updates)
	}
	if updates[0] != "f1" {
		t.Errorf("expected UpdateBookFile(f1) (memdb-slim, has fp per DurationSec proxy), got %v", updates)
	}
}

// TestLSHBackfill_EmptyStore verifies the no-rows path still emits Start +
// Done frames so the UI never sees a 0/0 bar.
func TestLSHBackfill_EmptyStore(t *testing.T) {
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return nil, nil
		},
	}
	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runLSHBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("empty store run: %v", err)
	}
	if len(r.frames) < 2 {
		t.Fatalf("expected at least 2 frames on empty run, got %d", len(r.frames))
	}
	for _, f := range r.frames {
		if f.total == 0 {
			t.Errorf("0/0 progress frame leaked on empty store: %+v", f)
		}
	}
}

// TestLSHBackfill_RegistersWithPlugin verifies the def is hooked into the
// plugin's op list via Register.
func TestLSHBackfill_RegistersWithPlugin(t *testing.T) {
	// Direct check: lshBackfillDef returns the expected ID + capabilities.
	p := &Plugin{}
	def := p.lshBackfillDef()
	if def.ID != "acoustid.lsh-backfill" {
		t.Errorf("ID = %q, want acoustid.lsh-backfill", def.ID)
	}
	if def.Plugin != "acoustid" {
		t.Errorf("Plugin = %q, want acoustid", def.Plugin)
	}
	if !def.Cancellable {
		t.Error("op should be cancellable")
	}
	if def.ResumePolicy != sdk.ResumeDrop {
		t.Errorf("ResumePolicy = %v, want ResumeDrop", def.ResumePolicy)
	}
	hasRead, hasWrite := false, false
	for _, c := range def.Capabilities {
		if c == sdk.CapLibraryRead {
			hasRead = true
		}
		if c == sdk.CapLibraryWrite {
			hasWrite = true
		}
	}
	if !hasRead || !hasWrite {
		t.Errorf("capabilities missing read/write: %v", def.Capabilities)
	}
}

// TestLSHBackfill_HasLSHIndexThroughDecorator verifies the fast-skip check
// keeps working when p.store is a decorator wrapping the real capable store,
// not just when the capability sits on the outermost value. Regression guard
// for routing the lshIndexChecker lookup through database.AsCapability
// instead of a bare type assertion — the bare assertion silently disables
// the fast-skip (and re-writes every row) the moment p.store is wrapped.
func TestLSHBackfill_HasLSHIndexThroughDecorator(t *testing.T) {
	// runDecorated wires two eligible rows behind a decorator and reports how
	// many of them got re-saved. GetBookFiles must be stubbed: the op hydrates
	// the full row before updating, and without it every row dies at
	// "hydrate: row not found" and the update count is 0 no matter what the
	// fast-skip did.
	runDecorated := func(t *testing.T, indexed map[string]bool) int {
		t.Helper()
		files := []database.BookFileCore{
			{ID: "f1", BookID: "b1", AcoustIDFingerprintDurationSec: 1800},
			{ID: "f2", BookID: "b2", AcoustIDFingerprintDurationSec: 1800},
		}
		updateCalls := 0
		mock := &database.MockStore{
			GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
				return files, nil
			},
			GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
				for _, f := range files {
					if f.BookID == bookID {
						return []database.BookFile{{ID: f.ID, BookID: f.BookID}}, nil
					}
				}
				return nil, nil
			},
			UpdateBookFileFunc: func(string, *database.BookFile) error {
				updateCalls++
				return nil
			},
		}
		inner := &indexableMockStore{MockStore: mock, indexed: indexed}
		p := &Plugin{store: &unwrappingStore{pluginStore: inner, inner: inner}}
		if err := p.runLSHBackfill(context.Background(), nil, &lshTestReporter{}); err != nil {
			t.Fatalf("runLSHBackfill: %v", err)
		}
		return updateCalls
	}

	// Instrument check FIRST. "0 updates" is only evidence that the fast-skip
	// fired if this same harness can produce updates at all; without this the
	// assertion below passes against a hydrate failure, a filtered-out row, or
	// a typo in the fixture. Do not delete it to "simplify" the test.
	if got := runDecorated(t, map[string]bool{}); got != 2 {
		t.Fatalf("instrument check: with nothing pre-indexed the harness must re-save both rows, got %d updates, want 2", got)
	}

	// The regression. Under a bare `p.store.(lshIndexChecker)` the decorator
	// hides HasLSHIndex, the fast-skip silently disables itself, and both rows
	// are re-written.
	if got := runDecorated(t, map[string]bool{"f1": true, "f2": true}); got != 0 {
		t.Fatalf("fast-skip through decorator did not activate: UpdateBookFile called %d times, want 0", got)
	}
}
