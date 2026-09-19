// file: internal/database/book_runtime_test.go
// version: 1.0.1
// guid: 295552ed-1de2-49e3-9bf6-7cb94a4ad6eb
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"testing"
)

func rows(durs ...int) []BookFile {
	out := make([]BookFile, len(durs))
	for i, d := range durs {
		out[i] = BookFile{ID: string(rune('a' + i)), Duration: d}
	}
	return out
}

func repeat(n, sec int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = sec
	}
	return out
}

func TestComputeBookRuntime(t *testing.T) {
	d := func(v int) *int { return &v }
	thirtyKnown := rows(repeat(30, 1200)...)
	twoOfThirty := rows(append(repeat(2, 1200), repeat(28, 0)...)...)
	withMissing := append(rows(34401), BookFile{ID: "old", Duration: 34401, Missing: true})
	withMissing[0].FileSize = 500_000_000
	withMissing[1].FileSize = 500_000_000
	// Reviewer probe: a 45 s intro on disk, 20 × 30 min chapters missing.
	introAndMissing := rows(45)
	for i := 0; i < 20; i++ {
		introAndMissing = append(introAndMissing, BookFile{ID: "m", FilePath: "/old/" + string(rune('a'+i)) + ".mp3", Duration: 1800, Missing: true})
	}
	renamedRepoint := []BookFile{
		{ID: "new", FilePath: "/lib/Book - 01.mp3", OriginalFileHash: "h1", FileHash: "h1-tagged", Duration: 1200, FileSize: 101},
		{ID: "old", FilePath: "/in/01 Chapter.mp3", FileHash: "h1", Duration: 1200, FileSize: 100, Missing: true},
	}
	allMissing := []BookFile{{ID: "x", Duration: 600, Missing: true}, {ID: "y", Duration: 900, Missing: true}}
	fpOnly := []BookFile{{ID: "fp", AcoustIDFingerprintDurationSec: 1799.6}}

	for _, c := range []struct {
		name       string
		book       *Book
		files      []BookFile
		wantSec    int
		wantStatus string
		wantKnown  bool
	}{
		{"30x20min all known is 10h even with a stale aggregate", &Book{Duration: d(1200)}, thirtyKnown, 36000, "complete", true},
		{"2 of 30 known is partial, a lower bound", &Book{Duration: d(2400)}, twoOfThirty, 2400, "partial", false},
		{"no file durations on a multi-file book is unknown, aggregate ignored", &Book{Duration: d(1200)}, rows(0, 0, 0), 0, "unknown", false},
		{"single row without duration falls back to Book.Duration", &Book{Duration: d(5000)}, rows(0), 5000, "book_aggregate", true},
		{"no rows falls back to Book.Duration", &Book{Duration: d(5000)}, nil, 5000, "book_aggregate", true},
		{"no rows and no aggregate is unknown", &Book{}, nil, 0, "unknown", false},
		{"missing row beside its present copy (same size) is not double counted", nil, withMissing, 34401, "complete", true},
		{"repoint duplicate matched by pre-organize hash is not double counted", nil, renamedRepoint, 1200, "complete", true},
		{"missing chapters with no present copy make the runtime partial, never a 45s book", nil, introAndMissing, 36045, "partial", false},
		{"unidentifiable missing row is counted, not assumed a duplicate", nil, append(rows(1200), BookFile{ID: "x", Duration: 1200, Missing: true}), 2400, "partial", false},
		{"all rows missing: runtime from the missing rows", nil, allMissing, 1500, "complete", true},
		{"fingerprint duration is a whole-file fallback", nil, fpOnly, 1800, "complete", true},
		{"millisecond row is normalized", nil, []BookFile{{ID: "ms", Duration: 3_600_000, FileSize: 57_600_000}}, 3600, "complete", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := ComputeBookRuntime(c.book, c.files)
			if rt.Status() != c.wantStatus {
				t.Fatalf("status = %s, want %s (%+v)", rt.Status(), c.wantStatus, rt)
			}
			sec, known := rt.KnownSeconds()
			if known != c.wantKnown {
				t.Fatalf("known = %v, want %v (%+v)", known, c.wantKnown, rt)
			}
			if rt.Seconds != c.wantSec {
				t.Fatalf("seconds = %d, want %d (%+v)", rt.Seconds, c.wantSec, rt)
			}
			if known && sec != c.wantSec {
				t.Fatalf("KnownSeconds = %d, want %d", sec, c.wantSec)
			}
		})
	}
}

