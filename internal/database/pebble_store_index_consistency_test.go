// file: internal/database/pebble_store_index_consistency_test.go
// version: 1.0.0
// guid: 3f7a9c21-6b4d-4e8f-a1c2-5d6e7f8091ab
// last-edited: 2026-07-13

package database

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func boolPtr(b bool) *bool { return &b }

// TestWorkIndexExcludesSoftDeleted covers Bug 1 for the work index: an
// UpdateBook that sets MarkedForDeletion without changing WorkID must not leave
// the book showing as live in GetBooksByWorkID (the index embeds a Book
// snapshot that UpdateBook only refreshes on WorkID change).
func TestWorkIndexExcludesSoftDeleted(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	workID := "WORK00000000000000000000AA"
	book := &Book{
		Title:    "Work Book",
		FilePath: "/test/work/soft.mp3",
		WorkID:   strPtr(workID),
	}
	created, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	if got, err := store.GetBooksByWorkID(workID); err != nil || len(got) != 1 {
		t.Fatalf("pre-delete GetBooksByWorkID = %d books, %v; want 1", len(got), err)
	}

	// Soft-delete via UpdateBook, same WorkID (mirrors SoftDeleteBook).
	upd := *created
	upd.MarkedForDeletion = boolPtr(true)
	if _, err := store.UpdateBook(created.ID, &upd); err != nil {
		t.Fatalf("UpdateBook soft-delete: %v", err)
	}

	got, err := store.GetBooksByWorkID(workID)
	if err != nil {
		t.Fatalf("post-delete GetBooksByWorkID: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("soft-deleted book still live in work listing: got %d books, want 0", len(got))
	}
}

// TestVersionGroupIndexExcludesSoftDeleted covers Bug 1 for the version-group
// index. Uses two books in the group so the fast-path (not the fallback scan)
// is what filters out the soft-deleted one.
func TestVersionGroupIndexExcludesSoftDeleted(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	vg := "VG0000000000000000000000BB"
	live := &Book{Title: "Live", FilePath: "/test/vg/live.mp3", VersionGroupID: strPtr(vg)}
	doomed := &Book{Title: "Doomed", FilePath: "/test/vg/doomed.mp3", VersionGroupID: strPtr(vg)}
	if _, err := store.CreateBook(live); err != nil {
		t.Fatalf("CreateBook live: %v", err)
	}
	createdDoomed, err := store.CreateBook(doomed)
	if err != nil {
		t.Fatalf("CreateBook doomed: %v", err)
	}

	if got, err := store.GetBooksByVersionGroup(vg); err != nil || len(got) != 2 {
		t.Fatalf("pre-delete GetBooksByVersionGroup = %d, %v; want 2", len(got), err)
	}

	upd := *createdDoomed
	upd.MarkedForDeletion = boolPtr(true)
	if _, err := store.UpdateBook(createdDoomed.ID, &upd); err != nil {
		t.Fatalf("UpdateBook soft-delete: %v", err)
	}

	got, err := store.GetBooksByVersionGroup(vg)
	if err != nil {
		t.Fatalf("post-delete GetBooksByVersionGroup: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Live" {
		t.Errorf("version-group listing = %d books %v; want [Live]", len(got), titles(got))
	}
}

// TestWorkIndexReflectsMetadataEdit covers Bug 1's staleness for a benign edit:
// a title change that keeps the same WorkID must be reflected in the listing
// (the old embedded snapshot carried the previous title).
func TestWorkIndexReflectsMetadataEdit(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	workID := "WORK00000000000000000000CC"
	created, err := store.CreateBook(&Book{
		Title:    "Old Title",
		FilePath: "/test/work/edit.mp3",
		WorkID:   strPtr(workID),
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	upd := *created
	upd.Title = "New Title"
	if _, err := store.UpdateBook(created.ID, &upd); err != nil {
		t.Fatalf("UpdateBook title edit: %v", err)
	}

	got, err := store.GetBooksByWorkID(workID)
	if err != nil {
		t.Fatalf("GetBooksByWorkID: %v", err)
	}
	if len(got) != 1 || got[0].Title != "New Title" {
		t.Errorf("work listing = %v; want [New Title]", titles(got))
	}
}

// TestDeleteBookRemovesWorkIndexRow covers Bug 2: DeleteBook must remove the
// dangling book:work index row (not just neutralize it on read).
func TestDeleteBookRemovesWorkIndexRow(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	workID := "WORK00000000000000000000DD"
	created, err := store.CreateBook(&Book{
		Title:    "To Delete",
		FilePath: "/test/work/delete.mp3",
		WorkID:   strPtr(workID),
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	if err := store.DeleteBook(created.ID); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}

	got, err := store.GetBooksByWorkID(workID)
	if err != nil {
		t.Fatalf("GetBooksByWorkID: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("hard-deleted book still live in work listing: got %d, want 0", len(got))
	}

	// Raw check: no book:work:<workID>: row should remain.
	ps, ok := store.(*PebbleStore)
	if !ok {
		t.Fatalf("store is not *PebbleStore: %T", store)
	}
	prefix := []byte(fmt.Sprintf("book:work:%s:", workID))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';'
	iter, err := ps.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer iter.Close()
	if iter.First(); iter.Valid() {
		t.Errorf("dangling book:work index row remains after DeleteBook: %q", string(iter.Key()))
	}
}

// TestCreateNarratorConcurrent covers Bug 3: concurrent CreateNarrator("X") from
// N goroutines must yield exactly one narrator ID and one record, with no
// duplicate/lost writes. Run under -race.
func TestCreateNarratorConcurrent(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	const n = 16
	var wg sync.WaitGroup
	ids := make([]int, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			nar, err := store.CreateNarrator("Contended Narrator")
			if err != nil {
				errs[idx] = err
				return
			}
			ids[idx] = nar.ID
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("CreateNarrator goroutine %d: %v", i, err)
		}
	}

	// Every call must observe the same single narrator ID.
	first := ids[0]
	for i, id := range ids {
		if id != first {
			t.Fatalf("goroutine %d got narrator ID %d, want %d (duplicate allocation)", i, id, first)
		}
	}

	// And exactly one narrator record exists.
	nar, err := store.GetNarratorByName("Contended Narrator")
	if err != nil {
		t.Fatalf("GetNarratorByName: %v", err)
	}
	if nar == nil || nar.ID != first {
		t.Fatalf("GetNarratorByName = %+v; want ID %d", nar, first)
	}

	ps, ok := store.(*PebbleStore)
	if !ok {
		t.Fatalf("store is not *PebbleStore: %T", store)
	}
	iter, err := ps.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("narrator:"),
		UpperBound: []byte("narrator:\xff"),
	})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer iter.Close()
	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	if count != 1 {
		t.Errorf("narrator record count = %d; want 1", count)
	}
}

func titles(books []Book) []string {
	out := make([]string, len(books))
	for i, b := range books {
		out[i] = b.Title
	}
	return out
}
