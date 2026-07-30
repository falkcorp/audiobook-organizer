// file: internal/database/pebble_store_syncfile_test.go
// version: 1.0.0
// guid: 80186a0c-f2d2-4c17-9ef2-cfb78d441e1f
// last-edited: 2026-07-30

package database

import (
	"path/filepath"
	"sync"
	"testing"
)

func newSyncFileTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSyncFile_MintOnFirstEncounter_Idempotent(t *testing.T) {
	store := newSyncFileTestStore(t)

	id1, err := store.MintOrGetSyncFileID("book-1", "file-1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty syncFileID")
	}

	id2, err := store.MintOrGetSyncFileID("book-1", "file-1")
	if err != nil {
		t.Fatalf("mint again: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same syncFileID on repeat mint, got %q then %q", id1, id2)
	}
}

func TestSyncFile_DifferentFilesOnSameBook_GetDifferentIDs(t *testing.T) {
	store := newSyncFileTestStore(t)

	id1, err := store.MintOrGetSyncFileID("book-1", "file-1")
	if err != nil {
		t.Fatalf("mint file-1: %v", err)
	}
	id2, err := store.MintOrGetSyncFileID("book-1", "file-2")
	if err != nil {
		t.Fatalf("mint file-2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct syncFileIDs for distinct files, got same %q", id1)
	}
}

func TestSyncFile_SameFileIDOnDifferentBooks_NoCollision(t *testing.T) {
	store := newSyncFileTestStore(t)

	id1, err := store.MintOrGetSyncFileID("book-1", "shared-file-id")
	if err != nil {
		t.Fatalf("mint book-1: %v", err)
	}
	id2, err := store.MintOrGetSyncFileID("book-2", "shared-file-id")
	if err != nil {
		t.Fatalf("mint book-2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected independent syncFileIDs across books for same fileID, got same %q", id1)
	}

	// Re-fetch confirms each book's mapping is independent and stable.
	got1, ok, err := store.GetSyncFileID("book-1", "shared-file-id")
	if err != nil || !ok {
		t.Fatalf("get book-1: ok=%v err=%v", ok, err)
	}
	if got1 != id1 {
		t.Fatalf("book-1 lookup mismatch: got %q want %q", got1, id1)
	}

	got2, ok, err := store.GetSyncFileID("book-2", "shared-file-id")
	if err != nil || !ok {
		t.Fatalf("get book-2: ok=%v err=%v", ok, err)
	}
	if got2 != id2 {
		t.Fatalf("book-2 lookup mismatch: got %q want %q", got2, id2)
	}
}

func TestSyncFile_GetSyncFileID_NotFound(t *testing.T) {
	store := newSyncFileTestStore(t)

	id, ok, err := store.GetSyncFileID("no-such-book", "no-such-file")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatalf("expected not-found, got ok=true id=%q", id)
	}
	if id != "" {
		t.Fatalf("expected empty id on not-found, got %q", id)
	}
}

func TestSyncFile_ListSyncFilesForBook_ScopedPerBook(t *testing.T) {
	store := newSyncFileTestStore(t)

	if _, err := store.MintOrGetSyncFileID("book-1", "file-a"); err != nil {
		t.Fatalf("mint book-1/file-a: %v", err)
	}
	if _, err := store.MintOrGetSyncFileID("book-1", "file-b"); err != nil {
		t.Fatalf("mint book-1/file-b: %v", err)
	}
	if _, err := store.MintOrGetSyncFileID("book-2", "file-c"); err != nil {
		t.Fatalf("mint book-2/file-c: %v", err)
	}

	list1, err := store.ListSyncFilesForBook("book-1")
	if err != nil {
		t.Fatalf("list book-1: %v", err)
	}
	if len(list1) != 2 {
		t.Fatalf("expected 2 entries for book-1, got %d: %+v", len(list1), list1)
	}
	seenFileIDs := map[string]bool{}
	for _, sf := range list1 {
		if sf.BookID != "book-1" {
			t.Fatalf("entry has wrong BookID: %+v", sf)
		}
		seenFileIDs[sf.CurrentFileID] = true
	}
	if !seenFileIDs["file-a"] || !seenFileIDs["file-b"] {
		t.Fatalf("expected file-a and file-b in list1, got %+v", list1)
	}

	list2, err := store.ListSyncFilesForBook("book-2")
	if err != nil {
		t.Fatalf("list book-2: %v", err)
	}
	if len(list2) != 1 {
		t.Fatalf("expected 1 entry for book-2, got %d: %+v", len(list2), list2)
	}
	if list2[0].CurrentFileID != "file-c" {
		t.Fatalf("expected file-c for book-2, got %+v", list2[0])
	}

	listNone, err := store.ListSyncFilesForBook("book-none")
	if err != nil {
		t.Fatalf("list book-none: %v", err)
	}
	if len(listNone) != 0 {
		t.Fatalf("expected 0 entries for book with no sync files, got %d", len(listNone))
	}
}

