// file: internal/database/hnsw_panic_safe_test.go
// version: 1.1.0
// guid: 5d9e1a73-2c4b-4f80-a6d1-8b3e0f2c9a45
// last-edited: 2026-11-08

package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestHNSWUpsert_ReinsertDoesNotCrash locks the fix that contains coder/hnsw's
// known crash bugs (e.g. the "node not added" panic on re-insert) at the store
// boundary: a mirror that trips the library must surface an error, never crash
// the process. Re-inserting the same key repeatedly is the documented trigger
// (HNSW-CRASH-2026-06-18); this must complete without panicking.
func TestHNSWUpsert_ReinsertDoesNotCrash(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(3)

	// Fresh inserts (the common re-embed path) must succeed.
	if err := s.Upsert(ctx, "book", "B1", []float32{1, 0, 0}, nil); err != nil {
		t.Fatalf("fresh insert B1: %v", err)
	}
	if err := s.Upsert(ctx, "book", "B2", []float32{0, 1, 0}, nil); err != nil {
		t.Fatalf("fresh insert B2: %v", err)
	}

	// Re-inserting an existing key is the documented "node not added" trigger
	// (HNSW-CRASH-2026-06-18). It must NOT crash the process — a recovered-panic
	// error is acceptable; a process crash is not. Reaching the assertion below
	// at all proves no panic escaped.
	for range 5 {
		_ = s.Upsert(ctx, "book", "B1", []float32{0, 0, 1}, nil)
	}

	// The store is still usable for a brand-new key.
	if err := s.Upsert(ctx, "book", "B3", []float32{1, 1, 1}, nil); err != nil {
		t.Logf("post-reinsert fresh insert returned (recovered) error, tolerated: %v", err)
	}
}

// TestHNSWUpsert_RecoveredPanicStopsExports (review m4): a recovered library
// panic can leave the graph part-mutated, so the store stops being a complete
// projection and no export writes the damaged graph over the last good
// snapshot. A dimension mismatch on a store with no configured dims is a
// deterministic trigger (coder/hnsw's assertDims panics inside Add).
func TestHNSWUpsert_RecoveredPanicStopsExports(t *testing.T) {
	ctx := context.Background()
	s := NewHNSWEmbeddingStore(0)
	if err := s.Upsert(ctx, "book", "alfred-pennyworth", []float32{1, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	s.SetComplete(true)
	if err := s.Export(t.TempDir()); err != nil {
		t.Fatalf("a complete store exports: %v", err)
	}
	if err := s.Upsert(ctx, "book", "lucius-fox", []float32{1, 0, 0, 1}, nil); err == nil {
		t.Fatal("a mismatched dimension was accepted; the trigger is gone")
	}
	if err := s.Export(t.TempDir()); !errors.Is(err, ErrHNSWIncomplete) {
		t.Fatalf("export after a recovered panic = %v, want ErrHNSWIncomplete", err)
	}
	res, err := s.ExportIfDirty(t.TempDir(), nil)
	if !errors.Is(err, ErrHNSWIncomplete) || res.Exported {
		t.Fatalf("ExportIfDirty after a recovered panic = %+v, %v; want ErrHNSWIncomplete", res, err)
	}
}

// TestHNSWLastExport_DoesNotWaitForARunningExport (review m2): the stop path
// reads LastExport against a shutdown budget, so it must not block behind an
// export holding exportMu.
func TestHNSWLastExport_DoesNotWaitForARunningExport(t *testing.T) {
	s := NewHNSWEmbeddingStore(3)
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	done := make(chan struct{})
	go func() {
		s.LastExport()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LastExport blocked behind exportMu")
	}
}
