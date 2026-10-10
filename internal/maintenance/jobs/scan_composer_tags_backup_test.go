// file: internal/maintenance/jobs/scan_composer_tags_backup_test.go
// version: 1.0.0
// guid: cefa0c9d-7187-413d-ad0b-faa8afc3f0f4
// last-edited: 2026-10-10

package jobs_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// scan-composer-tags rewrites COMPOSER across the whole library, a bulk tag
// write: with create_backups on it must leave no .bak-* sibling (owner
// decision D69). The file is a silent ffmpeg-synthesized m4a.
func TestScanComposerTags_LeavesNoBackup(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	path := filepath.Join(t.TempDir(), "book.m4a")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "1",
		"-c:a", "aac", "-b:a", "32k", path).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)

	store := newSyncPebbleStore(t)
	narrator := "Synthetic Narrator"
	b, err := store.CreateBook(&database.Book{Title: "Synthetic Title", FilePath: path, Format: "m4a", Narrator: &narrator})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path, Format: "m4a"}))

	j, err := maintenance.Get("scan-composer-tags")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), store, &noopReporter{}, false))

	tags, err := taglib.ReadTags(path)
	require.NoError(t, err)
	require.Equal(t, []string{narrator}, tags["COMPOSER"], "fixture error: the job wrote nothing")
	baks, err := filepath.Glob(path + ".bak-*")
	require.NoError(t, err)
	require.Empty(t, baks, "scan-composer-tags left backups")
}
