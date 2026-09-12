// file: internal/plugins/maintenance/tag_backfill_test.go
// version: 1.4.0
// guid: 5b6e7f4a-9c1d-4e0a-8f2b-3a6d1c9e5b70
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fakeTagExtractor is a deterministic, goroutine-safe metadata.MetadataExtractor
// stub used so TestTagBackfill_ParallelProducesSameResultAsSerial doesn't need
// real audio fixtures. Classifies files by a marker in their path:
//   - path contains "readerr" -> returns an error (readErr path)
//   - path contains "notags"  -> returns Metadata with no AllTags (skip path)
//   - otherwise               -> returns deterministic non-empty tags
type fakeTagExtractor struct {
	mu    sync.Mutex
	calls int
}

func (e *fakeTagExtractor) ExtractMetadata(filePath string) (metadata.Metadata, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()

	base := filepath.Base(filePath)
	switch {
	case strings.Contains(filePath, "readerr"):
		return metadata.Metadata{}, fmt.Errorf("simulated tag read failure for %s", base)
	case strings.Contains(filePath, "notags"):
		return metadata.Metadata{}, nil // AllTags empty -> "nothing capturable"
	default:
		return metadata.Metadata{
			Title:       "Title-" + base,
			TrackNumber: 3,
			TrackTotal:  12,
			DiscNumber:  1,
			DiscTotal:   2,
			AllTags:     map[string]string{"title": "Title-" + base, "artist": "Author"},
		}, nil
	}
}

// mapTagExtractor returns a fixed Metadata per file base name. The map is
// read-only during a run, so concurrent workers can share it. hook, when set,
// runs before each read (the cancel test uses it to cancel mid-run).
type mapTagExtractor struct {
	byBase map[string]metadata.Metadata
	hook   func(base string)
}

func (e *mapTagExtractor) ExtractMetadata(filePath string) (metadata.Metadata, error) {
	base := filepath.Base(filePath)
	if e.hook != nil {
		e.hook(base)
	}
	m, ok := e.byBase[base]
	if !ok {
		return metadata.Metadata{}, fmt.Errorf("no fixture for %s", filePath)
	}
	return m, nil
}

// tagMeta builds a fixture tag reading with a non-empty AllTags map that carries
// the same position (TRCK, and TPOS when there is a disc), as real capture does.
func tagMeta(track, trackTotal, disc, discTotal int) metadata.Metadata {
	all := map[string]string{"TRCK": fmt.Sprintf("%d/%d", track, trackTotal), "TALB": "Book"}
	if disc > 0 {
		all["TPOS"] = fmt.Sprintf("%d/%d", disc, discTotal)
	}
	return metadata.Metadata{
		Title:       fmt.Sprintf("Tag Title %d-%d", disc, track),
		TrackNumber: track,
		TrackTotal:  trackTotal,
		DiscNumber:  disc,
		DiscTotal:   discTotal,
		AllTags:     all,
	}
}

// tagFixture is a stateful MockStore: GetBookFiles filters by BookID (a mock
// that returned every file for any book would make the per-book guard
// meaningless) and BatchUpsertBookFiles writes back, so a second run sees what
// the first one wrote.
type tagFixture struct {
	t         *testing.T
	mu        sync.Mutex
	files     []database.BookFile
	batches   [][]*database.BookFile
	hydrates  atomic.Int32
	extractor *mapTagExtractor
	store     *database.MockStore
}

// newTagFixture defaults each FilePath into a temp dir and writes a placeholder
// on disk for every file with a tag fixture.
func newTagFixture(t *testing.T, files []database.BookFile, tags map[string]metadata.Metadata) *tagFixture {
	t.Helper()
	dir := t.TempDir()
	fx := &tagFixture{t: t, files: files, extractor: &mapTagExtractor{byBase: tags}}
	for i := range fx.files {
		if fx.files[i].FilePath == "" {
			fx.files[i].FilePath = filepath.Join(dir, fx.files[i].ID+".mp3")
		}
		if _, ok := tags[fx.files[i].ID+".mp3"]; ok {
			mustWriteFile(t, fx.files[i].FilePath)
		}
	}
	metadata.SetMetadataExtractor(fx.extractor)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	fx.store = &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			core := make([]database.BookFileCore, len(fx.files))
			for i := range fx.files {
				core[i] = fx.files[i].Core()
			}
			return core, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			fx.hydrates.Add(1)
			fx.mu.Lock()
			defer fx.mu.Unlock()
			var out []database.BookFile
			for _, f := range fx.files {
				if f.BookID == bookID {
					out = append(out, f)
				}
			}
			return out, nil
		},
		BatchUpsertBookFilesFunc: func(batch []*database.BookFile) error {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.batches = append(fx.batches, append([]*database.BookFile(nil), batch...))
			for _, u := range batch {
				for i := range fx.files {
					if fx.files[i].ID == u.ID {
						fx.files[i] = *u
					}
				}
			}
			return nil
		},
	}
	return fx
}

