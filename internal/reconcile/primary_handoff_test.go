// file: internal/reconcile/primary_handoff_test.go
// version: 1.0.0
// guid: 7b3e9c50-2a64-4d18-8f71-c5a0d2e6b394
// last-edited: 2026-09-24

package reconcile

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// CleanupDuplicateVersionGroups leaves each cleaned group with one primary.
// Before 2026-09-24 it crowned the oldest library copy by hand and left a
// duplicate it kept (because that duplicate still owns files) as primary too.
func TestCleanupDuplicateVersionGroups_LeavesOnePrimary(t *testing.T) {
	f := vptest.New(t)
	orig := f.Book(t, vptest.Spec{ID: "a-orig", Group: "g", Primary: "true", Outside: true})
	keep := f.Book(t, vptest.Spec{ID: "b-keep", Group: "g", Primary: "false"})
	dupB := f.Book(t, vptest.Spec{ID: "d-dupb", Group: "g", Primary: "true"})
	dupA := f.Book(t, vptest.Spec{ID: "c-dupa", Group: "g", Primary: "false", NoFile: true})
	_, err := f.S.ModifyBook(dupA, func(b *database.Book) error {
		b.FilePath = filepath.Join(f.Root, "Author", "dupa.m4b")
		return nil
	})
	require.NoError(t, err)

	res, err := CleanupDuplicateVersionGroups(f.S, f.Root, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.DuplicatesRemoved)
	require.Equal(t, 1, res.SkippedOwnsFiles)
	require.Zero(t, res.WriteErrors)
	got := f.RequireSinglePrimary(t, "g", "")
	require.Contains(t, []string{keep, dupB}, got, "the primary is an eligible library copy")
	require.Equal(t, "false", f.Flag(t, orig))
}

// Marking a broken-segment book soft-deletes it; a group it was primary of
// hands the flag on.
func TestFindBrokenSegmentBooks_HandsPrimaryOn(t *testing.T) {
	f := vptest.New(t)
	broken := f.Book(t, vptest.Spec{ID: "broken", Group: "g", Primary: "true", Outside: true})
	lib := f.Book(t, vptest.Spec{ID: "lib", Group: "g", Primary: "false"})
	b, err := f.S.GetBookByID(broken)
	require.NoError(t, err)
	// A directory book outside the library whose segment file is gone.
	dir := filepath.Dir(b.FilePath)
	_, err = f.S.ModifyBook(broken, func(row *database.Book) error {
		row.FilePath = dir
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, f.S.CreateBookFile(&database.BookFile{ID: "bf-gone", BookID: broken, FilePath: filepath.Join(dir, "gone.mp3")}))
	withRoot(t, f.Root)

	res, err := FindBrokenSegmentBooks(f.S, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.MarkedForReview)
	f.RequireSinglePrimary(t, "g", lib)
}

func withRoot(t *testing.T, root string) {
	t.Helper()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
}
