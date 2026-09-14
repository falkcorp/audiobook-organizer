// file: internal/server/movement_atom_protected_test.go
// version: 1.0.0
// guid: 7f2c4b18-e9a3-4d65-b1f0-38c6a5e2d947
// last-edited: 2026-09-14

package server

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// A protected file carrying a movement atom: the write guard refuses it
// (no book_file row, so no library copy to write), and the cleanup must count
// that as SkippedProtected, not Failed, and leave the file byte-identical.
// Before 2026-09-14 the refusal landed in Failed, so every seeding file with a
// movement atom read as a cleanup failure.
func TestStripMovementAtoms_ProtectedFileCountedAsSkippedNotFailed(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	root := t.TempDir()
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })
	withAppDirConfig(t, root)

	seeding := filepath.Join(root, "seeding")
	path := filepath.Join(seeding, "Book", "book.m4a")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "1",
		"-c:a", "aac", "-b:a", "32k", path)
	out, err := gen.CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	require.NoError(t, taglib.WriteTags(path, map[string][]string{"MOVEMENTNAME": {"Allegro"}}, 0))
	tags, err := taglib.ReadTags(path)
	require.NoError(t, err)
	require.Contains(t, tags, "MOVEMENTNAME", "fixture error: the movement atom was not written")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	srv := &Server{store: store, protectedPathCache: deluge.NewProtectedPathCache(nil, []string{seeding})}
	res := srv.stripMovementAtoms(context.Background())

	require.Equal(t, 1, res.SkippedProtected, "result: %+v", res)
	require.Equal(t, 0, res.Failed, "a protected refusal is not a failure: %+v", res)
	require.Equal(t, 1, res.Visited, "Visited must include the skipped file: %+v", res)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "the protected file was rewritten")
}
