// file: core/examples/hello/index_test.go
// version: 1.0.0
// guid: c20b05a9-13c4-4d13-84ec-f095be4e0e3a
// last-edited: 2026-11-11

package hello

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bleveindex"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// TestHelloNamesIndex runs hello's recorded names through the full-text index
// contract (F7-07, dark): hello declares one small derived index over its own
// keyspace, a write and its dirty mark commit in one batch, and the reconciler
// makes the name searchable. The kernel wiring that hands a module its Opener
// is F7-09's; the test builds the engine the way that wiring will.
func TestHelloNamesIndex(t *testing.T) {
	ctx := context.Background()
	spec := storageSpec()
	spec.Derived = []storage.DerivedIndex{{Name: "names", Version: "1"}}
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: "/batcave/hello-index.pebble",
		Owners: []storage.OwnerSpec{
			{Owner: "hello", Spec: spec, Enabled: true},
			{Owner: index.Owner, Spec: index.StorageSpec(), Enabled: true},
		},
		Build:           "hello+index-test",
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
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, _ := db.Keyspace("hello")
	root, _ := db.Keyspace(index.Owner)
	names := ks.Sub("name")

	eng, err := bleveindex.New(bleveindex.Options{
		Dir:     t.TempDir(),
		Root:    root,
		Derived: func(module, name string) (storage.Derived, error) { return db.Derived(storage.Owner(module), name) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	doc := func(id string, raw []byte) index.Doc {
		n, _ := strconv.ParseFloat(string(raw), 64)
		return index.Doc{"name": id, "greetings": n}
	}
	ix, err := eng.Opener("hello").Open(ctx, index.Spec{
		Name:           "names",
		MappingVersion: "1",
		Fields: []index.Field{
			{Name: "name", Kind: index.Text, Store: true},
			{Name: "greetings", Kind: index.Number, Store: true},
		},
		Rebuild: func(ctx context.Context, w index.RebuildWriter) error {
			for kv, err := range names.Scan(ctx, storage.ScanOptions{}) {
				if err != nil {
					return err
				}
				if err := w.Put(strings.Clone(string(kv.Key)), doc(string(kv.Key), kv.Value)); err != nil {
					return err
				}
			}
			return nil
		},
		Document: func(ctx context.Context, id string) (index.Doc, bool, error) {
			raw, err := names.Get(ctx, []byte(id))
			if errors.Is(err, storage.ErrNotFound) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			return doc(id, raw), true, nil
		},
	})
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer ix.Close()

	b := names.NewBatch()
	defer b.Close()
	if err := b.Set([]byte("alfred"), []byte("3")); err != nil {
		t.Fatal(err)
	}
	if err := ix.MarkDirty(b, "alfred"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := eng.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := ix.Search(ctx, index.Match("name", "alfred"), index.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Hits[0].ID != "alfred" || res.Hits[0].Fields["greetings"] != float64(3) {
		t.Fatalf("search: %+v", res)
	}
	// The seed row "world" was never marked: it reaches the index through the
	// full rebuild, which is how a module's index is first filled.
	if n, _ := ix.Count(ctx, index.Match("name", "world")); n != 0 {
		t.Fatalf("an unmarked row was indexed without a rebuild: %d", n)
	}
}
