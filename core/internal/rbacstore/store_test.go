// file: core/internal/rbacstore/store_test.go
// version: 1.0.0
// guid: 23a85290-0088-42d7-b522-d7086e7dcf49
// last-edited: 2026-11-11

package rbacstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// newStore boots a fresh MemFS store holding the role keyspace.
func newStore(t testing.TB) *Store {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: Owner, Spec: StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		if cerr := o.Close(); cerr != nil {
			t.Logf("close after a failed boot: %v", cerr)
		}
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ks, ok := db.Keyspace(Owner)
	if !ok {
		t.Fatal("no role keyspace")
	}
	st, err := New(ks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// role is a custom role at version v.
func role(title string, v int64) *rbac.Role {
	return &rbac.Role{Name: "custom.trustee", Title: title, Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:admin"}, Version: v}
}

func commit(t testing.TB, b *storage.Batch) {
	t.Helper()
	defer func() { _ = b.Close() }()
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// TestStore_SecondWriteToARoleInOneIndexedBatchIsRefused: a role write is
// derived from the committed bytes, so a second write to one role in a batch
// would replace the first without having seen it. A batch that reads its own
// writes (the kernel's re-apply batch is one) can tell, and refuses with
// ErrDoubleWrite; nothing the refused call staged is committed.
func TestStore_SecondWriteToARoleInOneIndexedBatchIsRefused(t *testing.T) {
	st := newStore(t)
	seed := st.NewBatch()
	if err := st.PutRole(seed, role("Trustee", 1)); err != nil {
		t.Fatal(err)
	}
	commit(t, seed)

	t.Run("a put after a put", func(t *testing.T) {
		b := st.ks.NewIndexedBatch()
		defer func() { _ = b.Close() }()
		if err := st.PutRole(b, role("Warden", 2)); err != nil {
			t.Fatalf("the first write: %v", err)
		}
		if err := st.PutRole(b, role("Gatekeeper", 2)); !errors.Is(err, ErrDoubleWrite) {
			t.Fatalf("the second write of one role in one batch: err = %v, want ErrDoubleWrite", err)
		}
	})

	t.Run("a delete after a put", func(t *testing.T) {
		b := st.ks.NewIndexedBatch()
		defer func() { _ = b.Close() }()
		if err := st.PutRole(b, role("Warden", 2)); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteRole(b, role("Trustee", 1)); !errors.Is(err, ErrDoubleWrite) {
			t.Fatalf("DeleteRole after PutRole in one batch: err = %v, want ErrDoubleWrite", err)
		}
	})

	t.Run("the same role in two batches is fine", func(t *testing.T) {
		for i, v := range []int64{2, 3} {
			b := st.ks.NewIndexedBatch()
			if err := st.PutRole(b, role("Warden", v)); err != nil {
				t.Fatalf("batch %d: %v", i, err)
			}
			commit(t, b)
		}
		got, err := st.GetRole(context.Background(), "custom.trustee")
		if err != nil || got.Version != 3 || got.Title != "Warden" {
			t.Fatalf("role = %+v, %v, want version 3", got, err)
		}
	})
}

// TestStore_StaleWantIsAConflictNotADoubleWrite: when the bytes the caller read
// are not what is committed any more (another writer committed since), the
// batch's view differs from the caller's, but it is not this batch's own write:
// the commit's Expect reports it as a conflict and the caller retries.
func TestStore_StaleWantIsAConflictNotADoubleWrite(t *testing.T) {
	st := newStore(t)
	seed := st.NewBatch()
	if err := st.PutRole(seed, role("Trustee", 1)); err != nil {
		t.Fatal(err)
	}
	commit(t, seed)
	key := roleKey("custom.trustee")
	stale, err := st.committed(key) // what the caller read
	if err != nil {
		t.Fatal(err)
	}
	other := st.NewBatch()
	if err := st.PutRole(other, role("Warden", 2)); err != nil {
		t.Fatal(err)
	}
	commit(t, other) // another writer commits first

	b := st.ks.NewIndexedBatch()
	defer func() { _ = b.Close() }()
	raw, err := roleCodec().Encode(*role("Gatekeeper", 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.stage(b, key, stale, raw); err != nil {
		t.Fatalf("stage over a stale read: %v, want nil (the commit reports the conflict)", err)
	}
	if err := b.Commit(context.Background(), storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("commit over a stale read: %v, want storage.ErrConflict", err)
	}
}