func (fx *tagFixture) path(id string) string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	for _, f := range fx.files {
		if f.ID == id {
			return f.FilePath
		}
	}
	fx.t.Fatalf("no fixture row %s", id)
	return ""
}

func (fx *tagFixture) row(id string) database.BookFile {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	for _, f := range fx.files {
		if f.ID == id {
			return f
		}
	}
	fx.t.Fatalf("no fixture row %s", id)
	return database.BookFile{}
}

// writtenIDs is every row ID any batch has written so far.
func (fx *tagFixture) writtenIDs() map[string]bool {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	out := map[string]bool{}
	for _, b := range fx.batches {
		for _, u := range b {
			out[u.ID] = true
		}
	}
	return out
}

// runErr runs the op and returns the reporter logs (summary last) and its error.
func (fx *tagFixture) runErr(ctx context.Context, params tagBackfillParams) ([]string, error) {
	fx.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		fx.t.Fatalf("marshal params: %v", err)
	}
	reporter := &fakeReporter{}
	err = New(fakeDeps{store: fx.store}).runTagBackfill(ctx, raw, reporter)
	return reporter.logs, err
}

// run runs the op, fails on error, and returns the summary line.
func (fx *tagFixture) run(params tagBackfillParams) string {
	fx.t.Helper()
	logs, err := fx.runErr(context.Background(), params)
	if err != nil {
		fx.t.Fatalf("runTagBackfill: %v", err)
	}
	if len(logs) == 0 {
		fx.t.Fatalf("no summary logged")
	}
	return logs[len(logs)-1]
}

func assertSummaryHas(t *testing.T, summary string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(summary, p) {
			t.Errorf("summary missing %q:\n%s", p, summary)
		}
	}
}

func assertPosition(t *testing.T, f database.BookFile, disc, track int) {
	t.Helper()
	if f.DiscNumber != disc || f.TrackNumber != track {
		t.Errorf("%s: disc/track = %d/%d, want %d/%d", f.ID, f.DiscNumber, f.TrackNumber, disc, track)
	}
}

// TestTagBackfill_ParallelProducesSameResultAsSerial exercises the
// registry.RunItems-based parallel loop (CONC-7) across a mix of
// skip-already-tagged / skip-empty-path / missing-on-disk / read-error /
// no-tags / needs-backfill BookFiles, and asserts the write set produced is
// exactly the set that a serial pass would have produced — independent of
// goroutine completion order, since RunItems runs with Concurrency ==
// runtime.NumCPU()*4 (I/O-bound sizing). Every fixture is its own single-file
// book, so the track guard accepts each tag. Run with -race to catch data races
// on the shared counters and the pending write batch.
func TestTagBackfill_ParallelProducesSameResultAsSerial(t *testing.T) {
	dir := t.TempDir()

	extractor := &fakeTagExtractor{}
	metadata.SetMetadataExtractor(extractor)
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	var files []database.BookFile
	wantBackfilled := map[string]bool{}
	add := func(f database.BookFile) {
		f.BookID = "book-" + f.ID
		files = append(files, f)
	}

	// Already has RawTags, Force=false -> must be skipped untouched (path is
	// never created on disk; if the code statted/opened it, any regression that
	// drops the skip would surface as a missing/readErr count mismatch below).
	for i := range 8 {
		id := fmt.Sprintf("skip-has-tags-%d", i)
		add(database.BookFile{ID: id, FilePath: filepath.Join(dir, id+".mp3"), RawTags: map[string]string{"title": "already tagged"}})
	}
	// Empty FilePath -> skipped.
	for i := range 5 {
		add(database.BookFile{ID: fmt.Sprintf("skip-empty-path-%d", i)})
	}
	// Path does not exist on disk -> missing.
	for i := range 6 {
		id := fmt.Sprintf("missing-%d", i)
		add(database.BookFile{ID: id, FilePath: filepath.Join(dir, "nope", id+".mp3")})
	}
	// Exists but the extractor errors on it -> readErr.
	for i := range 4 {
		id := fmt.Sprintf("readerr-%d", i)
		p := filepath.Join(dir, "readerr", id+".mp3")
		mustWriteFile(t, p)
		add(database.BookFile{ID: id, FilePath: p})
	}
	// Exists but the extractor finds no tags -> skipped ("nothing capturable").
	for i := range 3 {
		id := fmt.Sprintf("notags-%d", i)
		p := filepath.Join(dir, "notags", id+".mp3")
		mustWriteFile(t, p)
		add(database.BookFile{ID: id, FilePath: p})
	}
	// Exists and should be successfully backfilled.
	for i := range 25 {
		id := fmt.Sprintf("needs-backfill-%d", i)
		p := filepath.Join(dir, id+".mp3")
		mustWriteFile(t, p)
		add(database.BookFile{ID: id, FilePath: p})
		wantBackfilled[id] = true
	}

	core := make([]database.BookFileCore, len(files))
	for i := range files {
		core[i] = files[i].Core()
	}
	var (
		upsertMu      sync.Mutex
		upserted      []*database.BookFile
		upsertBatches int
	)
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return core, nil },
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			var out []database.BookFile
			for _, f := range files {
				if f.BookID == bookID {
					out = append(out, f)
				}
			}
			return out, nil
		},
		BatchUpsertBookFilesFunc: func(batch []*database.BookFile) error {
			upsertMu.Lock()
			upserted = append(upserted, batch...)
			upsertBatches++
			upsertMu.Unlock()
			return nil
		},
	}

	reporter := &fakeReporter{}
	raw, err := json.Marshal(tagBackfillParams{DryRun: false})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := New(fakeDeps{store: store}).runTagBackfill(context.Background(), raw, reporter); err != nil {
		t.Fatalf("runTagBackfill: %v", err)
	}
	if upsertBatches == 0 {
		t.Fatalf("expected at least one BatchUpsertBookFiles call")
	}

	gotByID := map[string]*database.BookFile{}
	for _, f := range upserted {
		gotByID[f.ID] = f
	}
	if len(gotByID) != len(wantBackfilled) {
		var gotList []string
		for id := range gotByID {
			gotList = append(gotList, id)
		}
		sort.Strings(gotList)
		t.Fatalf("upserted %d distinct files, want %d\n got=%v", len(gotByID), len(wantBackfilled), gotList)
	}
	for id := range wantBackfilled {
		if _, ok := gotByID[id]; !ok {
			t.Errorf("expected %s to be upserted, but it was not", id)
		}
	}
	for id, f := range gotByID {
		if len(f.RawTags) == 0 {
			t.Errorf("%s: expected RawTags to be backfilled", id)
		}
		if f.TrackNumber != 3 || f.TrackCount != 12 || f.DiscNumber != 1 || f.DiscCount != 2 {
			t.Errorf("%s: single-file book should take its tag values: %+v", id, f)
		}
	}
	summary := reporter.logs[len(reporter.logs)-1]
	assertSummaryHas(t, summary, "examined=51", "judged-rows=25", "read-took-tag-tracks=25", "read-rawtags-only=0",
		"siblings-renumbered=0", "missing-on-disk=6", "read-errors=4")
}

