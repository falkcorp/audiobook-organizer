// file: internal/audioutil/duration_errors_test.go
// version: 1.0.0
// guid: 8cfc53ba-ccca-4bb2-b36f-3dc11cecc94b
// last-edited: 2026-11-04

package audioutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A missing ffprobe binary must stay recognisable through the wrapping, because
// the duration writers classify it as "could not run" (no attempt counted)
// rather than "the file is bad".
func TestProbeDurationSeconds_MissingBinaryIsExecErrNotFound(t *testing.T) {
	t.Setenv(disableMediaInfoEnv, "1")
	t.Setenv("PATH", t.TempDir()) // empty directory: no ffprobe
	_, err := ProbeDurationSeconds(context.Background(), "", "/lib/Year One/01.m4b")
	if err == nil {
		t.Fatal("expected an error with no ffprobe on PATH")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("errors.Is(err, exec.ErrNotFound) = false for %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a missing binary must not read as a timeout: %v", err)
	}
}

// A probe killed by its context must say so, not just "signal: killed".
func TestProbeDurationSeconds_TimeoutWrapsDeadlineExceeded(t *testing.T) {
	t.Setenv(disableMediaInfoEnv, "1")
	dir := t.TempDir()
	slow := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := ProbeDurationSeconds(ctx, slow, "/lib/Year One/01.m4b")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false for %v", err)
	}
}

// A real non-zero exit is neither a timeout nor a missing binary.
func TestProbeDurationSeconds_ExitErrorIsPlain(t *testing.T) {
	t.Setenv(disableMediaInfoEnv, "1")
	dir := t.TempDir()
	bad := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ProbeDurationSeconds(context.Background(), bad, "/lib/Year One/01.m4b")
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want an *exec.ExitError, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("an exit error was misclassified: %v", err)
	}
}
