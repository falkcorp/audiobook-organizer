// file: internal/dedup/book_runtime_duration_test.go
// version: 1.0.0
// guid: 6ae0f0e7-3cf7-42c7-b2c5-88b2a24e8c27
// last-edited: 2026-09-19

package dedup

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// chapterRows is n present file rows of sec seconds for bookID; only the
// first known rows carry a duration (the rest were never probed).
func chapterRows(bookID string, n, sec, known int) []database.BookFile {
	out := make([]database.BookFile, n)
	for i := range out {
		out[i] = database.BookFile{ID: fmt.Sprintf("%s-f%02d", bookID, i), BookID: bookID}
		if i < known {
			out[i].Duration = sec
		}
	}
	return out
}

// runDurationPair runs CheckBook on BOOK_A against BOOK_B (same author) with
// the given Book.Duration aggregates and file rows, and returns how many
// exact-layer candidates were emitted.
func runDurationPair(t *testing.T, durA, durB int, filesA, filesB []database.BookFile) int {
	t.Helper()
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false
	authorID := 1
	bookA := &database.Book{ID: "BOOK_A", Title: "Foundation", AuthorID: &authorID, Duration: &durA}
	bookB := &database.Book{ID: "BOOK_B", Title: "Foundation Novel", AuthorID: &authorID, Duration: &durB}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_A":
			return bookA, nil
		case "BOOK_B":
			return bookB, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Isaac Asimov"}, nil
	}
	mock.GetBookByFileHashFunc = func(string) (*database.Book, error) { return nil, nil }
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "BOOK_A":
			return filesA, nil
		case "BOOK_B":
			return filesB, nil
		}
		return nil, nil
	}
	mock.GetBooksByAuthorIDCoreFunc = func(int) ([]database.BookCore, error) {
		return []database.BookCore{bookA.Core(), bookB.Core()}, nil
	}
	mock.GetAllBooksFunc = func(int, int) ([]database.Book, error) { return nil, nil }
	if _, err := engine.CheckBook(context.Background(), "BOOK_A"); err != nil {
		t.Fatalf("CheckBook: %v", err)
	}
	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Layer: "exact"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	return total
}

// TestDurationMatch_PartialRuntimeIsNotEvidence: BOOK_A is 30 chapters × 20
// min with only two probed, so its Book.Duration is a 40-minute partial sum.
// A genuinely 40-minute BOOK_B must NOT pair with it on duration: the
// partial sum is a lower bound, not the book's length.
func TestDurationMatch_PartialRuntimeIsNotEvidence(t *testing.T) {
	filesB := []database.BookFile{{ID: "B-f", BookID: "BOOK_B", Duration: 2400}}
	if n := runDurationPair(t, 2400, 2400, chapterRows("BOOK_A", 30, 1200, 2), filesB); n != 0 {
		t.Fatalf("partial-runtime book paired on duration: %d candidates, want 0", n)
	}
}

// TestDurationMatch_MultiFileComparedAtFullRuntime: BOOK_A's 30 chapters are
// all probed (10 h) but its Book.Duration is stale at one chapter. The 10 h
// single-file BOOK_B must pair with it — the runtime is the sum of the files.
func TestDurationMatch_MultiFileComparedAtFullRuntime(t *testing.T) {
	filesB := []database.BookFile{{ID: "B-f", BookID: "BOOK_B", Duration: 36000}}
	if n := runDurationPair(t, 1200, 36000, chapterRows("BOOK_A", 30, 1200, 30), filesB); n == 0 {
		t.Fatal("10h multi-file book did not pair with a 10h copy: compared at the stale aggregate")
	}
}
