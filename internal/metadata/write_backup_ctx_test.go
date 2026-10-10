// file: internal/metadata/write_backup_ctx_test.go
// version: 1.3.0
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
// is switched off here and a fake AtomicParsley on PATH rewrites the file IN
// PLACE (same inode), the case a hardlinked backup would get wrong.
func TestWriteMetadataToFile_CLIFallbackKeepsBackup(t *testing.T) {
	prevDeps := packageSafeWriteDeps
	prevTaglib := taglibAvailable
	t.Cleanup(func() { packageSafeWriteDeps, taglibAvailable = prevDeps, prevTaglib })
	packageSafeWriteDeps = tagger.SafeWriteDeps{}
	taglibAvailable = false
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "AtomicParsley"),
		[]byte("#!/bin/sh\nprintf ' tagged' >> \"$1\"\n"), 0o755))
	t.Setenv("PATH", bin)

	prev := config.Snapshot().CreateBackups
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.CreateBackups = prev }) })

	for _, on := range []bool{true, false} {
		config.Mutate(func(c *config.Config) { c.CreateBackups = on })
		path := filepath.Join(t.TempDir(), "synthetic.m4b")
		original := []byte("synthetic pre-write bytes")
		require.NoError(t, os.WriteFile(path, original, 0o644))

		require.NoError(t, WriteMetadataToFile(path, map[string]any{"title": "Synthetic Title"}, fileops.OperationConfig{}))
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, append(append([]byte{}, original...), " tagged"...), got, "the fake writer ran in place")

		baks, err := filepath.Glob(path + ".bak-*")
		require.NoError(t, err)
		if !on {
			require.Empty(t, baks, "create_backups off: no backup")
			continue
		}
		require.Len(t, baks, 1, "create_backups on: one backup before the CLI write")
		bakBytes, err := os.ReadFile(baks[0])
		require.NoError(t, err)
		require.Equal(t, original, bakBytes, "the backup must hold the pre-write bytes after an in-place write")
	}
}

// A CLI write that fails without touching the file (here: the tool is not on
// PATH) leaves no backup, as a failed WriteTagsSafe leaves none.
func TestWriteMetadataToFile_CLIFailureUntouchedKeepsNoBackup(t *testing.T) {
	prevDeps := packageSafeWriteDeps
	prevTaglib := taglibAvailable
	t.Cleanup(func() { packageSafeWriteDeps, taglibAvailable = prevDeps, prevTaglib })
	packageSafeWriteDeps = tagger.SafeWriteDeps{}
	taglibAvailable = false
	t.Setenv("PATH", "")
	prev := config.Snapshot().CreateBackups
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.CreateBackups = prev }) })
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })

	path := filepath.Join(t.TempDir(), "synthetic.m4b")
	original := []byte("synthetic pre-write bytes")
	require.NoError(t, os.WriteFile(path, original, 0o644))
	require.Error(t, WriteMetadataToFile(path, map[string]any{"title": "Synthetic Title"}, fileops.OperationConfig{}))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, got)
	baks, err := filepath.Glob(path + ".bak-*")
	require.NoError(t, err)
	require.Empty(t, baks, "a CLI write that touched nothing must leave no backup")
}

// A format no CLI writer handles is refused without a backup: nothing would
// be written, and the file need not even exist.
func TestWriteMetadataToFile_UnsupportedFormatKeepsNoBackup(t *testing.T) {
	prevDeps := packageSafeWriteDeps
	prevTaglib := taglibAvailable
	t.Cleanup(func() { packageSafeWriteDeps, taglibAvailable = prevDeps, prevTaglib })
	packageSafeWriteDeps = tagger.SafeWriteDeps{}
	taglibAvailable = false
	prev := config.Snapshot().CreateBackups
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.CreateBackups = prev }) })
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })

	path := filepath.Join(t.TempDir(), "synthetic.ogg")
	require.NoError(t, os.WriteFile(path, []byte("synthetic bytes"), 0o644))
	err := WriteMetadataToFile(path, map[string]any{"title": "Synthetic Title"}, fileops.OperationConfig{})
	require.EqualError(t, err, "unsupported file format: .ogg")
	baks, gerr := filepath.Glob(path + ".bak-*")
	require.NoError(t, gerr)
	require.Empty(t, baks)
}
