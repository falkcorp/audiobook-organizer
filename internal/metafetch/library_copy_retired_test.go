// file: internal/metafetch/library_copy_retired_test.go
// version: 1.0.0
// guid: f6a4347b-964b-4516-8665-b43d8e70f9ac
// last-edited: 2026-11-17

package metafetch

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// A file job locks the book's library copy, then re-reads it BY ID
// (fileWorkTarget) and syncs the original's columns onto it before the tag
// write. The lookup that picked the copy leaves trashed rows out, but nothing
// after it looked at the row again: a copy trashed or merged away during the
// job was still written (its columns, its author and narrator joins, then its
// files).

type retiredCopyShape struct {
	name   string
	retire func(*database.Book)
}

var retiredCopyShapes = []retiredCopyShape{
	{"soft_deleted", func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes }},
	{"merged_into", func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into }},
}

func withRootDir(t *testing.T, root string) {
	t.Helper()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
}

func versionPair() (book, cp *database.Book) {
	vg := "vg-batman"
	book = &database.Book{ID: "orig", Title: "Year One", FilePath: "/itunes/Year One/b.m4b", VersionGroupID: &vg}
	cp = &database.Book{ID: "copy", Title: "Year One", FilePath: "/lib/Year One/b.m4b", VersionGroupID: &vg}
	return book, cp
}

func TestLibraryCopyGone_RefusesARetiredCopy(t *testing.T) {
	withRootDir(t, "/lib")
	svc := NewService(&database.MockStore{})
	book, live := versionPair()
	require.Empty(t, svc.libraryCopyGone(book, live), "control: a live copy under root_dir in the group is still one")
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			_, cp := versionPair()
			shape.retire(cp)
			require.Contains(t, svc.libraryCopyGone(book, cp), "retired")
		})
	}
}

func TestFileWorkTarget_RefusesACopyRetiredAfterTheLockStep(t *testing.T) {
	withRootDir(t, "/lib")
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			book, cp := versionPair()
			shape.retire(cp)
			svc := NewService(&database.MockStore{
				GetBookByIDFunc: func(id string) (*database.Book, error) { return cp, nil },
			})
			got, err := svc.fileWorkTarget(book, "copy")
			require.ErrorIs(t, err, errFileTargetChanged)
			require.Nil(t, got)
		})
	}
}

// retiredUnderTheLock serves the copy live to the first read (the file job's
// re-read) and retired to every later one (the sync's locked read): a trash or
// merge that lands between them.
func retiredUnderTheLock(shape retiredCopyShape, reads *atomic.Int64) func(string) (*database.Book, error) {
	return func(id string) (*database.Book, error) {
		if id != "copy" {
			return nil, nil
		}
		_, cp := versionPair()
		if reads.Add(1) > 1 {
			shape.retire(cp)
		}
		return cp, nil
	}
}

func TestSyncMetadataToLibraryCopy_RefusesACopyRetiredUnderTheLock(t *testing.T) {
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			store, written, authorWrites, narratorWrites := libCopySyncFixture(nil)
			var reads atomic.Int64
			reads.Store(1) // the sync's read is the one after the job's re-read
			store.GetBookByIDFunc = retiredUnderTheLock(shape, &reads)
			svc := NewService(store)
			original, libCopy := libCopyPair()

			err := svc.syncMetadataToLibraryCopy(original, libCopy)

			require.ErrorIs(t, err, errFileTargetChanged)
			require.Nil(t, *written, "a retired copy's columns must not be written")
			require.Zero(t, *authorWrites, "its author join must not be rewritten")
			require.Zero(t, *narratorWrites, "its narrator join must not be rewritten")
		})
	}
}

// The same race through the write-back that runs the sync: the refusal stops
// the job before any tag is written to the copy's files.
func TestWriteBackForBook_StopsWhenTheCopyIsRetiredUnderTheSyncLock(t *testing.T) {
	withRootDir(t, "/lib")
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			store, written, authorWrites, narratorWrites := libCopySyncFixture(nil)
			var reads atomic.Int64
			copyReads := retiredUnderTheLock(shape, &reads)
			orig, _ := versionPair()
			store.GetBookByIDFunc = func(id string) (*database.Book, error) {
				if id == "orig" {
					return orig, nil
				}
				return copyReads(id)
			}
			svc := NewService(store)

			_, err := svc.writeBackForBook(context.Background(), "orig", nil, "copy", nil)

			require.ErrorIs(t, err, errFileTargetChanged)
			require.Nil(t, *written)
			require.Zero(t, *authorWrites)
			require.Zero(t, *narratorWrites)
		})
	}
}
