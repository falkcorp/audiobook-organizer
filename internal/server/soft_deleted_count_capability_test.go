// file: internal/server/soft_deleted_count_capability_test.go
// version: 1.2.0
// guid: 3a9f6e12-8c47-4d05-b2e1-9f7c0d6a4e38
// last-edited: 2026-10-13

package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestSoftDeletedCount_ResolvesThroughDecorator is the shape of the trash count
// behind a store decorator (until 07-S3 the audiobook service held the
// indexedStore decorator): a Store-embedding decorator promotes only
// database.Store's methods. CountSoftDeletedBooks is not one of them, so
// a bare type assertion missed it and every count paged the whole trash
// through ListSoftDeletedBooks (30-58 s warm on a 48k trash, 733 s cold). A
// test against a raw PebbleStore passes either way; this one does not.
func TestSoftDeletedCount_ResolvesThroughDecorator(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	wrapped := &embeddingDecorator{Store: store}

	if database.AsSoftDeletedCountStore(wrapped) == nil {
		t.Fatal("the counting capability does not resolve through a decorator; the trash count falls back to paging the whole trash")
	}

	yes := true
	now := time.Now()
	const trashed, live = 7, 3
	for i := range trashed + live {
		b := &database.Book{ID: fmt.Sprintf("b%02d", i), Title: fmt.Sprintf("Book %d", i), FilePath: fmt.Sprintf("/lib/b%02d.m4b", i), Format: "m4b"}
		if i < trashed {
			at := now.Add(-time.Duration(i) * time.Hour)
			b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &at
		}
		if _, err := store.CreateBook(b); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	svc := audiobookspkg.NewAudiobookService(wrapped)
	n, err := svc.CountSoftDeletedBooks(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != trashed {
		t.Fatalf("count = %d, want %d", n, trashed)
	}
	page, err := svc.GetSoftDeletedBooks(context.Background(), 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "b00" {
		t.Fatalf("first trash row = %v, want the most recently deleted b00", page)
	}
}

// TestSlowPathCapabilities_ResolveThroughDecorator: the two other
// capability lookups the 2026-10-06 latency fixes rely on, asserted against a
// decorated store handed to their callers (s.store / storeForWiring). A miss would silently restore the slow path: status decoding
// every operation row on each poll, and metadata/cached doing a full Pebble
// book read per cached row.
func TestSlowPathCapabilities_ResolveThroughDecorator(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	srv := &Server{store: &embeddingDecorator{Store: store}}

	if _, ok := database.AsCapability[interface{ OpsV2TimelineIndexTrusted() bool }](srv.storeForWiring()); !ok {
		t.Fatal("OpsV2TimelineIndexTrusted does not resolve through a decorator; ListRecentOperationsV2 would read all history on every status call")
	}
	if _, ok := database.AsCapability[database.BookListingFieldsReader](srv.storeForWiring()); !ok {
		t.Fatal("BookListingFieldsReader does not resolve through a decorator; metadata/cached would read every cached book from Pebble")
	}
}
