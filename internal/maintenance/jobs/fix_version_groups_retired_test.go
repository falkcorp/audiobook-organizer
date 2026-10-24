// file: internal/maintenance/jobs/fix_version_groups_retired_test.go
// version: 1.0.1
// guid: 8e3c1b57-4a92-4d06-b7f1-2c5d9a60e417
// last-edited: 2026-10-24

package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// vgRetireRowOnto creates a row for path under a fresh shell book and retires it
// into holderID: keyed under the holder, flagged Retired, the path index naming
// it, GetBookFileByPath unable to see it.
func vgRetireRowOnto(t *testing.T, ps *database.PebbleStore, holderID, path string) string {
	t.Helper()
	shell, err := ps.CreateBook(&database.Book{Title: "Retired shell", FilePath: filepath.Join(t.TempDir(), "shell")})
	require.NoError(t, err)
	now := time.Now()
	v, err := ps.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-vg", RetiredAt: &now,
	})
	require.NoError(t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: path, Format: "mp3", FileSize: 4}
	require.NoError(t, ps.CreateBookFile(row))
	require.NoError(t, ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holderID, v.ID))
	hidden, err := ps.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Nil(t, hidden)
	return row.ID
}

// The author-dir fix must not create a row on, or repoint a live row onto, a file
// a retired version holds. vgPlanAuthorDirFix asked GetBookFileByPath, which
// cannot see a retired row, so such a file read as free.
func TestVgPlanAuthorDirFix_FileHeldByRetiredVersionIsNeitherCreatedNorRepointed(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })

	root := t.TempDir()
	subdir := filepath.Join(root, "Author", "Book")
	require.NoError(t, os.MkdirAll(subdir, 0o755))
	free := filepath.Join(subdir, "01.mp3")
	heldNew := filepath.Join(subdir, "02.mp3")
	for _, p := range []string{free, heldNew} {
		require.NoError(t, os.WriteFile(p, []byte("1234"), 0o644))
	}

	book, err := ps.CreateBook(&database.Book{Title: "Book", FilePath: filepath.Join(root, "old")})
	require.NoError(t, err)
	holder, err := ps.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(root, "holder")})
	require.NoError(t, err)
	// The book's live row for 02.mp3 sits at a path that is gone: the plan would
	// repoint it to heldNew (same name, same size) if heldNew were free.
	oldPath := filepath.Join(root, "old", "02.mp3")
	require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: oldPath, Format: "mp3", FileSize: 4}))
	retiredID := vgRetireRowOnto(t, ps, holder.ID, heldNew)

	// Nothing of the book's verifiably lives in subdir except through the retired
	// file, so the fix refuses (and says why) rather than move the book onto it.
	_, err = vgPlanAuthorDirFix(ps, book.ID, subdir)
	require.ErrorIs(t, err, errVGRefused)
	require.Contains(t, err.Error(), "1 held by retired versions")

	// With a row of the book's own at 01.mp3 the plan stands: the retired file is
	// reported and left alone, the live row that would have been repointed onto
	// it stays where it is, and the free 03.mp3 still gets its row.
	require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: free, Format: "mp3", FileSize: 4}))
	third := filepath.Join(subdir, "03.mp3")
	require.NoError(t, os.WriteFile(third, []byte("1234"), 0o644))
	plan, err := vgPlanAuthorDirFix(ps, book.ID, subdir)
	require.NoError(t, err)
	require.Equal(t, 1, plan.keep)
	require.Equal(t, []string{third}, plan.create, "only the free file gets a row")
	require.Empty(t, plan.repoint, "a live row must not be repointed onto a retired version's file")
	require.Equal(t, []string{heldNew}, plan.heldRetired)
	require.Empty(t, plan.ownedElsewhere)
	require.Equal(t, 1, plan.left, "the live row that matched nothing is kept as it is")

	at, err := ps.BookFilesAtPath(heldNew)
	require.NoError(t, err)
	require.Len(t, at, 1)
	require.Equal(t, retiredID, at[0].ID)
}

// vgIndexDownStore reports the complete multi-row lookup as unavailable, the
// way the store does while memdb warms up after a restart. Every other read is
// the real store's.
type vgIndexDownStore struct{ *database.PebbleStore }

func (vgIndexDownStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, database.ErrBookFilesAtPathUnavailable
}

func vgWarmupFixture(t *testing.T) (ps *database.PebbleStore, book *database.Book, subdir, free string) {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	root := t.TempDir()
	subdir = filepath.Join(root, "Author", "Book")
	require.NoError(t, os.MkdirAll(subdir, 0o755))
	free = filepath.Join(subdir, "01.mp3")
	require.NoError(t, os.WriteFile(free, []byte("1234"), 0o644))
	book, err = ps.CreateBook(&database.Book{Title: "Book", FilePath: filepath.Join(root, "old")})
	require.NoError(t, err)
	return ps, book, subdir, free
}

// With no retired version in the store, the author-dir fix does not need the
// complete lookup (no retired row can hold a file), so it still plans while memdb
// is warming up, and still sees a live row another book owns at the path through
// the single-owner index.
func TestVgPlanAuthorDirFix_CompleteLookupDownWithNoRetiredVersionsStillPlans(t *testing.T) {
	ps, book, subdir, free := vgWarmupFixture(t)
	other, err := ps.CreateBook(&database.Book{Title: "Other", FilePath: filepath.Join(t.TempDir(), "other")})
	require.NoError(t, err)
	taken := filepath.Join(subdir, "02.mp3")
	require.NoError(t, os.WriteFile(taken, []byte("1234"), 0o644))
	require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: taken, Format: "mp3", FileSize: 4}))

	plan, err := vgPlanAuthorDirFix(vgIndexDownStore{ps}, book.ID, subdir)
	require.NoError(t, err)
	require.Equal(t, []string{free}, plan.create)
	require.Equal(t, []string{taken}, plan.ownedElsewhere)
	require.Empty(t, plan.heldRetired)
}

// Once a retired version exists the complete lookup is required, and when it
// cannot be answered the plan is an error, never a plan made from a lookup that
// cannot see a retired row.
func TestVgPlanAuthorDirFix_CompleteLookupDownWithRetiredVersionsIsAnError(t *testing.T) {
	ps, book, subdir, _ := vgWarmupFixture(t)
	holder, err := ps.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(t.TempDir(), "holder")})
	require.NoError(t, err)
	vgRetireRowOnto(t, ps, holder.ID, filepath.Join(t.TempDir(), "elsewhere.mp3"))

	_, err = vgPlanAuthorDirFix(vgIndexDownStore{ps}, book.ID, subdir)
	require.ErrorIs(t, err, database.ErrOwnershipLookupIncomplete)
}
