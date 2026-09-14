// file: internal/server/handlers/versions_split_one_book_test.go
// version: 1.3.0
// guid: 6f8a0c2e-4b1d-4a3f-9c5e-7d9f1b3a5c64
// last-edited: 2026-09-13

package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

func splitSource() *database.Book {
	authorID, seriesID := 7, 9
	narr := "Narrator"
	return &database.Book{ID: "src", Title: "Omnibus", AuthorID: &authorID, SeriesID: &seriesID, Narrator: &narr, Format: "mp3"}
}

func splitFiles() []database.BookFile {
	return []database.BookFile{
		{ID: "f1", BookID: "src", FilePath: "/lib/Omnibus/Book 1/01.mp3", Duration: 100, FileSize: 10},
		{ID: "f2", BookID: "src", FilePath: "/lib/Omnibus/Book 1/02.mp3", Duration: 200, FileSize: 20},
		{ID: "f3", BookID: "src", FilePath: "/lib/Omnibus/Book 2/01.mp3", Duration: 300, FileSize: 30},
	}
}

func splitReq(body string) (*gin.Context, *httptest.ResponseRecorder) {
	return newVersionsCtx(http.MethodPost, "/audiobooks/src/split-to-books", body, gin.Params{{Key: "id", Value: "src"}})
}

