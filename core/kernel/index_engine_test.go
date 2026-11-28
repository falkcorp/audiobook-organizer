// file: core/kernel/index_engine_test.go
// version: 0.1.0
// guid: c7a3e915-6b20-4d8f-a1c4-5e92b0f7d368
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// fakeIndexSettings is a core.index source a test changes under the engine.
type fakeIndexSettings struct{ v atomic.Pointer[indexSettings] }

func newFakeIndexSettings(workers int, interval time.Duration) *fakeIndexSettings {
	f := &fakeIndexSettings{}
	f.set(workers, interval)
	return f
}

func (f *fakeIndexSettings) set(workers int, interval time.Duration) {
	f.v.Store(&indexSettings{Reconcile: indexReconcile{Workers: workers, Interval: config.Duration(interval)}})
}

func (f *fakeIndexSettings) Get() *indexSettings { return f.v.Load() }

// indexStore boots a store with c/index/ and two modules that each declare an
// index named signal; gotham also declares cave.
func indexStore(t *testing.T) *storage.DB {
	t.Helper()
	jokerSpec := storage.Spec{Keyspace: true, Derived: []storage.DerivedIndex{{Name: "signal", Version: "1"}}}
	return bootDerivedOn(t, context.Background(), vfs.NewCrashableMem(), []storage.OwnerSpec{
		indexOwnerSpec(),
		{Owner: "gotham", Enabled: true, Spec: gothamSpec()},
		{Owner: "joker", Enabled: true, Spec: jokerSpec},
	})
}

func villainSpec(name string, document func(context.Context, string) (index.Doc, bool, error)) index.Spec {
	return index.Spec{
		Name: name, MappingVersion: "1",
		Fields:   []index.Field{{Name: "name", Kind: index.Text, Store: true}},
		Rebuild:  func(context.Context, index.RebuildWriter) error { return nil },
		Document: document,
	}
}

func TestIndexOwnerSpec(t *testing.T) {
	got := indexOwnerSpec()
	if got.Owner != "core/index" || !got.Enabled {
		t.Errorf("indexOwnerSpec = %+v, want the enabled core/index owner", got)
	}
	if !got.Spec.Keyspace || !slices.Equal(got.Spec.Serialized, []string{""}) {
		t.Errorf("spec = %+v, want a Serialized keyspace (the dirty sets' precondition writes need it)", got.Spec)
	}
	if want := index.StorageSpec(); !slices.Equal(got.Spec.Serialized, want.Serialized) || got.Spec.Keyspace != want.Keyspace {
		t.Errorf("spec = %+v, want index.StorageSpec %+v", got.Spec, want)
	}
}

