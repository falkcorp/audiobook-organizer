// file: internal/plugins/maintenance/fragment_itunes_ordinary_test.go
// version: 1.0.0
// guid: 6f2d8a41-3c7e-4b95-a1d0-8e4f7c2b9a63
// last-edited: 2026-10-08

package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// itunesChapterBooks seeds n chapter files under the iTunes library, each
// imported from iTunes as its own book, with every iTunes signal the import
// leaves: a book persistent id, a row persistent id and iTunes path, and a
// live itunes external id. Placeholder names (synthetic; no library rows).
func itunesChapterBooks(t *testing.T, f *fragFixture, dir, name string, n, dur int) (ids []string, paths map[string]string) {
	t.Helper()
	paths = map[string]string{}
	for i := 1; i <= n; i++ {
		stem := fmt.Sprintf("%02d %02d %s", i, i, name)
		p := f.file(t, filepath.Join(dir, stem+".mp3"), 900+i)
		id := f.book(t, stem, stem, p, nil)
		f.row(t, stem, id, p, stem+".mp3", int64(900+i), dur, 0)
		bookPID := fmt.Sprintf("B%015X", i)
		_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.ITunesPersistentID = &bookPID; return nil })
		require.NoError(t, err)
		f.updateRow(t, id, f.rowIDs[stem], func(r *database.BookFile) {
			r.ITunesPersistentID = fmt.Sprintf("R%015X", i)
			r.ITunesPath = "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/" + filepath.Base(dir) + "/" + stem + ".mp3"
		})
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{
			Source: "itunes", ExternalID: fmt.Sprintf("B%015X", i), BookID: id}))
		ids = append(ids, id)
		paths[f.rowIDs[stem]] = p
	}
	return ids, paths
}

// TestFragmentFixer_ITunesChapterBooksAssemble (owner 2026-10-08, "combine
// them too"): chapter books iTunes knows about, under the iTunes library,
// are planned and applied like any other fragments. The prod shape: one
// iTunes Media folder whose chapter files were each imported as a book.
// The apply writes database rows only: every file stays where it is, every
// row keeps its iTunes persistent id and iTunes path (the next import still
// finds each track on the assembled book), and the result is one book with
// the chapters in track order.
func TestFragmentFixer_ITunesChapterBooksAssemble(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	const dir = "books/itunes/iTunes Media/Audiobooks/Placeholder Author/Placeholder Saga"
	const n = 12
	ids, paths := itunesChapterBooks(t, f, dir, "Placeholder Title and th", n, 600)
	// The framework guard lifts the books/itunes check for this fixer only.
	require.True(t, repairs.AllowsITunesDatabaseOnly(newFragmentFixer(f.p)))

	res := f.plan(t, "op-plan")
	r := rowWithBooks(t, res, ids)
	require.NotEqual(t, fragClassManual, r.Class, "%s: %s", r.Skipped, r.SkipReason)
	require.NotEqual(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
	require.True(t, r.Applicable(), "%s %s: %s: %s", r.RowID, r.Class, r.Skipped, r.SkipReason)

	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	var live []string
	for _, id := range ids {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if !b.IsSoftDeleted() {
			live = append(live, id)
		}
	}
	require.Len(t, live, 1, "one book holds every chapter")
	rows, err := f.s.GetBookFiles(live[0])
	require.NoError(t, err)
	require.Len(t, rows, n)
	sort.Slice(rows, func(i, j int) bool { return rows[i].TrackNumber < rows[j].TrackNumber })
	for i, row := range rows {
		require.Equal(t, i+1, row.TrackNumber, "track order")
		require.Equal(t, paths[row.ID], row.FilePath, "no file row is repointed")
		require.FileExists(t, row.FilePath, "no file is moved")
		require.Equal(t, fmt.Sprintf("R%015X", i+1), row.ITunesPersistentID, "the row keeps its iTunes id")
		require.NotEmpty(t, row.ITunesPath, "the row keeps its iTunes path")
		got, err := f.s.GetBookFileByPID(row.ITunesPersistentID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, live[0], got.BookID, "the importer's lookup finds the track on the assembled book")
	}
	entries, err := os.ReadDir(f.path(dir))
	require.NoError(t, err)
	require.Len(t, entries, n, "nothing added to or removed from the iTunes folder")
}

// TestFragmentFixer_ITunesAuthorFolderSetHeldForTitle: chapter books directly
// in an iTunes author folder ("iTunes Media/Audiobooks/<Author>/NN NN
// <name>.mp3", the prod 2026-10-08 example) are no longer held for iTunes,
// but the numbered-set title rule still holds them: an author folder names
// no work, so the assembled book would keep one chapter's name.
func TestFragmentFixer_ITunesAuthorFolderSetHeldForTitle(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	const dir = "books/itunes/iTunes Media/Audiobooks/Placeholder Author"
	ids, _ := itunesChapterBooks(t, f, dir, "Placeholder Title and th", 12, 600)
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.NotEqual(t, repairs.SkipITunes, r.Skipped, r.SkipReason)
	require.NotEqual(t, fragClassManual, r.Class)
	require.Equal(t, fragSkipNumberedUnsure, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "the folder gives no title")
}

// TestFragmentFixer_ITunesDoctorWhoStaysManual: lifting the iTunes rule
// leaves the owner's Doctor Who / Big Finish / Torchwood rule in place, under
// the iTunes library too.
func TestFragmentFixer_ITunesDoctorWhoStaysManual(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	const dir = "books/itunes/iTunes Media/Audiobooks/Doctor Who - Placeholder"
	ids, _ := itunesChapterBooks(t, f, dir, "Placeholder Part", 6, 600)
	res := f.plan(t, "op-plan")
	r := rowWithBooks(t, res, ids)
	require.Equal(t, fragClassManual, r.Class)
	require.Equal(t, repairs.SkipOwnerManual, r.Skipped, r.SkipReason)
	require.False(t, r.Applicable())
	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Zero(t, out.Applied, "%+v", out.Rows)
	for _, id := range ids {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted())
	}
}
