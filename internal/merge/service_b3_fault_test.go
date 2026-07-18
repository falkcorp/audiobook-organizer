// file: internal/merge/service_b3_fault_test.go
// version: 1.0.0
// guid: 9c1d3e7a-4b6f-4a2d-8c5e-1f0a3b7d9e42
// last-edited: 2026-07-18

package merge

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// b3FaultStore wraps a real database.Store and lets a test force specific
// methods to fail (or return a fixed value), while delegating everything
// else to the embedded store. A real PebbleStore's writes don't fail under
// normal test conditions, so this is how the warn-and-continue /
// abort-with-error branches in MergeBooks and CombineBooks that depend on a
// store call failing get exercised without hand-rolling a full mock
// expectation sequence for the whole read-modify-write.
type b3FaultStore struct {
	database.Store

	failReassignExternalIDsFor map[string]bool
	failMoveBookFilesToBookFor map[string]bool
	failDeleteBookFor          map[string]bool
	failRecomputeAggregates    bool
	failUpdateBook             bool
	failGetAuthorByName        bool
	failCreateAuthor           bool
	failSetBookAuthors         bool
	failCreateBookFile         bool

	// stickyFilesFor forces GetBookFiles(id) to always report a fixed
	// non-empty file list for id, regardless of the underlying store's real
	// state — used to simulate the "book still owns files after move" guard.
	stickyFilesFor map[string][]database.BookFile

	// vanishAfterFirstGet makes GetBookByID(id) return (nil, nil) starting
	// on the SECOND call for this id (the first call — validation — still
	// sees the real book).
	vanishAfterFirstGet string
	getCount            map[string]int

	// failGetBookByIDAfterFirstFor makes GetBookByID(id) return an error for
	// id starting on its SECOND call — the first call is MergeBooks' own
	// initial fetch (must succeed so the merge proceeds); the second is
	// SoftDeleteBook's own re-fetch inside the loser-cleanup loop, which is
	// the call this is meant to fail.
	failGetBookByIDAfterFirstFor map[string]bool
}

func (f *b3FaultStore) GetBookByID(id string) (*database.Book, error) {
	if f.failGetBookByIDAfterFirstFor[id] {
		if f.getCount == nil {
			f.getCount = map[string]int{}
		}
		f.getCount[id]++
		if f.getCount[id] > 1 {
			return nil, fmt.Errorf("b3 injected GetBookByID failure")
		}
		return f.Store.GetBookByID(id)
	}
	if f.vanishAfterFirstGet == id {
		if f.getCount == nil {
			f.getCount = map[string]int{}
		}
		f.getCount[id]++
		if f.getCount[id] > 1 {
			return nil, nil
		}
	}
	return f.Store.GetBookByID(id)
}

func (f *b3FaultStore) GetBookFiles(id string) ([]database.BookFile, error) {
	if files, ok := f.stickyFilesFor[id]; ok {
		return files, nil
	}
	return f.Store.GetBookFiles(id)
}

func (f *b3FaultStore) ReassignExternalIDs(oldID, newID string) error {
	if f.failReassignExternalIDsFor[oldID] {
		return fmt.Errorf("b3 injected ReassignExternalIDs failure")
	}
	return f.Store.ReassignExternalIDs(oldID, newID)
}

func (f *b3FaultStore) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	if f.failMoveBookFilesToBookFor[sourceBookID] {
		return fmt.Errorf("b3 injected MoveBookFilesToBook failure")
	}
	return f.Store.MoveBookFilesToBook(fileIDs, sourceBookID, targetBookID)
}

func (f *b3FaultStore) DeleteBook(id string) error {
	if f.failDeleteBookFor[id] {
		return fmt.Errorf("b3 injected DeleteBook failure")
	}
	return f.Store.DeleteBook(id)
}

func (f *b3FaultStore) RecomputeBookAggregates(bookID string) error {
	if f.failRecomputeAggregates {
		return fmt.Errorf("b3 injected RecomputeBookAggregates failure")
	}
	return f.Store.RecomputeBookAggregates(bookID)
}

