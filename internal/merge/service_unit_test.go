// file: internal/merge/service_unit_test.go
// version: 1.1.0

package merge

import (
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// helpers

func ptr[T any](v T) *T { return &v }

func newBook(id, title, format, path string) *database.Book {
	return &database.Book{
		ID:       id,
		Title:    title,
		Format:   format,
		FilePath: path,
	}
}

// ---------- MergeBooks error paths ----------

func TestUnit_MergeBooks_GetBookByID_Error(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	mockStore.EXPECT().GetBookByID("book-1").Return(nil, fmt.Errorf("db connection lost"))

	_, err := svc.MergeBooks([]string{"book-1", "book-2"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "book-1 not found")
}

func TestUnit_MergeBooks_SecondBookNotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "Title A", "mp3", "/tmp/a.mp3")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(nil, nil) // nil book, no error

	_, err := svc.MergeBooks([]string{"book-1", "book-2"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "book-2 not found")
}

func TestUnit_MergeBooks_PrimaryIDNotInList(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "A", "mp3", "/tmp/a.mp3")
	book2 := newBook("book-2", "B", "m4b", "/tmp/b.m4b")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)

	_, err := svc.MergeBooks([]string{"book-1", "book-2"}, "book-999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary_id book-999 not in book_ids")
}

func TestUnit_MergeBooks_UpdateBookFails(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "A", "mp3", "/tmp/a.mp3")
	book2 := newBook("book-2", "B", "m4b", "/tmp/b.m4b")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)

	// The provisional-scan guard reads each book's files before any write.
	// No files => nothing provisional => the merge proceeds.
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)
	mockStore.EXPECT().GetBookFiles("book-2").Return(nil, nil)

	// The loop iterates in order: book-1 then book-2. Fail on the first.
	mockStore.EXPECT().UpdateBook("book-1", mock.Anything).Return(nil, fmt.Errorf("disk full"))

	_, err := svc.MergeBooks([]string{"book-1", "book-2"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update book")
}

// ---------- MergeBooks happy path with mocks ----------

func TestUnit_MergeBooks_AutoSelectM4B(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "A", "mp3", "/tmp/a.mp3")
	book2 := newBook("book-2", "A", "m4b", "/tmp/a.m4b")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)

	// The provisional-scan guard reads each book's files before any write.
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)
	mockStore.EXPECT().GetBookFiles("book-2").Return(nil, nil)

	// UpdateBook called for both books in the version-group loop
	mockStore.EXPECT().UpdateBook("book-1", mock.Anything).Return(book1, nil)
	mockStore.EXPECT().UpdateBook("book-2", mock.Anything).Return(book2, nil)

	// Loser cleanup: GetExternalIDsForBook, ReassignExternalIDs, then SoftDeleteBook
	mockStore.EXPECT().GetExternalIDsForBook("book-1").Return(nil, nil)
	mockStore.EXPECT().ReassignExternalIDs("book-1", "book-2").Return(nil)
	// SoftDeleteBook calls GetBookByID then UpdateBook again
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().UpdateBook("book-1", mock.Anything).Return(book1, nil)

	result, err := svc.MergeBooks([]string{"book-1", "book-2"}, "")
	require.NoError(t, err)
	assert.Equal(t, "book-2", result.PrimaryID, "M4B should be auto-selected")
	assert.Equal(t, 2, result.MergedCount)
	assert.NotEmpty(t, result.VersionGroupID)
}

func TestUnit_MergeBooks_ExplicitPrimaryOverridesAuto(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "A", "mp3", "/tmp/a.mp3")
	book2 := newBook("book-2", "A", "m4b", "/tmp/a.m4b")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)

	// The provisional-scan guard reads each book's files before any write.
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)
	mockStore.EXPECT().GetBookFiles("book-2").Return(nil, nil)

	// Both books updated in version-group loop
	mockStore.EXPECT().UpdateBook("book-1", mock.Anything).Return(book1, nil)
	mockStore.EXPECT().UpdateBook("book-2", mock.Anything).Return(book2, nil)

	// Loser is book-2 (explicit override)
	mockStore.EXPECT().GetExternalIDsForBook("book-2").Return(nil, nil)
	mockStore.EXPECT().ReassignExternalIDs("book-2", "book-1").Return(nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)
	mockStore.EXPECT().UpdateBook("book-2", mock.Anything).Return(book2, nil)

	result, err := svc.MergeBooks([]string{"book-1", "book-2"}, "book-1")
	require.NoError(t, err)
	assert.Equal(t, "book-1", result.PrimaryID, "explicit primary should override auto-select")
}

