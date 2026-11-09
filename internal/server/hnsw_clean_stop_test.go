// file: internal/server/hnsw_clean_stop_test.go
// version: 1.0.5
// guid: aeaaf5c1-04d2-4e80-9cdd-1edb60bf8015
// last-edited: 2026-11-08

package server

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// R1-15 follow-up (MAJOR-1): the server's half of the HNSW clean-stop marker.

// cleanStopServer returns a Server whose container holds a complete HNSW
// store with one book, persisting to a temp directory.
func cleanStopServer(t *testing.T) (*Server, *database.HNSWEmbeddingStore) {
	t.Helper()
	store := database.NewHNSWEmbeddingStore(4)
	if err := store.Upsert(context.Background(), "book", "the-long-halloween", []float32{1, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	store.SetComplete(true)
	c := serviceregistry.NewContainerFrom().Override("chromemstore", store)
	if err := c.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Server{hnswPersistDir: t.TempDir(), container: c}, store
}

func cleanStopMarkerExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "hnsw.clean-stop"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat marker: %v", err)
	}
	return err == nil
}

// shortBudget is a stop context with too little budget left for a
// stop-time export, so stopHNSWSnapshot skips it.
func shortBudget(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hnswStopExportMargin/2)
	t.Cleanup(cancel)
	return ctx
}

func TestMarkHNSWCleanStop(t *testing.T) {
	t.Run("after a stop-time export", func(t *testing.T) {
		s, _ := cleanStopServer(t)
		s.stopHNSWSnapshot(context.Background())
		s.markHNSWCleanStop(nil)
		if !cleanStopMarkerExists(t, s.hnswPersistDir) {
			t.Error("a stop whose export succeeded must leave the marker")
		}
	})

	// The stop-time export is skipped for the budget, but the periodic
	// snapshot already describes the graph: the marker is still true.
	t.Run("budget-skipped stop, graph unchanged since the last snapshot", func(t *testing.T) {
		s, _ := cleanStopServer(t)
		s.exportHNSWSnapshot("periodic")
		s.stopHNSWSnapshot(shortBudget(t))
		s.markHNSWCleanStop(nil)
		if !cleanStopMarkerExists(t, s.hnswPersistDir) {
			t.Error("an unchanged graph whose snapshot is on disk must leave the marker")
		}
	})

	t.Run("budget-skipped stop, graph changed since the last snapshot", func(t *testing.T) {
		s, store := cleanStopServer(t)
		s.exportHNSWSnapshot("periodic")
		if err := store.Upsert(context.Background(), "book", "dark-victory", []float32{0, 1, 0, 0}, nil); err != nil {
			t.Fatal(err)
		}
		s.stopHNSWSnapshot(shortBudget(t))
		s.markHNSWCleanStop(nil)
		if cleanStopMarkerExists(t, s.hnswPersistDir) {
			t.Error("a graph that changed after its last snapshot must not leave the marker")
		}
	})

	// A writer the stop left running can upsert after the marker: the marker goes.
	t.Run("write after the marker", func(t *testing.T) {
		s, store := cleanStopServer(t)
		s.stopHNSWSnapshot(context.Background())
		s.markHNSWCleanStop(nil)
		if err := store.Upsert(context.Background(), "book", "dark-victory", []float32{0, 1, 0, 0}, nil); err != nil {
			t.Fatal(err)
		}
		if cleanStopMarkerExists(t, s.hnswPersistDir) {
			t.Error("a write after the marker must remove it")
		}
	})
}

// TestImportHNSWSnapshot_NonHNSWStoreTakesTheMarker: a run on another vector
// backend lets the snapshot fall behind Pebble, so a later HNSW run must not
// find a marker vouching for it.
func TestImportHNSWSnapshot_NonHNSWStoreTakesTheMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hnsw.clean-stop"), []byte("clean stop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := serviceregistry.NewContainerFrom()
	if err := c.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	importHNSWSnapshot(c, dir)
	if cleanStopMarkerExists(t, dir) {
		t.Error("a boot without the HNSW store left the clean-stop marker")
	}
}

