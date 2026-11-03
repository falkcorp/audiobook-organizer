// file: internal/plugins/maintenance/folder_books_disk_test.go
// version: 1.0.0
// guid: 5e1c8b3a-72d4-4f09-9a6e-c3b81f0d2e47
// last-edited: 2026-11-03

package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seedPennyworth builds the author folder "Alfred Pennyworth" under fbITunes:
// three proper books (Year One, The Long Halloween, Hush: three files each),
// two works no proper book holds (Dark Victory, Knightfall: two files each),
// and one folder-book ("fb") holding all thirteen files. It returns each
// work's file paths by title.
func (f *fragFixture) seedPennyworth(t *testing.T) map[string][]string {
	t.Helper()
	rel := func(work string, i int) string {
		return filepath.Join(fbITunes, "Alfred Pennyworth", work, fmt.Sprintf("0%d.mp3", i))
	}
	works := map[string][]string{}
	var all []string
	for _, w := range []struct {
		role, title string
		n           int
		proper      bool
	}{
		{"year-one", "Year One", 3, true},
		{"halloween", "The Long Halloween", 3, true},
		{"hush", "Hush", 3, true},
		{"victory", "Dark Victory", 2, false},
		{"knightfall", "Knightfall", 2, false},
	} {
		var paths []string
		for i := 1; i <= w.n; i++ {
			paths = append(paths, f.file(t, rel(w.title, i), 10))
		}
		works[w.title] = paths
		all = append(all, paths...)
		if w.proper {
			f.fbBook(t, w.role, w.title, f.path(filepath.Join(fbITunes, "Alfred Pennyworth", w.title)), 3600, paths...)
		}
	}
	author, err := f.s.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	id := f.fbBook(t, "fb", "Alfred Pennyworth", f.path(filepath.Join(fbITunes, "Alfred Pennyworth")), 1200, all...)
	_, err = f.s.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &author.ID; return nil })
	require.NoError(t, err)
	return works
}

// fbPennyRowIdx is the index of a path among the folder-book's rows
// ("fb-<index>", the order seedPennyworth passed them).
func fbPennyRowIdx(works map[string][]string, title string, n int) int {
	idx := 0
	for _, w := range []string{"Year One", "The Long Halloween", "Hush", "Dark Victory", "Knightfall"} {
		if w == title {
			return idx + n
		}
		idx += len(works[w])
	}
	return -1
}

func (f *fragFixture) livePathTitled(t *testing.T, path string) *database.Book {
	t.Helper()
	b, err := f.s.GetBookByFilePath(path)
	require.NoError(t, err)
	if b != nil && b.IsSoftDeleted() {
		return nil
	}
	return b
}

// A file that is gone from disk is missing whatever its row says: the stale
// "present" rows of a deleted work build no book, and the retired folder-book
// keeps them.
func TestFolderBooksFixer_DiskGoneFileBuildsNothingDespitePresentFlag(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	works := f.seedPennyworth(t)
	for _, p := range works["Knightfall"] {
		require.NoError(t, os.Remove(p))
	}
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	require.Equal(t, "1", row.Proposed["new_books"])
	require.Equal(t, "2", row.Proposed["missing_orphans"])
	rowsBefore := fbRowCount(t, f.s)
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"))
	require.NotNil(t, f.livePathTitled(t, f.path(filepath.Join(fbITunes, "Alfred Pennyworth", "Dark Victory"))))
	require.Nil(t, f.livePathTitled(t, f.path(filepath.Join(fbITunes, "Alfred Pennyworth", "Knightfall"))))
	require.Zero(t, f.liveTitled(t, "Knightfall"))
	// Only Dark Victory's two rows were copied; the folder-book kept every one of its own.
	require.Equal(t, rowsBefore+2, fbRowCount(t, f.s))
	kept, err := f.s.GetBookFiles(f.ids["fb"])
	require.NoError(t, err)
	require.Len(t, kept, 13)
}