// (a) Every file tagged track 1 ("1/1"): RawTags filled, positional
// TrackNumber/TrackCount kept, empty Title filled.
func TestTagBackfill_AllTrackOneKeepsPositions(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "a1", BookID: "A", TrackNumber: 1, TrackCount: 3},
		{ID: "a2", BookID: "A", TrackNumber: 2, TrackCount: 3, Title: "Kept Title"},
		{ID: "a3", BookID: "A", TrackNumber: 3, TrackCount: 3},
	}, map[string]metadata.Metadata{
		"a1.mp3": tagMeta(1, 1, 0, 0), "a2.mp3": tagMeta(1, 1, 0, 0), "a3.mp3": tagMeta(1, 1, 0, 0),
	})
	summary := fx.run(tagBackfillParams{})
	for i, id := range []string{"a1", "a2", "a3"} {
		f := fx.row(id)
		if len(f.RawTags) == 0 {
			t.Errorf("%s: RawTags not filled", id)
		}
		if f.TrackNumber != i+1 || f.TrackCount != 3 {
			t.Errorf("%s: track = %d/%d, want %d/3 (positional order must survive)", id, f.TrackNumber, f.TrackCount, i+1)
		}
	}
	if fx.row("a1").Title != "Tag Title 0-1" || fx.row("a2").Title != "Kept Title" {
		t.Errorf("title fill wrong: a1=%q a2=%q", fx.row("a1").Title, fx.row("a2").Title)
	}
	assertSummaryHas(t, summary, "read-took-tag-tracks=0", "read-rawtags-only=3", "books-refused-duplicate=1", "books-refused-missing=0")
}

// (b) Distinct tag tracks are taken. A sibling that already has RawTags counts
// toward the judgement and, since the book is accepted, is moved to its own tag
// position too.
func TestTagBackfill_DistinctTagTracksAreTaken(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "b1", BookID: "B", TrackNumber: 1},
		{ID: "b2", BookID: "B", TrackNumber: 2},
		{ID: "b3", BookID: "B", TrackNumber: 3},
		{ID: "b4", BookID: "B", TrackNumber: 4, TrackCount: 4, RawTags: map[string]string{"trck": "1/4"}},
	}, map[string]metadata.Metadata{
		"b1.mp3": tagMeta(4, 4, 0, 0), "b2.mp3": tagMeta(2, 4, 0, 0), "b3.mp3": tagMeta(3, 4, 0, 0),
	})
	summary := fx.run(tagBackfillParams{})
	for id, want := range map[string]int{"b1": 4, "b2": 2, "b3": 3, "b4": 1} {
		if f := fx.row(id); f.TrackNumber != want || f.TrackCount != 4 {
			t.Errorf("%s: track = %d/%d, want %d/4", id, f.TrackNumber, f.TrackCount, want)
		}
	}
	assertSummaryHas(t, summary, "judged-rows=4", "read-took-tag-tracks=3", "read-rawtags-only=0", "siblings-renumbered=1", "books-refused-duplicate=0")
}

