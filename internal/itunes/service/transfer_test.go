// file: internal/itunes/service/transfer_test.go
// version: 2.2.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-08-30

package itunesservice

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestBackupITLFile verifies that backupITLFile creates a .bak-* copy.
func TestBackupITLFile(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "iTunes Library.itl")

	// Write a fake ITL file.
	if err := os.WriteFile(itlPath, []byte("fake-itl-data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := backupITLFile(itlPath); err != nil {
		t.Fatalf("backupITLFile: %v", err)
	}

	// Should have created exactly one .bak-* file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var bakFiles []string
	for _, e := range entries {
		if e.Name() != filepath.Base(itlPath) {
			bakFiles = append(bakFiles, e.Name())
		}
	}
	if len(bakFiles) != 1 {
		t.Fatalf("expected 1 backup file, got %d: %v", len(bakFiles), bakFiles)
	}

	// Backup should contain the same data.
	data, err := os.ReadFile(filepath.Join(dir, bakFiles[0]))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(data) != "fake-itl-data" {
		t.Errorf("backup content = %q, want %q", data, "fake-itl-data")
	}
}

// TestBackupITLFile_NoExistingFile verifies noop when file doesn't exist.
func TestBackupITLFile_NoExistingFile(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "nonexistent.itl")

	if err := backupITLFile(itlPath); err != nil {
		t.Fatalf("backupITLFile should be noop for missing file: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty dir, got %d entries", len(entries))
	}
}

// TestCopyFile verifies atomic copy semantics.
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.itl")
	dst := filepath.Join(dir, "dst.itl")

	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}

	// Source should still exist.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source should still exist: %v", err)
	}
}

// TestITLBackupEntry_Sort verifies newest-first sort order.
func TestITLBackupEntry_Sort(t *testing.T) {
	now := time.Now()
	entries := []ITLBackupEntry{
		{Name: "old.bak", Timestamp: now.Add(-time.Hour)},
		{Name: "newest.bak", Timestamp: now},
		{Name: "mid.bak", Timestamp: now.Add(-30 * time.Minute)},
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	if entries[0].Name != "newest.bak" {
		t.Errorf("first entry = %q, want %q", entries[0].Name, "newest.bak")
	}
	if entries[2].Name != "old.bak" {
		t.Errorf("last entry = %q, want %q", entries[2].Name, "old.bak")
	}
}

// ---------------------------------------------------------------------------
// copyFile — error paths
// ---------------------------------------------------------------------------

// TestCopyFile_SourceMissing verifies copyFile returns an error when the
// source does not exist.
func TestCopyFile_SourceMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nonexistent.itl")
	dst := filepath.Join(dir, "dst.itl")

	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist error, got: %v", err)
	}
}

// TestCopyFile_DestDirMissing verifies copyFile returns an error when the
// destination directory does not exist (temp-file creation fails).
func TestCopyFile_DestDirMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.itl")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Destination in a non-existent sub-directory
	dst := filepath.Join(dir, "nonexistent-subdir", "dst.itl")
	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("expected error for missing destination directory, got nil")
	}
}

// TestCopyFile_OverwritesExisting verifies that copyFile replaces an existing
// destination file atomically.
func TestCopyFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.itl")
	dst := filepath.Join(dir, "dst.itl")

	if err := os.WriteFile(src, []byte("new-content"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	// Pre-create destination with old content.
	if err := os.WriteFile(dst, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "new-content" {
		t.Errorf("dst content = %q, want %q", got, "new-content")
	}
}

// ---------------------------------------------------------------------------
// backupITLFile — timestamped filename
// ---------------------------------------------------------------------------

// TestBackupITLFile_TimestampFormat verifies that the backup file name
// contains the expected RFC-style timestamp suffix (yyyymmddThhmmssZ).
func TestBackupITLFile_TimestampFormat(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "iTunes Library.itl")
	if err := os.WriteFile(itlPath, []byte("itl"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	before := time.Now().UTC()
	if err := backupITLFile(itlPath); err != nil {
		t.Fatalf("backupITLFile: %v", err)
	}
	after := time.Now().UTC()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var bak string
	for _, e := range entries {
		if e.Name() != filepath.Base(itlPath) {
			bak = e.Name()
		}
	}
	if bak == "" {
		t.Fatal("no backup file found")
	}

	// The suffix should be like ".bak-20260101T000000Z"
	if !strings.Contains(bak, ".bak-") {
		t.Errorf("backup name %q does not contain '.bak-'", bak)
	}
	// Parse the timestamp from the suffix.
	suffix := strings.TrimPrefix(bak, filepath.Base(itlPath)+".bak-")
	ts, err := time.Parse("20060102T150405Z", suffix)
	if err != nil {
		t.Errorf("backup timestamp %q did not parse: %v", suffix, err)
		return
	}
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("backup timestamp %v outside expected range [%v, %v]", ts, before, after)
	}
}

// TestBackupITLFile_MultipleBackups verifies that calling backupITLFile twice
// creates two distinct backup files (different timestamps at second resolution).
// Runs inside a testing/synctest bubble: backupITLFile derives its suffix from
// time.Now(), so advancing the bubble's FAKE clock by 1.1s is enough to produce
// a distinct second-resolution name at zero wall-clock cost (~1.1s -> ~0s).
//
// SEMANTIC NOTE: the bubble clock starts at midnight UTC 2000-01-01, so the two
// backups here are named .bak-20000101T000000Z and .bak-20000101T000001Z rather
// than with today's date. This test asserts only on the COUNT of backup files,
// never on their timestamp values, so the fake clock changes nothing it checks.
// The sibling TestBackupITLFile does assert a real-time range and is therefore
// deliberately NOT bubbled.
func TestBackupITLFile_MultipleBackups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		itlPath := filepath.Join(dir, "iTunes Library.itl")
		if err := os.WriteFile(itlPath, []byte("itl"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		if err := backupITLFile(itlPath); err != nil {
			t.Fatalf("first backupITLFile: %v", err)
		}
		time.Sleep(1100 * time.Millisecond) // ensure distinct second-level timestamp
		if err := backupITLFile(itlPath); err != nil {
			t.Fatalf("second backupITLFile: %v", err)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		var bakCount int
		for _, e := range entries {
			if e.Name() != filepath.Base(itlPath) {
				bakCount++
			}
		}
		if bakCount != 2 {
			t.Errorf("expected 2 backup files, got %d", bakCount)
		}
	})
}

// ---------------------------------------------------------------------------
// newTransferService
// ---------------------------------------------------------------------------

// TestNewTransferService verifies the constructor returns a non-nil value
// and that the returned service can be used to build a router without panic.
func TestNewTransferService_NonNil(t *testing.T) {
	ts := newTransferService()
	if ts == nil {
		t.Fatal("newTransferService returned nil")
	}
}
