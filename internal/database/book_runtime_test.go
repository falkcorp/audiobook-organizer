// file: internal/database/book_runtime_test.go
// version: 1.0.0
// guid: 295552ed-1de2-49e3-9bf6-7cb94a4ad6eb
// last-edited: 2026-09-19

package database

import (
	"errors"
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
		{"missing row beside its present copy is not double counted", nil, withMissing, 34401, "complete", true},
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
