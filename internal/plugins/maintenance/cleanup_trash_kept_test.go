// file: internal/plugins/maintenance/cleanup_trash_kept_test.go
// version: 1.1.0
// guid: 2c8f5a17-6d3b-4e90-b1a4-9e7d0c3f8b52
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// servedFiles are the paths a live book still serves, as the cleanup reports them.
func servedFiles(paths ...string) []database.PurgeFileNote {
	notes := make([]database.PurgeFileNote, len(paths))
	for i, p := range paths {
		notes[i] = database.PurgeFileNote{Path: p, Reason: versions.KeptReason}
	}
	return notes
}

// The trash-cleanup op lists every version it left in the trash because its
// book still serves the files (one WARN each, with book id, version id and
// path count), and its summary line counts them.
func TestRunTrashCleanup_ReportsVersionsTheBookStillServes(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged: 2,
		Kept: []versions.KeptVersion{
			{BookID: "hush", VersionID: "ver-hush", Files: servedFiles("/lib/Gotham/hush.m4b")},
			{BookID: "year-one", VersionID: "ver-year-one", Files: servedFiles("/lib/Gotham/y1-01.m4b", "/lib/Gotham/y1-02.m4b", "/lib/Gotham/y1-03.m4b")},
		},
	}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	require.Equal(t, []string{
		"Trash cleanup: kept version ver-hush of book hush in the trash: 1 path(s) kept, book still serves this file",
		"Trash cleanup: kept version ver-year-one of book year-one in the trash: 3 path(s) kept, book still serves this file",
		"Trash cleanup: purged 2 versions, kept 2 versions whose files the book still serves",
	}, rep.logs)
}

// The same versions are in the persisted result, so the operation page can link
// to them: they are counted apart from Purged and from the purged-with-files-kept set.
func TestRunTrashCleanup_ServedVersionsAreInTheResult(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged: 2,
		Kept:   []versions.KeptVersion{{BookID: "hush", VersionID: "ver-hush", Files: servedFiles("/lib/Gotham/hush.m4b")}},
	}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	got, ok := rep.result.(TrashCleanupResult)
	require.True(t, ok, "result = %#v", rep.result)
	require.Equal(t, 1, got.ServedKept)
	require.Equal(t, []string{"ver-hush"}, got.ServedKeptVersionIDs)
	require.Equal(t, []string{"hush"}, got.ServedKeptBookIDs)
	require.Equal(t, []string{"/lib/Gotham/hush.m4b"}, got.ServedKeptFilePaths)
	require.Equal(t, 0, got.KeptFiles)
	require.Equal(t, 2, got.Purged)
}

func TestRunTrashCleanup_NothingKept(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{Purged: 1}})
	rep := &levelReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	require.Equal(t, []string{"Trash cleanup: purged 1 versions, kept 0 versions whose files the book still serves"}, rep.logs)
}