// ---------- SoftDeleteBook ----------

func TestUnit_SoftDeleteBook_UpdateFails_FallsBackToHardDelete(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	book := newBook("book-1", "A", "mp3", "/tmp/a.mp3")
	mockStore.EXPECT().GetBookByID("book-1").Return(book, nil)
	mockStore.EXPECT().UpdateBook("book-1", mock.Anything).Return(nil, fmt.Errorf("update failed"))
	mockStore.EXPECT().DeleteBook("book-1").Return(nil)

	err := SoftDeleteBook(mockStore, "book-1")
	assert.NoError(t, err)
}

func TestUnit_SoftDeleteBook_BookAlreadyGone(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetBookByID("book-1").Return(nil, nil)

	err := SoftDeleteBook(mockStore, "book-1")
	assert.NoError(t, err, "should be a no-op when book is already gone")
}

func TestUnit_SoftDeleteBook_GetBookByID_Error(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetBookByID("book-1").Return(nil, fmt.Errorf("connection refused"))

	err := SoftDeleteBook(mockStore, "book-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GetBookByID")
}

// ---------- BookIsBetter / BookCurationScore ----------

func TestUnit_BookIsBetter_ITunesGhostLoses(t *testing.T) {
	ghost := newBook("g", "X", "m4b", "/itunes/iTunes Media/x.m4b")
	ghost.Bitrate = ptr(256)

	organized := newBook("o", "X", "mp3", "/library/x.mp3")
	organized.Bitrate = ptr(64)

	assert.True(t, BookIsBetter(organized, ghost), "organized path should beat iTunes ghost")
	assert.False(t, BookIsBetter(ghost, organized), "iTunes ghost should lose")
}

func TestUnit_BookIsBetter_CurationBeatsFormat(t *testing.T) {
	pristine := newBook("p", "X", "m4b", "/lib/x.m4b")

	curated := newBook("c", "X", "mp3", "/lib/x.mp3")
	curated.MetadataReviewStatus = ptr("matched")
	curated.LastWrittenAt = ptr(time.Now())

	assert.True(t, BookIsBetter(curated, pristine), "curated book should beat pristine")
}

func TestUnit_BookIsBetter_SameFormatHigherBitrateWins(t *testing.T) {
	low := newBook("l", "X", "mp3", "/lib/a.mp3")
	low.Bitrate = ptr(64)

	high := newBook("h", "X", "mp3", "/lib/b.mp3")
	high.Bitrate = ptr(320)

	assert.True(t, BookIsBetter(high, low))
	assert.False(t, BookIsBetter(low, high))
}

func TestUnit_BookIsBetter_SameBitrateLargerFileWins(t *testing.T) {
	small := newBook("s", "X", "mp3", "/lib/a.mp3")
	small.FileSize = ptr(int64(100))

	large := newBook("l", "X", "mp3", "/lib/b.mp3")
	large.FileSize = ptr(int64(999))

	assert.True(t, BookIsBetter(large, small))
	assert.False(t, BookIsBetter(small, large))
}

func TestUnit_BookIsBetter_IdenticalBooksReturnsFalse(t *testing.T) {
	a := newBook("a", "X", "mp3", "/lib/x.mp3")
	b := newBook("b", "X", "mp3", "/lib/x.mp3")

	// When everything is equal, a is NOT better than b
	assert.False(t, BookIsBetter(a, b))
}

// ---------- BookTitle (collision.go) ----------

func TestUnit_BookTitle_ReturnsTitle(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookByID("book-1").Return(&database.Book{ID: "book-1", Title: "Foundation"}, nil)

	assert.Equal(t, "Foundation", BookTitle(mockStore, "book-1"))
}

func TestUnit_BookTitle_NotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookByID("missing").Return(nil, fmt.Errorf("not found"))

	assert.Equal(t, "", BookTitle(mockStore, "missing"))
}
