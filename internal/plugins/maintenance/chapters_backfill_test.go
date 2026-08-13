// file: internal/plugins/maintenance/chapters_backfill_test.go
// version: 1.1.0
// guid: 8a41c0e6-52b7-4d93-9f18-7c3ea05b61d4
// last-edited: 2026-08-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ── fixtures ────────────────────────────────────────────────────────────────
//
// Helper names are prefixed chbf* because this package's tests share one
// namespace and a generic name (seedBook, fakeProbe) collides with a sibling
// file's helper the moment two are added in the same week.

// chbfChapters is the probe result the fake returns for a book WITH markers.
// Six chapters on a cumulative timeline, mirroring the shape a real m4b yields.
func chbfChapters() []audioutil.Chapter {
	out := make([]audioutil.Chapter, 6)
	var start float64
	for i := range out {
		end := start + float64(600+i*30)
		out[i] = audioutil.Chapter{
			ID: i, StartSec: start, EndSec: end, Title: fmt.Sprintf("Chapter %d", i+1),
		}
		start = end
	}
	return out
}

// chbfProbeSpy replaces probeChaptersFn and records what it was asked to probe.
// Counting is atomic because RunItems fans out over runtime.NumCPU() workers.
type chbfProbeSpy struct {
	calls  atomic.Int64
	result []audioutil.Chapter
	err    error
}

func (s *chbfProbeSpy) install(t *testing.T) {
	t.Helper()
	prev := probeChaptersFn
	probeChaptersFn = func(_ context.Context, _, _ string) ([]audioutil.Chapter, error) {
		s.calls.Add(1)
		return s.result, s.err
	}
	t.Cleanup(func() { probeChaptersFn = prev })
}

// chbfStubFFprobe makes lookupFFprobeFn succeed without a real binary on PATH,
// so these tests do not depend on the host having ffmpeg installed.
func chbfStubFFprobe(t *testing.T) {
	t.Helper()
	prev := lookupFFprobeFn
	lookupFFprobeFn = func() (string, error) { return "/usr/bin/ffprobe", nil }
	t.Cleanup(func() { lookupFFprobeFn = prev })
}

// chbfSeedBook creates a book with nFiles book_file rows and returns its ID.
func chbfSeedBook(t *testing.T, s *database.PebbleStore, title string, nFiles int) string {
	t.Helper()
	bk, err := s.CreateBook(&database.Book{Title: title, FilePath: "/lib/" + title})
	if err != nil {
		t.Fatalf("CreateBook(%s): %v", title, err)
	}
	for i := 0; i < nFiles; i++ {
		bf := &database.BookFile{
			BookID:   bk.ID,
			FilePath: fmt.Sprintf("/lib/%s/track%02d.m4b", title, i),
		}
		if err := s.CreateBookFile(bf); err != nil {
			t.Fatalf("CreateBookFile(%s/%d): %v", title, i, err)
		}
	}
	return bk.ID
}

// chbfDecorator mirrors *server.indexedStore: it embeds database.Store and opts
// into the unwrap contract, so ONLY the methods on database.Store are promoted.
// The chapter methods are not on that interface, which means a bare
// `store.(chapterPersister)` fails through this wrapper exactly as it does in
// production.
//
// 🔴 EVERY TEST HERE RUNS THROUGH THIS WRAPPER, deliberately. The op originally
// shipped with a bare assertion and a green suite, because the suite handed it a
// raw *PebbleStore that production never uses — the decorator is installed at
// server_lifecycle.go:290. The first real run refused outright. Testing against
// the undecorated store is what made that possible, so the undecorated path is
// no longer reachable from these tests.
type chbfDecorator struct {
	database.Store
}

func (d *chbfDecorator) Unwrap() database.Store { return d.Store }

// Compile-time proof the wrapper advertises the unwrap capability, the same
// guard indexed_store.go carries.
var _ database.StoreUnwrapper = (*chbfDecorator)(nil)

// chbfRun executes the op against store with the given params, THROUGH the
// production-shaped decorator.
func chbfRun(t *testing.T, s *database.PebbleStore, params chaptersBackfillParams) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	p := &Plugin{deps: fakeDeps{store: &chbfDecorator{Store: s}}}
	return p.runChaptersBackfill(context.Background(), raw, &fakeReporter{})
}

func chbfStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}

