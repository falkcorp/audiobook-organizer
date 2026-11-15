// file: core/health/diagnostics/redesign_binary_test.go
// version: 1.0.0
// guid: 8d3f5b72-6a14-4c90-b7e2-1f9a4d6c0e53
// last-edited: 2026-11-15

package diagnostics

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// goTool is the go command that built this test binary's toolchain: the one on
// PATH, else GOROOT's.
func goTool(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("no go command on PATH or in GOROOT: %v", err)
	}
	return p
}

// moduleRoot is the directory with go.mod above the test's working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// TestRedesignRefusal_RealBinary (round 10, N-3) builds a small main that
// imports this package and runs it as a production binary (testing.Testing() is
// false in it), then checks that diagnostics.New and the zero Exporter's Build
// both return ErrRedesignPendingF8_03b. Every other test of the refusal passes
// the probe in, so a probe that always said "test binary" (New passing
// func() bool { return true }, or testBinary's fallback returning true)
// survived them all; both die here, because the binary reaches the probe the way
// a production caller does. Not skipped under -short: this is the one test that
// proves the refusal works in the process it exists for.
func TestRedesignRefusal_RealBinary(t *testing.T) {
	if !RedesignPendingF8_03b {
		t.Fatal("the marker is false; F8-03b deletes this test with the marker")
	}
	goBin, root := goTool(t), moduleRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "probemain")
	build := exec.CommandContext(ctx, goBin, "build", "-o", bin, "./health/diagnostics/testdata/probemain")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build of the probe binary: %v\n%s", err, out)
	}
	out, err := exec.CommandContext(ctx, bin).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "refused" {
		t.Fatalf("the production binary did not refuse: err = %v\n%s", err, out)
	}
}
