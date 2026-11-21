// file: core/coretest/index.go
// version: 0.1.0
// guid: 8d2e4b61-0f3a-4c97-b5e8-6a1f9c3d7e24
// last-edited: 2026-11-20

package coretest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/internal/bleveindex"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// IndexOwner is the owner spec of core's c/index/ keyspace (the durable dirty
// sets). Pass it to NewStore, beside the module's own owner, so the store can
// carry an IndexEngine.
func IndexOwner() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: index.Owner, Enabled: true, Spec: index.StorageSpec()}
}

// IndexEngine is core's real full-text engine (05-storage §5S.3.8) over a
// Store, for a module's tests: the Opener a module's index Spec opens through
// is the one the kernel will hand it, over the same bleve mapping, analyzers,
// query translation, limits and durable dirty set. The caller chooses the
// module and its derived index names: they are whatever owner specs the Store
// was booted with (a module's owner with storage.Spec.Derived naming each
// index, plus IndexOwner()).
//
// Nothing runs in the background: a test drains the dirty set with Reconcile
// and fills a fresh index with Rebuild, so every answer it asserts follows a
// step it took. A freshly created index is Rebuilding until Rebuild runs.
type IndexEngine struct {
	tb  testing.TB
	e   *bleveindex.Engine
	dir string

	once sync.Once
	err  error
}

// IndexEngineOptions configures NewIndexEngine. The zero value is the
// engine's production defaults over a fresh temp directory.
type IndexEngineOptions struct {
	// Dir is the index directory (<db parent>/index in production); an index
	// lives at <Dir>/<module>/<name>.bleve. "" is a fresh tb.TempDir(). Pass
	// a previous engine's Dir to reopen what it built.
	Dir string
	// MaxTermSearchers is the engine's term-searcher cap (index.ErrTooBroad
	// past it); 0 is the production default.
	MaxTermSearchers int
}

// NewIndexEngine builds the engine over st's current DB. st must have been
// booted with IndexOwner(); without it the test fails. tb's cleanup closes
// the engine. After st.Reboot the engine's handles are closed: Close it
// before the reboot and build a new one with the same Dir.
func NewIndexEngine(tb testing.TB, st *Store, o IndexEngineOptions) *IndexEngine {
	tb.Helper()
	root, ok := st.DB().Keyspace(index.Owner)
	if !ok {
		tb.Fatalf("coretest.NewIndexEngine: the store has no %s keyspace; boot it with coretest.IndexOwner()", index.Owner)
	}
	dir := o.Dir
	if dir == "" {
		dir = tb.TempDir()
	}
	db := st.DB()
	e, err := bleveindex.New(bleveindex.Options{
		Dir:              dir,
		Root:             root,
		Derived:          func(module, name string) (storage.Derived, error) { return db.Derived(storage.Owner(module), name) },
		MaxTermSearchers: o.MaxTermSearchers,
	})
	if err != nil {
		tb.Fatalf("coretest.NewIndexEngine: %v", err)
	}
	x := &IndexEngine{tb: tb, e: e, dir: dir}
	tb.Cleanup(func() {
		if err := x.Close(); err != nil {
			tb.Errorf("coretest.IndexEngine: close: %v", err)
		}
	})
	return x
}

// Opener is the index.Opener of module, as the kernel hands it to the
// module's StorageHandles.
func (x *IndexEngine) Opener(module storage.Owner) index.Opener { return x.e.Opener(string(module)) }

// Reconcile runs one reconcile pass over every open index: each pending mark
// is loaded through its Spec's Document and indexed or deleted. An index that
// a pass emptied of marks after a rebuild becomes Ready.
func (x *IndexEngine) Reconcile(ctx context.Context) error { return x.e.Reconcile(ctx) }

// Rebuild runs the full rebuild of module's open index name, as the
// core.index.rebuild task does: the index is dropped, refilled through its
// Spec's Rebuild, and the marks written meanwhile are drained. It returns
// the number of documents the rebuild indexed.
func (x *IndexEngine) Rebuild(ctx context.Context, module storage.Owner, name string) (int64, error) {
	res, err := x.e.Rebuild(taskstest.NewFakeRun(ctx, bleveindex.RebuildKind).RC(),
		bleveindex.RebuildParams{Module: string(module), Index: name})
	return res.Documents, err
}

// PendingRebuilds lists the open indexes the kernel would queue a rebuild
// for at Run start (fresh, recreated for a mapping change, or Degraded), as
// "<module>/<name>".
func (x *IndexEngine) PendingRebuilds() []string {
	var out []string
	for _, r := range x.e.PendingRebuilds() {
		out = append(out, r.Module+"/"+r.Index)
	}
	return out
}

// Dir is the engine's index directory.
func (x *IndexEngine) Dir() string { return x.dir }

// Path is where module's index name lives on disk.
func (x *IndexEngine) Path(module storage.Owner, name string) string {
	return filepath.Join(x.dir, string(module), name+".bleve")
}

// Close closes every open index. It is idempotent, so a test that reopens
// the directory with a new engine may close this one first.
func (x *IndexEngine) Close() error {
	x.once.Do(func() { x.err = x.e.Close() })
	return x.err
}
