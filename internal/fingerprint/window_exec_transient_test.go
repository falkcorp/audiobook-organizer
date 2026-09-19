// file: internal/fingerprint/window_exec_transient_test.go
// version: 1.0.0
// guid: b4d13f30-9bb8-452c-a88e-53807ef817ef
// last-edited: 2026-09-19

package fingerprint

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A durable tombstone is only right for a failure that re-reading the same
// bytes with the same tools would repeat. Process start failures (fork
// EAGAIN/ENOMEM, a missing binary), deaths by signal (the OOM killer) and I/O
// errors from the filesystem are not that, and must say so.
func TestFileWindow_TransientFailuresAreMarked(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	okFp := writeScript(t, dir, "fpcalc-ok", fakeFpcalcRaw(200))
	cases := []struct {
		name      string
		ffmpeg    string
		fpcalc    string
		transient bool
		sentinel  error
	}{
		{"ffmpeg killed by signal", writeScript(t, dir, "ff-kill", "kill -9 $$"), okFp, true, ErrWindowFFmpeg},
		{"fpcalc killed by signal", writeScript(t, dir, "ff-ok", fakeFFmpegOK(120)), writeScript(t, dir, "fp-kill", "kill -9 $$"), true, ErrWindowFpcalc},
		{"ffmpeg cannot start", filepath.Join(dir, "no-such-ffmpeg"), okFp, true, ErrWindowFFmpeg},
		{"fpcalc cannot start", writeScript(t, dir, "ff-ok2", fakeFFmpegOK(120)), filepath.Join(dir, "no-such-fpcalc"), true, ErrWindowFpcalc},
		{"ffmpeg I/O error", writeScript(t, dir, "ff-eio", "echo 'file:/x.m4b: Input/output error' >&2; exit 1"), okFp, true, ErrWindowFFmpeg},
		{"ffmpeg stale NFS handle", writeScript(t, dir, "ff-estale", "echo 'Stale file handle' >&2; exit 1"), okFp, true, ErrWindowFFmpeg},
		{"ffmpeg decoder error is durable", writeScript(t, dir, "ff-bad", "echo 'Invalid data found when processing input' >&2; exit 1"), okFp, false, ErrWindowFFmpeg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wt := WindowTools{FpcalcPath: tc.fpcalc, FFmpegPath: tc.ffmpeg}
			_, err := wt.FileWindow(context.Background(), "/x.m4b", testSpec)
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err %v does not wrap %v", err, tc.sentinel)
			}
			if got := errors.Is(err, ErrWindowTransient); got != tc.transient {
				t.Fatalf("transient = %v, want %v (err %v)", got, tc.transient, err)
			}
		})
	}
}