// TestImportHNSWSnapshot_TakesTheMarkerFromTheHNSWStore: the HNSW boot
// path consumes the marker through ImportWithStalenessCheck.
func TestImportHNSWSnapshot_TakesTheMarkerFromTheHNSWStore(t *testing.T) {
	prev, _ := cleanStopServer(t)
	prev.stopHNSWSnapshot(context.Background())
	prev.markHNSWCleanStop(nil)

	next := database.NewHNSWEmbeddingStore(4)
	c := serviceregistry.NewContainerFrom().Override("chromemstore", next)
	if err := c.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	importHNSWSnapshot(c, prev.hnswPersistDir)
	if n, _ := next.CountByType(context.Background(), "book"); n != 1 {
		t.Errorf("the snapshot was not loaded: CountByType(book) = %d", n)
	}
	if cleanStopMarkerExists(t, prev.hnswPersistDir) {
		t.Error("the boot did not consume the marker")
	}
}

// childModeFixture is a snapshot directory written by a previous parent
// process, and an empty Pebble embedding store standing in for the truth
// source. The snapshot records that store's count, so only the clean-stop
// marker can make a parent boot reconcile it.
type childModeFixture struct {
	dir string
	es  *database.EmbeddingStore
}

func newChildModeFixture(t *testing.T, cleanStop bool) childModeFixture {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := childModeFixture{dir: t.TempDir(), es: database.NewEmbeddingStore(database.NewGatedDB(db))}

	prev := database.NewHNSWEmbeddingStore(4)
	if err := prev.Upsert(context.Background(), "book", "the-court-of-owls", []float32{1, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	prev.SetComplete(true)
	if err := prev.ExportWithTruth(f.dir, f.es.CountByType); err != nil {
		t.Fatal(err)
	}
	if cleanStop {
		if written, reason, err := prev.MarkCleanStop(f.dir); err != nil || !written {
			t.Fatalf("MarkCleanStop = %v, %q, %v", written, reason, err)
		}
	}
	return f
}

// boot runs importHNSWSnapshot as a process of the given kind would at
// NewServer, and returns the store it loaded into.
func (f childModeFixture) boot(t *testing.T, child bool) *database.HNSWEmbeddingStore {
	t.Helper()
	old := isChildProcess
	isChildProcess = func() bool { return child }
	t.Cleanup(func() { isChildProcess = old })

	store := database.NewHNSWEmbeddingStore(4)
	c := serviceregistry.NewContainerFrom().
		Override("chromemstore", store).
		Override(serviceregistry.KeyEmbeddingStore, f.es)
	if err := c.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	importHNSWSnapshot(c, f.dir)
	isChildProcess = old
	return store
}

// TestImportHNSWSnapshot_ChildNeitherConsumesNorNeedsTheMarker: an
// operation-runner child loads the snapshot as is. It leaves the parent's
// marker where it is, and without one it does not reconcile, which would
// otherwise repeat a full Pebble classification in every op subprocess.
func TestImportHNSWSnapshot_ChildNeitherConsumesNorNeedsTheMarker(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cleanStop bool
	}{
		{"marker present", true},
		{"no marker", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newChildModeFixture(t, tc.cleanStop)
			store := f.boot(t, true)

			if store.NeedsReconcile() {
				t.Error("an operation-runner child must not reconcile the snapshot")
			}
			if n, _ := store.CountByType(context.Background(), "book"); n != 1 {
				t.Errorf("the child did not load the snapshot: CountByType(book) = %d", n)
			}
			if got := cleanStopMarkerExists(t, f.dir); got != tc.cleanStop {
				t.Errorf("marker on disk after the child's boot = %v, want %v (the child must leave it alone)", got, tc.cleanStop)
			}
		})
	}
}

