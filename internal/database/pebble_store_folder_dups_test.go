// file: internal/database/pebble_store_folder_dups_test.go
// version: 1.0.0
// guid: 7b16b9f0-cdcf-4018-bd7a-f293247a9a91
// last-edited: 2026-07-10

package database

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// folderDupFixtureIDs names the book IDs created by buildFolderDupFixture,
// keyed by mnemonic, so subtests can assert on specific books without
// re-deriving IDs from titles.
type folderDupFixtureIDs struct {
	sameA, sameB             string // (a): same normalized title, same dir -> grouped
	diffDirA, diffDirB       string // (b): same normalized title, different dirs -> not grouped
	uniqueOnly               string // (c): single book, unique title -> not grouped
	deletedKeep, deletedGone string // (d): marked-for-deletion excluded, leaving a singleton
	multiDir                 string // (e): files span two dirs -> UNKNOWN dir, skipped
}

// buildFolderDupFixture populates store with the TASK-01 acceptance fixture
// covering all five documented cases (a)-(e). Every created BookFile gets a
// globally-unique file path (via the fileSeq counter) so the book:path and
// book_file secondary indexes never silently collide across fixture books,
// even when two books intentionally share the same parent directory.
func buildFolderDupFixture(t *testing.T, store Store) folderDupFixtureIDs {
	t.Helper()

	fileSeq := 0
	nextFilePath := func(dir string) string {
		fileSeq++
		return filepath.Join(dir, fmt.Sprintf("file%d.mp3", fileSeq))
	}

	create := func(title string, dirs []string, opts ...func(*Book)) string {
		book := &Book{Title: title}
		if len(dirs) > 0 {
			book.FilePath = nextFilePath(dirs[0])
		}
		for _, o := range opts {
			o(book)
		}
		created, err := store.CreateBook(book)
		require.NoError(t, err)
		for _, dir := range dirs {
			bf := &BookFile{BookID: created.ID, FilePath: nextFilePath(dir)}
			require.NoError(t, store.CreateBookFile(bf))
		}
		return created.ID
	}

	var ids folderDupFixtureIDs

	// (a) two books, same normalized title (case/whitespace differ), files
	// in the same dir -> one group of 2.
	ids.sameA = create("Same Title", []string{"/library/AuthorA/BookX"})
	ids.sameB = create("  SAME TITLE  ", []string{"/library/AuthorA/BookX"})

	// (b) same normalized title, different dirs -> no group.
	ids.diffDirA = create("Different Dir Title", []string{"/library/AuthorB/BookY"})
	ids.diffDirB = create("different dir title", []string{"/library/AuthorB/BookZ"})

	// (c) single book, unique title -> no group.
	ids.uniqueOnly = create("Unique Title Only", []string{"/library/AuthorC/BookC"})

	// (d) marked-for-deletion book excluded: only one non-deleted book
	// remains sharing the title/dir, so no group forms.
	ids.deletedKeep = create("Deleted Group Title", []string{"/library/AuthorD/BookD"})
	ids.deletedGone = create("Deleted Group Title", []string{"/library/AuthorD/BookD"}, func(b *Book) {
		deleted := true
		b.MarkedForDeletion = &deleted
	})

	// (e) a book with files in two DIFFERENT dirs -> UNKNOWN parent dir,
	// silently skipped (never grouped, never an error). Its title is
	// unique so it wouldn't group with anything else anyway; the case of
	// interest is that its presence must not suppress the valid (a) group
	// (anti-over-suppression) — see the MultiDirSkippedOthersStillGrouped
	// subtest below.
	ids.multiDir = create("Multi Dir Skip Title", []string{
		"/library/AuthorE/BookE1",
		"/library/AuthorE/BookE2",
	})

	return ids
}

// groupIDs returns the sorted book IDs of a single group, for
// order-independent comparison.
func groupIDs(group []BookCore) []string {
	ids := make([]string, len(group))
	for i, b := range group {
		ids[i] = b.ID
	}
	sort.Strings(ids)
	return ids
}

// assertFolderDupGroups asserts that groups contains exactly one group,
// containing exactly ids.sameA/ids.sameB (case a), and that none of the
// deliberately-not-grouped fixture books (cases b/c/d/e) appear in any
// group.
func assertFolderDupGroups(t *testing.T, groups [][]BookCore, ids folderDupFixtureIDs) {
	t.Helper()
	require.Len(t, groups, 1, "expected exactly one folder-duplicate group")

	want := []string{ids.sameA, ids.sameB}
	sort.Strings(want)
	require.Equal(t, want, groupIDs(groups[0]))

	excluded := map[string]bool{
		ids.diffDirA:    true,
		ids.diffDirB:    true,
		ids.uniqueOnly:  true,
		ids.deletedKeep: true,
		ids.deletedGone: true,
		ids.multiDir:    true,
	}
	for _, g := range groups {
		for _, b := range g {
			require.False(t, excluded[b.ID], "book %s should not appear in any folder-duplicate group", b.ID)
		}
	}
}

// TestGetFolderDuplicatesCore runs the shared TASK-01 fixture through BOTH
// the memdb-delegation path (PebbleStore.UseMemDB=true, the production
// default) and the Pebble scan-fallback path (UseMemDB=false), asserting
// identical groups on both, plus a dedicated anti-over-suppression subtest
// for case (e).
func TestGetFolderDuplicatesCore(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ids := buildFolderDupFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()

	t.Run("MemDBPath", func(t *testing.T) {
		p.UseMemDB = true
		groups, err := store.GetFolderDuplicatesCore()
		require.NoError(t, err)
		assertFolderDupGroups(t, groups, ids)
	})

	t.Run("ScanFallbackPath", func(t *testing.T) {
		p.UseMemDB = false
		groups, err := store.GetFolderDuplicatesCore()
		require.NoError(t, err)
		assertFolderDupGroups(t, groups, ids)
	})

	t.Run("MultiDirSkippedOthersStillGrouped", func(t *testing.T) {
		// Case (e), anti-over-suppression: a book whose files span two
		// distinct parent dirs is silently skipped (UNKNOWN dir) rather
		// than erroring or suppressing the rest of the run — the valid
		// case-(a) group must still come back, on BOTH backends.
		for _, useMemDB := range []bool{true, false} {
			p.UseMemDB = useMemDB
			groups, err := store.GetFolderDuplicatesCore()
			require.NoError(t, err)
			require.Len(t, groups, 1)
			require.NotContains(t, groupIDs(groups[0]), ids.multiDir)
		}
	})
}