func (f *b3FaultStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	if f.failUpdateBook {
		return nil, fmt.Errorf("b3 injected UpdateBook failure")
	}
	return f.Store.UpdateBook(id, b)
}

func (f *b3FaultStore) GetAuthorByName(name string) (*database.Author, error) {
	if f.failGetAuthorByName {
		return nil, fmt.Errorf("b3 injected GetAuthorByName failure")
	}
	return f.Store.GetAuthorByName(name)
}

func (f *b3FaultStore) CreateAuthor(name string) (*database.Author, error) {
	if f.failCreateAuthor {
		return nil, fmt.Errorf("b3 injected CreateAuthor failure")
	}
	return f.Store.CreateAuthor(name)
}

func (f *b3FaultStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	if f.failSetBookAuthors {
		return fmt.Errorf("b3 injected SetBookAuthors failure")
	}
	return f.Store.SetBookAuthors(bookID, authors)
}

func (f *b3FaultStore) CreateBookFile(file *database.BookFile) error {
	if f.failCreateBookFile {
		return fmt.Errorf("b3 injected CreateBookFile failure")
	}
	return f.Store.CreateBookFile(file)
}

// ---------- MergeBooks warn-and-continue branches ----------

func TestB3_MergeBooks_ReassignExternalIDsError_NonFatal(t *testing.T) {
	real := setupTestStore(t)
	fault := &b3FaultStore{Store: real}

	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3reid-loser.mp3"}
	winner := &database.Book{ID: ulid.Make().String(), Title: "Winner", Format: "m4b", FilePath: "/tmp/b3reid-winner.m4b"}
	_, err := real.CreateBook(loser)
	require.NoError(t, err)
	_, err = real.CreateBook(winner)
	require.NoError(t, err)

	fault.failReassignExternalIDsFor = map[string]bool{loser.ID: true}

	ms := NewService(fault)
	result, err := ms.MergeBooks([]string{loser.ID, winner.ID}, winner.ID)
	require.NoError(t, err, "a failed external-id reassignment must not fail the whole merge")
	assert.Equal(t, winner.ID, result.PrimaryID)

	// The loser is still soft-deleted despite the reassignment failure.
	l, err := real.GetBookByID(loser.ID)
	require.NoError(t, err)
	require.NotNil(t, l.MarkedForDeletion)
	assert.True(t, *l.MarkedForDeletion)
}

func TestB3_MergeBooks_SoftDeleteError_NonFatal(t *testing.T) {
	real := setupTestStore(t)
	fault := &b3FaultStore{Store: real}

	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3sderr-loser.mp3"}
	winner := &database.Book{ID: ulid.Make().String(), Title: "Winner", Format: "m4b", FilePath: "/tmp/b3sderr-winner.m4b"}
	_, err := real.CreateBook(loser)
	require.NoError(t, err)
	_, err = real.CreateBook(winner)
	require.NoError(t, err)

	// SoftDeleteBook's own GetBookByID re-fetch fails, so it can't even
	// attempt the fallback hard delete's read step.
	fault.failGetBookByIDAfterFirstFor = map[string]bool{loser.ID: true}

	ms := NewService(fault)
	result, err := ms.MergeBooks([]string{loser.ID, winner.ID}, winner.ID)
	require.NoError(t, err, "a failed soft-delete must be warned, not fail the whole merge")
	assert.Equal(t, winner.ID, result.PrimaryID)

	w, err := real.GetBookByID(winner.ID)
	require.NoError(t, err)
	require.NotNil(t, w.IsPrimaryVersion)
	assert.True(t, *w.IsPrimaryVersion, "winner must still be primary")
}

// ---------- CombineBooks warn-and-continue / abort branches ----------

