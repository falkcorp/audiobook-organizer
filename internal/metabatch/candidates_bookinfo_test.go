// file: internal/metabatch/candidates_bookinfo_test.go
// version: 1.1.2
// guid: 52b4b4c0-7157-4e40-901c-2a88bddf805e
// last-edited: 2026-10-20

package metabatch_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
)

// The review cards' book-info block reads these off the list response.
func TestBuildCandidateBookInfo_ReviewCardFields(t *testing.T) {
	book := &database.Book{
		ID:                "b1",
		Title:             "T",
		Narrator:          new("Reader One"),
		ASIN:              new("B000TEST01"),
		ISBN10:            new("0000000001"),
		ISBN13:            new("9780000000002"),
		SeriesID:          new(7),
		Series:            &database.Series{ID: 7, Name: "Saga"},
		SeriesPositionRaw: new("1.5"),
	}
	store := &mockBookFileStore{files: []database.BookFile{{ID: "f1"}, {ID: "f2"}, {ID: "f3"}}}
	info := metabatch.BuildCandidateBookInfo(store, book)
	if info.Narrator != "Reader One" || info.ASIN != "B000TEST01" {
		t.Errorf("narrator/asin = %q/%q", info.Narrator, info.ASIN)
	}
	if info.ISBN != "9780000000002" {
		t.Errorf("isbn = %q, want the ISBN-13", info.ISBN)
	}
	if info.Series != "Saga" || info.SeriesPosition != "1.5" {
		t.Errorf("series = %q #%q", info.Series, info.SeriesPosition)
	}
	if info.FileCount != 3 {
		t.Errorf("file_count = %d, want 3", info.FileCount)
	}
}

func TestBuildCandidateBookInfo_FileCountUnknownOnReadErrorAndNoFiles(t *testing.T) {
	book := &database.Book{ID: "b1", ISBN10: new("0000000001"), SeriesSequence: new(3)}
	info := metabatch.BuildCandidateBookInfo(&mockBookFileStore{err: errors.New("boom")}, book)
	if info.FileCount != 0 {
		t.Errorf("file_count on failed read = %d, want 0 (unknown)", info.FileCount)
	}
	if info.ISBN != "0000000001" || info.SeriesPosition != "3" {
		t.Errorf("isbn/position fallbacks = %q/%q", info.ISBN, info.SeriesPosition)
	}
	if nf := metabatch.BuildCandidateBookInfoNoFiles(book); nf.FileCount != 0 {
		t.Errorf("no-files builder file_count = %d, want 0", nf.FileCount)
	}
}

type seriesStore struct {
	calls int
	ids   []int
	err   error
}

func (s *seriesStore) GetSeriesByIDs(ids []int) (map[int]*database.Series, error) {
	s.calls++
	s.ids = append(s.ids, ids...)
	if s.err != nil {
		return nil, s.err
	}
	out := map[int]*database.Series{}
	for _, id := range ids {
		out[id] = &database.Series{ID: id, Name: fmt.Sprintf("Series %d", id)}
	}
	return out, nil
}

func TestResolveSeriesNames_OneBatchReadForTheWholeListing(t *testing.T) {
	// A stale embedded object (another series' id) is not trusted.
	stale := &database.Book{ID: "b0", SeriesID: new(9), Series: &database.Series{ID: 8, Name: "Old"}}
	if info := metabatch.BuildCandidateBookInfoNoFiles(stale); info.Series != "" {
		t.Fatalf("stale embedded series leaked: %q", info.Series)
	}
	var results []metabatch.CandidateResult
	for i := range 500 {
		b := &database.Book{ID: fmt.Sprintf("b%d", i), SeriesID: new(i % 3)}
		results = append(results, metabatch.CandidateResult{Book: metabatch.BuildCandidateBookInfoNoFiles(b)})
	}
	embedded := &database.Book{ID: "e", SeriesID: new(5), Series: &database.Series{ID: 5, Name: "Kept"}}
	results = append(results, metabatch.CandidateResult{Book: metabatch.BuildCandidateBookInfoNoFiles(embedded)})
	results = append(results, metabatch.CandidateResult{Book: metabatch.BuildCandidateBookInfoNoFiles(&database.Book{ID: "none"})})

	store := &seriesStore{}
	metabatch.ResolveSeriesNames(store, results)
	if store.calls != 1 {
		t.Fatalf("series reads = %d, want 1 batch read for %d rows", store.calls, len(results))
	}
	if len(store.ids) != 3 {
		t.Errorf("ids asked = %v, want the 3 distinct missing ids", store.ids)
	}
	if results[4].Book.Series != "Series 1" || results[500].Book.Series != "Kept" || results[501].Book.Series != "" {
		t.Errorf("names = %q / %q / %q", results[4].Book.Series, results[500].Book.Series, results[501].Book.Series)
	}
}

