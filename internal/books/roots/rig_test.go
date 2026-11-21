// file: internal/books/roots/rig_test.go
// version: 0.1.0
// guid: 3d0f6a52-8b1e-4f0e-9d51-6a8f7c2b1e90
// last-edited: 2026-11-20

package roots_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

const (
	libOwner   storage.Owner = "core/library"
	booksOwner storage.Owner = "books"
	tvOwner    storage.Owner = "tv"
)

// configSource serves core.library.roots, as the kernel's config store does.
type configSource struct{ roots []library.RootSpec }

func (c configSource) Lookup(p config.Path) (config.KeyState, bool) {
	if p != "core.library.roots" {
		return config.KeyState{}, false
	}
	b, err := json.Marshal(c.roots)
	if err != nil {
		panic(err)
	}
	return config.KeyState{Path: p, Effective: b}, true
}

// bootStore opens a store with the library keyspace and a books owner that
// claims the rootscan: family, as the fold's books spec will, and returns the
// library keyspace and books' real LegacyRange.
func bootStore(t *testing.T) (storage.Keyspace, storage.LegacyRange) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: libOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
			{Owner: booksOwner, Spec: storage.Spec{Claims: []storage.Claim{{Prefix: roots.PrefixRootScan, Mode: storage.ClaimReadWrite}}}, Enabled: true},
		},
		Build:           "booksroots+fixture",
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
	ks, ok := db.Keyspace(libOwner)
	if !ok {
		t.Fatal("no library keyspace")
	}
	lr, ok := db.Legacy(booksOwner)
	if !ok {
		t.Fatal("no books legacy range")
	}
	return ks, lr
}

// rig is a real library.Manager over temp directories and the Scope it hands
// books. Roots, Batman themed: the books library, two import roots (the second
// not watched) and a root another module owns.
type rig struct {
	books  library.Scope
	rows   storage.LegacyRange // books' real rootscan: rows
	libDir string
	arkham string
	alley  string
	tvDir  string
	specs  []library.RootSpec
}

func newRig(t *testing.T, mod func(specs []library.RootSpec)) *rig {
	t.Helper()
	dir := func() string {
		d, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	r := &rig{libDir: dir(), arkham: dir(), alley: dir(), tvDir: dir()}
	r.specs = []library.RootSpec{
		{ID: "books-library", Name: "Wayne Manor Library", Path: r.libDir, Role: library.RoleLibrary, Owner: booksOwner, Watch: library.WatchOff, Sentinel: library.SentinelBootstrap, MinFree: 0},
		{ID: "import-1", Name: "Arkham intake", Path: r.arkham, Role: library.RoleImport, Owner: booksOwner, Watch: library.WatchNotify, Sentinel: library.SentinelBootstrap, MinFree: 0},
		{ID: "import-2", Name: "Crime Alley", Path: r.alley, Role: library.RoleImport, Owner: booksOwner, Watch: library.WatchOff, Sentinel: library.SentinelBootstrap, MinFree: 0},
		{ID: "tv-library", Name: "Gotham Tonight", Path: r.tvDir, Role: library.RoleLibrary, Owner: tvOwner, Watch: library.WatchOff, Sentinel: library.SentinelBootstrap, MinFree: 0},
	}
	if mod != nil {
		mod(r.specs)
	}
	ks, rows := bootStore(t)
	r.rows = rows
	m, err := library.New(library.Options{Keyspace: ks, Config: configSource{roots: r.specs}, InstallID: "install-gotham-1"})
	if err != nil {
		t.Fatalf("library.New: %v", err)
	}
	t.Cleanup(func() { _ = m.(interface{ Close() error }).Close() })
	if err := m.(library.Reloader).Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, s := range r.specs {
		if _, err := os.Stat(s.Path); err != nil {
			continue
		}
		if err := m.(library.SentinelWriter).WriteSentinel(context.Background(), s.ID); err != nil {
			t.Fatalf("sentinel %s: %v", s.ID, err)
		}
	}
	r.books, err = library.NewScope(m, booksOwner)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
