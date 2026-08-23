// file: internal/database/pebble_store_collections_test.go
// version: 1.0.0
// guid: 7a6f1e4c-92b8-4d13-9a5e-3c8f0d6e1b27
// last-edited: 2026-08-22

package database

import (
	"errors"
	"path/filepath"
	"testing"
)

// newTestCollectionsStore opens a fresh on-disk PebbleStore for the CAS tests
// below. Collections have no in-memory-only path worth exercising separately,
// so the real store (not MockStore) is the right target — it is the
// implementation that actually carries the compare-and-swap logic.
func newTestCollectionsStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestUpdateCollection_VersionConflict_Rejected pins the actual bug: two
// read-modify-write cycles built from the SAME stale read must not both
// succeed. It reads the collection once, then performs two separate mutate
// -> UpdateCollection cycles from that one stale copy — simulating two
// concurrent callers (e.g. two AddBookToCollection requests) that each read
// before either had written. The first save must win; the second must be
// rejected with a version-conflict error, and the first save's change must
// still be the one persisted (not silently overwritten by whichever save
// happened to run last).
func TestUpdateCollection_VersionConflict_Rejected(t *testing.T) {
	store := newTestCollectionsStore(t)

	created, err := store.CreateCollection(&Collection{
		Name:    "road trip",
		Type:    CollectionTypeStatic,
		BookIDs: []string{"book-1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// ONE read, shared by both simulated callers — this is the crux of the
	// race: both start from the same Version.
	base, err := store.GetCollection(created.ID)
	if err != nil || base == nil {
		t.Fatalf("get: %v / %v", base, err)
	}

	// Caller A: reads base, appends book-2, saves. This is the "first" write
	// and must succeed.
	callerA := *base
	callerA.BookIDs = append(append([]string{}, base.BookIDs...), "book-2")
	if err := store.UpdateCollection(&callerA); err != nil {
		t.Fatalf("caller A save should succeed, got: %v", err)
	}

	// Caller B: reads the SAME stale base (Version has since moved on under
	// it), appends a different book, saves. This is the "second" write and
	// must be rejected — not silently accepted and not silently dropping
	// caller A's change.
	callerB := *base
	callerB.BookIDs = append(append([]string{}, base.BookIDs...), "book-3")
	err = store.UpdateCollection(&callerB)
	if err == nil {
		t.Fatal("caller B save should have been rejected as a version conflict, got nil error")
	}
	if !errors.Is(err, ErrCollectionVersionConflict) {
		t.Fatalf("caller B error = %q, want errors.Is(err, ErrCollectionVersionConflict)", err.Error())
	}

	// Caller A's change must be the one that stuck — not lost, not merged,
	// not overwritten by caller B's rejected attempt.
	final, err := store.GetCollection(created.ID)
	if err != nil || final == nil {
		t.Fatalf("final get: %v / %v", final, err)
	}
	wantIDs := []string{"book-1", "book-2"}
	if len(final.BookIDs) != len(wantIDs) {
		t.Fatalf("final.BookIDs = %v, want %v", final.BookIDs, wantIDs)
	}
	for i, id := range wantIDs {
		if final.BookIDs[i] != id {
			t.Fatalf("final.BookIDs = %v, want %v", final.BookIDs, wantIDs)
		}
	}
	if final.Version != callerA.Version {
		t.Errorf("final.Version = %d, want %d (caller A's committed version)", final.Version, callerA.Version)
	}
}

// TestUpdateCollection_CorrectVersion_Succeeds is the anti-over-suppression
// counterpart: a normal, non-racing read-then-write with the correct current
// Version must still succeed. This must keep passing with the CAS guard
// active, or the guard is rejecting legitimate sequential updates.
func TestUpdateCollection_CorrectVersion_Succeeds(t *testing.T) {
	store := newTestCollectionsStore(t)

	created, err := store.CreateCollection(&Collection{
		Name: "commutes",
		Type: CollectionTypeStatic,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// CreateCollection assigns Version=1 to a freshly-created row (never 0),
	// so the very first update must compare its read against that, not 0.
	if created.Version != 1 {
		t.Fatalf("created.Version = %d, want 1", created.Version)
	}

	// Update #1: ordinary read-then-write.
	col, err := store.GetCollection(created.ID)
	if err != nil || col == nil {
		t.Fatalf("get: %v / %v", col, err)
	}
	col.Description = "morning commute"
	if err := store.UpdateCollection(col); err != nil {
		t.Fatalf("update 1 should succeed, got: %v", err)
	}
	if col.Version != 2 {
		t.Fatalf("col.Version after update 1 = %d, want 2", col.Version)
	}

	// Update #2: a second, sequential (non-racing) read-then-write against the
	// now-current Version must also succeed — proving the guard does not ratchet
	// down to rejecting everything after the first legitimate write.
	col2, err := store.GetCollection(created.ID)
	if err != nil || col2 == nil {
		t.Fatalf("get 2: %v / %v", col2, err)
	}
	if col2.Version != 2 {
		t.Fatalf("col2.Version = %d, want 2", col2.Version)
	}
	col2.Description = "evening commute"
	if err := store.UpdateCollection(col2); err != nil {
		t.Fatalf("update 2 should succeed, got: %v", err)
	}
	if col2.Version != 3 {
		t.Fatalf("col2.Version after update 2 = %d, want 3", col2.Version)
	}
}

// TestUpdateCollection_BlindOverwrite_ZeroVersionConflicts asserts the edge
// case the task brief calls out explicitly: a caller that constructs a bare
// Collection without ever reading the current row first (Version left at its
// Go zero value) gets a normal conflict against an already-updated row, not a
// panic and not a silent success. Zero is not a CAS bypass.
func TestUpdateCollection_BlindOverwrite_ZeroVersionConflicts(t *testing.T) {
	store := newTestCollectionsStore(t)

	created, err := store.CreateCollection(&Collection{
		Name: "blind write target",
		Type: CollectionTypeStatic,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Move the stored Version off of whatever a zero-valued struct would carry,
	// so the blind write below is unambiguously stale rather than coincidentally
	// matching.
	col, err := store.GetCollection(created.ID)
	if err != nil || col == nil {
		t.Fatalf("get: %v / %v", col, err)
	}
	col.Description = "bump the version once"
	if err := store.UpdateCollection(col); err != nil {
		t.Fatalf("setup update should succeed, got: %v", err)
	}

	blind := &Collection{
		ID:   created.ID,
		Name: "blind write target",
		Type: CollectionTypeStatic,
		// Version deliberately left unset (Go zero value).
	}
	err = store.UpdateCollection(blind)
	if err == nil {
		t.Fatal("blind overwrite with Version=0 against an updated row should be rejected, got nil error")
	}
	if !errors.Is(err, ErrCollectionVersionConflict) {
		t.Fatalf("blind overwrite error = %q, want errors.Is(err, ErrCollectionVersionConflict)", err.Error())
	}
}