func TestResolveSeriesNames_NoReadWhenNothingMissingAndSafeOnFailure(t *testing.T) {
	store := &seriesStore{}
	metabatch.ResolveSeriesNames(store, []metabatch.CandidateResult{{Book: metabatch.CandidateBookInfo{ID: "x"}}})
	if store.calls != 0 {
		t.Errorf("read issued with nothing to resolve")
	}
	failing := []metabatch.CandidateResult{{Book: metabatch.BuildCandidateBookInfoNoFiles(&database.Book{ID: "b", SeriesID: new(1)})}}
	metabatch.ResolveSeriesNames(&seriesStore{err: errors.New("x")}, failing)
	if failing[0].Book.Series != "" {
		t.Errorf("failed read set series %q", failing[0].Book.Series)
	}
	var none metabatch.SeriesBatchGetter
	metabatch.ResolveSeriesNames(none, failing) // nil store: no panic
}

// A partial runtime whose files carry sizes reports the most it can be, so the
// review row can warn about (and hide) a candidate the files cannot hold. Two
// 4 MB files with one measured at 480 s: the upper bound is the physical floor,
// 8 MB at 1000 B/s = 8000 s, which beats three times the 960 s estimate.
func TestBuildCandidateBookInfo_PartialRuntimeUpperBound(t *testing.T) {
	book := &database.Book{ID: "b1"}
	sized := &mockBookFileStore{files: []database.BookFile{
		{ID: "f1", FileSize: 4_000_000, Duration: 480},
		{ID: "f2", FileSize: 4_000_000},
	}}
	info := metabatch.BuildCandidateBookInfo(sized, book)
	if info.Duration != 0 || info.RuntimeLowerBoundSec != 480 || info.RuntimeUpperBoundSec != 8000 {
		t.Errorf("duration/lower/upper = %d/%d/%d, want 0/480/8000", info.Duration, info.RuntimeLowerBoundSec, info.RuntimeUpperBoundSec)
	}

	// No sizes: no estimate, so no upper bound; the lower bound still shows.
	unsized := &mockBookFileStore{files: []database.BookFile{
		{ID: "f1", Duration: 480},
		{ID: "f2"},
	}}
	info = metabatch.BuildCandidateBookInfo(unsized, book)
	if info.RuntimeLowerBoundSec != 480 || info.RuntimeUpperBoundSec != 0 {
		t.Errorf("unsized lower/upper = %d/%d, want 480/0", info.RuntimeLowerBoundSec, info.RuntimeUpperBoundSec)
	}

	// A complete runtime is a total, not a bound.
	complete := &mockBookFileStore{files: []database.BookFile{
		{ID: "f1", FileSize: 4_000_000, Duration: 480},
		{ID: "f2", FileSize: 4_000_000, Duration: 500},
	}}
	info = metabatch.BuildCandidateBookInfo(complete, book)
	if info.Duration != 980 || info.RuntimeLowerBoundSec != 0 || info.RuntimeUpperBoundSec != 0 {
		t.Errorf("complete duration/lower/upper = %d/%d/%d, want 980/0/0", info.Duration, info.RuntimeLowerBoundSec, info.RuntimeUpperBoundSec)
	}

	// A failed read is an unknown runtime.
	info = metabatch.BuildCandidateBookInfo(&mockBookFileStore{files: sized.files, err: errors.New("boom")}, book)
	if info.RuntimeLowerBoundSec != 0 || info.RuntimeUpperBoundSec != 0 {
		t.Errorf("failed read lower/upper = %d/%d, want 0/0", info.RuntimeLowerBoundSec, info.RuntimeUpperBoundSec)
	}
}
