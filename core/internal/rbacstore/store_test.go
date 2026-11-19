// file: core/internal/rbacstore/store_test.go
// version: 1.1.0
// guid: 23a85290-0088-42d7-b522-d7086e7dcf49
// last-edited: 2026-11-19

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

// TestStore_GuardRoleWritesNothingAndBindsTheKeyspace: GuardRole is a
// precondition, not a write. Into a batch of another keyspace (here an
// indexed batch over the role keyspace stands for auth's), it commits when
// the role is untouched and fails with ErrConflict once the role was
// replaced; a guard over a stale version or a missing role is refused before
// anything is staged; the nil cases are errors, not panics.
func TestStore_GuardRoleWritesNothingAndBindsTheKeyspace(t *testing.T) {
	st := newStore(t)
	seed := st.NewBatch()
	if err := st.PutRole(seed, role("Trustee", 1)); err != nil {
		t.Fatal(err)
	}
	commit(t, seed)
	read, err := st.GetRole(context.Background(), "custom.trustee")
	if err != nil {
		t.Fatal(err)
	}

	b := st.NewBatch()
	if err := st.GuardRole(b, read); err != nil {
		t.Fatalf("GuardRole: %v", err)
	}
	if n := b.Count(); n != 0 {
		t.Errorf("a guard staged %d writes", n)
	}
	commit(t, b)
	if got, _ := st.GetRole(context.Background(), "custom.trustee"); got == nil || got.Version != 1 || got.Title != "Trustee" {
		t.Fatalf("the guard changed the role: %+v", got)
	}

	b = st.NewBatch()
	if err := st.GuardRole(b, read); err != nil {
		t.Fatal(err)
	}
	other := st.NewBatch()
	if err := st.PutRole(other, role("Warden", 2)); err != nil {
		t.Fatal(err)
	}
	commit(t, other)
	if err := b.Commit(context.Background(), storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("Commit of a guard over a replaced role: %v, want storage.ErrConflict", err)
	}
	_ = b.Close()

	for name, tc := range map[string]*rbac.Role{
		"a stale version": read,
		"a missing role":  {Name: "custom.ghost", Version: 1},
	} {
		sb := st.NewBatch()
		if err := st.GuardRole(sb, tc); !errors.Is(err, storage.ErrConflict) {
			t.Errorf("GuardRole of %s: %v, want storage.ErrConflict", name, err)
		}
		_ = sb.Close()
	}
	if err := st.GuardRole(nil, read); err == nil {
		t.Error("GuardRole without a batch succeeded")
	}
	nb := st.NewBatch()
	defer func() { _ = nb.Close() }()
	if err := st.GuardRole(nb, nil); err == nil {
		t.Error("GuardRole of a nil role succeeded")
	}
	if err := st.GuardRole(nb, &rbac.Role{}); err == nil {
		t.Error("GuardRole of a nameless role succeeded")
	}
}