func TestB3_CombineBooks_BookVanishesBetweenValidateAndProcess_SkippedGracefully(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/b3vanish-survivor.mp3"}
	ghost := &database.Book{ID: ulid.Make().String(), Title: "Ghost", Format: "mp3", FilePath: "/tmp/b3vanish-ghost.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(ghost)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, vanishAfterFirstGet: ghost.ID}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, ghost.ID}, survivor.ID, nil)
	require.NoError(t, err, "a book vanishing mid-combine must be skipped, not fail the whole combine")
	assert.Equal(t, 0, res.BooksDeleted, "the vanished book was never processed, so nothing was deleted")

	// The ghost book is untouched (never deleted, still real in the store —
	// our fault store only faked ITS OWN second GetBookByID return, the real
	// store still has the row).
	g, err := real.GetBookByID(ghost.ID)
	require.NoError(t, err)
	require.NotNil(t, g, "the real store never actually lost the book")
}

func TestB3_CombineBooks_MoveBookFilesToBookError(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/b3moveerr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3moveerr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)
	require.NoError(t, real.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: loser.ID, FilePath: loser.FilePath, Format: "mp3",
	}))

	fault := &b3FaultStore{Store: real, failMoveBookFilesToBookFor: map[string]bool{loser.ID: true}}
	ms := NewService(fault)

	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "move files")

	// Aborted before delete: the loser must still exist.
	l, err := real.GetBookByID(loser.ID)
	require.NoError(t, err)
	assert.NotNil(t, l, "loser must not be deleted when the file move failed")
}

func TestB3_CombineBooks_ReassignExternalIDsError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/b3creid-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3creid-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failReassignExternalIDsFor: map[string]bool{loser.ID: true}}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, nil)
	require.NoError(t, err, "a failed external-id reassignment must not fail the whole combine")
	assert.Equal(t, 1, res.BooksDeleted)

	l, err := real.GetBookByID(loser.ID)
	require.NoError(t, err)
	assert.Nil(t, l, "loser is still deleted despite the reassignment warning")
}

func TestB3_CombineBooks_FilesRemainingAfterMove_AbortsDelete(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/b3remain-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3remain-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)
	loserFile := database.BookFile{ID: ulid.Make().String(), BookID: loser.ID, FilePath: loser.FilePath, Format: "mp3"}
	require.NoError(t, real.CreateBookFile(&loserFile))

	// Force GetBookFiles(loser.ID) to always report the file is still there,
	// even after the real MoveBookFilesToBook call succeeds — simulating a
	// phantom stuck file so the "still owns files after move" guard fires.
	fault := &b3FaultStore{Store: real, stickyFilesFor: map[string][]database.BookFile{loser.ID: {loserFile}}}
	ms := NewService(fault)

	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still owns")
	assert.Contains(t, err.Error(), "aborting delete")

	l, err := real.GetBookByID(loser.ID)
	require.NoError(t, err)
	assert.NotNil(t, l, "the guard must abort BEFORE DeleteBook runs")
}

func TestB3_CombineBooks_DeleteBookError(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/b3delerr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3delerr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failDeleteBookFor: map[string]bool{loser.ID: true}}
	ms := NewService(fault)

	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete absorbed book")
}

func TestB3_CombineBooks_RecomputeAggregatesError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/b3aggerr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3aggerr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failRecomputeAggregates: true}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, nil)
	require.NoError(t, err, "a failed aggregate recompute must not fail the whole combine")
	assert.Equal(t, 1, res.BooksDeleted)
}

// ---------- CombineBooks override error branches ----------

func TestB3_CombineBooks_OverrideUpdateBookError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Old Title", Format: "mp3", FilePath: "/tmp/b3ovupderr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3ovupderr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	// UpdateBook is only reached from the override block in CombineBooks
	// (the main move/delete loop never calls it), so failing it always
	// exercises BOTH warn branches: the title/narrator write and the
	// author-backward-compat AuthorID write.
	fault := &b3FaultStore{Store: real, failUpdateBook: true}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID,
		&CombineOverride{Title: "New Title", Author: "Some New Author"})
	require.NoError(t, err, "a failed override write must not fail the whole combine")
	assert.Equal(t, 1, res.BooksDeleted)

	fresh, err := real.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Equal(t, "Old Title", fresh.Title, "the override write failed, so the title must be unchanged")
}

