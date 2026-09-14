// file: internal/dedup/book_dedup_soft_delete_test.go
// version: 1.0.0
// guid: 4f0c9a2e-6b1d-4e7a-9c3f-2d8b5e1a7f60
// last-edited: 2026-09-14

package dedup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// seedHealPair creates a keep book and a loser, each with its own book_file,
// and gives the loser an iTunes PID mapping. It is the shape the iTunes heal
// collapses: two rows for acoustically identical copies at different paths.
func seedHealPair(t *testing.T, store database.Store, loserPath string) (keepID, loserID string) {
	t.Helper()
	keepID = ulid.Make().String()
	loserID = ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Heal Keep", Format: "m4b"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "Heal Loser", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		BookID: keepID, FilePath: "/audio/keep/book.m4b", FileHash: "hash-keep-" + keepID, FileSize: 5 << 20, Duration: 3600,
	}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		BookID: loserID, FilePath: loserPath, FileHash: "hash-loser-" + loserID, FileSize: 5 << 20, Duration: 3600,
	}))
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "PID-" + loserID, BookID: loserID,
	}))
	return keepID, loserID
}

// TestMergeBooks_SoftDeletesLoserAndReassignsExternalIDs is the A1#11
// regression test. dedup.MergeBooks (the iTunes heal's collapse) used to call
// store.DeleteBook on the loser: the row and its metadata were gone for good,
// its book_file rows were left pointing at no book, and its ext_id:* mapping
// kept naming a book that no longer existed. It fails on the hard-delete code
// at the first NotNil.
func TestMergeBooks_SoftDeletesLoserAndReassignsExternalIDs(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID, loserID := seedHealPair(t, store, "/audio/loser/book.m4b")

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 1, res.MergedCount)

	loser, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.NotNil(t, loser, "the loser must be soft-deleted, not hard-deleted")
	require.True(t, loser.IsSoftDeleted(), "the loser must be marked for deletion")

	files, err := store.GetBookFiles(loserID)
	require.NoError(t, err)
	require.Len(t, files, 1, "the loser keeps its book_file rows, so a restore brings them back")

	owner, err := store.GetBookByExternalID("itunes", "PID-"+loserID)
	require.NoError(t, err)
	require.Equal(t, keepID, owner, "the loser's iTunes PID must map to the kept book")

	keep, err := store.GetBookByID(keepID)
	require.NoError(t, err)
	require.False(t, keep.IsSoftDeleted())
}

// TestMergeBooks_RefusesSoftDeletedKeep: collapsing live rows into a book that
// is itself on the purge clock would put every survivor's audio on that clock.
func TestMergeBooks_RefusesSoftDeletedKeep(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID, loserID := seedHealPair(t, store, "/audio/loser/book.m4b")
	require.NoError(t, merge.SoftDeleteBook(store, keepID))

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	var sd *merge.SoftDeletedInputError
	require.True(t, errors.As(err, &sd), "want *merge.SoftDeletedInputError, got %T: %v", err, err)
	require.True(t, sd.AsPrimary)
	require.Equal(t, keepID, sd.BookID)
	require.True(t, merge.IsRefusal(err))
	require.Equal(t, 0, res.MergedCount)

	loser, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.False(t, loser.IsSoftDeleted(), "a refused merge must not touch the loser")
	owner, err := store.GetBookByExternalID("itunes", "PID-"+loserID)
	require.NoError(t, err)
	require.Equal(t, loserID, owner, "a refused merge must not move external IDs")
}

// TestMergeBooks_LeavesLoserLiveWhenItSharesKeepAudio: a purge with
// delete-files removes a soft-deleted book's own file paths. If the loser
// names the kept book's audio file, soft-deleting it would put that file on
// the purge clock, so the loser is left live and the conflict reported.
func TestMergeBooks_LeavesLoserLiveWhenItSharesKeepAudio(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID, loserID := seedHealPair(t, store, "/audio/loser/book.m4b")

	loser, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	loser.FilePath = "/audio/keep/book.m4b" // the kept book's audio file
	_, err = store.UpdateBook(loserID, loser)
	require.NoError(t, err)

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, res.MergedCount)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0], "/audio/keep/book.m4b")

	after, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.False(t, after.IsSoftDeleted(), "a loser sharing the kept audio must stay live")
	owner, err := store.GetBookByExternalID("itunes", "PID-"+loserID)
	require.NoError(t, err)
	require.Equal(t, loserID, owner, "external IDs move only when the loser is actually retired")
}

// TestMergeBooks_LeavesLoserLiveWhenItsFileIsInsideKeepDir: the kept book's
// FilePath is a directory with no book_file rows (the ~20% single-FilePath
// class), and the loser's own FilePath is a file inside that directory. An
// exact-match check misses it, and a purge with delete-files would os.Remove
// a file the kept book plays through its directory.
func TestMergeBooks_LeavesLoserLiveWhenItsFileIsInsideKeepDir(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Dir Keep", Format: "mp3", FilePath: "/audio/keep"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "File Loser", Format: "mp3", FilePath: "/audio/keep/ch1.mp3"})
	require.NoError(t, err)

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, res.MergedCount)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0], "/audio/keep/ch1.mp3")

	after, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.False(t, after.IsSoftDeleted(), "a loser inside the kept book's directory must stay live")
}

// TestMergeBooks_SiblingDirIsNotShared: a path-prefix check without a
// separator boundary would call "/audio/keep (2)" part of "/audio/keep" and
// refuse the ordinary organize-bug duplicate the heal exists to collapse.
func TestMergeBooks_SiblingDirIsNotShared(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Dir Keep", Format: "mp3", FilePath: "/audio/keep"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "Dir Loser", Format: "mp3", FilePath: "/audio/keep (2)"})
	require.NoError(t, err)

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 1, res.MergedCount)
}

// TestMergeBooks_AlreadySoftDeletedLoserLeftAsIs: re-marking a soft-deleted
// row restarts its retention clock, and reassigning its IDs would strip the
// ones a restore brings back. Same rule as merge.Service.MergeBooks.
func TestMergeBooks_AlreadySoftDeletedLoserLeftAsIs(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID, loserID := seedHealPair(t, store, "/audio/loser/book.m4b")

	loser, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	tr := true
	past := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	loser.MarkedForDeletion = &tr
	loser.MarkedForDeletionAt = &past
	_, err = store.UpdateBook(loserID, loser)
	require.NoError(t, err)

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 0, res.MergedCount)

	after, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.True(t, after.IsSoftDeleted())
	require.NotNil(t, after.MarkedForDeletionAt)
	require.True(t, after.MarkedForDeletionAt.Equal(past), "retention clock must not restart: got %v want %v", after.MarkedForDeletionAt, past)
	owner, err := store.GetBookByExternalID("itunes", "PID-"+loserID)
	require.NoError(t, err)
	require.Equal(t, loserID, owner, "an already-retired loser keeps its IDs for a restore")
}
