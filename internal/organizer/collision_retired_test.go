// file: internal/organizer/collision_retired_test.go
// version: 1.0.0
// guid: 8d2c5e90-31b7-4a6f-9c14-e07a5b3d2f68
// last-edited: 2026-10-26

package organizer

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A byte-identical occupant that is a RETIRED version's row still claims the
// path (its single-slot index entry is the only one). The resolver must read it
// as another row holding the kept file and tombstone ours, never repoint onto
// the path: that takes the slot from the retired row, and nothing rebuilds it.
func TestRenameFiles_RetiredOccupantBlocksTheRepoint(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "identical-bytes")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "identical-bytes")

	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()

	holder, err := store.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(root, "holder")})
	require.NoError(t, err)
	shell, err := store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: filepath.Join(root, "shell")})
	require.NoError(t, err)
	mine, err := store.CreateBook(&database.Book{Title: "Court of Owls", FilePath: src})
	require.NoError(t, err)
	v, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)

	retired := &database.BookFile{BookID: shell.ID, FilePath: dst, Format: "m4b", FileSize: 15}
	require.NoError(t, store.CreateBookFile(retired))
	require.NoError(t, store.RetireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{retired.ID}, SourceBookID: shell.ID}}, holder.ID, v.ID))
	live := &database.BookFile{BookID: mine.ID, FilePath: src, Format: "m4b", FileSize: 15}
	require.NoError(t, store.CreateBookFile(live))

	holderOf, err := database.BookFileByPathIncludingRetired(store, dst)
	require.NoError(t, err)
	require.NotNil(t, holderOf, "precondition: the retired row holds the destination's path key")
	require.Equal(t, retired.ID, holderOf.ID)

	_, err = RenameFiles(
		[]FileRenameEntry{{SegmentID: live.ID, SourcePath: src, TargetPath: dst, ExpectedSize: 15}},
		&CollisionPolicy{RootDir: root, BookID: mine.ID, Store: store},
	)
	require.NoError(t, err)

	got, err := store.GetBookFileByID(mine.ID, live.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "the losing row is tombstoned, never deleted")
	require.NotEqual(t, dst, got.FilePath, "our row must not be repointed onto a path a retired row holds")
	require.Equal(t, src, got.FilePath)
	require.True(t, got.Missing, "its bytes were just quarantined")

	still, err := database.BookFileByPathIncludingRetired(store, dst)
	require.NoError(t, err)
	require.NotNil(t, still)
	require.Equal(t, retired.ID, still.ID, "the retired row keeps the path's index entry")
}

// opaqueCollisionStore hides every capability of the store behind it (no
// Unwrap), so the retired-aware lookup cannot be answered.
type opaqueCollisionStore struct{ CollisionStore }

// A store that cannot say whether a retired row claims the occupied target is
// not "no owner": the batch fails with a collision failure and touches nothing.
func TestRenameFiles_UnanswerableOccupantLookupFailsTheCollision(t *testing.T) {
	root := t.TempDir()
	src := writeCollisionFile(t, filepath.Join(root, "incoming", "book.m4b"), "identical-bytes")
	dst := writeCollisionFile(t, filepath.Join(root, "Author", "Title.m4b"), "identical-bytes")

	inner := newFakeCollisionStore()
	inner.add(&database.BookFile{ID: "f1", BookID: "b1", FilePath: src, FileSize: 15})

	_, err := RenameFiles(
		[]FileRenameEntry{{SegmentID: "f1", SourcePath: src, TargetPath: dst, ExpectedSize: 15}},
		&CollisionPolicy{RootDir: root, BookID: "b1", Store: opaqueCollisionStore{inner}},
	)
	var ce *CollisionError
	require.True(t, errors.As(err, &ce), "got %v", err)
	require.Len(t, ce.Failures, 1)
	require.Contains(t, ce.Failures[0].Reason, "cannot determine which book_file row claims")
	require.Equal(t, 0, inner.updates, "nothing was written")
	mustCollisionExist(t, src)
	mustCollisionExist(t, dst)
}