// TestImportHNSWSnapshot_ParentReconcilesAfterAChildWithoutAMarker: a child
// loading the snapshot does not stand in for a clean stop. With no marker on
// disk, the parent's next boot still reconciles, though the counts match.
func TestImportHNSWSnapshot_ParentReconcilesAfterAChildWithoutAMarker(t *testing.T) {
	f := newChildModeFixture(t, false)
	if child := f.boot(t, true); child.NeedsReconcile() {
		t.Fatal("the child reconciled; the child-mode import is not in use")
	}

	parent := f.boot(t, false)
	if !parent.NeedsReconcile() {
		t.Error("with no clean-stop marker the parent's boot must reconcile, whatever a child did before it")
	}
	if cleanStopMarkerExists(t, f.dir) {
		t.Error("a marker appeared on disk although no process stopped cleanly")
	}
}

// pebbleCleanStopFixture is a real Pebble store seeded with primary Batman
// storylines and their embedding rows, and the snapshot directory the server
// derives beside it. Each open is one process: the store, the embedding store
// sharing its database, a dedup engine reading both, and a fresh HNSW store,
// wired into a container the way NewServer wires them.
type pebbleCleanStopFixture struct {
	dbPath  string
	hnswDir string
	vecs    map[string][]float32
}

// pebbleCleanStopProcess is one process over the fixture's store.
type pebbleCleanStopProcess struct {
	ps     *database.PebbleStore
	es     *database.EmbeddingStore
	engine *dedup.Engine
	hnsw   *database.HNSWEmbeddingStore
	c      *serviceregistry.Container
}

const pebbleCleanStopDims = 8

var pebbleCleanStopStorylines = []string{
	"year-one", "the-long-halloween", "dark-victory", "the-killing-joke",
	"hush", "knightfall", "no-mans-land", "the-court-of-owls",
	"a-death-in-the-family", "a-lonely-place-of-dying", "the-dark-knight-returns", "arkham-asylum",
	"gothic", "venom", "the-cult", "son-of-the-demon",
	"batman-rip", "war-games", "cataclysm", "contagion",
}

