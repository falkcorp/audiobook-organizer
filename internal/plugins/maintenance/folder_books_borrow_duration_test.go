// file: internal/plugins/maintenance/folder_books_borrow_duration_test.go
// version: 1.0.0
// guid: 7d3f9a2e-4b18-4c65-8e07-1a5b6c9d2f48
// last-edited: 2026-11-04

package maintenance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// fbBorrowSeed builds the Alfred Pennyworth shelf: a folder-book whose six
// rows (one per Batman storyline folder) carry duration 0 and sizes 1000+i,
// and returns its id and paths. Six zero-second works read as chapter-sized
// books, so without a borrowed duration the row is held fragmentary.
func (f *fragFixture) fbBorrowSeed(t *testing.T) (lead string, paths []string) {
	t.Helper()
	_, err := f.s.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	for _, n := range []string{"Hush", "The Long Halloween", "Year One", "Knightfall", "Court of Owls", "Zero Year"} {
		paths = append(paths, f.file(t, "Alfred Pennyworth/"+n+"/01.mp3", 10))
	}
	lead = f.fbBook(t, "flat", "Alfred Pennyworth", f.path("Alfred Pennyworth"), 0, paths...)
	for i := range paths {
		f.fbSetRow(t, "flat", i, func(bf *database.BookFile) { bf.FileSize = int64(1000 + i) })
	}
	return lead, paths
}

// fbDonorRows gives book id one row per path, with size 1000+i+sizeSkew and
// real durations (one hour each).
func (f *fragFixture) fbDonorRows(t *testing.T, role, id string, paths []string, sizeSkew int64) {
	t.Helper()
	for i, p := range paths {
		f.row(t, fmt.Sprintf("%s-%d", role, i), id, p, "", int64(1000+i)+sizeSkew, 3600, i+1)
	}
}

// fbLiveDonor is a larger live folder-book holding the same paths plus two
// more: it keeps the shared paths, so the lead's paths stay orphans of it.
func (f *fragFixture) fbLiveDonor(t *testing.T, paths []string, sizeSkew int64) {
	t.Helper()
	b, err := f.s.CreateBook(&database.Book{Title: "Alfred Pennyworth", FilePath: f.path("Alfred Pennyworth")})
	require.NoError(t, err)
	f.ids["donor"] = b.ID
	f.fbDonorRows(t, "donor", b.ID, paths, sizeSkew)
	for i, n := range []string{"Under the Red Hood", "Batman: Hush Returns"} {
		extra := f.file(t, "Alfred Pennyworth/"+n+"/01.mp3", 10)
		f.row(t, fmt.Sprintf("donor-x%d", i), b.ID, extra, "", int64(2000+i), 3600, 7+i)
	}
}

// fbTrashedDonor is a soft-deleted (trashed) book holding the same paths.
func (f *fragFixture) fbTrashedDonor(t *testing.T, paths []string, sizeSkew int64) {
	t.Helper()
	b, err := f.s.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: f.path("Alfred Pennyworth/Hush")})
	require.NoError(t, err)
	f.ids["donor"] = b.ID
	f.fbDonorRows(t, "donor", b.ID, paths, sizeSkew)
	_, err = f.s.ModifyBook(b.ID, func(bk *database.Book) error {
		bk.MarkedForDeletion = new(true)
		return nil
	})
	require.NoError(t, err)
}

// fbHeldFragmentary reports whether row carries the fragmentary hold, whether
// as its Skipped verdict or as any entry of its Holds list.
func fbHeldFragmentary(row *repairs.Row) bool {
	if row.Skipped == fbSkipFragmentary {
		return true
	}
	for _, h := range row.Holds {
		if h.Kind == fbSkipFragmentary {
			return true
		}
	}
	return false
}

func TestFolderBooksFixer_BorrowsKnownDuration(t *testing.T) {
	cases := []struct {
		name     string
		donor    func(f *fragFixture, t *testing.T, paths []string)
		fragment bool
	}{
		{"zero durations and no donor stay fragmentary", func(*fragFixture, *testing.T, []string) {}, true},
		{"a live book's same-size rows lend", func(f *fragFixture, t *testing.T, p []string) { f.fbLiveDonor(t, p, 0) }, false},
		{"a trashed book's same-size rows lend", func(f *fragFixture, t *testing.T, p []string) { f.fbTrashedDonor(t, p, 0) }, false},
		{"a live book's differently-sized rows do not", func(f *fragFixture, t *testing.T, p []string) { f.fbLiveDonor(t, p, 7) }, true},
		{"a trashed book's differently-sized rows do not", func(f *fragFixture, t *testing.T, p []string) { f.fbTrashedDonor(t, p, 7) }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			lead, paths := f.fbBorrowSeed(t)
			c.donor(f, t, paths)

			res := f.fbPlan(t, fbFixerID, "op-plan")
			row := fbFindRow(res, lead)
			require.NotNil(t, row, "the lead folder-book is planned")
			require.Equal(t, c.fragment, fbHeldFragmentary(row), "skipped=%q holds=%+v", row.Skipped, row.Holds)
		})
	}
}

// A donor row marked missing never lends.
func TestFolderBooksFixer_MissingDonorRowDoesNotLend(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, paths := f.fbBorrowSeed(t)
	f.fbTrashedDonor(t, paths, 0)
	for i := range paths {
		f.updateRow(t, f.ids["donor"], f.rowIDs[fmt.Sprintf("donor-%d", i)], func(bf *database.BookFile) { bf.Missing = true })
	}
	res := f.fbPlan(t, fbFixerID, "op-plan")
	row := fbFindRow(res, lead)
	require.NotNil(t, row)
	require.True(t, fbHeldFragmentary(row), "skipped=%q holds=%+v", row.Skipped, row.Holds)
}
