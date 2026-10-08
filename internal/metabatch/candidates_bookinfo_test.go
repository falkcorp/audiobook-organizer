// file: internal/metabatch/candidates_bookinfo_test.go
// version: 1.0.0
// guid: 52b4b4c0-7157-4e40-901c-2a88bddf805e
// last-edited: 2026-10-07

package metabatch_test

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
)

func strp(s string) *string { return &s }
func intp(i int) *int        { return &i }

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
	err   error
}

func (s *seriesStore) GetSeriesByID(id int) (*database.Series, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &database.Series{ID: id, Name: "Resolved"}, nil
}

func TestResolveSeriesName(t *testing.T) {
	// A stale embedded object (another series' id) is not trusted.
	book := &database.Book{ID: "b1", SeriesID: intp(9), Series: &database.Series{ID: 8, Name: "Old"}}
	info := metabatch.BuildCandidateBookInfoNoFiles(book)
	if info.Series != "" {
		t.Fatalf("stale embedded series leaked: %q", info.Series)
	}
	store := &seriesStore{}
	cache := map[int]string{}
	metabatch.ResolveSeriesName(store, &info, cache)
	again := metabatch.BuildCandidateBookInfoNoFiles(book)
	metabatch.ResolveSeriesName(store, &again, cache)
	if info.Series != "Resolved" || again.Series != "Resolved" {
		t.Errorf("resolved = %q / %q", info.Series, again.Series)
	}
	if store.calls != 1 {
		t.Errorf("series reads = %d, want 1 (memoized)", store.calls)
	}

	failing := metabatch.BuildCandidateBookInfoNoFiles(book)
	metabatch.ResolveSeriesName(&seriesStore{err: errors.New("x")}, &failing, nil)
	if failing.Series != "" {
		t.Errorf("failed read set series %q", failing.Series)
	}
	var none metabatch.SeriesGetter
	metabatch.ResolveSeriesName(none, &failing, nil) // nil store: no panic
}
