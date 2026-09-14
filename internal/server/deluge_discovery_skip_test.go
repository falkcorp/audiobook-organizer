// file: internal/server/deluge_discovery_skip_test.go
// version: 1.1.0
// guid: 3e7b9d05-8c21-4a6f-b0d4-5f92e1c6a738
// last-edited: 2026-09-14

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// A pending file that already sits at its library destination (its source is
// under RootDir, so source == destination): ImportToLibrary copies nothing and
// updates no row. Before 2026-09-14 the endpoint counted it as imported, so
// the bulk import reported success for a file it never touched -- and, with
// no row update, the same file came back as pending on every run.
func TestHandleDiscoveryImport_SourceIsDestination_ReportedSkippedNotImported(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Author", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
	require.NoError(t, os.WriteFile(src, []byte("audiodata"), 0o644))

	origRoot, origMove := config.AppConfig.RootDir, config.AppConfig.DelugeMoveEnabled
	config.AppConfig.RootDir = root
	config.AppConfig.DelugeMoveEnabled = false
	t.Cleanup(func() {
		config.AppConfig.RootDir = origRoot
		config.AppConfig.DelugeMoveEnabled = origMove
	})
	client, err := deluge.New("http://deluge.invalid:8112", "deluge")
	require.NoError(t, err)
	t.Cleanup(deluge.SetGlobalClientForTest(client))

	full := &database.BookFile{ID: "f1", BookID: "b1", FilePath: src, DelugeHash: "abc123"}
	resp, updated := callDiscoveryImport(t, full, func(string, string) (*database.BookFile, error) {
		cp := *full
		return &cp, nil
	})

	assert.Equal(t, 0, resp.Data.Imported, "nothing was copied, so nothing was imported")
	assert.Equal(t, 1, resp.Data.Skipped)
	assert.Equal(t, 0, resp.Data.Failed)
	require.Len(t, resp.Data.Results, 1)
	assert.Empty(t, resp.Data.Results[0].Error)
	assert.Empty(t, resp.Data.Results[0].NewPath, "a skipped file has no new path")
	// The row is marked imported in place, so it stops coming back as pending.
	require.NotNil(t, updated, "the row must be marked imported")
	assert.NotNil(t, updated.ImportedFromDelugeAt)
	assert.Equal(t, src, updated.FilePath, "nothing moved")
}
