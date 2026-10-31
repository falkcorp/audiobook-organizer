// file: internal/database/book_own_folder_slots_test.go
// version: 1.0.0
// guid: 5a1bde02-c19b-425f-be41-1c41e77c2ec2
// last-edited: 2026-10-30

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A version slot, <book folder>/.versions/<version id>/, holds the files of a
// trashed or alternate version. The folder is inside the book's own folder, so
// SplitOwnFolderFiles used to count its rows as the book's own, and the ABS
// mapper, the stream and RecomputeBookAggregates built the timeline from them.

const slotBookDir = "/srv/library/Gotham/Batman Hush"

// slotRows lists the slot rows FIRST, so a keeper chosen by row order would be
// the slot row.
func slotRows() []BookFile {
	return []BookFile{
		{ID: "alt-slot", FilePath: slotBookDir + "/.versions/v-alt/01 Hush.m4b", VersionID: "v-alt", FileHash: "h1", FileSize: 100, Duration: 60},
		{ID: "trash-slot", FilePath: slotBookDir + "/.versions/v-trash/02 Hush.m4b", VersionID: "v-trash", FileHash: "h2", FileSize: 200, Duration: 70},
		{ID: "live-1", FilePath: slotBookDir + "/01 Hush.m4b", VersionID: "v-live", FileHash: "h1", FileSize: 100, Duration: 60},
		{ID: "live-cd2", FilePath: slotBookDir + "/CD2/02 Hush.m4b", VersionID: "v-live", FileHash: "h3", FileSize: 300, Duration: 80},
	}
}

func TestSplitOwnFolderFiles_SlotRowsAreNotOwn(t *testing.T) {
	rows := slotRows()
	s := SplitOwnFolderFiles(&Book{FilePath: slotBookDir}, rows)

	assert.Equal(t, slotBookDir, s.Dir)
	assert.Equal(t, []string{"live-1", "live-cd2"}, ownFolderIDs(s.Own), "slot rows are not the book's own")
	assert.Equal(t, []string{"alt-slot", "trash-slot"}, ownFolderIDs(s.Slots))
	assert.Empty(t, s.Copies, "an alt slot row with the live row's hash must not make the live row (or itself) a copy")
	assert.Empty(t, s.Others)
	assert.False(t, s.HasOutside(), "a slot is not outside the folder either: it must not flip the zero_rows_only skip")
	assert.True(t, s.HasOwnBasis())

	// The timeline the ABS mapper, the stream and RecomputeBookAggregates use.
	want := []string{"live-1", "live-cd2"}
	assert.Equal(t, want, ownFolderIDs(s.Counted(rows)))
	assert.Equal(t, want, ownFolderIDs(OwnFolderFiles(&Book{FilePath: slotBookDir}, rows)))
}

func TestSplitOwnFolderFiles_SlotRowsWithAFilePath(t *testing.T) {
	// Book.FilePath is a file: the folder is its directory; same answer.
	rows := slotRows()
	b := &Book{FilePath: slotBookDir + "/01 Hush.m4b"}
	assert.Equal(t, []string{"live-1", "live-cd2"}, ownFolderIDs(OwnFolderFiles(b, rows)))
}

func TestSplitOwnFolderFiles_SlotRowsWithStaleOrNoBookPath(t *testing.T) {
	// Book.FilePath lags the rows (it is known to), or is empty: Dir is wrong,
	// so a slot row cannot be told by where it sits. Its own VersionID in its
	// own path still does.
	for name, b := range map[string]*Book{
		"stale folder": {FilePath: "/srv/library/Gotham/Batman Year One"},
		"no path":      {},
		"nil book":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			rows := slotRows()
			got := ownFolderIDs(OwnFolderFiles(b, rows))
			assert.NotContains(t, got, "alt-slot")
			assert.NotContains(t, got, "trash-slot")
			assert.Contains(t, got, "live-1")
			assert.Contains(t, got, "live-cd2")
		})
	}
}

func TestSplitOwnFolderFiles_SlotRowWithoutVersionID(t *testing.T) {
	// A row with no version id under the book's own .versions/ is still a slot
	// row (the folder test).
	rows := []BookFile{
		{ID: "live", FilePath: slotBookDir + "/01 Hush.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
		{ID: "slot", FilePath: slotBookDir + "/.versions/v-old/01 Hush.m4b", FileHash: "h9", FileSize: 111, Duration: 61},
	}
	s := SplitOwnFolderFiles(&Book{FilePath: slotBookDir}, rows)
	assert.Equal(t, []string{"live"}, ownFolderIDs(s.Own))
	assert.Equal(t, []string{"slot"}, ownFolderIDs(s.Slots))
	assert.Equal(t, []string{"live"}, ownFolderIDs(s.Counted(rows)))
}

func TestSplitOwnFolderFiles_VersionsNamedLikeAFolderOutsideTheBook(t *testing.T) {
	// A .versions component in or above the book's own folder is not a slot of
	// it, and a similar-looking name is not .versions.
	rows := []BookFile{
		{ID: "a", FilePath: "/srv/library/.versions/Gotham/Hush/01.m4b", FileHash: "a", FileSize: 1, Duration: 10},
		{ID: "b", FilePath: "/srv/library/.versions/Gotham/Hush/.versions-old/02.m4b", FileHash: "b", FileSize: 2, Duration: 20},
	}
	b := &Book{FilePath: "/srv/library/.versions/Gotham/Hush"}
	s := SplitOwnFolderFiles(b, rows)
	assert.Empty(t, s.Slots)
	assert.Equal(t, []string{"a", "b"}, ownFolderIDs(s.Own))
}

func TestRecomputeBookAggregates_ExcludesVersionSlotRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	d, sz := recomputeDuration(t, store, slotBookDir, slotRows())
	assert.Equal(t, 140, d, "live rows only: 60 + 80")
	assert.Equal(t, int64(400), sz)
}

func TestIsVersionSlotFile(t *testing.T) {
	require.True(t, IsVersionSlotFile(BookFile{FilePath: "/x/B/.versions/v1/a.m4b", VersionID: "v1"}, ""))
	require.True(t, IsVersionSlotFile(BookFile{FilePath: "/x/B/.versions/v1/a.m4b"}, "/x/B"))
	require.False(t, IsVersionSlotFile(BookFile{FilePath: "/x/B/.versions/v1/a.m4b"}, ""), "no version id and no folder: nothing says it is a slot")
	require.False(t, IsVersionSlotFile(BookFile{FilePath: "/x/B/.versions/v1/a.m4b", VersionID: "v2"}, "/other"), "another version's slot id does not make it this row's slot")
	require.False(t, IsVersionSlotFile(BookFile{FilePath: "/x/B/a.m4b", VersionID: "v1"}, "/x/B"))
}