// A sibling whose stored RawTags collide with a fresh reading refuses the book.
func TestTagBackfill_TaggedSiblingCollisionRefuses(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "s1", BookID: "S", TrackNumber: 1},
		{ID: "s2", BookID: "S", TrackNumber: 2, RawTags: map[string]string{"TRCK": "1"}},
	}, map[string]metadata.Metadata{"s1.mp3": tagMeta(1, 2, 0, 0)})
	summary := fx.run(tagBackfillParams{})
	if f := fx.row("s1"); f.TrackNumber != 1 || f.TrackCount != 0 || len(f.RawTags) == 0 {
		t.Fatalf("s1 should get RawTags only, got %+v", f)
	}
	if f := fx.row("s2"); f.TrackNumber != 2 {
		t.Errorf("s2 in a refused book was renumbered to %d", f.TrackNumber)
	}
	assertSummaryHas(t, summary, "books-refused-duplicate=1", "siblings-renumbered=0")
}

// (c) One file's tag has no track number: the whole book keeps its positions.
func TestTagBackfill_MissingTrackTagKeepsPositions(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "c1", BookID: "C", TrackNumber: 1},
		{ID: "c2", BookID: "C", TrackNumber: 2},
		{ID: "c3", BookID: "C", TrackNumber: 3},
	}, map[string]metadata.Metadata{
		"c1.mp3": tagMeta(7, 9, 0, 0), "c2.mp3": tagMeta(8, 9, 0, 0), "c3.mp3": tagMeta(0, 0, 0, 0),
	})
	summary := fx.run(tagBackfillParams{})
	for i, id := range []string{"c1", "c2", "c3"} {
		f := fx.row(id)
		if len(f.RawTags) == 0 {
			t.Fatalf("%s: RawTags not filled: %+v", id, f)
		}
		if f.TrackNumber != i+1 || f.TrackCount != 0 {
			t.Errorf("%s: track = %d/%d, want %d/0", id, f.TrackNumber, f.TrackCount, i+1)
		}
	}
	assertSummaryHas(t, summary, "read-rawtags-only=3", "books-refused-missing=1")
}

// (d) Multi-disc: (disc, track) pairs judged together. Track 1 on two discs is
// distinct; a repeated pair refuses the book and keeps its discs too.
func TestTagBackfill_MultiDiscPairs(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "m1", BookID: "M", TrackNumber: 1}, {ID: "m2", BookID: "M", TrackNumber: 2},
		{ID: "m3", BookID: "M", TrackNumber: 3}, {ID: "m4", BookID: "M", TrackNumber: 4},
		{ID: "x1", BookID: "X", TrackNumber: 1}, {ID: "x2", BookID: "X", TrackNumber: 2},
		{ID: "x3", BookID: "X", TrackNumber: 3}, {ID: "x4", BookID: "X", TrackNumber: 4},
	}, map[string]metadata.Metadata{
		"m1.mp3": tagMeta(1, 2, 1, 2), "m2.mp3": tagMeta(2, 2, 1, 2),
		"m3.mp3": tagMeta(1, 2, 2, 2), "m4.mp3": tagMeta(2, 2, 2, 2),
		"x1.mp3": tagMeta(1, 2, 1, 2), "x2.mp3": tagMeta(2, 2, 1, 2),
		"x3.mp3": tagMeta(1, 2, 2, 2), "x4.mp3": tagMeta(1, 2, 2, 2),
	})
	summary := fx.run(tagBackfillParams{})
	for id, want := range map[string][2]int{"m1": {1, 1}, "m2": {1, 2}, "m3": {2, 1}, "m4": {2, 2}} {
		f := fx.row(id)
		assertPosition(t, f, want[0], want[1])
		if f.DiscCount != 2 {
			t.Errorf("%s: DiscCount = %d, want 2", id, f.DiscCount)
		}
	}
	for i, id := range []string{"x1", "x2", "x3", "x4"} {
		f := fx.row(id)
		if f.TrackNumber != i+1 || f.DiscNumber != 0 || f.DiscCount != 0 {
			t.Errorf("%s: refused book changed position: disc %d/%d track %d", id, f.DiscNumber, f.DiscCount, f.TrackNumber)
		}
	}
	assertSummaryHas(t, summary, "read-took-tag-tracks=4", "read-rawtags-only=4", "books-refused-duplicate=1")
}

