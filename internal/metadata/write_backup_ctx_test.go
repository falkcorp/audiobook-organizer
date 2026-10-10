// file: internal/metadata/write_backup_ctx_test.go
// version: 1.1.0
// guid: 8f385b95-1f2d-4e61-9a15-32b784f3abc7
// last-edited: 2026-10-10

package metadata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/stretchr/testify/require"
)

// The bulk opt-out is carried by ctx, so it only works if ctx reaches the
// write. These drive the package's ctx-taking single-tag writer on a real
// (ffmpeg-synthesized, silent) file with create_backups on: a bulk-style ctx
// leaves no .bak-* sibling, a plain one leaves exactly one.
func TestWriteSingleTagContext_BackupFollowsContext(t *testing.T) {
	prevDeps := packageSafeWriteDeps
	t.Cleanup(func() { packageSafeWriteDeps = prevDeps })
	packageSafeWriteDeps = tagger.SafeWriteDeps{}

	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	cases := []struct {
		name     string
		ctx      context.Context
		wantBaks int
	}{
		{"bulk-style ctx (WithoutBackup)", tagger.WithoutBackup(context.Background()), 0},
		{"single-book ctx", context.Background(), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := makeTestAudioExt(t, "m4a", map[string][]string{"COMPOSER": {"Old Value"}})
			require.NoError(t, WriteSingleTagContext(tc.ctx, path, "COMPOSER", "New Value"))

			baks, err := filepath.Glob(path + ".bak-*")
			require.NoError(t, err)
			require.Len(t, baks, tc.wantBaks, "backup siblings: %v", baks)

			raw, err := readTagsWithTaglib(path)
			require.NoError(t, err)
			require.Equal(t, []string{"New Value"}, raw["COMPOSER"])
		})
	}
}

// WriteMetadataToFile falls back to the CLI writers when the native write
// fails. Those rewrite the file in place, outside fileops.WriteTagsSafe, so
// the create_backups backup is taken before them, as a copy. The native path
// is switched off here and the format is one no CLI writer handles, so the
// write fails at once; the backup must already exist, holding the original.
func TestWriteMetadataToFile_CLIFallbackKeepsBackup(t *testing.T) {
	prevDeps := packageSafeWriteDeps
	prevTaglib := taglibAvailable
	t.Cleanup(func() { packageSafeWriteDeps, taglibAvailable = prevDeps, prevTaglib })
	packageSafeWriteDeps = tagger.SafeWriteDeps{}
	taglibAvailable = false

	prev := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	for _, on := range []bool{true, false} {
		config.Mutate(func(c *config.Config) { c.CreateBackups = on })
		path := filepath.Join(t.TempDir(), "synthetic.ogg")
		original := []byte("synthetic pre-write bytes")
		require.NoError(t, os.WriteFile(path, original, 0o644))

		require.Error(t, WriteMetadataToFile(path, map[string]any{"title": "Synthetic Title"}, fileops.OperationConfig{}))

		baks, err := filepath.Glob(path + ".bak-*")
		require.NoError(t, err)
		if !on {
			require.Empty(t, baks, "create_backups off: no backup")
			continue
		}
		require.Len(t, baks, 1, "create_backups on: one backup before the CLI write")
		got, err := os.ReadFile(baks[0])
		require.NoError(t, err)
		require.Equal(t, original, got)
		a, _ := os.Stat(path)
		b, _ := os.Stat(baks[0])
		require.False(t, os.SameFile(a, b), "the CLI-path backup must be a copy, not a hardlink")
	}
}