// ── case 1: the happy path, asserted by VALUE not by count ──────────────────
//
// Asserting only len(chapters) > 1 would pass against a handler that wrote six
// zero-length chapters, which is the exact class of "plausible but wrong" this
// op exists to remove. Every field of every chapter is compared to what the
// probe returned.
func TestChaptersBackfill_SingleFileWithMarkers_PersistsVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	id := chbfSeedBook(t, s, "Deadly Jobs", 1)

	if err := chbfRun(t, s, chaptersBackfillParams{Apply: true}); err != nil {
		t.Fatalf("runChaptersBackfill: %v", err)
	}

	got, err := s.GetChaptersForBook(id)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	want := chbfChapters()
	if len(got) != len(want) {
		t.Fatalf("persisted %d chapters, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID ||
			got[i].StartSec != want[i].StartSec ||
			got[i].EndSec != want[i].EndSec ||
			got[i].Title != want[i].Title {
			t.Fatalf("chapter %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The timeline must be cumulative and gapless — a chapter list that does not
	// chain is unnavigable even when every individual row looks reasonable.
	for i := 1; i < len(got); i++ {
		if got[i].StartSec != got[i-1].EndSec {
			t.Fatalf("chapter %d starts at %v but %d ended at %v — timeline has a gap",
				i, got[i].StartSec, i-1, got[i-1].EndSec)
		}
	}
}

// ── case 2: dry run writes NOTHING ──────────────────────────────────────────
func TestChaptersBackfill_DryRunPersistsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	id := chbfSeedBook(t, s, "Dry Run Book", 1)

	if err := chbfRun(t, s, chaptersBackfillParams{Apply: false}); err != nil {
		t.Fatalf("runChaptersBackfill: %v", err)
	}

	// The probe MUST have run — a dry run that silently skipped the measurement
	// would report "0 would persist" and look identical to a clean library.
	if spy.calls.Load() == 0 {
		t.Fatal("dry run never probed anything, so its report measures nothing")
	}
	got, err := s.GetChaptersForBook(id)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dry run persisted %d chapters; it must write nothing", len(got))
	}
}

// ── case 3: already-extracted books are not re-probed ───────────────────────
//
// Asserted on the PROBE COUNT, not on the stored value. Checking only that the
// chapters are unchanged would also pass if the op re-probed every book on
// every run and happened to write the same bytes back — which is the expensive
// bug, not the cheap one.
func TestChaptersBackfill_AlreadyPersisted_SkipsWithoutProbing(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	id := chbfSeedBook(t, s, "Already Done", 1)
	seeded := []database.Chapter{{ID: 0, StartSec: 0, EndSec: 42, Title: "pre-existing"}}
	if err := s.SaveChaptersForBook(id, seeded); err != nil {
		t.Fatalf("SaveChaptersForBook: %v", err)
	}

	if err := chbfRun(t, s, chaptersBackfillParams{Apply: true}); err != nil {
		t.Fatalf("runChaptersBackfill: %v", err)
	}

	if n := spy.calls.Load(); n != 0 {
		t.Fatalf("probed %d times for a book that already had chapters; want 0", n)
	}
	got, err := s.GetChaptersForBook(id)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(got) != 1 || got[0].Title != "pre-existing" {
		t.Fatalf("existing chapters were overwritten: %+v", got)
	}
}

// ── case 4: no markers → nothing written, AND the re-probe is pinned ────────
//
// The second half of this test documents an accepted trade-off rather than a
// desirable behaviour: SaveChaptersForBook deletes on an empty slice, so
// "probed, found none" is byte-identical to "never probed" and a second run
// pays the probe again. If someone later wires a durable freshness stamp, this
// assertion is the one that must change — and it will fail loudly rather than
// letting the stamp go silently unused.
func TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: nil} // a container with no embedded markers
	spy.install(t)

	s := chbfStore(t)
	id := chbfSeedBook(t, s, "No Markers", 1)

	if err := chbfRun(t, s, chaptersBackfillParams{Apply: true}); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	got, err := s.GetChaptersForBook(id)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("persisted %d chapters for a book with no markers; want 0", len(got))
	}
	if spy.calls.Load() != 1 {
		t.Fatalf("run 1 probed %d times, want exactly 1", spy.calls.Load())
	}

	if err := chbfRun(t, s, chaptersBackfillParams{Apply: true}); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if n := spy.calls.Load(); n != 2 {
		t.Fatalf("after a second run the probe count is %d, want 2 — the accepted "+
			"re-probe of markerless books is no longer happening; if a durable "+
			"'probed, found none' marker was added, update this test to assert it", n)
	}
}

