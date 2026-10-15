// file: internal/database/book_runtime_test.go
// version: 1.1.0
// guid: 295552ed-1de2-49e3-9bf6-7cb94a4ad6eb
// last-edited: 2026-10-15

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
	withMissing[0].FileHash, withMissing[0].FileSize = "c0ffee", 500_000_000
	withMissing[1].FileHash, withMissing[1].FileSize = "c0ffee", 500_000_000
	sizeOnly := append(rows(34401), BookFile{ID: "old", Duration: 34401, Missing: true})
	sizeOnly[0].FileSize, sizeOnly[1].FileSize = 500_000_000, 500_000_000
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
		{"missing row beside its present copy (same hash) is not double counted", nil, withMissing, 34401, "complete", true},
		{"equal size alone does not prove a copy", nil, sizeOnly, 68802, "complete", true},
		{"repoint duplicate matched by pre-organize hash is not double counted", nil, renamedRepoint, 1200, "complete", true},
		{"missing chapters with no present copy are counted: a 10h book, never a 45s one", nil, introAndMissing, 36045, "complete", true},
		{"unidentifiable missing row is counted, not assumed a duplicate", nil, append(rows(1200), BookFile{ID: "x", Duration: 1200, Missing: true}), 2400, "complete", true},
		{"missing row with no duration makes the runtime partial", nil, append(rows(1200), BookFile{ID: "x", Missing: true}), 1200, "partial", false},
		{"disc 2 sharing disc 1's base names is not a duplicate", nil, discs(), 36000, "complete", true},
		{"equal-size CBR parts are not duplicates of each other", nil, equalSizeParts(), 36000, "complete", true},
		{"same hash but different duration is not a duplicate", nil, []BookFile{
			{ID: "p", FileHash: "h", Duration: 1800}, {ID: "m", FileHash: "h", Duration: 600, Missing: true}}, 2400, "complete", true},
		{"original filename plus size is a duplicate", nil, []BookFile{
			{ID: "p", OriginalFilename: "01.mp3", FileSize: 10, Duration: 1800}, {ID: "m", OriginalFilename: "01.mp3", FileSize: 10, Duration: 1800, Missing: true}}, 1800, "complete", true},
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
	if rt := ComputeBookRuntime(got, stored); !rt.Complete() || rt.Seconds != 36000 || rt.FilesMissingUnmatched != 19 {
		t.Fatalf("runtime = %+v, want complete 36000 with 19 unmatched missing rows", rt)
	}
}

// discs is a two-disc rip: CD1/01..10 present, CD2/01..10 missing, the same
// base names on both discs, distinct sizes, 30 min each.
func discs() []BookFile {
	var out []BookFile
	for d := 1; d <= 2; d++ {
		for i := 1; i <= 10; i++ {
			out = append(out, BookFile{ID: fmt.Sprint(d, i), FilePath: fmt.Sprintf("/lib/x/CD%d/%02d.mp3", d, i),
				OriginalFilename: fmt.Sprintf("%02d.mp3", i), FileSize: int64(d*1_000_000 + i), Duration: 1800, Missing: d == 2})
		}
	}
	return out
}

// equalSizeParts is a constant-bitrate split: 20 parts of identical size,
// the second ten missing.
func equalSizeParts() []BookFile {
	var out []BookFile
	for i := 0; i < 20; i++ {
		out = append(out, BookFile{ID: fmt.Sprint(i), FilePath: fmt.Sprintf("/lib/x/Part %02d.mp3", i),
			OriginalFilename: fmt.Sprintf("Part %02d.mp3", i), FileSize: 28_800_000, Duration: 1800, Missing: i >= 10})
	}
	return out
}

// sizedRows builds file rows with a size and a duration each (0 = unmeasured).
func sizedRows(sizes []int64, durs []int) []BookFile {
	out := make([]BookFile, len(sizes))
	for i := range sizes {
		out[i] = BookFile{ID: fmt.Sprintf("f%d", i), FileSize: sizes[i], Duration: durs[i]}
	}
	return out
}

func TestBookRuntimeEstimate(t *testing.T) {
	const mb = int64(1_000_000)
	// 4 MB measured at 8 min: 8333 B/s. Total 8 MB -> ~960 s, upper x3.
	ownerCase := sizedRows([]int64{4 * mb, 4 * mb}, []int{480, 0})
	// 10 h book, 2 of 20 chapters measured (30 min each, 28.8 MB each).
	tenHour := sizedRows(repeat64(20, 28_800_000), append(repeat(2, 1800), repeat(18, 0)...))
	noSize := sizedRows([]int64{4 * mb, 0}, []int{480, 0})
	measuredNoSize := sizedRows([]int64{0, 4 * mb}, []int{480, 0})
	complete := sizedRows([]int64{4 * mb, 4 * mb}, []int{480, 480})
	none := sizedRows([]int64{4 * mb, 4 * mb}, []int{0, 0})

	for _, c := range []struct {
		name      string
		files     []BookFile
		wantEst   int
		wantUpper int
		wantOK    bool
	}{
		{"owner case: 2 files, one measured", ownerCase, 960, 2880, true},
		{"10h book with 2 of 20 chapters measured", tenHour, 36000, 108000, true},
		{"an unmeasured row without a size: no estimate", noSize, 0, 0, false},
		{"the measured row has no size: no rate", measuredNoSize, 0, 0, false},
		{"complete runtime has nothing to estimate", complete, 0, 0, false},
		{"no measured file at all", none, 0, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := ComputeBookRuntime(nil, c.files)
			est, ok := rt.EstimatedSeconds()
			up, upOK := rt.EstimatedUpperSeconds()
			if ok != c.wantOK || upOK != c.wantOK {
				t.Fatalf("ok = %v/%v, want %v (rt %+v)", ok, upOK, c.wantOK, rt)
			}
			if est != c.wantEst || up != c.wantUpper {
				t.Errorf("estimate/upper = %d/%d, want %d/%d", est, up, c.wantEst, c.wantUpper)
			}
		})
	}
}

// The estimate, and so the upper bound, is never below the known lower bound,
// even for a hand-built runtime whose sample would extrapolate to less.
func TestBookRuntimeEstimateNeverBelowLowerBound(t *testing.T) {
	rt := BookRuntime{
		Seconds: 1000, Source: RuntimeSourceFiles, FilesCounted: 2, FilesKnown: 1,
		SizesComplete: true, TotalBytes: 10, RateBytes: 10, RateSeconds: 1000,
	}
	if up, ok := rt.EstimatedUpperSeconds(); !ok || up < 1000 {
		t.Errorf("upper = %d, %v; want >= 1000", up, ok)
	}
	broken := rt
	broken.RateBytes = 0 // a hand-built runtime with seconds but no bytes must not divide by zero
	if _, ok := broken.EstimatedSeconds(); ok {
		t.Error("estimate made from a zero-byte sample")
	}
	rt.RateSeconds = 100 // sample says 100 s, lower bound says 1000 s
	if est, _ := rt.EstimatedSeconds(); est < 1000 {
		t.Errorf("estimate %d below the lower bound 1000", est)
	}
	if up, _ := rt.EstimatedUpperSeconds(); up < 1000 {
		t.Errorf("upper %d below the lower bound 1000", up)
	}
}

func repeat64(n int, v int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