// A tag with no disc number is judged as disc 0, so an accepted book must also
// WRITE disc 0 — keeping a stored non-zero DiscNumber would sort the row away
// from where the judgement placed it.
func TestTagBackfill_NoDiscTagClearsStoredDisc(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "n1", BookID: "N", DiscNumber: 1, DiscCount: 1, TrackNumber: 1},
		{ID: "n2", BookID: "N", DiscNumber: 1, DiscCount: 1, TrackNumber: 2},
	}, map[string]metadata.Metadata{"n1.mp3": tagMeta(2, 2, 0, 0), "n2.mp3": tagMeta(1, 2, 0, 0)})
	fx.run(tagBackfillParams{})
	for id, track := range map[string]int{"n1": 2, "n2": 1} {
		f := fx.row(id)
		assertPosition(t, f, 0, track)
		if f.DiscCount != 0 {
			t.Errorf("%s: DiscCount = %d, want 0 with no disc", id, f.DiscCount)
		}
	}
}

// Finding from review round 1: a book refused on run 1 (one file missing) gets
// RawTags only; once the file is back, run 2 accepts the book and must move the
// nine siblings to their tag positions too, not just the recovered row —
// otherwise the recovered (1,3) sorts after nine disc-0 rows.
func TestTagBackfill_SecondRunRenumbersSiblings(t *testing.T) {
	var files []database.BookFile
	tags := map[string]metadata.Metadata{}
	type pos struct{ disc, track int }
	want := map[string]pos{}
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("t%02d", i)
		disc, track := 1+(i-1)/5, 1+(i-1)%5
		files = append(files, database.BookFile{ID: id, BookID: "T", TrackNumber: i, TrackCount: 10})
		tags[id+".mp3"] = tagMeta(track, 5, disc, 2)
		want[id] = pos{disc, track}
	}
	fx := newTagFixture(t, files, tags)
	if err := os.Remove(fx.path("t03")); err != nil {
		t.Fatal(err)
	}

	s1 := fx.run(tagBackfillParams{})
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("t%02d", i)
		assertPosition(t, fx.row(id), 0, i)
	}
	if len(fx.row("t03").RawTags) != 0 {
		t.Errorf("missing t03 should not have been written on run 1")
	}
	assertSummaryHas(t, s1, "read-rawtags-only=9", "books-refused-missing=1", "missing-on-disk=1", "siblings-renumbered=0")

	mustWriteFile(t, fx.path("t03"))
	s2 := fx.run(tagBackfillParams{})
	for id, p := range want {
		assertPosition(t, fx.row(id), p.disc, p.track)
	}
	assertSummaryHas(t, s2, "judged-rows=10", "read-took-tag-tracks=1", "siblings-renumbered=9")

	var order []string
	for id := range want {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := fx.row(order[i]), fx.row(order[j])
		if a.DiscNumber != b.DiscNumber {
			return a.DiscNumber < b.DiscNumber
		}
		return a.TrackNumber < b.TrackNumber
	})
	if got := strings.Join(order, ","); got != "t01,t02,t03,t04,t05,t06,t07,t08,t09,t10" {
		t.Errorf("disc-then-track order after run 2 = %s", got)
	}
}

// A book cut by the Limit window is refused: the file outside the window was
// never read and has no RawTags, so its tag track is unknown.
func TestTagBackfill_LimitWindowRefuses(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "L1", BookID: "L", TrackNumber: 1},
		{ID: "L2", BookID: "L", TrackNumber: 2},
		{ID: "L3", BookID: "L", TrackNumber: 3},
	}, map[string]metadata.Metadata{"L1.mp3": tagMeta(3, 3, 0, 0), "L2.mp3": tagMeta(1, 3, 0, 0), "L3.mp3": tagMeta(2, 3, 0, 0)})
	summary := fx.run(tagBackfillParams{Limit: 2})
	assertPosition(t, fx.row("L1"), 0, 1)
	assertPosition(t, fx.row("L2"), 0, 2)
	if fx.writtenIDs()["L3"] {
		t.Errorf("L3 is outside the Limit window and must not be written")
	}
	assertSummaryHas(t, summary, "examined=2", "read-rawtags-only=2", "books-refused-missing=1", "file L3 has no tag track")
}

// A single-file book takes its "3/12" tag.
func TestTagBackfill_SingleFileBookTakesTag(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{{ID: "one", BookID: "ONE", TrackNumber: 1}},
		map[string]metadata.Metadata{"one.mp3": tagMeta(3, 12, 0, 0)})
	summary := fx.run(tagBackfillParams{})
	if f := fx.row("one"); f.TrackNumber != 3 || f.TrackCount != 12 {
		t.Errorf("single-file book: track = %d/%d, want 3/12", f.TrackNumber, f.TrackCount)
	}
	assertSummaryHas(t, summary, "read-took-tag-tracks=1")
}

