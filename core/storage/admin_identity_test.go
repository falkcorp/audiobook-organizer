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

// TestAdminDataMigrationDone: the record's state decides, and only done counts.
func TestAdminDataMigrationDone(t *testing.T) {
	e := newEnv(t, offlineOwners()...)
	db := e.open()
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	adm := db.Admin()
	done := func() bool {
		t.Helper()
		got, err := adm.DataMigrationDone(ctx, "test.cutover")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if done() {
		t.Fatal("a migration with no record is not done")
	}
	for _, c := range []struct {
		state string
		want  bool
	}{{"pending", false}, {"running", false}, {"verifying", false}, {"failed", false}, {"held", false}, {DataMigrationDone, true}} {
		raw, err := DataMigrationCodec().Encode(DataMigrationRecord{State: c.state})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.storageKS().Set(ctx, DataMigrationKey("test.cutover"), raw); err != nil {
			t.Fatal(err)
		}
		if got := done(); got != c.want {
			t.Errorf("state %q: done = %v, want %v", c.state, got, c.want)
		}
	}
	if err := db.storageKS().Set(ctx, DataMigrationKey("test.cutover"), []byte("not a record")); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.DataMigrationDone(ctx, "test.cutover"); err == nil {
		t.Error("an unreadable record must be an error, not a false")
	}
	if got, err := adm.DataMigrationDone(ctx, "test.other"); err != nil || got {
		t.Errorf("another migration's record leaked: %v %v", got, err)
	}
}
