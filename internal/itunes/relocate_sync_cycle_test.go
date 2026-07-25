// file: internal/itunes/relocate_sync_cycle_test.go
// version: 1.0.0
// guid: 8d3f1c92-6b74-4d50-9e18-2c7b5a0e1d63
// last-edited: 2026-07-24

package itunes

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireSyncWriteLock_SingleFlight(t *testing.T) {
	itl := filepath.Join(t.TempDir(), "iTunes Library.itl")
	release, err := acquireSyncWriteLock(itl)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// A second acquire while held must fail (single-flight).
	if _, err2 := acquireSyncWriteLock(itl); err2 == nil {
		t.Error("second acquire should fail while the lock is held")
	}
	// After release, the lock file is gone and a re-acquire succeeds.
	release()
	if _, err := os.Stat(itl + ".ao-writeback.lock"); !os.IsNotExist(err) {
		t.Error("lock file should be removed on release")
	}
	release2, err := acquireSyncWriteLock(itl)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release2()
}

// TestFileActivityLibraryCheck_ReadVsWrite confirms the quiescence gate: a recent
// WRITE (mtime bump) blocks; an aged file (outside the window) does not.
func TestFileActivityLibraryCheck_ReadVsWrite(t *testing.T) {
	dir := t.TempDir()
	itl := filepath.Join(dir, "iTunes Library.itl")
	if err := os.WriteFile(itl, []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Just written → within window → blocked.
	if err := FileActivityLibraryCheck(itl, 2*time.Minute)(); err == nil {
		t.Error("a freshly-written .itl should be reported in use")
	}
	// Age it past the window → allowed (a stale/uncleaned file ages out).
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(itl, old, old); err != nil {
		t.Fatal(err)
	}
	if err := FileActivityLibraryCheck(itl, 2*time.Minute)(); err != nil {
		t.Errorf("an aged .itl should not block: %v", err)
	}
	// A recent sentinel sibling also blocks (iTunes' open marker).
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("PIDPIDPI"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FileActivityLibraryCheck(itl, 2*time.Minute)(); err == nil {
		t.Error("a recent sentinel should block the write")
	}
}

func TestRelocatedPIDSet(t *testing.T) {
	ops := ITLOperationSet{LocationUpdates: []ITLLocationUpdate{
		{PersistentID: "AABBCCDD00000001", NewLocation: `W:\a.mp3`},
		{PersistentID: "eeff000000000002", NewLocation: `W:\b.mp3`},
	}}
	got := relocatedPIDSet(ops)
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	// Keys must be lower-hex (matches extractMithPIDLE / the oracle).
	if !got["aabbccdd00000001"] || !got["eeff000000000002"] {
		t.Errorf("expected lowercased keys, got %v", got)
	}
}

func TestRestoreITLBackup(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "iTunes Library.itl")
	bak := filepath.Join(dir, "iTunes Library.itl.bak-x")

	if err := os.WriteFile(bak, []byte("GOOD-BACKUP"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("CORRUPT-LIVE"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := restoreITLBackup(bak, live); err != nil {
		t.Fatalf("restoreITLBackup: %v", err)
	}
	got, _ := os.ReadFile(live)
	if string(got) != "GOOD-BACKUP" {
		t.Errorf("live = %q, want restored backup", got)
	}
	// Missing backup → error, live untouched.
	if err := restoreITLBackup(filepath.Join(dir, "nope"), live); err == nil {
		t.Error("expected error restoring a missing backup")
	}
}
