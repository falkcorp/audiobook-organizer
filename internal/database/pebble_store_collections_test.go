// file: internal/database/pebble_store_collections_test.go
// version: 1.2.0
// guid: 7a6f1e4c-92b8-4d13-9a5e-3c8f0d6e1b27
// last-edited: 2026-10-18

package database

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

// newTestCollectionsStore opens a fresh on-disk PebbleStore for the CAS tests
// below. Collections have no in-memory-only path worth exercising separately,
// so the real store (not MockStore) is the right target — it is the
// implementation that actually carries the compare-and-swap logic.
func newTestCollectionsStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStoreInMemory("db")
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

// TestCollectionNameInUse_IsSentinel pins that both duplicate-name paths
// (create, and rename-onto-a-taken-name) wrap ErrCollectionNameInUse. The HTTP
// handlers map it to 409 with errors.Is; if either path went back to a bare
// fmt.Errorf the handlers would answer 500 for a mistake the user can fix.
func TestCollectionNameInUse_IsSentinel(t *testing.T) {
	store := newTestCollectionsStore(t)

	if _, err := store.CreateCollection(&Collection{Name: "Road Trip", Type: CollectionTypeStatic}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	// Same name after normalization, different case.
	_, err := store.CreateCollection(&Collection{Name: "road trip", Type: CollectionTypeStatic})
	if !errors.Is(err, ErrCollectionNameInUse) {
		t.Fatalf("duplicate create error = %v, want errors.Is(err, ErrCollectionNameInUse)", err)
	}

	other, err := store.CreateCollection(&Collection{Name: "Commute", Type: CollectionTypeStatic})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	other.Name = "ROAD TRIP"
	err = store.UpdateCollection(other)
	if !errors.Is(err, ErrCollectionNameInUse) {
		t.Fatalf("rename-onto-taken error = %v, want errors.Is(err, ErrCollectionNameInUse)", err)
	}
	if errors.Is(err, ErrCollectionVersionConflict) {
		t.Fatalf("a name conflict must not also read as a version conflict: %v", err)
	}
}

// newStaticCollection creates a static collection with the given members.
func newStaticCollection(t *testing.T, store *PebbleStore, name string, ids ...string) *Collection {
	t.Helper()
	col, err := store.CreateCollection(&Collection{Name: name, Type: CollectionTypeStatic, BookIDs: ids})
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return col
}

func TestAddBooksToCollection_AppendsSkipsDuplicatesKeepsOrder(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf", "b1", "b2")

	got, err := store.AddBooksToCollection(col.ID, []string{"b3", "b1", "b4", "b3"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := []string{"b1", "b2", "b3", "b4"}
	if !slices.Equal(got.BookIDs, want) {
		t.Fatalf("BookIDs = %v, want %v", got.BookIDs, want)
	}
	if got.Version != col.Version+1 {
		t.Fatalf("Version = %d, want %d", got.Version, col.Version+1)
	}
	stored, _ := store.GetCollection(col.ID)
	if !slices.Equal(stored.BookIDs, want) || stored.Version != got.Version {
		t.Fatalf("stored row %v v%d does not match the returned one", stored.BookIDs, stored.Version)
	}
}

func TestAddBooksToCollection_NoChangeIsNoOp(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf", "b1", "b2")

	got, err := store.AddBooksToCollection(col.ID, []string{"b2", "b1"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if got.Version != col.Version || !got.UpdatedAt.Equal(col.UpdatedAt) {
		t.Fatalf("an add that changes nothing must not bump Version/UpdatedAt: v%d (was %d)", got.Version, col.Version)
	}
}

func TestRemoveBookFromCollection_RemovesAndKeepsOrder(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf", "b1", "b2", "b3")

	got, err := store.RemoveBookFromCollection(col.ID, "b2")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if want := []string{"b1", "b3"}; !slices.Equal(got.BookIDs, want) {
		t.Fatalf("BookIDs = %v, want %v", got.BookIDs, want)
	}
	if got.Version != col.Version+1 {
		t.Fatalf("Version = %d, want %d", got.Version, col.Version+1)
	}
}

func TestRemoveBookFromCollection_AbsentMember(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf", "b1")

	_, err := store.RemoveBookFromCollection(col.ID, "b9")
	if !errors.Is(err, ErrCollectionBookNotMember) {
		t.Fatalf("err = %v, want ErrCollectionBookNotMember", err)
	}
	stored, _ := store.GetCollection(col.ID)
	if stored.Version != col.Version || !slices.Equal(stored.BookIDs, []string{"b1"}) {
		t.Fatalf("a refused remove must write nothing, got %v v%d", stored.BookIDs, stored.Version)
	}
}

func TestCollectionMembership_DynamicRefused(t *testing.T) {
	store := newTestCollectionsStore(t)
	dyn, err := store.CreateCollection(&Collection{Name: "live", Type: CollectionTypeDynamic, Query: "read_status:finished"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.AddBooksToCollection(dyn.ID, []string{"b1"}); !errors.Is(err, ErrCollectionNotStatic) {
		t.Fatalf("add err = %v, want ErrCollectionNotStatic", err)
	}
	if _, err := store.RemoveBookFromCollection(dyn.ID, "b1"); !errors.Is(err, ErrCollectionNotStatic) {
		t.Fatalf("remove err = %v, want ErrCollectionNotStatic", err)
	}
	if _, err := store.UpdateCollectionBooks(dyn.ID, func(c []string) ([]string, error) { return c, nil }); !errors.Is(err, ErrCollectionNotStatic) {
		t.Fatalf("update err = %v, want ErrCollectionNotStatic", err)
	}
	stored, _ := store.GetCollection(dyn.ID)
	if stored.Version != dyn.Version {
		t.Fatalf("a refused write must not bump Version")
	}
}

func TestCollectionMembership_NotFound(t *testing.T) {
	store := newTestCollectionsStore(t)
	if _, err := store.AddBooksToCollection("nope", []string{"b1"}); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("add err = %v, want ErrCollectionNotFound", err)
	}
	if _, err := store.RemoveBookFromCollection("nope", "b1"); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("remove err = %v, want ErrCollectionNotFound", err)
	}
	// UpdateCollection keeps its historical wording.
	err := store.UpdateCollection(&Collection{ID: "nope", Name: "x", Type: CollectionTypeStatic})
	if !errors.Is(err, ErrCollectionNotFound) || err.Error() != "collection nope not found" {
		t.Fatalf("UpdateCollection err = %v", err)
	}
}

func TestUpdateCollectionBooks_FnErrorAbortsWithoutWriting(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf", "b1")
	boom := errors.New("boom")

	_, err := store.UpdateCollectionBooks(col.ID, func(c []string) ([]string, error) {
		return append(c, "b2"), boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	stored, _ := store.GetCollection(col.ID)
	if stored.Version != col.Version || !slices.Equal(stored.BookIDs, []string{"b1"}) {
		t.Fatalf("an aborted update must write nothing, got %v v%d", stored.BookIDs, stored.Version)
	}
}

// TestCollectionMembership_ConcurrentAddsLoseNothing is the lost-update test:
// N goroutines each add one disjoint id to the same collection. Before the
// locked primitive each did read, append, UpdateCollection and the Version
// compare-and-swap was not atomic in-process, so adds were lost or refused.
// Run with -race.
func TestCollectionMembership_ConcurrentAddsLoseNothing(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf")

	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := store.AddBooksToCollection(col.ID, []string{fmt.Sprintf("b%d", i)}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent add: %v", err)
	}

	stored, _ := store.GetCollection(col.ID)
	if len(stored.BookIDs) != n {
		t.Fatalf("len(BookIDs) = %d, want %d (an add was lost)", len(stored.BookIDs), n)
	}
	have := map[string]bool{}
	for _, b := range stored.BookIDs {
		have[b] = true
	}
	for i := 0; i < n; i++ {
		if !have[fmt.Sprintf("b%d", i)] {
			t.Errorf("b%d missing", i)
		}
	}
	if stored.Version != col.Version+n {
		t.Fatalf("Version = %d, want %d", stored.Version, col.Version+n)
	}
}

// TestCollectionMembership_ConcurrentUpdateStillConflicts pins that
// UpdateCollection is not clobbered by, and does not clobber, the membership
// operations: a caller holding a pre-add copy of the row gets a conflict, and
// the added book survives.
func TestCollectionMembership_ConcurrentUpdateStillConflicts(t *testing.T) {
	store := newTestCollectionsStore(t)
	col := newStaticCollection(t, store, "shelf", "b1")

	stale, _ := store.GetCollection(col.ID)
	if _, err := store.AddBooksToCollection(col.ID, []string{"b2"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	stale.Description = "stale writer"
	if err := store.UpdateCollection(stale); !errors.Is(err, ErrCollectionVersionConflict) {
		t.Fatalf("stale UpdateCollection err = %v, want ErrCollectionVersionConflict", err)
	}
	stored, _ := store.GetCollection(col.ID)
	if !slices.Equal(stored.BookIDs, []string{"b1", "b2"}) || stored.Description != "" {
		t.Fatalf("stale writer clobbered the row: %+v", stored)
	}

	// And the other direction, under -race: UpdateCollection (fresh read each
	// time, retrying on conflict) racing adds must keep every add.
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if _, err := store.AddBooksToCollection(col.ID, []string{fmt.Sprintf("x%d", i)}); err != nil {
				t.Errorf("add: %v", err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			for {
				cur, err := store.GetCollection(col.ID)
				if err != nil || cur == nil {
					t.Errorf("get: %v", err)
					return
				}
				cur.Description = fmt.Sprintf("edit %d", i)
				err = store.UpdateCollection(cur)
				if errors.Is(err, ErrCollectionVersionConflict) {
					continue
				}
				if err != nil {
					t.Errorf("update: %v", err)
				}
				return
			}
		}(i)
	}
	wg.Wait()
	stored, _ = store.GetCollection(col.ID)
	if len(stored.BookIDs) != 2+n {
		t.Fatalf("len(BookIDs) = %d, want %d: an add was clobbered by UpdateCollection", len(stored.BookIDs), 2+n)
	}
}