// ── case 5: multi-file books are skipped entirely ───────────────────────────
//
// Pins the scoping decision. A multi-file book's persisted chapters would be
// byte-identical to the mapper's live synthesis, so writing them changes no
// response while creating a staleness hazard the read path cannot detect.
// Asserted on the probe count so a future change that merely declines to WRITE
// (while still paying the ffprobe) also fails here.
func TestChaptersBackfill_MultiFileBook_NeverProbedOrWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	multi := chbfSeedBook(t, s, "Multi File", 6)
	// A single-file control in the same run: without it, a bug that skipped
	// EVERY book would pass this test.
	single := chbfSeedBook(t, s, "Single Control", 1)

	if err := chbfRun(t, s, chaptersBackfillParams{Apply: true}); err != nil {
		t.Fatalf("runChaptersBackfill: %v", err)
	}

	if got, _ := s.GetChaptersForBook(multi); len(got) != 0 {
		t.Fatalf("multi-file book got %d persisted chapters; want 0", len(got))
	}
	if got, _ := s.GetChaptersForBook(single); len(got) != 6 {
		t.Fatalf("single-file control got %d chapters, want 6 — the op skipped "+
			"everything, so the multi-file assertion above proves nothing", len(got))
	}
	if n := spy.calls.Load(); n != 1 {
		t.Fatalf("probe ran %d times; want exactly 1 (the single-file control only) "+
			"— the multi-file book is paying for an ffprobe it never uses", n)
	}
}

// ── case 6b: the decorator chain is walked, not asserted through ────────────
//
// 🔴 THE BUG THIS PINS SHIPPED. Production wraps the store in
// *server.indexedStore (server_lifecycle.go:290), which embeds database.Store and
// therefore promotes only that interface's methods. The chapter methods are not
// on it, so the op's original bare `store.(chapterPersister)` failed at runtime
// with "store is *server.indexedStore, which does not persist chapters" — while
// every test passed, because the tests handed it a raw *PebbleStore.
//
// This test wraps the store TWICE. One layer would pass against an
// implementation that unwraps exactly once; the chain has to actually be walked.
func TestChaptersBackfill_ResolvesChapterStoreThroughDecoratorChain(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	id := chbfSeedBook(t, s, "Behind Two Wrappers", 1)

	doubled := &chbfDecorator{Store: &chbfDecorator{Store: s}}
	p := &Plugin{deps: fakeDeps{store: doubled}}
	raw, err := json.Marshal(chaptersBackfillParams{Apply: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := p.runChaptersBackfill(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("op refused to run behind a decorator chain: %v", err)
	}

	got, err := s.GetChaptersForBook(id)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("persisted %d chapters through the decorator chain, want 6", len(got))
	}
}

// ── case 6: a missing ffprobe is a hard failure, not a clean run ────────────
func TestChaptersBackfill_NoFFprobe_RefusesToRun(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	prev := lookupFFprobeFn
	lookupFFprobeFn = func() (string, error) { return "", audioutil.ErrFFprobeNotAvailable }
	t.Cleanup(func() { lookupFFprobeFn = prev })

	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	chbfSeedBook(t, s, "Unreachable", 1)

	err := chbfRun(t, s, chaptersBackfillParams{Apply: true})
	if err == nil {
		t.Fatal("op reported success without ffprobe — a run that could not measure " +
			"anything must not look like a library with no markers")
	}
	if spy.calls.Load() != 0 {
		t.Fatalf("probed %d times after the ffprobe lookup failed", spy.calls.Load())
	}
}

// ── case 7: BookIDs restricts the run to an explicit cohort ─────────────────
//
// This is how the op is exercised against the 'job' test cohort before any
// whole-library pass, so the restriction is asserted rather than assumed.
func TestChaptersBackfill_BookIDsRestrictsScope(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	chbfStubFFprobe(t)
	spy := &chbfProbeSpy{result: chbfChapters()}
	spy.install(t)

	s := chbfStore(t)
	inScope := chbfSeedBook(t, s, "In Cohort", 1)
	outOfScope := chbfSeedBook(t, s, "Out Of Cohort", 1)

	if err := chbfRun(t, s, chaptersBackfillParams{
		Apply: true, BookIDs: []string{inScope},
	}); err != nil {
		t.Fatalf("runChaptersBackfill: %v", err)
	}

	if got, _ := s.GetChaptersForBook(inScope); len(got) != 6 {
		t.Fatalf("in-scope book got %d chapters, want 6", len(got))
	}
	if got, _ := s.GetChaptersForBook(outOfScope); len(got) != 0 {
		t.Fatalf("out-of-scope book got %d chapters; the BookIDs restriction leaked", len(got))
	}
}