type filesStub struct {
	files []BookFile
	err   error
}

func (s filesStub) GetBookFiles(string) ([]BookFile, error) { return s.files, s.err }

// TestLoadBookRuntime_ReadErrorIsUnknown: an unreadable file set must not
// promote Book.Duration to a total.
func TestLoadBookRuntime_ReadErrorIsUnknown(t *testing.T) {
	dur := 1200
	book := &Book{ID: "b", Duration: &dur}
	rt, err := LoadBookRuntime(filesStub{err: errors.New("boom")}, book)
	if err == nil {
		t.Fatal("read error not returned")
	}
	if _, ok := rt.KnownSeconds(); ok || rt.BookAggregateSec != 1200 {
		t.Fatalf("runtime = %+v, want unknown with aggregate reported", rt)
	}
	if rt, _ := LoadBookRuntime(nil, book); rt.Complete() {
		t.Fatalf("nil store produced a complete runtime: %+v", rt)
	}
	rt, err = LoadBookRuntime(filesStub{files: rows(repeat(30, 1200)...)}, book)
	if err != nil || rt.Seconds != 36000 || !rt.Complete() {
		t.Fatalf("runtime = %+v, %v; want 36000 complete", rt, err)
	}
}

// TestStoredAggregateSec pins what RecomputeBookAggregates writes.
func TestStoredAggregateSec(t *testing.T) {
	if v, ok := ComputeBookRuntime(nil, rows(append(repeat(2, 1200), 0)...)).StoredAggregateSec(); !ok || v != 2400 {
		t.Fatalf("partial aggregate = %d,%v; want 2400,true", v, ok)
	}
	if _, ok := ComputeBookRuntime(nil, rows(0, 0)).StoredAggregateSec(); ok {
		t.Fatal("no-duration rows produced a stored aggregate")
	}
}

// TestRecomputeBookAggregates_MissingChapterNeverLowersDuration runs the real
// persistence path: 20 chapters are imported, then 19 go missing (the file
// rows stay, flagged missing) with no present copy. The stored Book.Duration
// must stay the whole book's 20 × 30 min — the first version of the
// canonical runtime dropped every missing row whenever one present row
// existed and persisted the one surviving chapter.
func TestRecomputeBookAggregates_MissingChapterNeverLowersDuration(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	b, err := store.CreateBook(&Book{Title: "Chaptered", FilePath: "/lib/Chaptered"})
	if err != nil {
		t.Fatal(err)
	}
	var files []*BookFile
	for i := 0; i < 20; i++ {
		files = append(files, &BookFile{
			ID: fmt.Sprintf("f%02d", i), BookID: b.ID,
			FilePath: fmt.Sprintf("/lib/Chaptered/%02d.mp3", i),
			Duration: 1800, FileSize: int64(40_000_000 + i),
		})
	}
	if err := store.BatchUpsertBookFiles(files); err != nil {
		t.Fatal(err)
	}
	for _, f := range files[1:] {
		f.Missing = true
		if err := store.UpdateBookFile(f.ID, f); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetBookByID(b.ID)
	if err != nil || got == nil || got.Duration == nil {
		t.Fatalf("book = %+v, %v", got, err)
	}
	if *got.Duration != 36000 {
		t.Fatalf("Book.Duration = %d after chapters went missing, want 36000", *got.Duration)
	}
	stored, _ := store.GetBookFiles(b.ID)
	if rt := ComputeBookRuntime(got, stored); rt.Complete() || rt.FilesMissingUnmatched != 19 {
		t.Fatalf("runtime = %+v, want partial with 19 unmatched missing rows", rt)
	}
}
