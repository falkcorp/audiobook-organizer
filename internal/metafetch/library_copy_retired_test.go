// file: internal/metafetch/library_copy_retired_test.go
// version: 1.1.0
// guid: f6a4347b-964b-4516-8665-b43d8e70f9ac
// last-edited: 2026-11-18

package metafetch

import (
	"context"
	"path/filepath"
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

// B4. The sync decides on the copy's row under its write stripe, then used to
// rewrite the author and narrator joins after the stripe was released: a copy
// trashed or merged between the column write and the join writes still had its
// credits overwritten from the original. The joins are now written in a second
// locked pass that rechecks the row.
func TestSyncMetadataToLibraryCopy_RefusesACopyRetiredBetweenTheColumnWriteAndTheJoins(t *testing.T) {
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			store, written, authorWrites, narratorWrites := libCopySyncFixture(nil)
			var reads atomic.Int64 // the column pass reads first (live), the join pass second (retired)
			store.GetBookByIDFunc = retiredUnderTheLock(shape, &reads)
			svc := NewService(store)
			original, libCopy := libCopyPair()

			err := svc.syncMetadataToLibraryCopy(original, libCopy)

			require.ErrorIs(t, err, errFileTargetChanged, "the file job must stop before it tags the copy's files")
			require.NotNil(t, *written, "the columns were written while the copy was live")
			require.Zero(t, *authorWrites, "a copy retired before the join pass must not have its author join rewritten")
			require.Zero(t, *narratorWrites, "nor its narrator join")
		})
	}
	t.Run("live_control", func(t *testing.T) {
		store, written, authorWrites, narratorWrites := libCopySyncFixture(nil)
		svc := NewService(store)
		original, libCopy := libCopyPair()

		require.NoError(t, svc.syncMetadataToLibraryCopy(original, libCopy))
		require.NotNil(t, *written)
		require.Equal(t, 1, *authorWrites, "a live copy's author join is replaced once")
		require.Equal(t, 1, *narratorWrites, "a live copy's narrator join is replaced once")
	})
}

// B5. A protected book's library copy is found by a lookup over its version
// group. A merged row that still carries the primary flag sorts ahead of a live
// copy, and the lookup used to hand that merged row back: the file job then
// refused it as retired and failed although a live copy existed.
func TestLibraryCopyLookup_FindsTheLiveCopyBehindAMergedPrimary(t *testing.T) {
	root, itunes := protectedSetup(t)
	vg := "vg-year-one"
	primary := true
	into := "survivor-book-id"
	book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg}
	merged := database.Book{ID: "merged-copy", Title: "Year One", FilePath: filepath.Join(root, "Year One"), VersionGroupID: &vg, IsPrimaryVersion: &primary, MergedIntoBookID: &into}
	live := database.Book{ID: "live-copy", Title: "Year One", FilePath: filepath.Join(root, "Year One (2)"), VersionGroupID: &vg}
	svc := NewService(&database.MockStore{
		GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book, merged, live}, nil },
		GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
		GetBookByIDFunc:            func(string) (*database.Book, error) { b := book; return &b, nil },
	})

	got, ok := svc.existingLibraryCopy(&book)
	require.True(t, ok)
	require.NotNil(t, got)
	require.Equal(t, "live-copy", got.ID, "the merged row is not a library copy; the live one behind it is")

	resolved, err := svc.resolveLibraryCopy("orig", &book, createLibraryCopy, func() error { return nil })
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, "live-copy", resolved.ID)
}

// B5, the other half: when the only library copy is merged away there is no
// copy to write, and none is created beside it. The file work is skipped with a
// reason that is true (the copy was merged away), not "changed after its file
// work locked one".
func TestResolveLibraryCopy_OnlyAMergedCopyMeansNoCopyAndNoneIsCreated(t *testing.T) {
	root, itunes := protectedSetup(t)
	vg := "vg-year-one"
	into := "survivor-book-id"
	book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg}
	merged := database.Book{ID: "merged-copy", Title: "Year One", FilePath: filepath.Join(root, "Year One"), VersionGroupID: &vg, MergedIntoBookID: &into}
	svc := NewService(&database.MockStore{
		GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book, merged}, nil },
		GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
		GetBookByIDFunc:            func(string) (*database.Book, error) { b := book; return &b, nil },
	})
	made := 0
	svc.libraryCopyMaker = func(*database.Book) *database.Book { made++; return &database.Book{ID: "fresh-copy"} }

	got, err := svc.resolveLibraryCopy("orig", &book, createLibraryCopy, func() error { return nil })

	require.Error(t, err, "a merged-away copy must not be written, and the job must say so")
	require.Nil(t, got)
	require.Zero(t, made, "no second organized copy is made beside the merged one")
	require.Contains(t, err.Error(), "merged away")
	require.Contains(t, err.Error(), "merged-copy")
	require.NotContains(t, err.Error(), "changed after its file work locked one")

	target, err := svc.fileWorkTarget(&book, "")
	require.NoError(t, err)
	require.Nil(t, target, "a job that locked no copy writes nothing")
	require.False(t, svc.autoFetchHasLibraryCopy(&book), "auto-fetch has no copy to do file work on")

	// The same group with no merged row still gets a copy made, as before.
	svc2 := NewService(&database.MockStore{
		GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book}, nil },
		GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
		GetBookByIDFunc:            func(string) (*database.Book, error) { b := book; return &b, nil },
	})
	made2 := 0
	svc2.libraryCopyMaker = func(*database.Book) *database.Book { made2++; return nil }
	_, _ = svc2.resolveLibraryCopy("orig", &book, createLibraryCopy, func() error { return nil })
	require.Equal(t, 1, made2, "control: a protected book with no copy at all still gets one")
}

// Item 3. A retired book that is its own file-work target (it lives under
// root_dir, or is not protected) used to have its tags written: the copy case
// was refused, the self case had no retired check at all.
func TestWriteBackForBook_RefusesARetiredBookThatIsItsOwnTarget(t *testing.T) {
	withRootDir(t, "/lib")
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			book := &database.Book{ID: "self", Title: "Year One", FilePath: "/lib/Year One"}
			shape.retire(book)
			var fileReads atomic.Int64
			svc := NewService(&database.MockStore{
				GetBookByIDFunc: func(string) (*database.Book, error) { b := *book; return &b, nil },
				GetBookFilesFunc: func(string) ([]database.BookFile, error) {
					fileReads.Add(1)
					return nil, nil
				},
			})

			_, err := svc.writeBackForBook(context.Background(), "self", nil, "self", nil)

			require.ErrorIs(t, err, ErrBookRetired)
			require.Zero(t, fileReads.Load(), "no file row is read, so no tag is written, for a retired book")
		})
	}
	t.Run("live_control", func(t *testing.T) {
		book := &database.Book{ID: "self", Title: "Year One", FilePath: "/lib/Year One"}
		var fileReads atomic.Int64
		svc := NewService(&database.MockStore{
			GetBookByIDFunc: func(string) (*database.Book, error) { b := *book; return &b, nil },
			GetBookFilesFunc: func(string) ([]database.BookFile, error) {
				fileReads.Add(1)
				return nil, nil
			},
		})
		_, err := svc.writeBackForBook(context.Background(), "self", nil, "self", nil)
		require.Positive(t, fileReads.Load(), "a live book goes on to its files (err: %v)", err)
	})
}
