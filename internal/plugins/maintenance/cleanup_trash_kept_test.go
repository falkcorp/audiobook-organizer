// file: internal/plugins/maintenance/cleanup_trash_kept_test.go
// version: 1.0.0
// guid: 2c8f5a17-6d3b-4e90-b1a4-9e7d0c3f8b52
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

type trashCleanupDeps struct {
	fakeDeps
	res versions.CleanupResult
}

func (d trashCleanupDeps) CleanupTrashedVersions() versions.CleanupResult { return d.res }

// The trash-cleanup op lists every version it left in the trash because its
// book still serves the files (one WARN each, with book id, version id and
// path count), and its summary line counts them.
func TestRunTrashCleanup_ReportsVersionsTheBookStillServes(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{
		Purged: 2,
		Kept: []versions.KeptVersion{
			{BookID: "hush", VersionID: "ver-hush", Paths: 1},
			{BookID: "year-one", VersionID: "ver-year-one", Paths: 3},
		},
	}})
	rep := &fakeReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	require.Equal(t, []string{
		"Trash cleanup: kept version ver-hush of book hush in the trash: 1 path(s) kept, book still serves this file",
		"Trash cleanup: kept version ver-year-one of book year-one in the trash: 3 path(s) kept, book still serves this file",
		"Trash cleanup: purged 2 versions, kept 2 versions whose files the book still serves",
	}, rep.logs)
}

func TestRunTrashCleanup_NothingKept(t *testing.T) {
	p := New(trashCleanupDeps{res: versions.CleanupResult{Purged: 1}})
	rep := &fakeReporter{}

	require.NoError(t, p.runTrashCleanup(context.Background(), nil, rep))

	require.Equal(t, []string{"Trash cleanup: purged 1 versions, kept 0 versions whose files the book still serves"}, rep.logs)
}
