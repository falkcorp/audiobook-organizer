// file: internal/metafetch/library_copy_retired_test.go
// version: 1.1.1
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
			got, err := svc.fileWorkTarget(book, "copy", Unattended)
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

			_, err := svc.writeBackForBook(context.Background(), "orig", nil, "copy", Unattended, nil)

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

	resolved, err := svc.resolveLibraryCopy("orig", &book, createLibraryCopy, Interactive, func() error { return nil })
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

	got, err := svc.resolveLibraryCopy("orig", &book, createLibraryCopy, Interactive, func() error { return nil })

	require.Error(t, err, "a merged-away copy must not be written, and the job must say so")
	require.Nil(t, got)
	require.Zero(t, made, "no second organized copy is made beside the merged one")
	require.Contains(t, err.Error(), "merged away")
	require.Contains(t, err.Error(), "merged-copy")
	require.NotContains(t, err.Error(), "changed after its file work locked one")

	target, err := svc.fileWorkTarget(&book, "", Unattended)
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
	_, _ = svc2.resolveLibraryCopy("orig", &book, createLibraryCopy, Interactive, func() error { return nil })
	require.Equal(t, 1, made2, "control: a protected book with no copy at all still gets one")
}

// M1 residual. An unattended job writes none of a retired book's files, and
// making a library copy writes a new set of them: a retired protected book with
// no copy gets none made by an unattended job, and the refusal is the one the
// callers count as a skip. The owner working on the book (Interactive) still
// gets a copy, and so does a live book.
func TestResolveLibraryCopy_UnattendedMakesNoCopyOfARetiredBook(t *testing.T) {
	root, itunes := protectedSetup(t)
	_ = root
	vg := "vg-year-one"
	build := func(retire func(*database.Book)) (*Service, *database.Book, *int) {
		book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg}
		retire(&book)
		svc := NewService(&database.MockStore{
			GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book}, nil },
			GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
			GetBookByIDFunc:            func(string) (*database.Book, error) { b := book; return &b, nil },
		})
		made := 0
		svc.libraryCopyMaker = func(*database.Book) *database.Book { made++; return nil }
		return svc, &book, &made
	}
	merge := func(b *database.Book) { into := "survivor"; b.MergedIntoBookID = &into }
	trash := func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes }
	live := func(*database.Book) {}
	noCP := func() error { return nil }

	for name, retire := range map[string]func(*database.Book){"merged": merge, "trashed": trash} {
		t.Run(name, func(t *testing.T) {
			svc, book, made := build(retire)
			got, err := svc.resolveLibraryCopy("orig", book, createLibraryCopy, Unattended, noCP)
			require.ErrorIs(t, err, ErrBookRetired, "counted as a retired skip by every caller")
			require.Nil(t, got)
			require.Zero(t, *made, "an unattended job makes no copy of a retired book")

			// Through lockLibraryCopy, the door every file-work entry point uses.
			_, release, err := svc.lockLibraryCopy("orig", createLibraryCopy, Unattended, noCP)
			release()
			require.ErrorIs(t, err, ErrBookRetired)
			require.Zero(t, *made)

			svc, book, made = build(retire)
			_, _ = svc.resolveLibraryCopy("orig", book, createLibraryCopy, Interactive, noCP)
			require.Equal(t, 1, *made, "the owner working on the book still gets a copy")
		})
	}

	svc, book, made := build(live)
	_, _ = svc.resolveLibraryCopy("orig", book, createLibraryCopy, Unattended, noCP)
	require.Equal(t, 1, *made, "control: an unattended job still copies a live book")

	svc, book, _ = build(live)
	_, err := svc.resolveLibraryCopy("orig", book, createLibraryCopy, FetchIntent(0), noCP)
	require.Error(t, err, "an unstated intent is an error")
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

			_, err := svc.writeBackForBook(context.Background(), "self", nil, "self", Unattended, nil)

			require.ErrorIs(t, err, ErrBookRetired)
			require.Zero(t, fileReads.Load(), "no file row is read, so no tag is written, for a retired book")
		})
		// The attended twin: the owner's own write-back of this one book still
		// goes on to its files (the refusal is the unattended job's).
		t.Run(shape.name+"_attended", func(t *testing.T) {
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

			_, err := svc.writeBackForBook(context.Background(), "self", nil, "self", Interactive, nil)

			require.NotErrorIs(t, err, ErrBookRetired)
			require.Positive(t, fileReads.Load(), "an attended write-back of a retired book reaches its files (err: %v)", err)
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
		_, err := svc.writeBackForBook(context.Background(), "self", nil, "self", Unattended, nil)
		require.Positive(t, fileReads.Load(), "a live book goes on to its files (err: %v)", err)
	})
}

