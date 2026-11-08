// file: core/coretest/store_test.go
// version: 1.0.0
// guid: 3ed977a4-35a1-4bd1-aee7-b64ee67ae210
// last-edited: 2026-11-07

package coretest

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

func signalOwner(migs ...storage.Migration) storage.OwnerSpec {
	return storage.OwnerSpec{Owner: "gordon", Enabled: true, Spec: storage.Spec{Keyspace: true, Migrations: migs}}
}

func signalStep(v int, key, val string, seed bool) storage.Migration {
	return storage.Migration{Version: v, Name: "signal-" + key, Compat: storage.Additive, Seed: seed,
		Up: func(ctx context.Context, tx *storage.MigrationTx) error {
			if _, err := tx.Keyspace().Get(ctx, []byte(key)); !errors.Is(err, storage.ErrNotFound) {
				return err
			}
			return tx.Set([]byte(key), []byte(val))
		}}
}

// TestStoreFixture: NewStore boots a fresh store as the kernel does (the
// seed runs); Reboot with a newer build runs its new step over the same
// directory, with a checkpoint, and keeps the rows.
func TestStoreFixture(t *testing.T) {
	ctx := t.Context()
	s := NewStore(t, signalOwner(signalStep(1, "batsignal", "on", true)))
	ks := s.Keyspace("gordon")
	if v, err := ks.Get(ctx, []byte("batsignal")); err != nil || string(v) != "on" {
		t.Fatalf("seeded row = %q, %v", v, err)
	}
	db := s.Reboot(signalOwner(signalStep(1, "batsignal", "on", true), signalStep(2, "rooftop", "lit", false)))
	if db.CheckpointName() == "" {
		t.Fatal("an upgrade reboot took no checkpoint")
	}
	if _, err := ks.Get(ctx, []byte("batsignal")); !errors.Is(err, storage.ErrClosed) && !errors.Is(err, storage.ErrInvalidKeyspace) {
		t.Fatalf("a handle of the closed store still reads: %v", err)
	}
	ks = s.Keyspace("gordon")
	for key, want := range map[string]string{"batsignal": "on", "rooftop": "lit"} {
		if v, err := ks.Get(ctx, []byte(key)); err != nil || string(v) != want {
			t.Fatalf("%s after the reboot = %q, %v; want %q", key, v, err, want)
		}
	}
	if s.DB().AppliedVersion("gordon") != 2 {
		t.Fatalf("applied version = %d, want 2", s.DB().AppliedVersion("gordon"))
	}
}
