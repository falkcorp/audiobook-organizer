// file: internal/maintenance/jobs/purge_ua_primary_handoff_test.go
// version: 1.0.0
// guid: 8f4d2a19-3c7b-4e65-9a81-b0e6c5d27f34
// last-edited: 2026-09-24

package jobs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A purged Unknown Author copy that was its group's primary hands the flag to
// its twin in the same group.
func TestPurgeUADuplicates_PurgedPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	old := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = old })

	content := bytes.Repeat([]byte{0xAB}, 1<<20)
	mk := func(id, rel, primary string) string {
		p := filepath.Join(f.Root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, content, 0o644))
		g, st, pv := "g", "organized", primary == "true"
		b, err := f.S.CreateBook(&database.Book{ID: id, Title: id, FilePath: p, VersionGroupID: &g, LibraryState: &st, IsPrimaryVersion: &pv})
		require.NoError(t, err)
		_, err = f.S.ModifyBook(b.ID, func(row *database.Book) error { row.IsPrimaryVersion = &pv; return nil })
		require.NoError(t, err)
		require.NoError(t, f.S.CreateBookFile(&database.BookFile{ID: "bf-" + id, BookID: b.ID, FilePath: p, FileSize: int64(len(content))}))
		return b.ID
	}
	ua := mk("ua", "Unknown Author/Dup/dup.m4b", "true")
	twin := mk("twin", "Real Author/Dup/twin.m4b", "false")

	require.NoError(t, (&purgeUADuplicatesJob{}).Run(context.Background(), f.S, &nopReporter{}, false))

	b, err := f.S.GetBookByID(ua)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted(), "the UA copy is purged")
	f.RequireSinglePrimary(t, "g", twin)
}