func TestSyncFile_RepointSyncFile_MovesLookupIndex(t *testing.T) {
	store := newSyncFileTestStore(t)

	original, err := store.MintOrGetSyncFileID("book-1", "old-file-id")
	if err != nil {
		t.Fatalf("mint original: %v", err)
	}

	if err := store.RepointSyncFile("book-1", "old-file-id", "new-file-id"); err != nil {
		t.Fatalf("repoint: %v", err)
	}

	// The syncFileID must now resolve via the new fileID and carry the same identity.
	viaNew, ok, err := store.GetSyncFileID("book-1", "new-file-id")
	if err != nil || !ok {
		t.Fatalf("get via new fileID: ok=%v err=%v", ok, err)
	}
	if viaNew != original {
		t.Fatalf("expected repoint to preserve syncFileID %q, got %q", original, viaNew)
	}

	// The old (bookID, fileID) pair no longer resolves.
	_, ok, err = store.GetSyncFileID("book-1", "old-file-id")
	if err != nil {
		t.Fatalf("get via old fileID: %v", err)
	}
	if ok {
		t.Fatal("expected old (bookID, fileID) pair to no longer resolve after repoint")
	}

	// Minting again on the OLD pair must produce a brand NEW, different syncFileID --
	// proving the lookup index really moved rather than merely being duplicated.
	reMinted, err := store.MintOrGetSyncFileID("book-1", "old-file-id")
	if err != nil {
		t.Fatalf("re-mint on old pair: %v", err)
	}
	if reMinted == original {
		t.Fatalf("expected re-mint on stale (bookID, oldFileID) to mint a NEW id, got the same original %q", reMinted)
	}

	// The book-level index should now reflect the new fileID for the original syncFileID.
	list, err := store.ListSyncFilesForBook("book-1")
	if err != nil {
		t.Fatalf("list after repoint: %v", err)
	}
	foundNew := false
	for _, sf := range list {
		if sf.SyncFileID == original && sf.CurrentFileID == "new-file-id" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("expected list to show original syncFileID %q pointing at new-file-id, got %+v", original, list)
	}
}

func TestSyncFile_RepointSyncFile_NoOpWhenNothingToRepoint(t *testing.T) {
	store := newSyncFileTestStore(t)

	if err := store.RepointSyncFile("book-1", "never-existed", "new-file-id"); err != nil {
		t.Fatalf("expected nil error for no-op repoint, got %v", err)
	}
}

func TestSyncFile_ConcurrentMintRace_SingleWinner(t *testing.T) {
	store := newSyncFileTestStore(t)

	const workers = 16
	ids := make([]string, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			id, err := store.MintOrGetSyncFileID("race-book", "race-file")
			ids[idx] = id
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	first := ids[0]
	if first == "" {
		t.Fatal("expected non-empty syncFileID")
	}
	for i, id := range ids {
		if id != first {
			t.Fatalf("worker %d produced divergent syncFileID %q, want %q", i, id, first)
		}
	}

	list, err := store.ListSyncFilesForBook("race-book")
	if err != nil {
		t.Fatalf("list race-book: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 live sync_file record after race, got %d: %+v", len(list), list)
	}
}

func TestAsSyncFileStore(t *testing.T) {
	store := newSyncFileTestStore(t)

	sf := AsSyncFileStore(store)
	if sf == nil {
		t.Fatal("expected *PebbleStore to satisfy SyncFileStore")
	}

	if AsSyncFileStore(nil) != nil {
		t.Fatal("expected nil input to yield nil SyncFileStore")
	}

	if AsSyncFileStore("not a store") != nil {
		t.Fatal("expected non-conforming type to yield nil SyncFileStore")
	}
}
