// file: internal/fileops/write_tags_safe_test.go
// version: 1.3.0
// guid: c5d6e7f8-a9b0-1c2d-3e4f-5a6b7c8d9e0f
// last-edited: 2026-10-10

package fileops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// noopStore satisfies database.BookFileHashUpdater without any real DB.
type noopStore struct {
	called       bool
	lastOriginal string
	lastPost     string
	returnErr    error
}

func (s *noopStore) UpdateBookFileHashes(fileID, originalHash, postHash, fileHash string) error {
	s.called = true
	s.lastOriginal = originalHash
	s.lastPost = postHash
	return s.returnErr
}

// writeBytes is a writeFn that appends extra bytes to the file so the hash changes.
func writeBytes(extra []byte) func(tmpPath string) error {
	return func(tmpPath string) error {
		f, err := os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(extra)
		return err
	}
}

func TestWriteTagsSafe_HashChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	original := []byte("original audio bytes")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	store := &noopStore{}
	origHash, postHash, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")),
		WriteTagsSafeOptions{BookFileID: "file-1", Store: store})
	if err != nil {
		t.Fatalf("WriteTagsSafe returned error: %v", err)
	}
	if origHash == "" {
		t.Error("original hash must not be empty")
	}
	if postHash == "" {
		t.Error("post hash must not be empty")
	}
	if origHash == postHash {
		t.Errorf("original and post hashes must differ after write; both are %s", origHash)
	}
	if !store.called {
		t.Error("expected Store.UpdateBookFileHashes to be called")
	}
	if store.lastOriginal != origHash {
		t.Errorf("store received wrong original hash: got %s, want %s", store.lastOriginal, origHash)
	}
	if store.lastPost != postHash {
		t.Errorf("store received wrong post hash: got %s, want %s", store.lastPost, postHash)
	}
}

func TestWriteTagsSafe_NoTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.mp3")
	if err := os.WriteFile(path, []byte("mp3 data"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, err := WriteTagsSafe(path, writeBytes([]byte(" extra")), WriteTagsSafeOptions{})
	if err != nil {
		t.Fatalf("WriteTagsSafe returned error: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".writetmp-") {
			t.Errorf("temp file %s was not cleaned up after successful write", e.Name())
		}
	}
}

func TestWriteTagsSafe_OriginalPreservedOnWriteFnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.flac")
	original := []byte("flac audio content")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	failFn := func(tmpPath string) error {
		return errors.New("simulated tag write failure")
	}

	_, _, err := WriteTagsSafe(path, failFn, WriteTagsSafeOptions{})
	if err == nil {
		t.Fatal("expected an error when writeFn fails")
	}

	// Original file must be intact.
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("original file unreadable after writeFn error: %v", readErr)
	}
	if string(got) != string(original) {
		t.Errorf("original file was modified on writeFn error:\ngot  %q\nwant %q", got, original)
	}

	// Temp file must be gone.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".writetmp-") {
			t.Errorf("temp file %s was not cleaned up after writeFn error", e.Name())
		}
	}
}

func TestWriteTagsSafe_NoStoreCallWhenBookFileIDEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.ogg")
	if err := os.WriteFile(path, []byte("ogg data"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &noopStore{}
	_, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")),
		WriteTagsSafeOptions{BookFileID: "", Store: store})
	if err != nil {
		t.Fatalf("WriteTagsSafe returned error: %v", err)
	}
	if store.called {
		t.Error("Store.UpdateBookFileHashes must not be called when BookFileID is empty")
	}
}

func TestWriteTagsSafe_NoStoreCallWhenStoreNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4a")
	if err := os.WriteFile(path, []byte("m4a data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should succeed without panicking even with nil Store.
	_, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")),
		WriteTagsSafeOptions{BookFileID: "file-2", Store: nil})
	if err != nil {
		t.Fatalf("WriteTagsSafe returned error: %v", err)
	}
}

func TestWriteTagsSafe_NonExistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.m4b")

	_, _, err := WriteTagsSafe(path, writeBytes(nil), WriteTagsSafeOptions{})
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

// TestWriteTagsSafe_PreservesFileMode pins the permission contract: the
// rewritten file must carry the ORIGINAL's mode, not os.CreateTemp's 0600.
// The 0600 leak zeroes the POSIX-ACL mask and locked 100 books' files out of
// the share on 2026-08-14 (E08 canary) — OpenFile's mode argument applies
// only at creation, so an existing temp file needs an explicit chmod.
func TestWriteTagsSafe_PreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	if err := os.WriteFile(path, []byte("original audio bytes"), 0o664); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile honors umask; force the exact mode under test.
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}

	if _, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")),
		WriteTagsSafeOptions{}); err != nil {
		t.Fatalf("WriteTagsSafe: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o664 {
		t.Fatalf("rewritten file mode = %o, want 664 (mode must survive the temp-file rename)", got)
	}
}

