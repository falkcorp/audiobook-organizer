// file: internal/plugins/maintenance/kinds_t6_g3_m4_test.go
// version: 1.0.0
// guid: be169947-3387-43a5-abcf-5f5b078ec4dc
// last-edited: 2026-11-15

package maintenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
)

// A transcode-temp-cleanup PREVIEW reports what a live run would remove. The gated
// writer's Do does not run the call in Preview (t6g3Gate.Do models that: it returns
// without calling fn), so a count taken through the writer was always 0 and the
// preview said "would remove 0" over any number of stale files. The count is a read
// outside the writer, so the run also makes no writer call and deletes nothing.
func TestT6G3_TranscodeTempCleanupPreviewCountsStaleFilesWithoutTheWriter(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	stale := []string{
		filepath.Join(root, "Hush-transcode.tmp"),
		filepath.Join(root, "Year One.ch.m4b"),
	}
	for _, p := range stale {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(p, old, old))
	}
	fresh := filepath.Join(root, "Arkham Asylum-transcode.tmp")
	require.NoError(t, os.WriteFile(fresh, []byte("x"), 0o600))

	f := newT6G3Fake(t)
	f.store.paths = []database.ImportPath{{ID: 1, Path: root}}
	d := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(f.deps(), nil), "maintenance.transcode-temp-cleanup")

	_, rc := newT6G3Host(d.Kind(), tasks.ModePreview, &t6g3Gate{})
	out, err := t6g3Run(t, d, rc, "")
	require.NoError(t, err)
	var res transcodeTempCleanupResult
	require.NoError(t, json.Unmarshal(out, &res))
	assert.Equal(t, 2, res.Removed, "the preview counts the two stale files and not the fresh one")
	assert.Equal(t, 1, res.Swept)
	assert.Empty(t, f.writer.called(), "a preview makes no writer call")
	for _, p := range append(stale, fresh) {
		assert.FileExists(t, p, "a preview deletes nothing")
	}

	// The live run of the same tree goes through the writer (the recording writer
	// answers 1) and never takes the preview's read.
	lf := newT6G3Fake(t)
	lf.store.paths = []database.ImportPath{{ID: 1, Path: root}}
	ld := t6g3KindByName(t, (&Plugin{}).t6g3Kinds(lf.deps(), nil), "maintenance.transcode-temp-cleanup")
	_, lrc := newT6G3Host(ld.Kind(), tasks.ModeLive, &t6g3Gate{})
	lout, err := t6g3Run(t, ld, lrc, "")
	require.NoError(t, err)
	var live transcodeTempCleanupResult
	require.NoError(t, json.Unmarshal(lout, &live))
	assert.Equal(t, 1, live.Removed)
	assert.Equal(t, []string{"CleanupStaleTempFiles"}, lf.writer.called())
}
