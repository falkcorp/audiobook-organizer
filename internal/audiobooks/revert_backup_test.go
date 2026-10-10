// file: internal/audiobooks/revert_backup_test.go
// version: 1.0.0
// guid: b6eccdfb-58cb-4b57-a684-da00c47257fd
// last-edited: 2026-10-10

package audiobooks

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// A revert writes one tag row at a time; with create_backups on, each write
// used to copy the whole file. The revert's writer opts out (owner decision
// D69, applied to reverts); the rename's writer, a single-book edit, keeps
// its backup. Both run on a silent ffmpeg-synthesized m4a.
func TestRevertWriteTags_LeavesNoBackup(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	cases := []struct {
		name     string
		write    func(string, map[string]any) error
		wantBaks int
	}{
		{"revert writer", NewRevertService(nil).WriteTags, 0},
		{"rename writer", defaultRenameWriteTags, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "book.m4a")
			out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "1",
				"-c:a", "aac", "-b:a", "32k", path).CombinedOutput()
			require.NoError(t, err, "ffmpeg: %s", out)

			require.NoError(t, tc.write(path, map[string]any{"title": "Synthetic Title"}))

			baks, err := filepath.Glob(path + ".bak-*")
			require.NoError(t, err)
			require.Len(t, baks, tc.wantBaks, "backup siblings: %v", baks)
		})
	}
}
