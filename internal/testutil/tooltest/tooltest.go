// file: internal/testutil/tooltest/tooltest.go
// version: 0.1.0
// guid: 8e3b1f27-4c60-4a95-b7d1-92f0a5c6e413
// last-edited: 2026-11-21

// Package tooltest gates tests on external media tools (ffmpeg, ffprobe,
// fpcalc). exec.LookPath only proves a file with that name is on PATH. A
// dynamically linked build whose shared libraries are missing is found, then
// exits 127 the first time a test runs it, so a LookPath-only guard turns an
// unusable tool into a red test instead of a skip. Require probes the binary
// with -version, which loads it and prints a banner but decodes nothing.
package tooltest

import (
	"os/exec"
	"sync"
	"testing"
)

var (
	mu    sync.Mutex
	probe = map[string]error{} // resolved path -> result of the -version run
)

// Require returns the path of the named tool, or skips the test when it is
// not on PATH or does not start.
func Require(t testing.TB, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not on PATH", name)
	}
	mu.Lock()
	perr, seen := probe[path]
	mu.Unlock()
	if !seen {
		out, rerr := exec.Command(path, "-version").CombinedOutput()
		if rerr != nil {
			perr = &startError{err: rerr, out: string(out)}
		}
		mu.Lock()
		probe[path] = perr
		mu.Unlock()
	}
	if perr != nil {
		t.Skipf("%s at %s does not start: %v", name, path, perr)
	}
	return path
}

// RequireAll skips the test unless every named tool is usable.
func RequireAll(t testing.TB, names ...string) {
	t.Helper()
	for _, n := range names {
		Require(t, n)
	}
}

type startError struct {
	err error
	out string
}

func (e *startError) Error() string { return e.err.Error() + ": " + e.out }