// Siblings whose stored RawTags use MP4 keys (trkn/disk) are parsed: one book is
// accepted and its sibling renumbered, another collides and is refused.
func TestTagBackfill_MP4SiblingKeys(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "p1", BookID: "P", TrackNumber: 1},
		{ID: "p2", BookID: "P", TrackNumber: 2, RawTags: map[string]string{"trkn": "2", "disk": "1"}},
		{ID: "q1", BookID: "Q", TrackNumber: 1},
		{ID: "q2", BookID: "Q", TrackNumber: 2, RawTags: map[string]string{"trkn": "1", "disk": "1"}},
	}, map[string]metadata.Metadata{"p1.mp3": tagMeta(1, 2, 1, 1), "q1.mp3": tagMeta(1, 2, 1, 1)})
	summary := fx.run(tagBackfillParams{})
	assertPosition(t, fx.row("p1"), 1, 1)
	assertPosition(t, fx.row("p2"), 1, 2)
	assertPosition(t, fx.row("q1"), 0, 1)
	assertPosition(t, fx.row("q2"), 0, 2)
	assertSummaryHas(t, summary, "siblings-renumbered=1", "books-refused-duplicate=1")
}

// (e) Dry-run writes nothing and reports the same counts an apply would,
// sibling renumbering included.
func TestTagBackfill_DryRunWritesNothingAndCounts(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "a1", BookID: "A", TrackNumber: 1}, {ID: "a2", BookID: "A", TrackNumber: 2},
		{ID: "b1", BookID: "B", TrackNumber: 1}, {ID: "b2", BookID: "B", TrackNumber: 2},
		{ID: "c1", BookID: "C", TrackNumber: 1}, {ID: "c2", BookID: "C", TrackNumber: 2},
		{ID: "gone", BookID: "C", TrackNumber: 3, FilePath: filepath.Join(t.TempDir(), "absent", "gone.mp3")},
		{ID: "d1", BookID: "D", TrackNumber: 1},
		{ID: "d2", BookID: "D", TrackNumber: 2, RawTags: map[string]string{"TRCK": "1"}},
	}, map[string]metadata.Metadata{
		"a1.mp3": tagMeta(1, 1, 0, 0), "a2.mp3": tagMeta(1, 1, 0, 0),
		"b1.mp3": tagMeta(2, 2, 0, 0), "b2.mp3": tagMeta(1, 2, 0, 0),
		"c1.mp3": tagMeta(1, 3, 0, 0), "c2.mp3": tagMeta(2, 3, 0, 0),
		"d1.mp3": tagMeta(2, 2, 0, 0),
	})
	summary := fx.run(tagBackfillParams{DryRun: true})
	if len(fx.batches) != 0 {
		t.Fatalf("dry run wrote %d batches", len(fx.batches))
	}
	assertPosition(t, fx.row("d2"), 0, 2)
	assertSummaryHas(t, summary, "would backfill", "examined=9", "books=4", "judged-rows=8", "read-took-tag-tracks=3",
		"read-rawtags-only=4", "siblings-renumbered=1", "books-refused-duplicate=1", "books-refused-missing=1",
		"missing-on-disk=1", "book A (")
}

// (f) More rows than one batch: every row is written, and every batch ends on a
// book boundary, so it may exceed the batch size by at most one book.
func TestTagBackfill_WritesInBoundedBatches(t *testing.T) {
	old := tagBackfillWriteBatchSize
	tagBackfillWriteBatchSize = 2
	t.Cleanup(func() { tagBackfillWriteBatchSize = old })

	var files []database.BookFile
	tags := map[string]metadata.Metadata{}
	for i := range 7 {
		id := fmt.Sprintf("solo-%d", i)
		files = append(files, database.BookFile{ID: id, BookID: "book-" + id})
		tags[id+".mp3"] = tagMeta(1, 1, 0, 0)
	}
	for i := range 5 {
		id := fmt.Sprintf("multi-%d", i)
		files = append(files, database.BookFile{ID: id, BookID: "multi", TrackNumber: i + 1})
		tags[id+".mp3"] = tagMeta(i+1, 5, 0, 0)
	}
	fx := newTagFixture(t, files, tags)
	summary := fx.run(tagBackfillParams{})

	bookOf := map[string]string{}
	for _, f := range files {
		bookOf[f.ID] = f.BookID
	}
	batchOfBook := map[string]int{}
	rows := 0
	for i, b := range fx.batches {
		if len(b) > 2-1+5 {
			t.Errorf("batch %d has %d rows, want <= size-1+largest book = 6", i, len(b))
		}
		for _, u := range b {
			book := bookOf[u.ID]
			if prev, ok := batchOfBook[book]; ok && prev != i {
				t.Errorf("book %s split across batches %d and %d", book, prev, i)
			}
			batchOfBook[book] = i
		}
		rows += len(b)
	}
	if rows != 12 || len(fx.writtenIDs()) != 12 {
		t.Errorf("wrote %d rows (%d distinct), want 12", rows, len(fx.writtenIDs()))
	}
	if len(fx.batches) < 2 {
		t.Errorf("got %d batches, want more than one", len(fx.batches))
	}
	assertSummaryHas(t, summary, "wrote 12 rows; not-written=0", "judged-rows=12", "read-took-tag-tracks=12")
}

