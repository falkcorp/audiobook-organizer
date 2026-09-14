// file: internal/server/movement_atom_no_import_test.go
// version: 1.0.0
// guid: 9e4a7c23-1b5d-4f86-a2c0-6d8e3f1b7a54
// last-edited: 2026-09-14

package server

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"

	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// seedingDirChecker reports every path under dir as protected, the way the
// Deluge list reports a seeding path. A static ProtectedPaths prefix would
// not reproduce the hole: ImportToLibrary already refuses those (#3402).
type seedingDirChecker struct{ dir string }

func (c seedingDirChecker) IsProtected(p string) bool {
	return strings.HasPrefix(p, c.dir+string(os.PathSeparator))
}

// countingLibraryImporter stands in for the Deluge adapter: it reports a copy
// in the library root, which is what the real one made.
type countingLibraryImporter struct {
	calls atomic.Int32
	dest  string
}

func (c *countingLibraryImporter) ImportPath(context.Context, string, string) (string, error) {
	c.calls.Add(1)
	return c.dest, nil
}

// Before 2026-09-14 the cleanup handed a protected path to the importer, which
// copied the seeding file to RootDir/<basename> and repointed its row there.
// The cleanup must refuse it: ErrProtectedPathWrite, the importer never
// called, the seeding file byte-identical.
func TestRemoveMovementAtomsFromFile_ProtectedPathRefusedNeverImported(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	seeding := filepath.Join(t.TempDir(), "seeding")
	path := filepath.Join(seeding, "book.m4a")
	require.NoError(t, os.MkdirAll(seeding, 0o755))
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "1",
		"-c:a", "aac", "-b:a", "32k", path)
	out, err := gen.CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	require.NoError(t, taglib.WriteTags(path, map[string][]string{"MOVEMENTNAME": {"Allegro"}}, 0))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	imp := &countingLibraryImporter{dest: filepath.Join(t.TempDir(), "book.m4a")}
	deps := tagger.SafeWriteDeps{ProtectedCache: seedingDirChecker{dir: seeding}, Importer: imp}

	changed, err := removeMovementAtomsFromFile(path, deps)
	require.True(t, errors.Is(err, tagger.ErrProtectedPathWrite), "err = %v, want ErrProtectedPathWrite", err)
	require.False(t, changed)
	require.Zero(t, imp.calls.Load(), "a protected file was handed to the importer")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "the protected file was rewritten")
}

// The server's safe-write deps must carry no importer, so no server-package
// tag write can import a protected file into the library root.
func TestServerSafeWriteDeps_CarryNoImporter(t *testing.T) {
	srv := &Server{protectedPathCache: deluge.NewProtectedPathCache(nil, nil)}
	deps := srv.safeWriteDeps()
	require.NotNil(t, deps.ProtectedCache, "the guard must still be wired")
	require.Nil(t, deps.Importer, "safeWriteDeps must refuse protected paths, not import them")
}
