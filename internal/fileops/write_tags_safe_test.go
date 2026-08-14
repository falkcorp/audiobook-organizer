// file: internal/fileops/write_tags_safe_test.go
// version: 1.1.0
// guid: c5d6e7f8-a9b0-1c2d-3e4f-5a6b7c8d9e0f
// last-edited: 2026-08-14

package fileops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noopStore satisfies database.BookFileHashUpdater without any real DB.
type noopStore struct {
	called       bool
	lastOriginal string
	lastPost     string
	returnErr    error
}

func (s *noopStore) UpdateBookFileHashes(fileID, originalHash, postHash string) error {
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