func TestB3_CombineBooks_OverrideGetAuthorByNameError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "T", Format: "mp3", FilePath: "/tmp/b3ovganerr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3ovganerr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failGetAuthorByName: true}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Author: "Whoever"})
	require.NoError(t, err, "a failed author lookup must not fail the whole combine")
	assert.Equal(t, 1, res.BooksDeleted)

	fresh, err := real.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Nil(t, fresh.AuthorID, "author lookup failed, so no author should have been linked")
}

func TestB3_CombineBooks_OverrideCreateAuthorError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "T", Format: "mp3", FilePath: "/tmp/b3ovcaerr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3ovcaerr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failCreateAuthor: true}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Author: "Brand New"})
	require.NoError(t, err, "a failed author creation must not fail the whole combine")
	assert.Equal(t, 1, res.BooksDeleted)

	fresh, err := real.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Nil(t, fresh.AuthorID, "author creation failed, so no author should have been linked")
}

func TestB3_CombineBooks_OverrideSetBookAuthorsError_StillSetsAuthorID(t *testing.T) {
	real := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "T", Format: "mp3", FilePath: "/tmp/b3ovsbaerr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3ovsbaerr-loser.mp3"}
	_, err := real.CreateBook(survivor)
	require.NoError(t, err)
	_, err = real.CreateBook(loser)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failSetBookAuthors: true}
	ms := NewService(fault)

	res, err := ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Author: "Reaches Fallback"})
	require.NoError(t, err, "a failed SetBookAuthors must not fail the whole combine")
	assert.Equal(t, 1, res.BooksDeleted)

	// SetBookAuthors failing does not gate the backward-compat AuthorID
	// write on the book row — the two are independent.
	fresh, err := real.GetBookByID(survivor.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.AuthorID, "book-row AuthorID should still be set even though SetBookAuthors failed")

	author, err := real.GetAuthorByName("Reaches Fallback")
	require.NoError(t, err)
	require.NotNil(t, author)
	assert.Equal(t, author.ID, *fresh.AuthorID)
}

// ---------- attachVirtualFile error branches (unexported helper) ----------

func TestB3_AttachVirtualFile_MoveBookFilesToBookError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	strayOwner := &database.Book{ID: ulid.Make().String(), Title: "Stray", FilePath: "/tmp/b3avmoveerr-stray.mp3"}
	target := &database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/b3avmoveerr-shared.mp3"}
	_, err := real.CreateBook(strayOwner)
	require.NoError(t, err)
	_, err = real.CreateBook(target)
	require.NoError(t, err)
	require.NoError(t, real.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: strayOwner.ID, FilePath: target.FilePath, Format: "mp3",
	}))

	fault := &b3FaultStore{Store: real, failMoveBookFilesToBookFor: map[string]bool{strayOwner.ID: true}}
	ms := NewService(fault)

	n := ms.attachVirtualFile(target, target.ID)
	assert.Equal(t, 0, n, "a failed reattach move must be warned and return 0, not panic")

	// The stray file is still owned by its original book — the move never
	// actually happened.
	strayFiles, err := real.GetBookFiles(strayOwner.ID)
	require.NoError(t, err)
	assert.Len(t, strayFiles, 1)
}

func TestB3_AttachVirtualFile_CreateBookFileError_NonFatal(t *testing.T) {
	real := setupTestStore(t)

	b := &database.Book{ID: ulid.Make().String(), Title: "No existing row", FilePath: "/tmp/b3avcreateerr.mp3"}
	_, err := real.CreateBook(b)
	require.NoError(t, err)

	fault := &b3FaultStore{Store: real, failCreateBookFile: true}
	ms := NewService(fault)

	n := ms.attachVirtualFile(b, b.ID)
	assert.Equal(t, 0, n, "a failed CreateBookFile must be warned and return 0, not panic")

	files, err := real.GetBookFiles(b.ID)
	require.NoError(t, err)
	assert.Empty(t, files)
}
