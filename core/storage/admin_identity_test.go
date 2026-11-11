// file: core/storage/admin_identity_test.go
// version: 1.0.0
// guid: 4d1f0a9e-3b7c-4e2a-8f65-a1c9d0e7b342
// last-edited: 2026-11-11

package storage

import "testing"

// TestAdminStoreIdentity: Admin.StoreIdentity reports the generation the boot
// recorded (c/storage/epoch) and the directory identity it was stamped with;
// the same store reopened reports the same identity, and a store of its own
// (another directory) starts its own generation.
func TestAdminStoreIdentity(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	db := e.open()
	first := db.Admin().StoreIdentity()
	if first.Epoch != 1 {
		t.Fatalf("a fresh store's identity = %+v, want epoch 1", first)
	}
	if want := db.boot.epoch; first.Epoch != want.Epoch || first.DirFSID != want.DirFSID || first.DirIno != want.DirIno {
		t.Errorf("identity %+v differs from the boot's epoch record %+v", first, want)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := e.tryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Admin().StoreIdentity(); got != first {
		t.Errorf("reopened identity = %+v, want the same store's %+v", got, first)
	}
}
