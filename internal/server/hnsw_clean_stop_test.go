// file: internal/server/hnsw_clean_stop_test.go
// version: 1.0.0
// guid: aeaaf5c1-04d2-4e80-9cdd-1edb60bf8015
// last-edited: 2026-11-08

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