// Cancel mid-run: books judged in full before the cancel are still flushed (in
// one stop-flush, since the batch size is never reached), the book being read
// when the cancel lands is not written at all, and the stop is logged.
func TestTagBackfill_CancelFlushesCompleteBooks(t *testing.T) {
	old := tagBackfillWriteBatchSize
	tagBackfillWriteBatchSize = 1000
	t.Cleanup(func() { tagBackfillWriteBatchSize = old })

	var files []database.BookFile
	tags := map[string]metadata.Metadata{}
	for i := range 10 {
		for j, suffix := range []string{"a", "b"} {
			id := fmt.Sprintf("done-%d-%s", i, suffix)
			files = append(files, database.BookFile{ID: id, BookID: fmt.Sprintf("done-%d", i), TrackNumber: j + 1})
			tags[id+".mp3"] = tagMeta(j+1, 2, 0, 0)
		}
	}
	// The trigger book is last, so every done-* book is dispatched before it.
	files = append(files, database.BookFile{ID: "trig-a", BookID: "trig", TrackNumber: 1},
		database.BookFile{ID: "trig-b", BookID: "trig", TrackNumber: 2})
	tags["trig-a.mp3"], tags["trig-b.mp3"] = tagMeta(1, 2, 0, 0), tagMeta(2, 2, 0, 0)

	fx := newTagFixture(t, files, tags)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.extractor.hook = func(base string) {
		if base != "trig-a.mp3" {
			return
		}
		// Wait for the ten done-* books to hydrate; judging and enqueueing
		// follow without I/O, so the grace sleep covers them comfortably.
		deadline := time.Now().Add(10 * time.Second)
		for fx.hydrates.Load() < 10 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}

	logs, err := fx.runErr(ctx, tagBackfillParams{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	written := fx.writtenIDs()
	if len(written) != 20 || written["trig-a"] || written["trig-b"] {
		t.Errorf("wrote %d rows (trig-a %v, trig-b %v), want the 20 done-* rows only", len(written), written["trig-a"], written["trig-b"])
	}
	if len(fx.batches) != 1 {
		t.Errorf("got %d batches, want 1 stop-flush", len(fx.batches))
	}
	joined := strings.Join(logs, "\n")
	assertSummaryHas(t, joined, "flushed 20 pending rows of complete books; 20 rows written in total, 0 judged rows not written after a write error", "wrote 20 rows; not-written=0")
}

// A failed batch write stops the run: nothing is written after it, every judged
// row that did not reach the store is reported not written, the error counts only
// rows actually written, and the summary is still emitted.
func TestTagBackfill_WriteErrorReportsNotWrittenAndSummary(t *testing.T) {
	old := tagBackfillWriteBatchSize
	tagBackfillWriteBatchSize = 2
	t.Cleanup(func() { tagBackfillWriteBatchSize = old })

	var files []database.BookFile
	tags := map[string]metadata.Metadata{}
	for i := range 6 {
		for j, suffix := range []string{"a", "b"} {
			id := fmt.Sprintf("w-%d-%s", i, suffix)
			files = append(files, database.BookFile{ID: id, BookID: fmt.Sprintf("w-%d", i), TrackNumber: j + 1})
			tags[id+".mp3"] = tagMeta(j+1, 2, 0, 0)
		}
	}
	fx := newTagFixture(t, files, tags)
	var calls atomic.Int32
	fx.store.BatchUpsertBookFilesFunc = func(batch []*database.BookFile) error {
		calls.Add(1)
		return errors.New("simulated store failure")
	}

	logs, err := fx.runErr(context.Background(), tagBackfillParams{})
	if err == nil || !strings.Contains(err.Error(), "batch write of 2 rows failed (0 rows written before it)") {
		t.Fatalf("err = %v, want the failed-batch error counting 0 rows written", err)
	}
	// Every two-row book fills a batch, so the first enqueue flushes and fails;
	// no later book may reach the store.
	if n := calls.Load(); n != 1 {
		t.Errorf("BatchUpsertBookFiles called %d times, want 1 (no writes after the first failure)", n)
	}
	joined := strings.Join(logs, "\n")
	assertSummaryHas(t, joined, "flushed 0 pending rows of complete books; 0 rows written in total", "wrote 0 rows;", "examined=12")
	// Every judged row is accounted for: none was written, so every one of them
	// — the failed batch plus any book that finished judging after the failure —
	// must be in not-written. (How many books finish before RunItems cancels the
	// rest depends on scheduling, so the invariant is asserted, not a count.)
	judged, notWritten := summaryInt(t, joined, "judged-rows"), summaryInt(t, joined, "not-written")
	if judged < 2 || notWritten != judged {
		t.Errorf("judged-rows=%d not-written=%d: want not-written == judged-rows >= 2 with 0 written", judged, notWritten)
	}
	assertSummaryHas(t, joined, fmt.Sprintf("%d judged rows not written after a write error", judged))
}

// A write failure at the final flush: every book was judged and pending when the
// store failed, so all 12 rows must be reported not written — not just the rows
// of the last batch.
func TestTagBackfill_FinalFlushErrorCountsEveryJudgedRow(t *testing.T) {
	old := tagBackfillWriteBatchSize
	tagBackfillWriteBatchSize = 1000
	t.Cleanup(func() { tagBackfillWriteBatchSize = old })

	var files []database.BookFile
	tags := map[string]metadata.Metadata{}
	for i := range 6 {
		for j, suffix := range []string{"a", "b"} {
			id := fmt.Sprintf("f-%d-%s", i, suffix)
			files = append(files, database.BookFile{ID: id, BookID: fmt.Sprintf("f-%d", i), TrackNumber: j + 1})
			tags[id+".mp3"] = tagMeta(j+1, 2, 0, 0)
		}
	}
	fx := newTagFixture(t, files, tags)
	fx.store.BatchUpsertBookFilesFunc = func([]*database.BookFile) error { return errors.New("simulated store failure") }

	logs, err := fx.runErr(context.Background(), tagBackfillParams{})
	if err == nil || !strings.Contains(err.Error(), "batch write of 12 rows failed") {
		t.Fatalf("err = %v, want the failed 12-row final flush", err)
	}
	assertSummaryHas(t, strings.Join(logs, "\n"), "wrote 0 rows; not-written=12", "judged-rows=12",
		"flushed 0 pending rows of complete books; 0 rows written in total, 12 judged rows not written after a write error")
}

// Finding from review round 2: a two-disc book whose disc-1 files carry TPOS=1
// (tracks 1-5) and whose disc-2 files have no disc frame (tracks 1-5). Absent
// disc = 0 never collides with disc 1, so without the disc-presence guard every
// (disc, track) key is distinct, the book is accepted, disc 2 is written as disc
// 0, and disc-then-track order plays disc 2 first. The book must be refused.
func TestTagBackfill_MixedDiscTagsRefuse(t *testing.T) {
	var files []database.BookFile
	tags := map[string]metadata.Metadata{}
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("md%02d", i)
		files = append(files, database.BookFile{ID: id, BookID: "MD", TrackNumber: i, TrackCount: 10})
		if i <= 5 {
			tags[id+".mp3"] = tagMeta(i, 5, 1, 0) // disc 1: TPOS=1
		} else {
			tags[id+".mp3"] = tagMeta(i-5, 5, 0, 0) // disc 2: no disc frame
		}
	}
	fx := newTagFixture(t, files, tags)
	summary := fx.run(tagBackfillParams{})
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("md%02d", i)
		f := fx.row(id)
		assertPosition(t, f, 0, i)
		if f.TrackCount != 10 || len(f.RawTags) == 0 {
			t.Errorf("%s: want RawTags filled and positional TrackCount 10 kept, got count %d, %d tags", id, f.TrackCount, len(f.RawTags))
		}
	}
	assertSummaryHas(t, summary, "books-refused-mixed-disc=1", "books-refused-duplicate=0", "books-refused-missing=0",
		"read-took-tag-tracks=0", "read-rawtags-only=10", "judged-rows=10", "disc tag on only some files")
}

