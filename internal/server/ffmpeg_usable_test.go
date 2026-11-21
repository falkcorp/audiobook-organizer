// file: internal/server/ffmpeg_usable_test.go
// version: 0.1.0
// guid: 5d0e8a63-2b7f-4c19-9a4e-c1f37b86d205
// last-edited: 2026-11-21

package server

import (
	"os/exec"
	"testing"
)

// requireUsableFFmpeg returns the path of an ffmpeg that actually starts, or
// skips the test. exec.LookPath only proves a file named ffmpeg is on PATH: a
// dynamically linked build whose shared libraries are missing (the prod host's
// user-local ffmpeg lacks libsndio) is found, then dies with exit 127 when the
// fixture generator runs it, so a LookPath-only guard turned an unusable tool
// into three red tests instead of a skip. `-version` loads the binary and
// prints a banner; it decodes nothing.
func requireUsableFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if out, err := exec.Command(path, "-hide_banner", "-version").CombinedOutput(); err != nil {
		t.Skipf("ffmpeg at %s does not start (%v): %s", path, err, out)
	}
	return path
}
