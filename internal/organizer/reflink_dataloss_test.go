// file: internal/organizer/reflink_dataloss_test.go
// version: 1.1.0
// guid: 5c6177ff-1c47-4386-ab1e-8bef9b9faf05
// last-edited: 2026-09-01

//go:build darwin || linux

package organizer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReflinkRefusesExistingDestination proves DL-3: the organize path must
// NOT truncate an existing destination (os.Create did — a stat→create TOCTOU
// under the concurrent organize pool could zero a file another worker just
// wrote). With O_EXCL the open fails with an exists-error that callers'
// os.IsExist recovery understands, matching the os.Link fallback.
//
// The clone itself now lives in fileops, but this test deliberately stays
// here and calls through o.reflinkFile: it pins the property at the seam the
// organize pool actually uses, so a future delegation change that loses the
// un-wrapped exists-error (and with it the pool's race recovery) fails here
// rather than only in the fileops unit tests.
func TestReflinkRefusesExistingDestination(t *testing.T) {
	o := &Organizer{}
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	if err := os.WriteFile(src, []byte("source bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing book"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := o.reflinkFile(src, dst)
	if err == nil {
		t.Fatal("expected error for existing destination, got nil")
	}
	if !os.IsExist(err) {
		t.Errorf("expected os.IsExist(err) so the organize pool's race recovery fires, got: %v", err)
	}
	// The destination must NOT have been truncated.
	data, rerr := os.ReadFile(dst)
	if rerr != nil {
		t.Fatalf("read dst: %v", rerr)
	}
	if string(data) != "existing book" {
		t.Errorf("destination truncated/overwritten: %q", data)
	}
}
