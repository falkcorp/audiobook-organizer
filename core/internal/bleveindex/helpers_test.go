// file: core/internal/bleveindex/helpers_test.go
// version: 1.0.2
// guid: 9f7ed2d6-e091-4ae0-91b9-cd628b1bd107
// last-edited: 2026-11-11

package bleveindex

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

const dbPath = "/batcave/index.pebble"

// rig is a booted store, an engine over a real temp directory and one open
// index of villains kept in the module's own keyspace.
type rig struct {
	t    *testing.T
	f    *indextest.Fixture
	e    *Engine
	dir  string
	spec index.Spec
	ix   index.Index

	loads atomic.Int64 // Document calls
}

func villainFields() []index.Field {
	return []index.Field{
		{Name: "name", Kind: index.Text, Store: true},
		{Name: "alias", Kind: index.Keyword, Store: true, Facet: true},
		{Name: "year", Kind: index.Number, Store: true},
		{Name: "debut", Kind: index.Date, Store: true},
		{Name: "free", Kind: index.Bool, Store: true, Facet: true},
	}
}

// villain is the entity a row holds: name|alias|year|free.
func villainRow(name, alias string, year int, free bool) []byte {
	return fmt.Appendf(nil, "%s|%s|%d|%t", name, alias, year, free)
}

func parseVillain(raw []byte) index.Doc {
	p := strings.Split(string(raw), "|")
	year, _ := strconv.Atoi(p[2])
	free, _ := strconv.ParseBool(p[3])
	return index.Doc{
		"name": p[0], "alias": p[1], "year": float64(year), "free": free,
		"debut": time.Date(1900+year%100, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newSpec(f *indextest.Fixture, version string, loads *atomic.Int64) index.Spec {
	return index.Spec{
		Name:           indextest.Signal,
		MappingVersion: version,
		Fields:         villainFields(),
		Rebuild: func(ctx context.Context, w index.RebuildWriter) error {
			for kv, err := range f.Module.ScanPrefix(ctx, []byte("villain/")) {
				if err != nil {
					return err
				}
				if err := w.Put(strings.TrimPrefix(string(kv.Key), "villain/"), parseVillain(kv.Value)); err != nil {
					return err
				}
			}
			return nil
		},
		Document: func(ctx context.Context, id string) (index.Doc, bool, error) {
			if loads != nil {
				loads.Add(1)
			}
			raw, err := f.Module.Get(ctx, []byte("villain/"+id))
			if err == storage.ErrNotFound {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			return parseVillain(raw), true, nil
		},
	}
}

func engineFor(t testing.TB, f *indextest.Fixture, dir string, opts ...func(*Options)) *Engine {
	t.Helper()
	o := Options{
		Dir: dir, Root: f.Root,
		Derived: func(module, name string) (storage.Derived, error) { return f.DB.Derived(storage.Owner(module), name) },
		Workers: func() int { return 4 },
		Chunk:   16,
	}
	for _, fn := range opts {
		fn(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func newRig(t *testing.T, opts ...func(*Options)) *rig {
	t.Helper()
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	return rigOn(t, f, t.TempDir(), "1", opts...)
}

func rigOn(t *testing.T, f *indextest.Fixture, dir, version string, opts ...func(*Options)) *rig {
	t.Helper()
	r := &rig{t: t, f: f, dir: dir}
	r.e = engineFor(t, f, dir, opts...)
	r.spec = newSpec(f, version, &r.loads)
	r.open()
	return r
}

func (r *rig) open() {
	r.t.Helper()
	ix, err := r.e.Opener(string(indextest.Module)).Open(r.t.Context(), r.spec)
	if err != nil {
		r.t.Fatalf("open: %v", err)
	}
	r.ix = ix
}

// save writes the villain row and marks it in ONE batch, as a module does.
func (r *rig) save(id, name, alias string, year int) {
	r.t.Helper()
	r.saveRow(id, villainRow(name, alias, year, false))
}

func (r *rig) saveRow(id string, row []byte) {
	r.t.Helper()
	err := storage.RetryOnConflict(r.t.Context(), 8, func(ctx context.Context) error {
		b := r.f.Module.NewBatch()
		defer b.Close()
		if err := b.Set([]byte("villain/"+id), row); err != nil {
			return err
		}
		if err := r.ix.MarkDirty(b, id); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		r.t.Fatalf("save %s: %v", id, err)
	}
}

// remove deletes the row and marks it in one batch.
func (r *rig) remove(id string) {
	r.t.Helper()
	err := storage.RetryOnConflict(r.t.Context(), 8, func(ctx context.Context) error {
		b := r.f.Module.NewBatch()
		defer b.Close()
		if err := b.Delete([]byte("villain/" + id)); err != nil {
			return err
		}
		if err := r.ix.MarkDirty(b, id); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		r.t.Fatalf("remove %s: %v", id, err)
	}
}

// testWatchdog is how long one reconcile pass may take in a test. A drain that
// stops making progress (a cursor that does not advance) then fails in seconds
// with a context error instead of running to go test's ten-minute timeout.
const testWatchdog = 20 * time.Second

func (r *rig) reconcile() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), testWatchdog)
	defer cancel()
	if err := r.e.Reconcile(ctx); err != nil {
		r.t.Fatalf("reconcile: %v", err)
	}
}

// fakeClock is the failure backoff's time source in tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// useClock replaces the engine's backoff clock with one the test advances.
func (r *rig) useClock() *fakeClock {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r.e.now = c.Now
	return c
}

func (r *rig) dirtyCount() int {
	r.t.Helper()
	n, err := r.e.mustIdx(r.t, indextest.Signal).dirty.Count(r.t.Context())
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (e *Engine) mustIdx(t testing.TB, name string) *idx {
	t.Helper()
	i, err := e.lookup(string(indextest.Module), name)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func (r *rig) count(q index.Query) uint64 {
	r.t.Helper()
	n, err := r.ix.Count(r.t.Context(), q)
	if err != nil {
		r.t.Fatalf("count: %v", err)
	}
	return n
}

func (r *rig) ids(q index.Query) []string {
	r.t.Helper()
	res, err := r.ix.Search(r.t.Context(), q, index.Page{Size: 100, SortBy: []string{"_id"}})
	if err != nil {
		r.t.Fatalf("search: %v", err)
	}
	var out []string
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

func runRebuild(t testing.TB, e *Engine) (RebuildResult, error) {
	t.Helper()
	return e.runRebuild(taskstest.NewFakeRun(t.Context(), RebuildKind).RC(),
		RebuildParams{Module: string(indextest.Module), Index: indextest.Signal})
}

func indexDir(dir string) string {
	return filepath.Join(dir, string(indextest.Module), indextest.Signal+".bleve")
}
