// file: internal/plugins/maintenance/tag_backfill_test.go
// version: 1.2.0
// guid: 5b6e7f4a-9c1d-4e0a-8f2b-3a6d1c9e5b70
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

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
// read-only during a run, so concurrent workers can share it.
type mapTagExtractor struct{ byBase map[string]metadata.Metadata }

func (e mapTagExtractor) ExtractMetadata(filePath string) (metadata.Metadata, error) {
	m, ok := e.byBase[filepath.Base(filePath)]
	if !ok {
		return metadata.Metadata{}, fmt.Errorf("no fixture for %s", filePath)
	}
	return m, nil
}

// tagMeta builds a fixture tag reading with a non-empty AllTags map.
func tagMeta(track, trackTotal, disc, discTotal int) metadata.Metadata {
	return metadata.Metadata{
		Title:       fmt.Sprintf("Tag Title %d-%d", disc, track),
		TrackNumber: track,
		TrackTotal:  trackTotal,
		DiscNumber:  disc,
		DiscTotal:   discTotal,
		AllTags:     map[string]string{"TRCK": fmt.Sprintf("%d/%d", track, trackTotal), "TALB": "Book"},
	}
}

type tagBackfillRun struct {
	byID    map[string]*database.BookFile
	batches [][]*database.BookFile
	summary string
}

// runTagBackfillFixture wires files into a MockStore whose GetBookFiles filters
// by BookID (a mock that returned every file for any book would make the
// per-book guard meaningless), writes a placeholder on disk for every file with
// a tag fixture, and runs the op.
func runTagBackfillFixture(t *testing.T, files []database.BookFile, tags map[string]metadata.Metadata, dryRun bool) tagBackfillRun {
	t.Helper()
	dir := t.TempDir()
	for i := range files {
		if files[i].FilePath == "" {
			files[i].FilePath = filepath.Join(dir, files[i].ID+".mp3")
		}
		if _, ok := tags[files[i].ID+".mp3"]; ok {
			mustWriteFile(t, files[i].FilePath)
		}
	}
	metadata.SetMetadataExtractor(mapTagExtractor{byBase: tags})
	t.Cleanup(func() { metadata.SetMetadataExtractor(nil) })

	core := make([]database.BookFileCore, len(files))
	for i := range files {
		core[i] = files[i].Core()
	}
	var (
		mu  sync.Mutex
		run = tagBackfillRun{byID: map[string]*database.BookFile{}}
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
			mu.Lock()
			defer mu.Unlock()
			run.batches = append(run.batches, append([]*database.BookFile(nil), batch...))
			for _, f := range batch {
				run.byID[f.ID] = f
			}
			return nil
		},
	}
	reporter := &fakeReporter{}
	raw, err := json.Marshal(tagBackfillParams{DryRun: dryRun})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := New(fakeDeps{store: store}).runTagBackfill(context.Background(), raw, reporter); err != nil {
		t.Fatalf("runTagBackfill: %v", err)
	}
	if len(reporter.logs) > 0 {
		run.summary = reporter.logs[len(reporter.logs)-1]
	}
	return run
}

func assertSummaryHas(t *testing.T, summary string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(summary, p) {
			t.Errorf("summary missing %q:\n%s", p, summary)
		}
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
	assertSummaryHas(t, summary, "examined=51", "needed=25", "took-tag-tracks=25", "rawtags-only=0",
		"missing-on-disk=6", "read-errors=4")
}

// (a) Every file tagged track 1 ("1/1"): RawTags filled, positional
// TrackNumber/TrackCount kept, empty Title filled.
func TestTagBackfill_AllTrackOneKeepsPositions(t *testing.T) {
	files := []database.BookFile{
		{ID: "a1", BookID: "A", TrackNumber: 1, TrackCount: 3},
		{ID: "a2", BookID: "A", TrackNumber: 2, TrackCount: 3, Title: "Kept Title"},
		{ID: "a3", BookID: "A", TrackNumber: 3, TrackCount: 3},
	}
	tags := map[string]metadata.Metadata{
		"a1.mp3": tagMeta(1, 1, 0, 0), "a2.mp3": tagMeta(1, 1, 0, 0), "a3.mp3": tagMeta(1, 1, 0, 0),
	}
	run := runTagBackfillFixture(t, files, tags, false)
	if len(run.byID) != 3 {
		t.Fatalf("upserted %d rows, want 3", len(run.byID))
	}
	for i, id := range []string{"a1", "a2", "a3"} {
		f := run.byID[id]
		if len(f.RawTags) == 0 {
			t.Errorf("%s: RawTags not filled", id)
		}
		if f.TrackNumber != i+1 || f.TrackCount != 3 {
			t.Errorf("%s: track = %d/%d, want %d/3 (positional order must survive)", id, f.TrackNumber, f.TrackCount, i+1)
		}
	}
	if run.byID["a1"].Title != "Tag Title 0-1" || run.byID["a2"].Title != "Kept Title" {
		t.Errorf("title fill wrong: a1=%q a2=%q", run.byID["a1"].Title, run.byID["a2"].Title)
	}
	assertSummaryHas(t, run.summary, "took-tag-tracks=0", "rawtags-only=3", "books-refused-duplicate=1", "books-refused-missing=0")
}

