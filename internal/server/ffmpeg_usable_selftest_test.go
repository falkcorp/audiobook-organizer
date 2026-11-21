// file: internal/server/ffmpeg_usable_selftest_test.go
// version: 0.1.0
// guid: 0b6c91e4-7a38-4f52-8d17-e3a9c24f6b80
// last-edited: 2026-11-21

package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// An ffmpeg that is on PATH but cannot start (exit 127, as with a missing
// shared library) must skip the dependent test, not fail it.
func TestRequireUsableFFmpeg_BrokenBinarySkips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\necho 'error while loading shared libraries: libbatcave.so.1' >&2\nexit 127\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(stub), 0o755))
	t.Setenv("PATH", dir)

	skipped := false
	t.Run("inner", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		requireUsableFFmpeg(t)
		t.Error("requireUsableFFmpeg returned for a binary that cannot start")
	})
	require.True(t, skipped)
}
