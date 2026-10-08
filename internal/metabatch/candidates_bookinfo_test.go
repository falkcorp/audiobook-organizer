// file: internal/metabatch/candidates_bookinfo_test.go
// version: 1.0.1
// guid: 52b4b4c0-7157-4e40-901c-2a88bddf805e
// last-edited: 2026-10-07

package metabatch_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// The review cards' book-info block reads these off the list response.
func TestBuildCandidateBookInfo_ReviewCardFields(t *testing.T) {
	book := &database.Book{
		ID:                "b1",
		Title:             "T",
		Narrator:          strp("Reader One"),
		ASIN:              strp("B000TEST01"),
		ISBN10:            strp("0000000001"),
		ISBN13:            strp("9780000000002"),
		SeriesID:          intp(7),
		Series:            &database.Series{ID: 7, Name: "Saga"},
		SeriesPositionRaw: strp("1.5"),
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
	book := &database.Book{ID: "b1", ISBN10: strp("0000000001"), SeriesSequence: intp(3)}
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
	stale := &database.Book{ID: "b0", SeriesID: intp(9), Series: &database.Series{ID: 8, Name: "Old"}}
	if info := metabatch.BuildCandidateBookInfoNoFiles(stale); info.Series != "" {
		t.Fatalf("stale embedded series leaked: %q", info.Series)
	}
	var results []metabatch.CandidateResult
	for i := 0; i < 500; i++ {
		b := &database.Book{ID: fmt.Sprintf("b%d", i), SeriesID: intp(i % 3)}
		results = append(results, metabatch.CandidateResult{Book: metabatch.BuildCandidateBookInfoNoFiles(b)})
	}
	embedded := &database.Book{ID: "e", SeriesID: intp(5), Series: &database.Series{ID: 5, Name: "Kept"}}
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
	failing := []metabatch.CandidateResult{{Book: metabatch.BuildCandidateBookInfoNoFiles(&database.Book{ID: "b", SeriesID: intp(1)})}}
	metabatch.ResolveSeriesNames(&seriesStore{err: errors.New("x")}, failing)
	if failing[0].Book.Series != "" {
		t.Errorf("failed read set series %q", failing[0].Book.Series)
	}
	var none metabatch.SeriesBatchGetter
	metabatch.ResolveSeriesNames(none, failing) // nil store: no panic
}
