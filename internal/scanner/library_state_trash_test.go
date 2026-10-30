// file: internal/scanner/library_state_trash_test.go
// version: 1.0.0
// guid: 267da50d-ed73-4d0d-856a-d8144270fd81
// last-edited: 2026-10-30

package scanner

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// suspiciousFloor is a MinBookSizeBytes every fixture file here sits under.
const suspiciousFloor = 1024

// rescanTinyFile rescans the one file of an already-stored book whose size is
// under the MinBookSizeBytes floor, so the scan derives LibraryState
// "suspicious" for it, and returns the stored row afterwards.
func rescanTinyFile(t *testing.T, f scanLockFixture, row *database.Book) *database.Book {
	t.Helper()
	config.AppConfig.MinBookSizeBytes = suspiciousFloor
	scanOne(t, Book{FilePath: row.FilePath, Title: row.Title, Format: ".m4b"})
	got, err := f.store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	return got
}

// A book in the trash by its "deleted" label alone (no deletion flag: the batch
// library_state:"deleted" path makes these) must stay there when a rescan finds
// its file under the size floor.
func TestRescan_LabelOnlyTrashedBookIsNotMarkedSuspicious(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "gotham/the-long-halloween.m4b", "tiny")
	row, err := f.store.CreateBook(&database.Book{
		Title: "The Long Halloween", FilePath: p, Format: "m4b", LibraryState: new("deleted"),
	})
	require.NoError(t, err)
	require.True(t, database.IsInTrash(row), "fixture: the label alone must put the row in the trash")

	got := rescanTinyFile(t, f, row)

	require.NotNil(t, got.LibraryState)
	require.Equal(t, "deleted", *got.LibraryState, "the scan relabelled a trashed book")
	require.True(t, database.IsInTrash(got), "the scan took a label-only trashed book out of the trash")
}

// A flag-trashed book likewise keeps its state label: a restore reads the
// state back from it.
func TestRescan_FlagTrashedBookKeepsItsLibraryState(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "gotham/year-one.m4b", "tiny")
	row, err := f.store.CreateBook(&database.Book{
		Title: "Batman Year One", FilePath: p, Format: "m4b",
		LibraryState: new("organized"), MarkedForDeletion: new(true),
	})
	require.NoError(t, err)
	require.True(t, database.IsInTrash(row))

	got := rescanTinyFile(t, f, row)

	require.NotNil(t, got.LibraryState)
	require.Equal(t, "organized", *got.LibraryState, "the scan relabelled a flag-trashed book")
	require.True(t, database.IsInTrash(got))
}

// The existing behaviour, unchanged: a live book whose file is under the floor
// becomes suspicious.
func TestRescan_LiveUnderFloorBookBecomesSuspicious(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "gotham/hush.m4b", "tiny")
	row, err := f.store.CreateBook(&database.Book{
		Title: "Batman Hush", FilePath: p, Format: "m4b", LibraryState: new("imported"),
	})
	require.NoError(t, err)

	got := rescanTinyFile(t, f, row)

	require.NotNil(t, got.LibraryState)
	require.Equal(t, "suspicious", *got.LibraryState)
	require.False(t, database.IsInTrash(got))
}

// The scan still refreshes every other column of a trashed row: only the state
// is held.
func TestRescan_TrashedBookStillGetsItsFileColumnsRefreshed(t *testing.T) {
	f := newScanLockFixture(t, "")
	p := f.file(t, "gotham/knightfall.m4b", "tiny")
	row, err := f.store.CreateBook(&database.Book{
		Title: "Knightfall", FilePath: p, Format: "m4b", LibraryState: new("deleted"),
	})
	require.NoError(t, err)
	require.Nil(t, row.FileSize)

	got := rescanTinyFile(t, f, row)

	require.NotNil(t, got.FileSize, "the scan stopped refreshing a trashed row's file columns")
	require.Equal(t, "deleted", *got.LibraryState)
}

// applyScannerFields in isolation: the scan-derived state never lands on a
// trashed row, whatever the trash shape, and always lands on a live one.
func TestApplyScannerFields_LibraryStateHonoursTheTrash(t *testing.T) {
	suspicious := func() *database.Book { return &database.Book{LibraryState: new("suspicious")} }
	for _, tc := range []struct {
		name      string
		dst       *database.Book
		wantState string
		wantHeld  bool
	}{
		{"label-only trash", &database.Book{LibraryState: new("deleted")}, "deleted", true},
		{"label in another case", &database.Book{LibraryState: new(" Deleted ")}, " Deleted ", true},
		{"flag-only trash", &database.Book{LibraryState: new("organized"), MarkedForDeletion: new(true)}, "organized", true},
		{"flag with no state", &database.Book{MarkedForDeletion: new(true)}, "", true},
		{"live organized", &database.Book{LibraryState: new("organized")}, "suspicious", false},
		{"live with no state", &database.Book{}, "suspicious", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanned := suspicious()
			require.Equal(t, tc.wantHeld, libraryStateHeldForTrash(tc.dst, scanned))
			applyScannerFields(tc.dst, scanned, nil)
			if tc.wantState == "" {
				require.Nil(t, tc.dst.LibraryState)
				return
			}
			require.NotNil(t, tc.dst.LibraryState)
			require.Equal(t, tc.wantState, *tc.dst.LibraryState)
		})
	}

	t.Run("nothing derived, nothing withheld", func(t *testing.T) {
		dst := &database.Book{LibraryState: new("deleted")}
		require.False(t, libraryStateHeldForTrash(dst, &database.Book{}))
		def := libraryStateImported
		require.False(t, libraryStateHeldForTrash(dst, &database.Book{LibraryState: &def}),
			"the bare creation default is never an overlay, so nothing was held back")
	})
	t.Run("same value is not withheld", func(t *testing.T) {
		dst := &database.Book{LibraryState: new("suspicious"), MarkedForDeletion: new(true)}
		require.False(t, libraryStateHeldForTrash(dst, suspicious()))
	})
}
