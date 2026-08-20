// file: internal/writeback/outbox_test.go
// version: 1.0.1
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-08-20

package writeback

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func TestWriteBackOutbox_EnqueueAndDequeue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	outbox := NewWriteBackOutbox(store)

	// Enqueue a book ID.
	if err := outbox.Enqueue("book-123"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Verify it was stored via the preference system.
	pref, _ := store.GetUserPreferenceForUser("_system", outboxPrefix+"book-123")
	if pref == nil || pref.Value == "" {
		t.Fatal("expected preference to be set after enqueue")
	}

	// Parse the stored timestamp.
	_, err = time.Parse(time.RFC3339, pref.Value)
	if err != nil {
		t.Errorf("stored value should be RFC3339 timestamp, got %q", pref.Value)
	}

	// Dequeue should clear it.
	if err := outbox.Dequeue("book-123"); err != nil {
		t.Fatalf("dequeue: %v", err)
	}

	pref, _ = store.GetUserPreferenceForUser("_system", outboxPrefix+"book-123")
	if pref != nil && pref.Value != "" {
		t.Errorf("expected empty value after dequeue, got %q", pref.Value)
	}
}

func TestWriteBackOutbox_Idempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	outbox := NewWriteBackOutbox(store)

	// Enqueue the same ID twice should not error.
	if err := outbox.Enqueue("book-456"); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if err := outbox.Enqueue("book-456"); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
}

func TestWriteBackOutbox_ReplayOrphans_NoBatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	outbox := NewWriteBackOutbox(store)
	replayed := outbox.ReplayOrphans(nil)
	if replayed != 0 {
		t.Errorf("expected 0 replayed with nil batcher, got %d", replayed)
	}
}

func TestWriteBackOutbox_ListPending(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	outbox := NewWriteBackOutbox(store)

	// ListPending currently returns nil (noted in the source code
	// as a future iteration). Verify it doesn't crash.
	pending := outbox.ListPending()
	if pending != nil {
		t.Errorf("expected nil from ListPending, got %v", pending)
	}
}

func TestEnqueueWithOutbox_NilOutbox(t *testing.T) {
	// Should not panic with nil outbox and nil batcher.
	EnqueueWithOutbox(nil, nil, "book-789")
}
