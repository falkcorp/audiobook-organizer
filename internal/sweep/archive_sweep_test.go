// file: internal/sweep/archive_sweep_test.go
// version: 1.0.1
// guid: d4c3b2a1-9087-6543-2109-fedcba987654
// last-edited: 2026-08-20

package sweep

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func TestSweepArchivedBooks_SkipsNonDeletedBooks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBook(&database.Book{
		ID: "b-active", Title: "Active Book", FilePath: "/tmp/active.m4b",
	})

	cleaned := SweepArchivedBooks(store)
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (not deleted), got %d", cleaned)
	}
}

func TestSweepArchivedBooks_SkipsRecentDeletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	markedTrue := true
	recentDate := time.Now().Add(-5 * 24 * time.Hour) // 5 days ago, within 30-day retention

	_, _ = store.CreateBook(&database.Book{
		ID:                  "b-recent",
		Title:               "Recently Deleted Book",
		FilePath:            "/tmp/recent.m4b",
		MarkedForDeletion:   &markedTrue,
		MarkedForDeletionAt: &recentDate,
	})

	cleaned := SweepArchivedBooks(store)
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (too recent), got %d", cleaned)
	}

	book, _ := store.GetBookByID("b-recent")
	if book == nil {
		t.Error("recently deleted book should still exist")
	}
}