// A file flagged Missing that is on disk is not missing: it becomes an orphan
// and its new row is written present, while the retired row keeps its flag.
func TestFolderBooksFixer_DiskPresentFileWithMissingFlagBecomesPresentBook(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	works := f.seedPennyworth(t)
	for n := range works["Knightfall"] {
		f.fbSetRow(t, "fb", fbPennyRowIdx(works, "Knightfall", n), func(r *database.BookFile) { r.Missing = true })
	}
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	require.Equal(t, "2", row.Proposed["new_books"])
	require.Equal(t, "0", row.Proposed["missing_orphans"])
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	created := f.livePathTitled(t, f.path(filepath.Join(fbITunes, "Alfred Pennyworth", "Knightfall")))
	require.NotNil(t, created)
	newRows, err := f.s.GetBookFiles(created.ID)
	require.NoError(t, err)
	require.Len(t, newRows, 2)
	for _, r := range newRows {
		require.False(t, r.Missing, "row for %s must be written present", r.FilePath)
	}
	// The retired folder-book's own rows are untouched.
	old, err := f.s.GetBookFiles(f.ids["fb"])
	require.NoError(t, err)
	flagged := 0
	for _, r := range old {
		if r.Missing {
			flagged++
		}
	}
	require.Equal(t, 2, flagged)
}

// A stat error other than "does not exist" makes the row unreadable: it is
// listed, never applied, and nothing is guessed about the file.
func TestFolderBooksFixer_DiskStatErrorSkipsRowUnreadable(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	works := f.seedPennyworth(t)
	denied := works["Knightfall"][1]
	fx := newFolderBooksFixer(f.p)
	fx.stat = func(p string) (os.FileInfo, error) {
		if p == denied {
			return nil, &fs.PathError{Op: "stat", Path: p, Err: syscall.EACCES}
		}
		return os.Stat(p)
	}
	rows, err := fx.Plan(context.Background(), nil, &repairsOpReporter{id: "op-plan"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, fbSkipUnreadable, rows[0].Skipped, rows[0].SkipReason)
	require.Contains(t, rows[0].SkipReason, denied)
	require.False(t, rows[0].Applicable())
}

// ENOENT is trusted only under a shelf root that still stats as a populated
// directory: an unmounted volume must not read as a shelf of missing files.
func TestFolderBooksFixer_DiskShelfRootSanityGuardsEnoent(t *testing.T) {
	t.Run("shelf root gone", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		f.seedPennyworth(t)
		require.NoError(t, os.RemoveAll(f.path(fbITunes)))
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipUnreadable, row.Skipped, row.SkipReason)
	})
	t.Run("shelf root empty", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		f.seedPennyworth(t)
		require.NoError(t, os.RemoveAll(f.path(fbITunes)))
		require.NoError(t, os.MkdirAll(f.path(fbITunes), 0o755))
		row := f.fbSingleRow(t, "op-plan")
		require.Equal(t, fbSkipUnreadable, row.Skipped, row.SkipReason)
	})
	t.Run("injected check refuses", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		works := f.seedPennyworth(t)
		for _, p := range works["Knightfall"] {
			require.NoError(t, os.Remove(p))
		}
		fx := newFolderBooksFixer(f.p)
		fx.shelfPopulated = func(string) error { return fmt.Errorf("not mounted") }
		rows, err := fx.Plan(context.Background(), nil, &repairsOpReporter{id: "op-plan"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, fbSkipUnreadable, rows[0].Skipped, rows[0].SkipReason)
		require.Contains(t, rows[0].SkipReason, "not mounted")
	})
}

func TestFbShelfPopulated(t *testing.T) {
	empty := t.TempDir()
	require.Error(t, fbShelfPopulated(empty))
	require.Error(t, fbShelfPopulated(filepath.Join(empty, "absent")))
	full := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(full, "x"), []byte("x"), 0o644))
	require.NoError(t, fbShelfPopulated(full))
	require.Error(t, fbShelfPopulated(filepath.Join(full, "x")), "a file is not a shelf")
}

// Apply re-plans under the merge lock, so the disk is read again there: a file
// that disappears after the plan changes the row's fingerprint and the apply
// writes nothing.
func TestFolderBooksFixer_DiskFileGoneAfterPlanChangesSincePlan(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	works := f.seedPennyworth(t)
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s %s", row.Skipped, row.SkipReason)
	require.Equal(t, "2", row.Proposed["new_books"])
	for _, p := range works["Knightfall"] {
		require.NoError(t, os.Remove(p))
	}
	booksBefore, rowsBefore := f.liveTitled(t, "Knightfall"), fbRowCount(t, f.s)
	out := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.True(t, f.live(t, "fb"))
	require.Equal(t, booksBefore, f.liveTitled(t, "Knightfall"))
	require.Equal(t, rowsBefore, fbRowCount(t, f.s))
}
