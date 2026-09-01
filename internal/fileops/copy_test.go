// file: internal/fileops/copy_test.go
// version: 1.1.0
// guid: 7d4b0c19-5e83-4a26-9f10-2b6ce8417d3a
// last-edited: 2026-09-01

package fileops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests replace four that lived in internal/itunes/service/transfer_test.go
// against that package's private copyFile. Their subject moved here, and they
// gained the assertions they were missing: mode preservation (the axis on which
// the six former implementations disagreed most damagingly) and partial-file
// cleanup.

func writeFileMode(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile's mode is masked by umask; force the exact bits so the
	// preservation assertions below are testing this package, not the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestCopyFilePreservesSourceMode is the regression guard for the defect that
// motivated this package: itunes/service/transfer.go copied through
// os.CreateTemp (0600) and renamed, so every ITL backup it wrote was
// owner-only; writeback_batcher hardcoded 0644 regardless of the library's own
// mode. Both are silent — the copy succeeds and the file is simply wrong.
func TestCopyFilePreservesSourceMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, src, "payload", 0o640)

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if got := modeOf(t, dst); got != 0o640 {
		t.Errorf("dst mode = %v, want %v (source's mode, not a literal or the umask default)", got, fs.FileMode(0o640))
	}
	if got := readAll(t, dst); got != "payload" {
		t.Errorf("dst content = %q, want %q", got, "payload")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source must survive a copy: %v", err)
	}
}

// TestCopyFileTruncatesExistingDestination proves O_TRUNC. Without it a copy
// over a LONGER existing file leaves that file's tail in place, producing a
// destination that is neither the old file nor the new one — and every one of
// the replaced implementations wrote destinations that could already exist.
func TestCopyFileTruncatesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, src, "short", 0o644)
	writeFileMode(t, dst, "a much longer previous body", 0o644)

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if got := readAll(t, dst); got != "short" {
		t.Errorf("dst = %q, want exactly %q — a longer previous body must not survive as a tail", got, "short")
	}
}

// TestCopyFileRemovesPartialDestinationOnCopyFailure uses a directory as the
// source: os.Open and Stat both succeed on a directory, and the read inside
// io.Copy is what fails, so the failure lands AFTER dst has been created. That
// is the only window in which a partial destination can exist.
func TestCopyFileRemovesPartialDestinationOnCopyFailure(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "srcdir")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dst := filepath.Join(dir, "dst.bin")

	if err := CopyFile(srcDir, dst); err == nil {
		t.Fatal("copying a directory must fail")
	}
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed CopyFile must not leave the destination behind; stat err = %v", err)
	}
}

func TestCopyFileSourceMissing(t *testing.T) {
	dir := t.TempDir()
	err := CopyFile(filepath.Join(dir, "nope.bin"), filepath.Join(dir, "dst.bin"))
	if err == nil {
		t.Fatal("expected an error for a missing source")
	}
	// errors.Is rather than os.IsNotExist: the error is wrapped so it carries
	// the path and the operation, and os.IsNotExist does not unwrap.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want one satisfying errors.Is(err, fs.ErrNotExist)", err)
	}
	if !strings.Contains(err.Error(), "cannot read source file") {
		t.Errorf("error = %v, want it to name the source as the failing side", err)
	}
}

func TestCopyFileDestinationDirMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	writeFileMode(t, src, "x", 0o644)

	err := CopyFile(src, filepath.Join(dir, "absent", "dst.bin"))
	if err == nil {
		t.Fatal("expected an error for a missing destination directory")
	}
	if !strings.Contains(err.Error(), "cannot create destination file") {
		t.Errorf("error = %v, want it to name the destination as the failing side", err)
	}
}

// TestCopyFileIntoRequiresExistingDestination pins the one thing that
// distinguishes CopyFileInto from CopyFile: no O_CREATE. A caller reaches for
// it precisely because it already owns dst.
func TestCopyFileIntoRequiresExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, src, "payload", 0o644)

	if err := CopyFileInto(src, dst); err == nil {
		t.Fatal("CopyFileInto must not create a destination that does not exist")
	}
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CopyFileInto created dst; stat err = %v", err)
	}
}

// TestCopyFileIntoAppliesSourceMode is the E08 canary in test form: dst is an
// os.CreateTemp-shaped 0600 file, and after the copy it must carry the
// source's mode. Skipping the chmod here is exactly how 100 books' files went
// share-unreadable on 2026-08-14 — the copy succeeds, the content is right,
// and only a non-owner reader ever finds out.
func TestCopyFileIntoAppliesSourceMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	writeFileMode(t, src, "payload", 0o664)

	tmp, err := os.CreateTemp(dir, "dst-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	dst := tmp.Name()
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	if got := modeOf(t, dst); got != 0o600 {
		t.Fatalf("precondition: CreateTemp mode = %v, want 0600", got)
	}

	if err := CopyFileInto(src, dst); err != nil {
		t.Fatalf("CopyFileInto: %v", err)
	}
	if got := modeOf(t, dst); got != 0o664 {
		t.Errorf("dst mode = %v, want %v (the source's); an 0600 destination locks out every non-owner reader", got, fs.FileMode(0o664))
	}
}