// Two of three files move, in ONE store call, into ONE new standalone book
// that carries the source's author/series/narrator and the requested title.
func TestSplitSegmentsToBooks_AsOneBook(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	src := splitSource()
	store.EXPECT().GetBookByID("src").Return(src, nil).Once()
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil).Once()

	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 1").Return(nil, nil).Once()
	var createdBook *database.Book
	store.EXPECT().CreateBook(mock.Anything).RunAndReturn(func(b *database.Book) (*database.Book, error) {
		createdBook = b
		out := *b
		out.ID = "new"
		return &out, nil
	})
	store.EXPECT().GetBookAuthors("src").Return([]database.BookAuthor{{BookID: "src", AuthorID: 7, Role: "author"}}, nil)
	store.EXPECT().SetBookAuthors("new", mock.MatchedBy(func(a []database.BookAuthor) bool {
		return len(a) == 1 && a[0].BookID == "new" && a[0].AuthorID == 7
	})).Return(nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f1", "f2"}, "src", "new").Return(nil).Once()
	store.EXPECT().GetExternalIDsForBook("src").Return(nil, nil).Maybe()
	store.EXPECT().GetBookFiles("src").Return(splitFiles()[2:], nil).Once()
	// The second read of the source is the post-move row, whose totals the
	// move's recompute has rewritten. The path update must write THAT row,
	// not the one read before the move (Duration 999 here stands for the
	// stale pre-move total).
	staleDur := 999
	src.Duration = &staleDur
	freshDur := 300
	fresh := *splitSource()
	fresh.Duration = &freshDur
	// The source has no FilePath the remaining file sits in, so it moves to
	// that file's FOLDER (never the file's own path), once that folder is
	// checked free.
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 2").Return(nil, nil).Once()
	var written *database.Book
	store.EXPECT().ModifyBook("src", mock.Anything).RunAndReturn(func(id string, fn func(*database.Book) error) (*database.Book, error) {
		b, err := modifyOn(fresh)(id, fn)
		written = b
		return b, err
	})
	store.EXPECT().GetBookByID("new").Return(&database.Book{ID: "new", Title: "Book One"}, nil)

	c, w := splitReq(`{"segment_ids":["f1","f2","f1"],"as_one_book":true,"title":"Book One"}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if createdBook == nil {
		t.Fatal("no book created")
	}
	if written == nil || written.FilePath != "/lib/Omnibus/Book 2" || written.Duration == nil || *written.Duration != 300 {
		t.Fatalf("the path write must land on the post-move row, got %+v", written)
	}
	if createdBook.Title != "Book One" || createdBook.VersionGroupID != nil || createdBook.IsPrimaryVersion != nil {
		t.Fatalf("new book must be standalone with the given title: %+v", createdBook)
	}
	if createdBook.AuthorID == nil || *createdBook.AuthorID != 7 || createdBook.SeriesID == nil || *createdBook.SeriesID != 9 ||
		createdBook.Narrator == nil || *createdBook.Narrator != "Narrator" {
		t.Fatalf("author/series/narrator not copied: %+v", createdBook)
	}
	if createdBook.FilePath != "/lib/Omnibus/Book 1" {
		t.Fatalf("FilePath = %q, want the selected files' common dir", createdBook.FilePath)
	}
	if createdBook.Duration != nil || createdBook.FileSize != nil {
		t.Fatalf("aggregates must come from the move's recompute, not a hand sum: %+v", createdBook)
	}
	if !strings.Contains(w.Body.String(), `"segments_moved":2`) {
		t.Fatalf("want segments_moved 2: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "warnings") {
		t.Fatalf("a clean split carries no warnings: %s", w.Body.String())
	}
}

// expectSplitThroughMove sets up a one-book split of f1+f2 (folder
// "/lib/Omnibus/Book 1") from src up to and including a successful move and
// an author read with no authors. The source's FilePath is srcPath.
func expectSplitThroughMove(store *handlersmocks.MockVersionsStore, srcPath string) {
	src := splitSource()
	src.FilePath = srcPath
	store.EXPECT().GetBookByID("src").Return(src, nil).Once()
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil).Once()
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 1").Return(nil, nil).Once()
	store.EXPECT().CreateBook(mock.Anything).RunAndReturn(func(b *database.Book) (*database.Book, error) {
		out := *b
		out.ID = "new"
		return &out, nil
	})
	store.EXPECT().MoveBookFilesToBook([]string{"f1", "f2"}, "src", "new").Return(nil).Once()
	store.EXPECT().GetBookAuthors("src").Return(nil, nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles()[2:], nil).Once()
	store.EXPECT().GetBookByID("new").Return(&database.Book{ID: "new"}, nil).Once()
}

func splitOneBookReq() (*gin.Context, *httptest.ResponseRecorder) {
	return splitReq(`{"segment_ids":["f1","f2"],"as_one_book":true}`)
}

// The source's library_state is copied onto the new book, so ABS (which
// lists only "organized" books) shows both halves.
func TestSplitSegmentsToBooks_AsOneBookCopiesLibraryState(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	organized := "organized"
	src := splitSource()
	src.FilePath = "/lib/Omnibus"
	src.LibraryState = &organized
	store.EXPECT().GetBookByID("src").Return(src, nil).Once()
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil).Once()
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 1").Return(nil, nil).Once()
	store.EXPECT().CreateBook(mock.MatchedBy(func(b *database.Book) bool {
		return b.LibraryState != nil && *b.LibraryState == "organized"
	})).Return(&database.Book{ID: "new"}, nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f1", "f2"}, "src", "new").Return(nil).Once()
	store.EXPECT().GetBookAuthors("src").Return(nil, nil)
	store.EXPECT().GetExternalIDsForBook("src").Return(nil, nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles()[2:], nil).Once()
	store.EXPECT().GetBookByID("new").Return(&database.Book{ID: "new"}, nil).Once()

	c, w := splitOneBookReq()
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The source still contains its remaining file, so its path is left alone:
// no UpdateBook at all (the strict mock fails on one).
func TestSplitSegmentsToBooks_AsOneBookSourcePathKeptWhenFilesStillInside(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	expectSplitThroughMove(store, "/lib/Omnibus")
	store.EXPECT().GetExternalIDsForBook("src").Return(nil, nil)

	c, w := splitOneBookReq()
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The files moved, but the source's path update failed: 207 with the reason
// in warnings, not a 200 over a log line.
func TestSplitSegmentsToBooks_AsOneBookSourcePathUpdateFailureIs207(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	expectSplitThroughMove(store, "")
	store.EXPECT().GetExternalIDsForBook("src").Return(nil, nil)
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 2").Return(nil, nil).Once()
	store.EXPECT().ModifyBook("src", mock.Anything).Return(nil, errors.New("disk full"))

	c, w := splitOneBookReq()
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "warnings") ||
		!strings.Contains(w.Body.String(), "disk full") {
		t.Fatalf("want 207 with the update failure in warnings, got %d: %s", w.Code, w.Body.String())
	}
}

// The remaining files' folder already belongs to another book: the source's
// path is left as is and the operator is told why.
func TestSplitSegmentsToBooks_AsOneBookSourceTargetOccupiedIs207(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	expectSplitThroughMove(store, "")
	store.EXPECT().GetExternalIDsForBook("src").Return(nil, nil)
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 2").Return([]string{"other"}, nil).Once()

	c, w := splitOneBookReq()
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "other") {
		t.Fatalf("want 207 naming the occupant, got %d: %s", w.Code, w.Body.String())
	}
}

// An external-ID mapping that could not be moved to the new book is reported.
func TestSplitSegmentsToBooks_AsOneBookExternalIDFailureIs207(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	expectSplitThroughMove(store, "/lib/Omnibus")
	store.EXPECT().GetExternalIDsForBook("src").Return([]database.ExternalIDMapping{
		{Source: "itunes", ExternalID: "PID1", BookID: "src", FilePath: "/lib/Omnibus/Book 1/01.mp3"},
	}, nil)
	store.EXPECT().ReassignExternalID("itunes", "PID1", "new").Return(errors.New("write refused"))

	c, w := splitOneBookReq()
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "external IDs") ||
		!strings.Contains(w.Body.String(), "write refused") {
		t.Fatalf("want 207 with the external-ID failure in warnings, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSplitSegmentsToBooks_AsOneBookRejectsUnknownFile(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("src").Return(splitSource(), nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil)

	c, w := splitReq(`{"segment_ids":["f1","nope"],"as_one_book":true}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "nope") {
		t.Fatalf("want 400 naming the unknown id, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSplitSegmentsToBooks_AsOneBookRejectsEveryFile(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("src").Return(splitSource(), nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil)

	c, w := splitReq(`{"segment_ids":["f1","f2","f3"],"as_one_book":true}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

// A failed move deletes the book the split just created, so a retry never
// accumulates empty books; no author, path or aggregate write follows (the
// mock fails on any call not expected here).
func TestSplitSegmentsToBooks_AsOneBookMoveFailure(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("src").Return(splitSource(), nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil)
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 2/01.mp3").Return(nil, nil)
	store.EXPECT().CreateBook(mock.Anything).Return(&database.Book{ID: "new"}, nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f3"}, "src", "new").Return(errors.New("file not found: f3"))
	store.EXPECT().DeleteBook("new").Return(nil).Once()

	c, w := splitReq(`{"segment_ids":["f3"],"as_one_book":true}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "new book was deleted") {
		t.Fatalf("want 500 saying the new book was deleted, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "created_book_id") {
		t.Fatalf("a deleted book must not be named as left behind: %s", w.Body.String())
	}
}

// When the cleanup delete fails too, the empty book is named so an operator
// can remove it.
func TestSplitSegmentsToBooks_AsOneBookMoveAndCleanupFailure(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("src").Return(splitSource(), nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil)
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 2/01.mp3").Return(nil, nil)
	store.EXPECT().CreateBook(mock.Anything).Return(&database.Book{ID: "new"}, nil)
	store.EXPECT().MoveBookFilesToBook([]string{"f3"}, "src", "new").Return(errors.New("file not found: f3"))
	store.EXPECT().DeleteBook("new").Return(errors.New("disk full"))

	c, w := splitReq(`{"segment_ids":["f3"],"as_one_book":true}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"created_book_id":"new"`) {
		t.Fatalf("want 500 naming the leftover book, got %d: %s", w.Code, w.Body.String())
	}
}

// LiveBookIDsAtPath fails closed; its error must refuse the split, not be
// read as "path free". Nothing is created (the mock fails on any write).
func TestSplitSegmentsToBooks_AsOneBookPathLookupError(t *testing.T) {
	store := handlersmocks.NewMockVersionsStore(t)
	store.EXPECT().GetBookByID("src").Return(splitSource(), nil)
	store.EXPECT().GetBookFiles("src").Return(splitFiles(), nil)
	store.EXPECT().LiveBookIDsAtPath("/lib/Omnibus/Book 1").Return(nil, errors.New("1 book row(s) cannot be decoded"))

	c, w := splitReq(`{"segment_ids":["f1","f2"],"as_one_book":true}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", w.Code, w.Body.String())
	}
}
