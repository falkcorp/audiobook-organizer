// file: internal/itunes/relocate_sync_cycle_test.go
// version: 1.0.0
// guid: 8d3f1c92-6b74-4d50-9e18-2c7b5a0e1d63
// last-edited: 2026-07-24

package itunes

import (
	"os"
	"path/filepath"
	"testing"
)

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