// (b) Distinct tag tracks: taken, including when a sibling that already has
// RawTags contributes its stored tag position to the judgement.
func TestTagBackfill_DistinctTagTracksAreTaken(t *testing.T) {
	files := []database.BookFile{
		{ID: "b1", BookID: "B", TrackNumber: 1},
		{ID: "b2", BookID: "B", TrackNumber: 2},
		{ID: "b3", BookID: "B", TrackNumber: 3},
		{ID: "b4", BookID: "B", TrackNumber: 4, TrackCount: 4, RawTags: map[string]string{"trck": "1/4"}},
	}
	tags := map[string]metadata.Metadata{
		"b1.mp3": tagMeta(4, 4, 0, 0), "b2.mp3": tagMeta(2, 4, 0, 0), "b3.mp3": tagMeta(3, 4, 0, 0),
	}
	run := runTagBackfillFixture(t, files, tags, false)
	if len(run.byID) != 3 {
		t.Fatalf("upserted %d rows, want 3 (the already-tagged sibling must not be written)", len(run.byID))
	}
	for id, want := range map[string]int{"b1": 4, "b2": 2, "b3": 3} {
		if f := run.byID[id]; f.TrackNumber != want || f.TrackCount != 4 {
			t.Errorf("%s: track = %d/%d, want %d/4", id, f.TrackNumber, f.TrackCount, want)
		}
	}
	assertSummaryHas(t, run.summary, "took-tag-tracks=3", "rawtags-only=0", "books-refused-duplicate=0")
}

// A sibling whose stored RawTags collide with a fresh reading refuses the book.
func TestTagBackfill_TaggedSiblingCollisionRefuses(t *testing.T) {
	files := []database.BookFile{
		{ID: "s1", BookID: "S", TrackNumber: 1},
		{ID: "s2", BookID: "S", TrackNumber: 2, RawTags: map[string]string{"TRCK": "1"}},
	}
	tags := map[string]metadata.Metadata{"s1.mp3": tagMeta(1, 2, 0, 0)}
	run := runTagBackfillFixture(t, files, tags, false)
	if f := run.byID["s1"]; f == nil || f.TrackNumber != 1 || f.TrackCount != 0 || len(f.RawTags) == 0 {
		t.Fatalf("s1 should get RawTags only, got %+v", f)
	}
	assertSummaryHas(t, run.summary, "books-refused-duplicate=1")
}

// (c) One file's tag has no track number: the whole book keeps its positions.
func TestTagBackfill_MissingTrackTagKeepsPositions(t *testing.T) {
	files := []database.BookFile{
		{ID: "c1", BookID: "C", TrackNumber: 1},
		{ID: "c2", BookID: "C", TrackNumber: 2},
		{ID: "c3", BookID: "C", TrackNumber: 3},
	}
	tags := map[string]metadata.Metadata{
		"c1.mp3": tagMeta(7, 9, 0, 0), "c2.mp3": tagMeta(8, 9, 0, 0), "c3.mp3": tagMeta(0, 0, 0, 0),
	}
	run := runTagBackfillFixture(t, files, tags, false)
	for i, id := range []string{"c1", "c2", "c3"} {
		f := run.byID[id]
		if f == nil || len(f.RawTags) == 0 {
			t.Fatalf("%s: RawTags not filled: %+v", id, f)
		}
		if f.TrackNumber != i+1 || f.TrackCount != 0 {
			t.Errorf("%s: track = %d/%d, want %d/0", id, f.TrackNumber, f.TrackCount, i+1)
		}
	}
	assertSummaryHas(t, run.summary, "rawtags-only=3", "books-refused-missing=1")
}