// B6, the resolver's third answer. organize's resolver (ExistingLibraryCopyOfFile)
// reports a protected book whose only same-file library copy is merged away as
// "no live copy, here is the merged row", so organize refuses and names it
// instead of laying a second copy beside the merged one's file.
func TestExistingLibraryCopyOfFile_OnlyAMergedCopyIsReportedNotSkipped(t *testing.T) {
	root, itunes := protectedSetup(t)
	vg := "vg-year-one"
	into := "survivor-book-id"
	hash := "aaaa1111"
	retaggedHash := "bbbb2222" // a re-scan of the tagged copy overwrote FileHash

	cases := []struct {
		name  string
		merge func(*database.Book)
	}{
		{"untagged_same_hash", func(b *database.Book) { b.FileHash = &hash }},
		{"tagged_and_rehashed", func(b *database.Book) {
			b.FileHash = &retaggedHash
			b.OriginalFileHash = &hash
			b.OrganizedFileHash = &hash
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg, FileHash: &hash}
			merged := database.Book{ID: "merged-copy", Title: "Year One", FilePath: filepath.Join(root, "Year One"), VersionGroupID: &vg, MergedIntoBookID: &into}
			tc.merge(&merged)
			svc := NewService(&database.MockStore{
				GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book, merged}, nil },
				GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
			})

			cp, ok, retired := svc.ExistingLibraryCopyOfFile(&book)

			require.False(t, ok, "there is no live copy")
			require.Nil(t, cp)
			require.NotNil(t, retired, "the merged copy is reported, not read as 'no copy yet'")
			require.Equal(t, "merged-copy", retired.ID)
		})
	}

	t.Run("a_live_copy_ahead_of_a_merged_primary_is_the_subject", func(t *testing.T) {
		primary := true
		book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg, FileHash: &hash}
		merged := database.Book{ID: "merged-copy", Title: "Year One", FilePath: filepath.Join(root, "Year One"), VersionGroupID: &vg, IsPrimaryVersion: &primary, MergedIntoBookID: &into, FileHash: &hash}
		live := database.Book{ID: "live-copy", Title: "Year One", FilePath: filepath.Join(root, "Year One (2)"), VersionGroupID: &vg, FileHash: &hash}
		svc := NewService(&database.MockStore{
			GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book, merged, live}, nil },
			GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
		})

		cp, ok, retired := svc.ExistingLibraryCopyOfFile(&book)

		require.True(t, ok)
		require.NotNil(t, cp)
		require.Equal(t, "live-copy", cp.ID)
		require.Nil(t, retired, "a live copy exists, so there is nothing to refuse")
	})

	t.Run("a_merged_other_edition_is_not_this_files_copy", func(t *testing.T) {
		other := "cccc3333"
		book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg, FileHash: &hash}
		merged := database.Book{ID: "merged-mp3", Title: "Year One", FilePath: filepath.Join(root, "Year One"), VersionGroupID: &vg, MergedIntoBookID: &into, FileHash: &other}
		svc := NewService(&database.MockStore{
			GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book, merged}, nil },
			GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
		})

		cp, ok, retired := svc.ExistingLibraryCopyOfFile(&book)

		require.False(t, ok)
		require.Nil(t, cp)
		require.Nil(t, retired, "another edition's merged row does not block organizing this file")
	})

	t.Run("no_sibling_at_all", func(t *testing.T) {
		book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(itunes, "a.m4b"), VersionGroupID: &vg, FileHash: &hash}
		svc := NewService(&database.MockStore{
			GetBooksByVersionGroupFunc: func(string) ([]database.Book, error) { return []database.Book{book}, nil },
			GetBookFilesFunc:           func(string) ([]database.BookFile, error) { return nil, nil },
		})

		cp, ok, retired := svc.ExistingLibraryCopyOfFile(&book)

		require.False(t, ok)
		require.Nil(t, cp)
		require.Nil(t, retired)
	})

	t.Run("an_unprotected_book_is_its_own_subject", func(t *testing.T) {
		book := database.Book{ID: "orig", Title: "Year One", FilePath: filepath.Join(root, "Year One", "a.m4b"), VersionGroupID: &vg, FileHash: &hash}
		svc := NewService(&database.MockStore{})

		cp, ok, retired := svc.ExistingLibraryCopyOfFile(&book)

		require.True(t, ok)
		require.Equal(t, "orig", cp.ID)
		require.Nil(t, retired)
	})
}