// TestCopyFileIntoKeepsDestinationIdentity proves CopyFileInto writes in place
// rather than replacing dst: a second hardlink to dst must observe the new
// content. A rename-based implementation would leave the other link on the old
// inode with the old bytes.
func TestCopyFileIntoKeepsDestinationIdentity(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	other := filepath.Join(dir, "other.bin")
	writeFileMode(t, src, "new bytes", 0o644)
	writeFileMode(t, dst, "old bytes", 0o644)
	if err := os.Link(dst, other); err != nil {
		t.Fatalf("link: %v", err)
	}

	if err := CopyFileInto(src, dst); err != nil {
		t.Fatalf("CopyFileInto: %v", err)
	}
	if got := readAll(t, other); got != "new bytes" {
		t.Errorf("hardlink sees %q, want %q — CopyFileInto must not replace dst's inode", got, "new bytes")
	}
}

// TestCopyFileIntoDoesNotRemoveDestinationOnFailure: the caller created dst and
// owns its lifetime, so a failed copy must leave it for the caller to clean up
// (CopyFileAtomic relies on this to remove its own temp file).
func TestCopyFileIntoDoesNotRemoveDestinationOnFailure(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "srcdir")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, dst, "caller's file", 0o644)

	if err := CopyFileInto(srcDir, dst); err == nil {
		t.Fatal("copying a directory must fail")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("CopyFileInto removed a destination it did not create: %v", err)
	}
}

func TestCopyFileAtomicReplacesExistingAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, src, "restored body", 0o640)
	writeFileMode(t, dst, "a much longer corrupted body", 0o600)

	if err := CopyFileAtomic(src, dst); err != nil {
		t.Fatalf("CopyFileAtomic: %v", err)
	}
	if got := readAll(t, dst); got != "restored body" {
		t.Errorf("dst = %q, want %q", got, "restored body")
	}
	if got := modeOf(t, dst); got != 0o640 {
		t.Errorf("dst mode = %v, want %v — the temp file's 0600 must not survive the rename", got, fs.FileMode(0o640))
	}
}

// TestCopyFileAtomicLeavesNoTempBehind covers both outcomes, because a temp
// file left in the ITL directory is not inert: the retention sweeps there match
// on name, and an orphan is a file an operator has to reason about.
func TestCopyFileAtomicLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, src, "body", 0o644)

	if err := CopyFileAtomic(src, dst); err != nil {
		t.Fatalf("CopyFileAtomic: %v", err)
	}
	assertNoTemps(t, dir)

	srcDir := filepath.Join(dir, "srcdir")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := CopyFileAtomic(srcDir, dst); err == nil {
		t.Fatal("copying a directory must fail")
	}
	assertNoTemps(t, dir)

	// The failed call must not have damaged the destination it was replacing.
	if got := readAll(t, dst); got != "body" {
		t.Errorf("dst = %q after a failed atomic copy, want the previous %q untouched", got, "body")
	}
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// --- fsync wiring -----------------------------------------------------------
//
// M7 in this PR's mutation run: deleting the fsync from copyBytes left all
// eight other copy tests green. The whole premise of this package is that two
// production backup writers silently lacked that call, so an untestable
// guarantee is not acceptable here. These tests assert the call is wired up and
// that its failure is not swallowed — not that the bytes reached the platter,
// which no unit test can show.

func swapSyncFile(t *testing.T, fn func(*os.File) error) {
	t.Helper()
	prev := syncFile
	syncFile = fn
	t.Cleanup(func() { syncFile = prev })
}

func TestCopyPathsFsyncBeforeClosing(t *testing.T) {
	cases := []struct {
		name string
		call func(t *testing.T, dir, src string) error
	}{
		{"CopyFile", func(t *testing.T, dir, src string) error {
			return CopyFile(src, filepath.Join(dir, "dst.bin"))
		}},
		{"CopyFileExclusive", func(t *testing.T, dir, src string) error {
			return CopyFileExclusive(src, filepath.Join(dir, "dst.bin"))
		}},
		{"CopyFileInto", func(t *testing.T, dir, src string) error {
			dst := filepath.Join(dir, "dst.bin")
			writeFileMode(t, dst, "old", 0o644)
			return CopyFileInto(src, dst)
		}},
		{"CopyFileAtomic", func(t *testing.T, dir, src string) error {
			return CopyFileAtomic(src, filepath.Join(dir, "dst.bin"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			swapSyncFile(t, func(f *os.File) error { calls++; return f.Sync() })

			dir := t.TempDir()
			src := filepath.Join(dir, "src.bin")
			writeFileMode(t, src, "payload", 0o644)
			if err := tc.call(t, dir, src); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if calls != 1 {
				t.Errorf("%s fsynced %d time(s), want exactly 1 — a copy that never reaches the disk is not a backup", tc.name, calls)
			}
		})
	}
}

// TestCopyFileFailsAndCleansUpWhenFsyncFails: a sync error means the bytes are
// not durable, so the call must not report success, and the destination this
// package created must not be left behind looking complete.
func TestCopyFileFailsAndCleansUpWhenFsyncFails(t *testing.T) {
	swapSyncFile(t, func(*os.File) error { return errors.New("simulated fsync failure") })

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFileMode(t, src, "payload", 0o644)

	err := CopyFile(src, dst)
	if err == nil {
		t.Fatal("CopyFile must fail when the fsync fails")
	}
	if !strings.Contains(err.Error(), "sync destination") {
		t.Errorf("error = %v, want it to name the sync as the failing step", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a non-durable destination was left behind; stat err = %v", statErr)
	}
}
