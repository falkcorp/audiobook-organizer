// file: internal/testutil/tooltest/tooltest_test.go
// version: 0.1.0
// guid: 3a9d6c05-71e8-4b2f-a4c3-5d18f7e09b62
// last-edited: 2026-11-21

package tooltest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func stubOnPath(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// skips runs fn in a subtest and reports whether it skipped.
func skips(t *testing.T, fn func(*testing.T)) (skipped bool) {
	t.Helper()
	t.Run("inner", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		fn(t)
	})
	return skipped
}

// A tool that is on PATH but cannot start (exit 127, as with a missing shared
// library) must skip the dependent test, not fail it.
func TestRequire_BrokenBinarySkips(t *testing.T) {
	stubOnPath(t, "ffmpeg", "#!/bin/sh\necho 'error while loading shared libraries: libbatcave.so.1' >&2\nexit 127\n")
	if !skips(t, func(t *testing.T) { Require(t, "ffmpeg"); t.Error("returned for a binary that cannot start") }) {
		t.Fatal("did not skip")
	}
}

func TestRequire_MissingToolSkips(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if !skips(t, func(t *testing.T) { Require(t, "fpcalc"); t.Error("returned for a missing tool") }) {
		t.Fatal("did not skip")
	}
}

func TestRequire_WorkingToolReturnsPath(t *testing.T) {
	stubOnPath(t, "ffprobe", "#!/bin/sh\necho 'ffprobe version 0'\n")
	var got string
	if skips(t, func(t *testing.T) { got = Require(t, "ffprobe") }) {
		t.Fatal("skipped a working tool")
	}
	if filepath.Base(got) != "ffprobe" {
		t.Fatalf("path = %q", got)
	}
}

func TestRequireAll_SkipsWhenAnyBroken(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "fpcalc"), []byte("#!/bin/sh\nexit 127\n"), 0o755)
	t.Setenv("PATH", dir)
	if !skips(t, func(t *testing.T) { RequireAll(t, "ffmpeg", "fpcalc"); t.Error("returned") }) {
		t.Fatal("did not skip")
	}
}
