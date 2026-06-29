// file: internal/dedup/engine_acoustid_test.go
// version: 1.0.0
// guid: c8f25a0d-8e69-47f7-a36f-60c191b73810
// last-edited: 2026-06-28

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestAcoustIDScan_BoilerplateTitleDoesNotEmitCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	books := []database.Book{
		{ID: "BOOK_A", Title: "  THIS   IS\tAUDIBLE  "},
		{ID: "BOOK_B", Title: "A Real Audiobook"},
	}
	filesByBook := map[string][]database.BookFile{
		"BOOK_A": {{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/intro.mp3", AcoustIDSeg0: validFP80}},
		"BOOK_B": {{ID: "FILE_B", BookID: "BOOK_B", FilePath: "/lib/b/book.mp3", AcoustIDSeg0: validFP80}},
	}
	wireAcoustIDScanMock(mock, books, filesByBook)

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	_, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "acoustid",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected no acoustid candidates for boilerplate title, got %d", total)
	}
}

func TestAcoustIDScan_NormalTitleStillEmitsCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	books := []database.Book{
		{ID: "BOOK_A", Title: "A Real Audiobook"},
		{ID: "BOOK_B", Title: "A Real Audiobook"},
	}
	filesByBook := map[string][]database.BookFile{
		"BOOK_A": {{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/book.mp3", AcoustIDSeg0: validFP80}},
		"BOOK_B": {{ID: "FILE_B", BookID: "BOOK_B", FilePath: "/lib/b/book.mp3", AcoustIDSeg0: validFP80}},
	}
	wireAcoustIDScanMock(mock, books, filesByBook)

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	candidates, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "acoustid",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected one acoustid candidate for normal titles, got %d (%+v)", total, candidates)
	}
}

func TestAcoustIDScan_BoilerplateFileTitleDoesNotEmitCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	books := []database.Book{
		{ID: "BOOK_A", Title: "A Real Audiobook"},
		{ID: "BOOK_B", Title: "A Real Audiobook"},
	}
	filesByBook := map[string][]database.BookFile{
		"BOOK_A": {{ID: "FILE_A", BookID: "BOOK_A", Title: "This Is Audible", FilePath: "/lib/a/intro.mp3", AcoustIDSeg0: validFP80}},
		"BOOK_B": {{ID: "FILE_B", BookID: "BOOK_B", FilePath: "/lib/b/book.mp3", AcoustIDSeg0: validFP80}},
	}
	wireAcoustIDScanMock(mock, books, filesByBook)

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	_, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "acoustid",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected no acoustid candidates for boilerplate file title, got %d", total)
	}
}

func TestAcoustIDScan_NormalTitleContainingBoilerplateWordStillEmitsCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	books := []database.Book{
		{ID: "BOOK_A", Title: "Introduction to Algorithms"},
		{ID: "BOOK_B", Title: "Introduction to Algorithms"},
	}
	filesByBook := map[string][]database.BookFile{
		"BOOK_A": {{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/book.mp3", AcoustIDSeg0: validFP80}},
		"BOOK_B": {{ID: "FILE_B", BookID: "BOOK_B", FilePath: "/lib/b/book.mp3", AcoustIDSeg0: validFP80}},
	}
	wireAcoustIDScanMock(mock, books, filesByBook)

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	candidates, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "acoustid",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected one acoustid candidate for real title, got %d (%+v)", total, candidates)
	}
}

func wireAcoustIDScanMock(
	mock *database.MockStore,
	books []database.Book,
	filesByBook map[string][]database.BookFile,
) {
	filesBySeg := make(map[string]database.BookFile)
	for _, files := range filesByBook {
		for _, file := range files {
			if file.AcoustIDSeg0 != "" {
				filesBySeg[file.AcoustIDSeg0] = file
			}
		}
	}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return books, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return filesByBook[bookID], nil
	}
	mock.GetBookFileByAcoustIDFunc = func(fingerprint string) (*database.BookFile, error) {
		if file, ok := filesBySeg[fingerprint]; ok {
			return &file, nil
		}
		return nil, nil
	}
}
