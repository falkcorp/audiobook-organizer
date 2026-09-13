// file: internal/metafetch/rename_only_preflight_test.go
// version: 1.0.0
// guid: 1e3a5c7d-9f0b-4d2e-8a6c-3b5d7f9a1c42
// last-edited: 2026-09-13

package metafetch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

func renameOnlyFixture(t *testing.T, files func(dir string) []database.BookFile) (*Service, []string) {
	t.Helper()
	root := config.AppConfig.RootDir
	dir := filepath.Join(root, "Someone", "Old")
	rows := files(dir)
	var paths []string
	for _, f := range rows {
		writeFile(t, f.FilePath, f.ID)
		paths = append(paths, f.FilePath)
	}
	book := &database.Book{ID: "b1", Title: "Old", FilePath: dir, Author: &database.Author{ID: 1, Name: "Someone"}}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc:  func(string) (*database.Book, error) { c := *book; return &c, nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return append([]database.BookFile(nil), rows...), nil },
	})
	return svc, paths
}

// The write-back preflight plans the book as it stands, and runs even with
// auto_rename_on_apply OFF: the write-back renames on an explicit
// rename:true regardless, and RenamePreflight's preview stands down there.
func TestRenameOnlyPreflight_RefusesUnplannableRenameWithAutoRenameOff(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = t.TempDir()
	config.AppConfig.AutoRenameOnApply = false
	config.AppConfig.FolderNamingPattern = "{author}/{title}"

	svc, paths := renameOnlyFixture(t, func(dir string) []database.BookFile {
		return []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: filepath.Join(dir, "1.mp3"), Format: "mp3", TrackNumber: 1},
			{ID: "f2", BookID: "b1", FilePath: filepath.Join(dir, "2.mp3"), Format: "mp3", TrackNumber: 2},
		}
	})

	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"
	require.NoError(t, svc.RenameOnlyPreflight("b1"), "a plannable rename must not be refused")

	config.AppConfig.FileNamingPattern = "{title} - {track:x}"
	err := svc.RenameOnlyPreflight("b1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrApplyFileWorkWouldFail)

	for _, p := range paths {
		_, statErr := os.Stat(p)
		assert.NoError(t, statErr, "preflight moved %s", p)
	}
}

// Two files on one track whose names do not tell them apart: the planner
// refuses (organizer.ErrDuplicateRenameTarget) and the preflight carries it
// as a refusal before anything moves -- the failure that left three books
// half renamed on 2026-09-13.
func TestRenameOnlyPreflight_RefusesDuplicateTarget(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = t.TempDir()
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"

	svc, paths := renameOnlyFixture(t, func(dir string) []database.BookFile {
		return []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: filepath.Join(dir, "Part 1.mp3"), Format: "mp3", TrackNumber: 1},
			{ID: "f2", BookID: "b1", FilePath: filepath.Join(dir, "part 01.mp3"), Format: "mp3", TrackNumber: 1},
		}
	})

	// The planner's own verdict, so the test fails loudly if the fixture
	// stops reaching the duplicate-target branch.
	_, planErr := organizer.ComputeTargetPaths(config.AppConfig.RootDir, config.AppConfig.FolderNamingPattern,
		config.AppConfig.FileNamingPattern, []database.BookFile{
			{ID: "f1", FilePath: paths[0], TrackNumber: 1},
			{ID: "f2", FilePath: paths[1], TrackNumber: 1},
		}, organizer.PathVars{Author: "Someone", Title: "Old"}, organizer.BuildOpts{})
	require.ErrorIs(t, planErr, organizer.ErrDuplicateRenameTarget)

	err := svc.RenameOnlyPreflight("b1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrApplyFileWorkWouldFail)
	assert.Contains(t, err.Error(), "same target")
	for _, p := range paths {
		_, statErr := os.Stat(p)
		assert.NoError(t, statErr, "preflight moved %s", p)
	}
}