// newPebbleCleanStopFixture seeds the store in a process of its own, so the
// processes the test models start from a store nobody has written to since it
// opened.
func newPebbleCleanStopFixture(t *testing.T) *pebbleCleanStopFixture {
	t.Helper()
	root := t.TempDir()
	f := &pebbleCleanStopFixture{
		dbPath:  filepath.Join(root, "audiobooks.pebble"),
		hnswDir: database.HNSWSnapshotDir(filepath.Join(root, "audiobooks.pebble")),
		vecs:    map[string][]float32{},
	}
	ps, err := database.NewPebbleStore(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	es := database.NewEmbeddingStore(ps.DB())
	rng := rand.New(rand.NewSource(7))
	for _, id := range pebbleCleanStopStorylines {
		primary := true
		if _, err := ps.CreateBook(&database.Book{ID: id, Title: id, IsPrimaryVersion: &primary}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		v := make([]float32, pebbleCleanStopDims)
		for i := range v {
			v[i] = rng.Float32() + 0.01
		}
		f.vecs[id] = v
		if err := es.Upsert(database.Embedding{EntityType: "book", EntityID: id, Vector: v, Model: "bge-m3"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

// open starts a process whose dedup engine classifies rows against model.
func (f *pebbleCleanStopFixture) open(t *testing.T, model string) *pebbleCleanStopProcess {
	t.Helper()
	ps, err := database.NewPebbleStore(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &pebbleCleanStopProcess{ps: ps, es: database.NewEmbeddingStore(ps.DB())}
	p.engine, err = dedup.NewEngine(p.es, ps, ai.NewEmbeddingClientWithOptions("k", model, ""), nil, merge.NewService(ps), unified.DefaultScoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	p.hnsw = database.NewHNSWEmbeddingStore(pebbleCleanStopDims)
	p.c = serviceregistry.NewContainerFrom().
		Override("chromemstore", p.hnsw).
		Override(serviceregistry.KeyEmbeddingStore, p.es).
		Override(serviceregistry.KeyDedup, p.engine)
	if err := p.c.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

// close ends the process: the engine's background work, then the store.
func (p *pebbleCleanStopProcess) close(t *testing.T) {
	t.Helper()
	if err := p.engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.ps.Close(); err != nil {
		t.Fatal(err)
	}
}

// buildGraph gives the HNSW store the graph a hydrate of the seeded rows
// builds: every storyline, primary.
func (p *pebbleCleanStopProcess) buildGraph(t *testing.T, f *pebbleCleanStopFixture) {
	t.Helper()
	for _, id := range pebbleCleanStopStorylines {
		if err := p.hnsw.Upsert(context.Background(), "book", id, f.vecs[id], map[string]string{"is_primary_version": "true"}); err != nil {
			t.Fatal(err)
		}
	}
	p.hnsw.SetComplete(true)
}

// postInit runs the dedup engine's PostInit and waits for a reconcile it
// started to finish.
func (p *pebbleCleanStopProcess) postInit(t *testing.T) {
	t.Helper()
	if err := p.engine.PostInit(context.Background(), p.c); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for p.hnsw.NeedsReconcile() {
		if time.Now().After(deadline) {
			t.Fatal("the reconcile did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func primaryNearest(t *testing.T, s *database.HNSWEmbeddingStore, q []float32) []string {
	t.Helper()
	res, err := s.FindSimilar(context.Background(), "book", q, 5, map[string]string{"is_primary_version": "true"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(res))
	for i, r := range res {
		ids[i] = r.EntityID
	}
	return ids
}

// TestHNSWCleanStop_APebbleOnlyMerge (R1-15 follow-up review, MAJOR-1): a
// merge that makes a book non-primary in Pebble after the last snapshot, and
// is never mirrored into the graph, must not leave a clean-stop marker that
// lets the next boot load the snapshot as fresh and serve the merged book as
// a primary match. It runs through the server's own import and stop helpers
// over a real PebbleStore, so the merge reaches Pebble the way production
// writes it (UpdateBook). The first case is the control: with no write after
// the snapshot, the same setup leaves a marker the next boot trusts.
func TestHNSWCleanStop_APebbleOnlyMerge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		merged bool
	}{
		{"no write after the snapshot", false},
		{"merge in Pebble after the snapshot", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPebbleCleanStopFixture(t)

			// The previous process: the boot path (no snapshot yet), a
			// complete graph, a periodic snapshot, the merge, and its stop.
			prev := f.open(t, "bge-m3")
			s := &Server{hnswPersistDir: f.hnswDir, container: prev.c}
			importHNSWSnapshot(prev.c, f.hnswDir)
			prev.buildGraph(t, f)
			s.exportHNSWSnapshot("periodic")
			if tc.merged {
				venom, err := prev.ps.GetBookByID("venom")
				if err != nil || venom == nil {
					t.Fatalf("GetBookByID(venom) = %v, %v", venom, err)
				}
				notPrimary := false
				venom.IsPrimaryVersion = &notPrimary
				if _, err := prev.ps.UpdateBook("venom", venom); err != nil {
					t.Fatal(err)
				}
			}
			s.stopHNSWSnapshot(context.Background())
			s.markHNSWCleanStop(nil)
			prev.close(t)

			next := f.open(t, "bge-m3")
			t.Cleanup(func() { next.close(t) })
			importHNSWSnapshot(next.c, f.hnswDir)
			if got := next.hnsw.NeedsReconcile(); got != tc.merged {
				t.Errorf("the next boot's NeedsReconcile = %v, want %v", got, tc.merged)
			}
			next.postInit(t)
			if got := primaryNearest(t, next.hnsw, f.vecs["venom"]); slices.Contains(got, "venom") == tc.merged {
				t.Errorf("primary matches for venom's vector after the boot = %v; merged=%v", got, tc.merged)
			}
		})
	}
}

// TestHNSWCleanStop_AnotherEmbeddingModelAfterTheRestart: the stop vouched for
// a graph classified against one embedding model, and the next boot
// classifies against another, so every row's stale-model verdict moved
// although nothing was written. The marker records the model; the boot
// reconciles.
func TestHNSWCleanStop_AnotherEmbeddingModelAfterTheRestart(t *testing.T) {
	for _, tc := range []struct {
		name, next string
		stale      bool
	}{
		{"same model", "bge-m3", false},
		{"another model", "text-embedding-3-large", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPebbleCleanStopFixture(t)
			prev := f.open(t, "bge-m3")
			s := &Server{hnswPersistDir: f.hnswDir, container: prev.c}
			importHNSWSnapshot(prev.c, f.hnswDir)
			prev.buildGraph(t, f)
			s.stopHNSWSnapshot(context.Background())
			s.markHNSWCleanStop(nil)
			prev.close(t)
			if !cleanStopMarkerExists(t, f.hnswDir) {
				t.Fatal("the clean stop wrote no marker; the setup is wrong")
			}

			next := f.open(t, tc.next)
			t.Cleanup(func() { next.close(t) })
			importHNSWSnapshot(next.c, f.hnswDir)
			if got := next.hnsw.NeedsReconcile(); got != tc.stale {
				t.Errorf("the boot's NeedsReconcile = %v, want %v", got, tc.stale)
			}
		})
	}
}

// TestHNSWCleanStop_AToolRunBetweenTheStopAndTheBoot (R1-15 follow-up review,
// MINOR-1): a process that opens the database between a clean stop and the
// next boot (a CLI command, a tool) can change the rows the snapshot
// projects, and only the server counts those writes against the marker. Its
// open takes the marker, so the next boot reconciles, and the merge it made
// in Pebble does not survive as a primary match.
func TestHNSWCleanStop_AToolRunBetweenTheStopAndTheBoot(t *testing.T) {
	f := newPebbleCleanStopFixture(t)
	prev := f.open(t, "bge-m3")
	s := &Server{hnswPersistDir: f.hnswDir, container: prev.c}
	importHNSWSnapshot(prev.c, f.hnswDir)
	prev.buildGraph(t, f)
	s.stopHNSWSnapshot(context.Background())
	s.markHNSWCleanStop(nil)
	prev.close(t)
	if !cleanStopMarkerExists(t, f.hnswDir) {
		t.Fatal("the clean stop left no marker; the case needs one")
	}

	tool, err := database.NewPebbleStore(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if cleanStopMarkerExists(t, f.hnswDir) {
		t.Error("the tool's open left the marker on disk")
	}
	venom, err := tool.GetBookByID("venom")
	if err != nil || venom == nil {
		t.Fatalf("GetBookByID(venom) = %v, %v", venom, err)
	}
	notPrimary := false
	venom.IsPrimaryVersion = &notPrimary
	if _, err := tool.UpdateBook("venom", venom); err != nil {
		t.Fatal(err)
	}
	if err := tool.Close(); err != nil {
		t.Fatal(err)
	}

	next := f.open(t, "bge-m3")
	t.Cleanup(func() { next.close(t) })
	importHNSWSnapshot(next.c, f.hnswDir)
	if !next.hnsw.NeedsReconcile() {
		t.Error("the boot after a tool's run must reconcile the snapshot")
	}
	next.postInit(t)
	if got := primaryNearest(t, next.hnsw, f.vecs["venom"]); slices.Contains(got, "venom") {
		t.Errorf("primary matches for venom's vector after the boot = %v; the tool made it non-primary", got)
	}
}

// TestImportHNSWSnapshot_AChildReconcilesWhenTheCountsMoved (MINOR-3): a
// child imports by the row counts alone, so a snapshot whose Pebble rows
// moved since it was written is reconciled in the child, and the parent's
// marker stays where it is.
func TestImportHNSWSnapshot_AChildReconcilesWhenTheCountsMoved(t *testing.T) {
	f := newChildModeFixture(t, true)
	if err := f.es.Upsert(database.Embedding{EntityType: "book", EntityID: "the-black-mirror", Vector: []float32{0, 1, 0, 0}, Model: "bge-m3"}); err != nil {
		t.Fatal(err)
	}
	store := f.boot(t, true)
	if !store.NeedsReconcile() {
		t.Error("a child whose snapshot's row counts moved must reconcile it")
	}
	if !cleanStopMarkerExists(t, f.dir) {
		t.Error("the child took the parent's marker")
	}
}
