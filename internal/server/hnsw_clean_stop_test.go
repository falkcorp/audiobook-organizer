// file: internal/server/hnsw_clean_stop_test.go
// version: 1.0.1
// guid: aeaaf5c1-04d2-4e80-9cdd-1edb60bf8015
// last-edited: 2026-11-08

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	c := serviceregistry.NewContainer().Override("chromemstore", store)
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
		s.markHNSWCleanStop()
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
		s.markHNSWCleanStop()
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
		s.markHNSWCleanStop()
		if cleanStopMarkerExists(t, s.hnswPersistDir) {
			t.Error("a graph that changed after its last snapshot must not leave the marker")
		}
	})

	// The embed queue's drain can upsert after the marker: the marker goes.
	t.Run("write after the marker", func(t *testing.T) {
		s, store := cleanStopServer(t)
		s.stopHNSWSnapshot(context.Background())
		s.markHNSWCleanStop()
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
	c := serviceregistry.NewContainer()
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
	prev.markHNSWCleanStop()

	next := database.NewHNSWEmbeddingStore(4)
	c := serviceregistry.NewContainer().Override("chromemstore", next)
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
	c := serviceregistry.NewContainer().
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
