// file: internal/dedup/book_runtime_duration_test.go
// version: 1.0.3
// guid: 6ae0f0e7-3cf7-42c7-b2c5-88b2a24e8c27
// last-edited: 2026-09-19

package dedup

import (
	"context"
	"fmt"
	"testing"
	"time"

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

// introAndMissingChapters is a book whose only present file is a 45 s intro;
// its 20 × 30 min chapters are rows flagged missing with no present copy.
func introAndMissingChapters(bookID string) []database.BookFile {
	files := []database.BookFile{{ID: bookID + "-intro", BookID: bookID, FilePath: "/lib/x/00 intro.mp3", Duration: 45}}
	for i := 0; i < 20; i++ {
		files = append(files, database.BookFile{
			ID: fmt.Sprintf("%s-m%02d", bookID, i), BookID: bookID,
			FilePath: fmt.Sprintf("/old/x/%02d.mp3", i+1), Duration: 1800, Missing: true,
		})
	}
	return files
}

// TestDrainStale_MissingChaptersAreNotAShortBook (review ERROR 3): the
// min-duration gate must not read "45 s intro present, chapters missing" as a
// 45-second book — drain would soft-delete the candidate as short_duration.
func TestDrainStale_MissingChaptersAreNotAShortBook(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(36045), files: introAndMissingChapters("BOOK_A")},
		{id: "BOOK_B", title: "A Real Book", duration: new(36000), files: chapterRows("BOOK_B", 20, 1800, 20)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if n := res.ReasonCounts[drainReasonShortDuration]; n != 0 {
		t.Fatalf("book with missing chapters drained as short_duration (%v)", res.ReasonCounts)
	}
	if engine.hasKnownShortDuration(&database.Book{ID: "BOOK_A"}) {
		t.Fatal("hasKnownShortDuration: missing chapters read as a 45 s book")
	}
}

// TestDrainStale_ReadsEachBooksFilesOnce: the drain's runtime gates cost one
// GetBookFiles per BOOK, not per candidate.
func TestDrainStale_ReadsEachBooksFilesOnce(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	reads := map[string]int{}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, Title: "Book " + id}, nil
	}
	mock.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		reads[id]++
		return chapterRows(id, 3, 1200, 3), nil
	}
	for _, other := range []string{"B", "C", "D"} {
		seedDrainCandidate(t, es, "A", other)
	}
	if _, err := engine.DrainStaleCandidates(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	for id, n := range reads {
		if n != 1 {
			t.Fatalf("GetBookFiles(%s) called %d times, want 1 (all: %v)", id, n, reads)
		}
	}
}

// TestDurationMatch_NoFileReadForTitleMismatches: checkDurationMatch reads
// file rows only for same-author books that pass every title guard, since
// only those can act.
func TestDurationMatch_NoFileReadForTitleMismatches(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	authorID := 1
	d := 36000
	book := &database.Book{ID: "A", Title: "Foundation", AuthorID: &authorID, Duration: &d}
	cores := []database.BookCore{book.Core()}
	for i := 0; i < 10; i++ {
		o := &database.Book{ID: fmt.Sprintf("O%d", i), Title: fmt.Sprintf("Entirely Different Story %c", 'A'+i), AuthorID: &authorID, Duration: &d}
		cores = append(cores, o.Core())
	}
	reads := map[string]int{}
	mock.GetBooksByAuthorIDCoreFunc = func(int) ([]database.BookCore, error) { return cores, nil }
	mock.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		reads[id]++
		return chapterRows(id, 1, 36000, 1), nil
	}
	if err := engine.checkDurationMatch(book); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads["A"] != 1 {
		t.Fatalf("file reads = %v, want only the book itself", reads)
	}
}

// TestBookRuntimeMemo_ReadsOncePerUpdatedAt: within a run the memo reads a
// book's rows once, and again only after the book row changed.
func TestBookRuntimeMemo_ReadsOncePerUpdatedAt(t *testing.T) {
	reads := 0
	store := filesGetterFunc(func(id string) ([]database.BookFile, error) {
		reads++
		return chapterRows(id, 2, 600, 2), nil
	})
	memo := newBookRuntimeMemo()
	t0 := time.Unix(1_700_000_000, 0)
	book := &database.Book{ID: "A", UpdatedAt: &t0}
	for i := 0; i < 3; i++ {
		if rt, rows, ok := runtimeAndRows(store, memo, book); !ok || rows != 2 || rt.Seconds != 1200 {
			t.Fatalf("runtime = %+v rows=%d ok=%v", rt, rows, ok)
		}
	}
	if reads != 1 {
		t.Fatalf("reads = %d, want 1", reads)
	}
	t1 := t0.Add(time.Second)
	book.UpdatedAt = &t1
	runtimeAndRows(store, memo, book)
	if reads != 2 {
		t.Fatalf("reads after UpdatedAt changed = %d, want 2", reads)
	}
}

type filesGetterFunc func(string) ([]database.BookFile, error)

func (f filesGetterFunc) GetBookFiles(id string) ([]database.BookFile, error) { return f(id) }

// TestUpsertExactCandidate_ReadsEachBookOnce: the min-duration and
// part-vs-whole gates share one runtime read per book per call. The pair is
// a single-file part of a multi-file whole, so the part-vs-whole gate drops
// it before label capture (which reads files on its own).
func TestUpsertExactCandidate_ReadsEachBookOnce(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	reads := map[string]int{}
	mock.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		reads[id]++
		if id == "A" {
			return chapterRows(id, 1, 600, 1), nil
		}
		return chapterRows(id, 3, 1200, 3), nil
	}
	a := &database.Book{ID: "A", Title: "Foundation"}
	b := &database.Book{ID: "B", Title: "Foundation"}
	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatal(err)
	}
	if reads["A"] != 1 || reads["B"] != 1 {
		t.Fatalf("reads = %v, want one per book", reads)
	}
}

// TestRuntimeGates_NoStaleRuntimeAcrossCalls: a chapter that goes missing
// with no present copy and no duration known changes the runtime without
// moving the book's UpdatedAt. The next gate call must see it, so no runtime
// may be cached beyond one call.
func TestRuntimeGates_NoStaleRuntimeAcrossCalls(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	files := chapterRows("A", 1, 45, 1) // a 45 s single-file book: "short"
	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return files, nil }
	stamp := time.Unix(1_700_000_000, 0)
	book := &database.Book{ID: "A", UpdatedAt: &stamp}
	if !engine.hasKnownShortDuration(book) {
		t.Fatal("45 s book not short")
	}
	// A second row appears with no duration; UpdatedAt does not move.
	files = append(files, database.BookFile{ID: "A-2", BookID: "A"})
	if engine.hasKnownShortDuration(book) {
		t.Fatal("stale runtime: the book gained a row of unknown length but still reads as a complete 45 s book")
	}
}