// backupSiblings lists the .bak-* siblings of path.
func backupSiblings(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestWriteTagsSafe_KeepBackupLeavesPreWriteBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	original := []byte("synthetic pre-write audio bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// Store+BookFileID make WriteTagsSafe compute originalHash, which the
	// backup's digest is checked against.
	origHash, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")),
		WriteTagsSafeOptions{BookFileID: "file-1", Store: &noopStore{}, KeepBackup: true})
	if err != nil {
		t.Fatalf("WriteTagsSafe: %v", err)
	}

	baks := backupSiblings(t, path)
	if len(baks) != 1 {
		t.Fatalf("want exactly one .bak-* sibling, got %v", baks)
	}
	got, err := os.ReadFile(baks[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("backup bytes = %q, want the pre-write bytes %q", got, original)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != origHash {
		t.Errorf("backup sha256 = %x, want originalHash %s", sum, origHash)
	}

	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte{}, original...), []byte(" tagged")...); !bytes.Equal(final, want) {
		t.Errorf("final file = %q, want the tagged bytes %q", final, want)
	}
}

func TestWriteTagsSafe_NoBackupWhenKeepBackupOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.mp3")
	if err := os.WriteFile(path, []byte("synthetic bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")), WriteTagsSafeOptions{}); err != nil {
		t.Fatalf("WriteTagsSafe: %v", err)
	}
	if baks := backupSiblings(t, path); len(baks) != 0 {
		t.Errorf("want no .bak-* sibling with KeepBackup off, got %v", baks)
	}
}

func TestWriteTagsSafe_NoBackupWhenWriteFnFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.flac")
	if err := os.WriteFile(path, []byte("synthetic bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := WriteTagsSafe(path, func(string) error { return errors.New("tag write failed") },
		WriteTagsSafeOptions{KeepBackup: true})
	if err == nil {
		t.Fatal("want the writeFn error")
	}
	if baks := backupSiblings(t, path); len(baks) != 0 {
		t.Errorf("a failed write must leave no backup, got %v", baks)
	}
}

func TestKeepBackup_SameSecondDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	first, _, err := keepBackup(path, now)
	if err != nil {
		t.Fatal(err)
	}
	// Replace path with a new inode, as WriteTagsSafe's rename does.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, _, err := keepBackup(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("second backup reused the name %s", first)
	}
	if got, _ := os.ReadFile(first); string(got) != "first" {
		t.Errorf("first backup was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(second); string(got) != "second" {
		t.Errorf("second backup = %q, want %q", got, "second")
	}
}

// The backup is a hardlink to the pre-write inode (no data blocks copied),
// and after the rename its mtime is the backup time. The cleanup sweeps age
// .bak-* files by mtime, so a link that kept the audio's old mtime would be
// deleted on the next sweep (KeepBackup, MTIME CONTRACT).
func TestWriteTagsSafe_KeepBackupIsHardlinkDatedNow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	if err := os.WriteFile(path, []byte("synthetic bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")), WriteTagsSafeOptions{KeepBackup: true}); err != nil {
		t.Fatalf("WriteTagsSafe: %v", err)
	}
	baks := backupSiblings(t, path)
	if len(baks) != 1 {
		t.Fatalf("want one backup, got %v", baks)
	}
	bak, err := os.Stat(baks[0])
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, bak) {
		t.Error("backup is not the pre-write inode; want a hardlink, not a copy")
	}
	if bak.ModTime().Before(start.Add(-2 * time.Second)) {
		t.Errorf("backup mtime = %v, want about now (>= %v): the sweep would age it from the audio's old mtime", bak.ModTime(), start)
	}
	now, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(now, bak) {
		t.Error("the live file still shares the backup's inode after the write")
	}
}

// A failed rename leaves the original as it was and removes the backup: the
// original is intact, so the backup would only be clutter.
func TestWriteTagsSafe_RenameFailureRemovesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	original := []byte("synthetic bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	prev := renameFile
	renameFile = func(string, string) error { return errors.New("synthetic rename failure") }
	t.Cleanup(func() { renameFile = prev })

	if _, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")), WriteTagsSafeOptions{KeepBackup: true}); err == nil {
		t.Fatal("want the rename error")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, original) {
		t.Errorf("original changed after a failed rename: %q", got)
	}
	if baks := backupSiblings(t, path); len(baks) != 0 {
		t.Errorf("a failed rename must remove the backup, got %v", baks)
	}
}

// Where the filesystem refuses a hardlink (EXDEV here), the backup is a full
// copy of the pre-write bytes with its own inode and a current mtime.
func TestWriteTagsSafe_KeepBackupCopyFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4b")
	original := []byte("synthetic bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	prev := linkFile
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { linkFile = prev })

	start := time.Now()
	if _, _, err := WriteTagsSafe(path, writeBytes([]byte(" tagged")), WriteTagsSafeOptions{KeepBackup: true}); err != nil {
		t.Fatalf("WriteTagsSafe: %v", err)
	}
	baks := backupSiblings(t, path)
	if len(baks) != 1 {
		t.Fatalf("want one backup, got %v", baks)
	}
	bak, err := os.Stat(baks[0])
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, bak) {
		t.Error("fallback backup shares the original inode; want a copy")
	}
	if got, _ := os.ReadFile(baks[0]); !bytes.Equal(got, original) {
		t.Errorf("fallback backup = %q, want the pre-write bytes", got)
	}
	if bak.ModTime().Before(start.Add(-2 * time.Second)) {
		t.Errorf("fallback backup mtime = %v, want about now", bak.ModTime())
	}
}
