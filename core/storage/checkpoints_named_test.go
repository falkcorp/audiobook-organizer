// file: core/storage/checkpoints_named_test.go
// version: 0.1.0
// guid: 5a2f9c61-3e84-4b07-a1d5-c8706e2b94f3
// last-edited: 2026-11-21

package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestCheckpointsNamed: a restore boot finds the checkpoint it restored by
// name, whatever its status (a restore marks it consumed), with the journal
// seq its record carries; a name with no record is not found, with no error.
func TestCheckpointsNamed(t *testing.T) {
	fs := vfs.NewMem()
	db := filepath.Join(string(filepath.Separator), "var", "gotham", "main.pebble")
	cps, err := CheckpointsOf(FS{fs: fs}, db, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	const name = "20261121T000000Z-ba7c4ae1"
	if err := fs.MkdirAll(filepath.Join(cps.Dir(), name, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := checkpointRecord{InstallID: "wayne-install", Epoch: 2, JournalSeq: 9, Status: statusConsumed,
		TakenAt: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)}
	if err := cps.a.writeRecordAt(cps.Dir(), name, rec); err != nil {
		t.Fatal(err)
	}

	cp, ok, err := cps.Named(name)
	if err != nil || !ok {
		t.Fatalf("Named(%s) = %v, %v", name, ok, err)
	}
	if cp.JournalSeq != 9 || cp.Status != statusConsumed || cp.InstallID != "wayne-install" {
		t.Errorf("Named(%s) = %+v, want journal seq 9, consumed, wayne-install", name, cp)
	}
	if _, ok, err := cps.Named("20261121T000000Z-00000000"); ok || err != nil {
		t.Errorf("Named of a name with no record = %v, %v, want not found and no error", ok, err)
	}
}