func TestBuildIndexEngine_Refusals(t *testing.T) {
	db := indexStore(t)
	// A store opened without indexOwnerSpec has no c/index/.
	bare := bootDerivedOn(t, context.Background(), vfs.NewCrashableMem(), gothamOwners())
	for name, in := range map[string]indexInputs{
		"no store":          {Dir: t.TempDir()},
		"a relative dir":    {DB: db, Dir: "index"},
		"no dir":            {DB: db},
		"no c/index/ owner": {DB: bare, Dir: t.TempDir()},
	} {
		if e, err := buildIndexEngine(in); err == nil {
			t.Errorf("%s: built %v", name, e)
		}
	}
	_, err := buildIndexEngine(indexInputs{DB: bare, Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "indexOwnerSpec") {
		t.Errorf("the missing owner is not named: %v", err)
	}
	if e, err := buildIndexEngine(indexInputs{DB: db, Dir: t.TempDir()}); err != nil || e == nil {
		t.Errorf("a good build = %v, %v", e, err)
	}
}

// TestIndexOptions_ReadsCoreIndex: workers and interval come from the core.index
// section, read on every call so a reload applies; with no section the engine's
// own defaults stand (nil funcs).
func TestIndexOptions_ReadsCoreIndex(t *testing.T) {
	db := indexStore(t)
	dir := t.TempDir()

	// The real section, at its declared defaults.
	st, err := config.NewStore(config.Options{AppID: "gotham", Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	handles, err := config.RegisterSections(st.Registry(), coreSections()...)
	if err != nil {
		t.Fatal(err)
	}
	var h *config.Handle[indexSettings]
	for _, x := range handles {
		if th, ok := x.(*config.Handle[indexSettings]); ok {
			h = th
		}
	}
	if h == nil {
		t.Fatal("core.index is not among the core sections")
	}
	o, err := indexOptions(indexInputs{DB: db, Dir: dir, Settings: h})
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Workers(); got != runtime.NumCPU() {
		t.Errorf("Workers = %d, want core.index.reconcile.workers' default %d", got, runtime.NumCPU())
	}
	if got := o.Interval(); got != 5*time.Second {
		t.Errorf("Interval = %s, want core.index.reconcile.interval's default 5s", got)
	}
	if o.Dir != dir || o.Root.Owner() != index.Owner || o.Derived == nil {
		t.Errorf("options = dir %q, root owner %q, derived %v", o.Dir, o.Root.Owner(), o.Derived != nil)
	}

	// A change of the section shows on the next read.
	f := newFakeIndexSettings(3, time.Second)
	o, err = indexOptions(indexInputs{DB: db, Dir: dir, Settings: f})
	if err != nil {
		t.Fatal(err)
	}
	if o.Workers() != 3 || o.Interval() != time.Second {
		t.Fatalf("initial = %d, %s", o.Workers(), o.Interval())
	}
	f.set(9, 40*time.Millisecond)
	if o.Workers() != 9 || o.Interval() != 40*time.Millisecond {
		t.Errorf("after a reload = %d, %s; the options captured the first values", o.Workers(), o.Interval())
	}

	// No section: nil funcs, the engine's defaults.
	o, err = indexOptions(indexInputs{DB: db, Dir: dir})
	if err != nil || o.Workers != nil || o.Interval != nil {
		t.Errorf("with no section: workers nil %t, interval nil %t, err %v", o.Workers == nil, o.Interval == nil, err)
	}
}

// TestIndexEngine_OpenerScopedToModule: an opener belongs to its module. It
// opens only indexes that module declared, two modules' same-named indexes do
// not share a directory or a dirty set, and the module is the opener's.
func TestIndexEngine_OpenerScopedToModule(t *testing.T) {
	ctx := context.Background()
	db := indexStore(t)
	dir := t.TempDir()
	e, err := buildIndexEngine(indexInputs{DB: db, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	noDoc := func(context.Context, string) (index.Doc, bool, error) { return nil, false, nil }

	gotham, joker := indexOpenerFor(e, "gotham"), indexOpenerFor(e, "joker")
	if _, err := gotham.Open(ctx, villainSpec("signal", noDoc)); err != nil {
		t.Fatalf("gotham's own index: %v", err)
	}
	if _, err := joker.Open(ctx, villainSpec("signal", noDoc)); err != nil {
		t.Fatalf("joker's own index of the same name: %v", err)
	}
	// Each lives under its own module's directory.
	for _, m := range []string{"gotham", "joker"} {
		if fi, err := os.Stat(filepath.Join(dir, m, "signal.bleve")); err != nil || !fi.IsDir() {
			t.Errorf("%s/signal.bleve: %v", m, err)
		}
	}
	// A module opens only what it declared: joker declares no cave, gotham no batmobile.
	if _, err := joker.Open(ctx, villainSpec("cave", noDoc)); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Errorf("joker opening gotham's cave = %v, want ErrDerivedUndeclared", err)
	}
	if _, err := gotham.Open(ctx, villainSpec("batmobile", noDoc)); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Errorf("gotham opening an undeclared index = %v, want ErrDerivedUndeclared", err)
	}
	// An index opens once.
	if _, err := gotham.Open(ctx, villainSpec("signal", noDoc)); err == nil {
		t.Error("gotham opened signal twice")
	}
	// A module outside the store opens nothing.
	if _, err := indexOpenerFor(e, "penguin").Open(ctx, villainSpec("signal", noDoc)); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Errorf("an unknown module = %v, want ErrDerivedUndeclared", err)
	}
}

// markDirty marks id of module's index name in the store, as an entity write
// would in its own batch.
func markDirty(t *testing.T, db *storage.DB, module, name, id string) {
	t.Helper()
	ctx := context.Background()
	root, _ := db.Keyspace(index.Owner)
	der, err := db.Derived(storage.Owner(module), name)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := index.NewDirtySet(root, der, module, name)
	if err != nil {
		t.Fatal(err)
	}
	err = storage.RetryOnConflict(ctx, 5, func(ctx context.Context) error {
		b := root.NewBatch()
		if err := ds.Mark(ctx, b, id); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestStartStopIndex: startIndex runs the reconciler, which applies the marks
// of an open index; stopIndex ends it, and is safe twice and with nil.
func TestStartStopIndex(t *testing.T) {
	ctx := context.Background()
	db := indexStore(t)
	e, err := buildIndexEngine(indexInputs{DB: db, Dir: t.TempDir(), Settings: newFakeIndexSettings(2, 10*time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })

	var mu sync.Mutex
	loaded := map[string]int{}
	loadedCh := make(chan string, 16)
	doc := func(_ context.Context, id string) (index.Doc, bool, error) {
		mu.Lock()
		loaded[id]++
		mu.Unlock()
		loadedCh <- id
		return index.Doc{"name": id}, true, nil
	}
	if _, err := indexOpenerFor(e, "gotham").Open(ctx, villainSpec("signal", doc)); err != nil {
		t.Fatal(err)
	}
	// Opened but not started: a mark waits.
	markDirty(t, db, "gotham", "signal", "penguin")
	select {
	case id := <-loadedCh:
		t.Fatalf("%s was loaded before the reconciler started", id)
	case <-time.After(150 * time.Millisecond):
	}

	r := startIndex(ctx, e, nil)
	wait := func(want string) {
		t.Helper()
		select {
		case id := <-loadedCh:
			if id != want {
				t.Fatalf("loaded %q, want %q", id, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the reconciler never loaded %q", want)
		}
	}
	wait("penguin")
	markDirty(t, db, "gotham", "signal", "riddler")
	wait("riddler")

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := stopIndex(stopCtx, r); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
	default:
		t.Fatal("stopIndex returned before the reconciler goroutine did")
	}
	if err := stopIndex(stopCtx, r); err != nil {
		t.Errorf("a second stopIndex = %v", err)
	}
	if err := stopIndex(stopCtx, nil); err != nil {
		t.Errorf("stopIndex(nil) = %v", err)
	}
	markDirty(t, db, "gotham", "signal", "bane")
	select {
	case id := <-loadedCh:
		t.Errorf("%s was loaded after stopIndex", id)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestStartIndex_StopRightAfterStart: a stop that wins the race against the
// goroutine's first step still returns cleanly and logs no failure.
func TestStartIndex_StopRightAfterStart(t *testing.T) {
	db := indexStore(t)
	for range 50 {
		e, err := buildIndexEngine(indexInputs{DB: db, Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		h := &recHandler{}
		r := startIndex(context.Background(), e, slog.New(h))
		if err := stopIndex(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		_ = e.Close()
		if len(h.recs) != 0 {
			t.Fatalf("a stop racing the start logged %+v", h.recs)
		}
	}
}

// TestStartIndex_CtxEndsTheReconciler: cancelling ctx ends the reconciler too.
func TestStartIndex_CtxEndsTheReconciler(t *testing.T) {
	db := indexStore(t)
	e, err := buildIndexEngine(indexInputs{DB: db, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	r := startIndex(ctx, e, nil)
	cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconciler outlived its context")
	}
	if err := stopIndex(context.Background(), r); err != nil {
		t.Errorf("stopIndex after the context ended = %v", err)
	}
}