// (d) Multi-disc: (disc, track) pairs judged together. Track 1 on two discs is
// distinct; a repeated pair refuses the book and keeps its discs too.
func TestTagBackfill_MultiDiscPairs(t *testing.T) {
	files := []database.BookFile{
		{ID: "m1", BookID: "M", TrackNumber: 1}, {ID: "m2", BookID: "M", TrackNumber: 2},
		{ID: "m3", BookID: "M", TrackNumber: 3}, {ID: "m4", BookID: "M", TrackNumber: 4},
		{ID: "x1", BookID: "X", TrackNumber: 1}, {ID: "x2", BookID: "X", TrackNumber: 2},
		{ID: "x3", BookID: "X", TrackNumber: 3}, {ID: "x4", BookID: "X", TrackNumber: 4},
	}
	tags := map[string]metadata.Metadata{
		"m1.mp3": tagMeta(1, 2, 1, 2), "m2.mp3": tagMeta(2, 2, 1, 2),
		"m3.mp3": tagMeta(1, 2, 2, 2), "m4.mp3": tagMeta(2, 2, 2, 2),
		"x1.mp3": tagMeta(1, 2, 1, 2), "x2.mp3": tagMeta(2, 2, 1, 2),
		"x3.mp3": tagMeta(1, 2, 2, 2), "x4.mp3": tagMeta(1, 2, 2, 2),
	}
	run := runTagBackfillFixture(t, files, tags, false)
	for id, want := range map[string][2]int{"m1": {1, 1}, "m2": {1, 2}, "m3": {2, 1}, "m4": {2, 2}} {
		f := run.byID[id]
		if f.DiscNumber != want[0] || f.TrackNumber != want[1] || f.DiscCount != 2 {
			t.Errorf("%s: disc/track = %d/%d (discs %d), want %d/%d (discs 2)", id, f.DiscNumber, f.TrackNumber, f.DiscCount, want[0], want[1])
		}
	}
	for i, id := range []string{"x1", "x2", "x3", "x4"} {
		f := run.byID[id]
		if f.TrackNumber != i+1 || f.DiscNumber != 0 || f.DiscCount != 0 {
			t.Errorf("%s: refused book changed position: disc %d/%d track %d", id, f.DiscNumber, f.DiscCount, f.TrackNumber)
		}
	}
	assertSummaryHas(t, run.summary, "took-tag-tracks=4", "rawtags-only=4", "books-refused-duplicate=1")
}

// (e) Dry-run writes nothing and reports the same counts an apply would.
func TestTagBackfill_DryRunWritesNothingAndCounts(t *testing.T) {
	files := []database.BookFile{
		{ID: "a1", BookID: "A", TrackNumber: 1}, {ID: "a2", BookID: "A", TrackNumber: 2},
		{ID: "b1", BookID: "B", TrackNumber: 1}, {ID: "b2", BookID: "B", TrackNumber: 2},
		{ID: "c1", BookID: "C", TrackNumber: 1}, {ID: "c2", BookID: "C", TrackNumber: 2},
		{ID: "gone", BookID: "C", TrackNumber: 3, FilePath: filepath.Join(t.TempDir(), "absent", "gone.mp3")},
	}
	tags := map[string]metadata.Metadata{
		"a1.mp3": tagMeta(1, 1, 0, 0), "a2.mp3": tagMeta(1, 1, 0, 0),
		"b1.mp3": tagMeta(2, 2, 0, 0), "b2.mp3": tagMeta(1, 2, 0, 0),
		"c1.mp3": tagMeta(1, 3, 0, 0), "c2.mp3": tagMeta(2, 3, 0, 0),
	}
	run := runTagBackfillFixture(t, files, tags, true)
	if len(run.batches) != 0 {
		t.Fatalf("dry run wrote %d batches", len(run.batches))
	}
	assertSummaryHas(t, run.summary, "would backfill", "examined=7", "books=3", "needed=6", "took-tag-tracks=2",
		"rawtags-only=4", "books-refused-duplicate=1", "books-refused-missing=1", "missing-on-disk=1", "book A (")
}

// (f) More rows than one batch: every row is written, no batch exceeds the size.
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
	run := runTagBackfillFixture(t, files, tags, false)
	if len(run.byID) != 12 {
		t.Fatalf("wrote %d distinct rows, want 12", len(run.byID))
	}
	rows := 0
	for i, b := range run.batches {
		if len(b) > 2 {
			t.Errorf("batch %d has %d rows, want <= 2", i, len(b))
		}
		rows += len(b)
	}
	if rows != 12 || len(run.batches) != 6 {
		t.Errorf("got %d rows in %d batches, want 12 in 6", rows, len(run.batches))
	}
	assertSummaryHas(t, run.summary, "backfilled 12;", "took-tag-tracks=12")
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
