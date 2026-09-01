// file: internal/fileops/reflink_test.go
// version: 1.1.0
// guid: f3c4cfb5-9506-4f5a-b176-95cccb39a4e1
// last-edited: 2026-09-01

package fileops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes content and fails the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReflinkOrCopyProducesIdenticalContent covers the outcome every caller
// depends on, on whichever path the host filesystem takes: clone if it can,
// byte copy if it cannot. Both must yield the same bytes.
func TestReflinkOrCopyProducesIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "the quick brown fox")

	if err := ReflinkOrCopy(src, dst); err != nil {
		t.Fatalf("ReflinkOrCopy: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "the quick brown fox" {
		t.Errorf("content = %q, want %q", got, "the quick brown fox")
	}
}

// TestReflinkOrCopyRefusesExistingDestination is the load-bearing safety
// property. Two of the four implementations this package replaced used
// os.Create, which truncates; under a concurrent worker pool that can zero a
// file another worker just finished. An existing destination must be refused
// and left byte-for-byte intact.
func TestReflinkOrCopyRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "new content")
	writeFile(t, dst, "PRECIOUS EXISTING DATA")

	err := ReflinkOrCopy(src, dst)
	if err == nil {
		t.Fatal("ReflinkOrCopy overwrote an existing destination; it must refuse")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error = %v, want one satisfying errors.Is(err, fs.ErrExist)", err)
	}

	survived, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatalf("read dst: %v", readErr)
	}
	if string(survived) != "PRECIOUS EXISTING DATA" {
		t.Errorf("destination was modified: got %q", survived)
	}
}

// TestReflinkRefusesExistingDestination pins the same contract on the clone
// path directly, so a platform implementation that pre-creates the destination
// cannot quietly regain truncating behavior.
func TestReflinkRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "new content")
	writeFile(t, dst, "PRECIOUS EXISTING DATA")

	err := Reflink(src, dst)
	if err == nil {
		t.Fatal("Reflink overwrote an existing destination; it must refuse")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error = %v, want one satisfying errors.Is(err, fs.ErrExist)", err)
	}
	if errors.Is(err, ErrReflinkUnsupported) {
		t.Error("an existing destination was reported as ErrReflinkUnsupported; " +
			"ReflinkOrCopy would then fall back to a copy and clobber it")
	}
}

// TestReflinkOrCopyMissingSourceLeavesNoDestination guards the cleanup path:
// a failed transfer must not strand an empty or partial file where a later
// retry (or a caller's fallback) would trip over it.
func TestReflinkOrCopyMissingSourceLeavesNoDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "does-not-exist.bin")
	dst := filepath.Join(dir, "dst.bin")

	if err := ReflinkOrCopy(src, dst); err == nil {
		t.Fatal("ReflinkOrCopy succeeded with a missing source")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("destination exists after a failed transfer (Lstat err = %v)", err)
	}
}

// TestReflinkClonesWhenSupported asserts the clone actually happened rather
// than silently degrading to a copy.
//
// A bare skip would be worthless here: a regression that disables cloning
// looks exactly like a filesystem that cannot clone. So when Reflink reports
// ErrReflinkUnsupported the test issues the raw platform clone primitive as a
// KNOWN-GOOD TWIN. If the raw clone succeeds where Reflink gave up, that is a
// regression and the test FAILS; only when both agree the filesystem cannot
// clone does it skip.
//
// A clone yields a distinct inode holding identical bytes; a hardlink would
// share the inode, which is the outcome this must never produce because a
// later write to one side would reach the other.
func TestReflinkClonesWhenSupported(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "cloneable payload")

	if err := Reflink(src, dst); err != nil {
		if !errors.Is(err, ErrReflinkUnsupported) {
			t.Fatalf("Reflink: %v", err)
		}
		// Control: can the raw primitive clone here?
		controlSrc := filepath.Join(dir, "control-src.bin")
		controlDst := filepath.Join(dir, "control-dst.bin")
		writeFile(t, controlSrc, "control payload")
		if controlErr := rawClone(controlSrc, controlDst); controlErr == nil {
			t.Fatalf("Reflink reported %v, but a raw clone on the same "+
				"filesystem SUCCEEDED -- cloning regressed to copy-only", err)
		}
		t.Skipf("filesystem under %s cannot clone (confirmed by control): %v", dir, err)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat src: %v", err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if os.SameFile(srcInfo, dstInfo) {
		t.Error("clone shares an inode with the source; a reflink must be an " +
			"independent inode sharing extents, not a hardlink")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "cloneable payload" {
		t.Errorf("clone content = %q, want %q", got, "cloneable payload")
	}

	// Writing through the clone must not reach the source.
	writeFile(t, dst, "mutated")
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("re-read src: %v", err)
	}
	if string(orig) != "cloneable payload" {
		t.Errorf("writing the clone modified the source: src = %q", orig)
	}
}

// TestCopyFileExclusiveRefusesExistingDestination tests the fallback copy
// DIRECTLY.
//
// Going through ReflinkOrCopy cannot reach this: Reflink refuses the existing
// destination first and ReflinkOrCopy returns that error without ever calling
// the copy. Mutation-testing proved it -- swapping this function's O_EXCL for
// a truncating os.Create left every other test in this file passing. The copy
// path is the one that historically truncated (two of the four
// implementations this package replaced used os.Create), so it needs its own
// assertion rather than an inherited one.
func TestCopyFileExclusiveRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "new content")
	writeFile(t, dst, "PRECIOUS EXISTING DATA")

	err := CopyFileExclusive(src, dst)
	if err == nil {
		t.Fatal("copyFileExclusive overwrote an existing destination; it must refuse")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("error = %v, want one satisfying errors.Is(err, fs.ErrExist)", err)
	}

	survived, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatalf("read dst: %v", readErr)
	}
	if string(survived) != "PRECIOUS EXISTING DATA" {
		t.Errorf("destination was modified: got %q", survived)
	}
}

// TestCopyFileExclusiveCopiesContent pins the happy path of the fallback so
// the exclusive-create guard above cannot be "satisfied" by a function that
// refuses everything.
func TestCopyFileExclusiveCopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "payload to copy")

	if err := CopyFileExclusive(src, dst); err != nil {
		t.Fatalf("copyFileExclusive: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "payload to copy" {
		t.Errorf("content = %q, want %q", got, "payload to copy")
	}
}
