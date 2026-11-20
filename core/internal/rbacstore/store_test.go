// file: core/internal/rbacstore/store_test.go
// version: 1.3.0
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

// otherRole is a second custom role, at version v.
func otherRole(v int64) *rbac.Role {
	return &rbac.Role{Name: "custom.gatekeeper", Title: "Gatekeeper", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:self"}, Version: v}
}

func generation(t testing.TB, st *Store) uint64 {
	t.Helper()
	g, err := st.RoleSetGeneration(context.Background())
	if err != nil {
		t.Fatalf("RoleSetGeneration: %v", err)
	}
	return g
}

// TestStore_GenerationRowIsNotARole: the generation row lives beside role/, so
// a scan of the roles meets only roles, and a bump never makes ScanRoles
// yield an undecodable row.
func TestStore_GenerationRowIsNotARole(t *testing.T) {
	st := newStore(t)
	b := st.NewBatch()
	if err := st.PutRole(b, role("Trustee", 1)); err != nil {
		t.Fatal(err)
	}
	commit(t, b)
	if g := generation(t, st); g != 1 {
		t.Fatalf("generation after one role write = %d, want 1", g)
	}
	n := 0
	for r, err := range st.ScanRoles(context.Background()) {
		if err != nil {
			t.Fatalf("ScanRoles met the generation row: %v", err)
		}
		if r.Name != "custom.trustee" {
			t.Fatalf("ScanRoles yielded %q", r.Name)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("ScanRoles yielded %d roles, want 1", n)
	}
}

// TestStore_ClaimCounterIsNotARole: the role-claim counter lives beside role/
// like the generation row, so a scan of the roles meets only roles after a
// claim has been committed.
func TestStore_ClaimCounterIsNotARole(t *testing.T) {
	st := newStore(t)
	b := st.NewBatch()
	if err := st.PutRole(b, role("Trustee", 1)); err != nil {
		t.Fatal(err)
	}
	commit(t, b)
	cb := st.NewBatch()
	if err := st.ClaimRoleSet(cb, generation(t, st)); err != nil {
		t.Fatal(err)
	}
	commit(t, cb)
	n := 0
	for r, err := range st.ScanRoles(context.Background()) {
		if err != nil {
			t.Fatalf("ScanRoles met the claim counter: %v", err)
		}
		if r.Name != "custom.trustee" {
			t.Fatalf("ScanRoles yielded %q", r.Name)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("ScanRoles yielded %d roles, want 1", n)
	}
}

// TestStore_TwoRolesInOneIndexedBatchRaiseTheGenerationAndStillCommit: the
// kernel's re-apply batch is indexed and writes many roles; each bump reads
// its own batch's earlier one, and the batch commits once.
func TestStore_TwoRolesInOneIndexedBatchRaiseTheGenerationAndStillCommit(t *testing.T) {
	st := newStore(t)
	before := generation(t, st)
	b := st.ks.NewIndexedBatch()
	if err := st.PutRole(b, role("Trustee", 1)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole(b, otherRole(1)); err != nil {
		t.Fatalf("the second role of the batch: %v", err)
	}
	commit(t, b)
	if g := generation(t, st); g <= before {
		t.Fatalf("generation %d after a two-role batch, was %d: it must rise", g, before)
	}
}

// TestStore_AGenerationRowThatIsNotEightBytesIsAnError: a damaged generation
// row is an error from the read, from a guard's caller and from every role
// write, never generation 0 (which would let a stale reader's guard pass).
func TestStore_AGenerationRowThatIsNotEightBytesIsAnError(t *testing.T) {
	st := newStore(t)
	b := st.NewBatch()
	b.Bind(st.ks)
	if err := b.Expect(st.ks, generationKey(), nil); err != nil {
		t.Fatal(err)
	}
	if err := b.In(st.ks).Set(generationKey(), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	commit(t, b)
	if _, err := st.RoleSetGeneration(context.Background()); err == nil {
		t.Error("RoleSetGeneration over a 3-byte row succeeded")
	}
	wb := st.NewBatch()
	defer func() { _ = wb.Close() }()
	if err := st.PutRole(wb, role("Trustee", 1)); err == nil {
		t.Error("PutRole over a 3-byte generation row succeeded")
	}
}