// With force=true a candidate missing on disk can carry stored RawTags. When its
// book is accepted it is renumbered from those tags, and it must be counted once
// — under siblings-renumbered, not also under missing-on-disk.
func TestTagBackfill_ForceMissingCandidateCountsOnce(t *testing.T) {
	fx := newTagFixture(t, []database.BookFile{
		{ID: "fm1", BookID: "FM", TrackNumber: 2, RawTags: map[string]string{"TRCK": "1"}},
		{ID: "fm2", BookID: "FM", TrackNumber: 1, RawTags: map[string]string{"TRCK": "2"}}, // no file on disk
	}, map[string]metadata.Metadata{"fm1.mp3": tagMeta(1, 2, 0, 0)})
	summary := fx.run(tagBackfillParams{Force: true})
	assertPosition(t, fx.row("fm1"), 0, 1)
	assertPosition(t, fx.row("fm2"), 0, 2)
	assertSummaryHas(t, summary, "missing-on-disk=0", "siblings-renumbered=1", "read-took-tag-tracks=1",
		"judged-rows=2", "wrote 2 rows; not-written=0")
}

// summaryInt reads the integer after "key=" in a summary line.
func summaryInt(t *testing.T, summary, key string) int {
	t.Helper()
	m := regexp.MustCompile(`(?:^|[ ;])` + regexp.QuoteMeta(key) + `=(\d+)`).FindStringSubmatch(summary)
	if m == nil {
		t.Fatalf("summary has no %s=N:\n%s", key, summary)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("%s=%q: %v", key, m[1], err)
	}
	return n
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
