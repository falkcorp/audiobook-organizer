// file: core/internal/bleveindex/coretest_test.go
// version: 1.0.0
// guid: d31c16ba-24d5-4e24-95fd-a106cf5f1f4b
// last-edited: 2026-11-11

package bleveindex_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/internal/bleveindex"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// TestEngineOnCoretestStore runs the engine on coretest's store fixture, which
// boots the way the kernel's P2, P4a and P4b do over a real directory. This
// external test package may use coretest; the in-package tests cannot.
func TestEngineOnCoretestStore(t *testing.T) {
	ctx := context.Background()
	st := coretest.NewStore(t,
		storage.OwnerSpec{Owner: "gotham", Enabled: true, Spec: storage.Spec{
			Keyspace: true,
			Derived:  []storage.DerivedIndex{{Name: "signal", Version: "1"}},
		}},
		storage.OwnerSpec{Owner: index.Owner, Enabled: true, Spec: index.StorageSpec()},
	)
	mod := st.Keyspace("gotham")
	eng, err := bleveindex.New(bleveindex.Options{
		Dir:     t.TempDir(),
		Root:    st.Keyspace(index.Owner),
		Derived: func(m, n string) (storage.Derived, error) { return st.DB().Derived(storage.Owner(m), n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ix, err := eng.Opener("gotham").Open(ctx, index.Spec{
		Name: "signal", MappingVersion: "1",
		Fields:  []index.Field{{Name: "title", Kind: index.Text, Store: true}},
		Rebuild: func(context.Context, index.RebuildWriter) error { return nil },
		Document: func(ctx context.Context, id string) (index.Doc, bool, error) {
			raw, err := mod.Get(ctx, []byte(id))
			if errors.Is(err, storage.ErrNotFound) {
				return nil, false, nil
			}
			return index.Doc{"title": string(raw)}, err == nil, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := mod.NewBatch()
	defer b.Close()
	if err := b.Set([]byte("s1"), []byte("Bat Signal over Gotham")); err != nil {
		t.Fatal(err)
	}
	if err := ix.MarkDirty(b, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := eng.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := ix.Count(ctx, index.Match("title", "signal")); err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}
}
