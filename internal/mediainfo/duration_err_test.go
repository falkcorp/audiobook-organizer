// file: internal/mediainfo/duration_err_test.go
// version: 1.0.0
// guid: f887e2cd-a3a0-4243-8bbd-b87a475f4f35
// last-edited: 2026-11-04

package mediainfo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeFFprobe puts a stub "ffprobe" shell script first (and only) on PATH and
// disables mediainfo, so the duration probe's outcome is exactly what the script
// does. No audio is read or decoded.
func fakeFFprobe(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	if script == "" {
		t.Setenv("PATH", dir) // nothing but an empty directory: ffprobe is not installed
	}
	t.Setenv("ABK_DISABLE_MEDIAINFO", "1")
}

// junkAudio is a non-empty file with an audio extension whose tags cannot be
// read, so Extract takes the inferFromFormat path and has bytes to estimate from.
func junkAudio(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, make([]byte, 480_000), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtract_DurationErr(t *testing.T) {
	t.Run("stream read succeeds: no error, not estimated", func(t *testing.T) {
		fakeFFprobe(t, "echo 3725.4")
		info, err := Extract(junkAudio(t, "Year One.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Duration != 3725 || info.DurationEstimated || info.DurationErr != nil {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("ffprobe missing: estimate flagged and the cause is exec.ErrNotFound", func(t *testing.T) {
		fakeFFprobe(t, "")
		info, err := Extract(junkAudio(t, "Year One.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.DurationEstimated || info.Duration <= 0 {
			t.Fatalf("want a flagged estimate, got %+v", info)
		}
		if !errors.Is(info.DurationErr, exec.ErrNotFound) {
			t.Fatalf("DurationErr = %v, want exec.ErrNotFound in the chain", info.DurationErr)
		}
	})
	t.Run("ffprobe exits 1: estimate flagged and the cause is the exit error", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		var ee *exec.ExitError
		if !info.DurationEstimated || !errors.As(info.DurationErr, &ee) {
			t.Fatalf("got %+v", info)
		}
		if errors.Is(info.DurationErr, exec.ErrNotFound) || errors.Is(info.DurationErr, fs.ErrNotExist) {
			t.Fatalf("an exit error was misclassified: %v", info.DurationErr)
		}
	})
	t.Run("ffprobe reports 0: error says so", func(t *testing.T) {
		fakeFFprobe(t, "echo 0.000000")
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		if info.DurationErr == nil || info.DurationErr.Error() != "probe returned 0s" {
			t.Fatalf("DurationErr = %v", info.DurationErr)
		}
	})
	t.Run("ffprobe prints nonsense: error is a parse error", func(t *testing.T) {
		fakeFFprobe(t, "echo N/A")
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		if info.DurationErr == nil || info.Duration <= 0 || !info.DurationEstimated {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("timeout: the cause is context.DeadlineExceeded", func(t *testing.T) {
		fakeFFprobe(t, "exec sleep 30")
		prev := ffprobeDurationTimeout
		ffprobeDurationTimeout = 200 * time.Millisecond
		t.Cleanup(func() { ffprobeDurationTimeout = prev })
		info, _ := Extract(junkAudio(t, "Year One.m4b"))
		if !errors.Is(info.DurationErr, context.DeadlineExceeded) {
			t.Fatalf("DurationErr = %v, want context.DeadlineExceeded in the chain", info.DurationErr)
		}
	})
	t.Run("no bitrate to estimate from: still carries the error with Duration 0", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		p := filepath.Join(t.TempDir(), "empty.m4b") // zero bytes: nothing to estimate from
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		info, _ := Extract(p)
		if info.Duration != 0 || info.DurationEstimated || info.DurationErr == nil {
			t.Fatalf("got %+v", info)
		}
	})
	t.Run("file absent: Extract fails with fs.ErrNotExist", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		_, err := Extract(filepath.Join(t.TempDir(), "gone.m4b"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsupported container: no duration, error says so", func(t *testing.T) {
		fakeFFprobe(t, "exit 1")
		p := filepath.Join(t.TempDir(), "notes.txt")
		if err := os.WriteFile(p, []byte("not audio"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := Extract(p)
		if err == nil && (info == nil || info.DurationErr == nil) {
			t.Fatalf("an unsupported file must report an error: %+v", info)
		}
	})
}
