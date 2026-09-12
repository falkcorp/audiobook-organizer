// file: internal/database/pebble_store_syncfile_batch_test.go
// version: 1.0.1
// guid: 3f8c17ad-45b2-4e60-9d31-7ca0e6b85219
// last-edited: 2026-09-12

package database

import (
	"fmt"
	"sync"
	"testing"
)

// MintOrGetSyncFileID and MintOrGetSyncFileIDs both mint into the same
// sync_file: keyspace, and as of 2026-09-08 both reach that keyspace through an
// UNLOCKED read before taking syncFileMintMu. That makes the same-pair
// invariant — "concurrent callers racing on the same pair all observe the same
// winning ID" — the property most likely to break silently here, and the one
// with durable consequences: a split identity means one physical file with two
// live sync_file: records, and an offline client's cached
// /api/items/{itemId}/file/{ino} URL pointing at the loser.
//
// TestSyncFile_ConcurrentMintRace_SingleWinner already covers the singular path
// alone. What is new is that a batch caller and a singular caller can now race
// each other on the same pair, so that is what these tests drive.

// TestSyncFileBatch_ConcurrentMixedPathsSinglePair runs both entry points at the
// same pair concurrently and requires one winner. Run under -race.
func TestSyncFileBatch_ConcurrentMixedPathsSinglePair(t *testing.T) {
	store := newSyncFileTestStore(t)

	const workers = 32
	ids := make([]string, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			// Alternate the two paths so the interesting interleaving — a batch
			// miss and a singular miss both landing in the window between the
			// unlocked read and the lock — actually gets exercised.
			if idx%2 == 0 {
				id, err := store.MintOrGetSyncFileID("mixed-book", "mixed-file")
				ids[idx], errs[idx] = id, err
				return
			}
			got, err := store.MintOrGetSyncFileIDs("mixed-book", []string{"mixed-file"})
			if err != nil {
				errs[idx] = err
				return
			}
			ids[idx] = got["mixed-file"]
		}(i)
	}
	waitGroupOrFatal(t, &wg, "concurrent MintOrGetSyncFileIDs callers")

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	first := ids[0]
	if first == "" {
		t.Fatal("expected a non-empty syncFileID")
	}
	for i, id := range ids {
		if id != first {
			t.Fatalf("worker %d produced divergent syncFileID %q, want %q "+
				"(the double-checked re-read under syncFileMintMu is not holding)", i, id, first)
		}
	}

	// One winner means exactly one record, not merely agreement among callers:
	// a loser that minted and then returned the winner's id would still have
	// written its own orphan sync_file: record and left it live.
	list, err := store.ListSyncFilesForBook("mixed-book")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 sync_file record, got %d — a loser minted an orphan", len(list))
	}
	if list[0].SyncFileID != first {
		t.Fatalf("persisted record %q does not match the id callers observed %q", list[0].SyncFileID, first)
	}
}

// TestSyncFileBatch_ConcurrentDistinctPairs is the same-lock, different-pair
// case. Every pair must get its own id and every id must be distinct — a batch
// that reused one minted id across its members, or dropped members, would pass
// the single-pair test above and fail here.
func TestSyncFileBatch_ConcurrentDistinctPairs(t *testing.T) {
	store := newSyncFileTestStore(t)

	const books = 8
	const filesPerBook = 12

	var wg sync.WaitGroup
	results := make([]map[string]string, books)
	errs := make([]error, books)
	wg.Add(books)
	for b := range books {
		go func(bi int) {
			defer wg.Done()
			fileIDs := make([]string, 0, filesPerBook)
			for f := range filesPerBook {
				fileIDs = append(fileIDs, fmt.Sprintf("file-%d", f))
			}
			results[bi], errs[bi] = store.MintOrGetSyncFileIDs(fmt.Sprintf("book-%d", bi), fileIDs)
		}(b)
	}
	waitGroupOrFatal(t, &wg, "concurrent per-book MintOrGetSyncFileIDs batches")

	seen := make(map[string]string, books*filesPerBook)
	for b := range books {
		if errs[b] != nil {
			t.Fatalf("book %d: %v", b, errs[b])
		}
		if len(results[b]) != filesPerBook {
			t.Fatalf("book %d: got %d ids, want %d", b, len(results[b]), filesPerBook)
		}
		for fileID, syncFileID := range results[b] {
			if syncFileID == "" {
				t.Fatalf("book %d file %s: empty syncFileID", b, fileID)
			}
			key := fmt.Sprintf("book-%d|%s", b, fileID)
			if prev, dup := seen[syncFileID]; dup {
				t.Fatalf("syncFileID %q minted for both %s and %s", syncFileID, prev, key)
			}
			seen[syncFileID] = key
		}
	}
}

// TestSyncFileBatch_AgreesWithSingularPath pins the conformance claim in both
// directions: whichever path mints first, the other must return that same id
// rather than minting a second one.
func TestSyncFileBatch_AgreesWithSingularPath(t *testing.T) {
	t.Run("singular first, batch observes it", func(t *testing.T) {
		store := newSyncFileTestStore(t)

		want, err := store.MintOrGetSyncFileID("book-1", "file-1")
		if err != nil {
			t.Fatalf("singular mint: %v", err)
		}
		got, err := store.MintOrGetSyncFileIDs("book-1", []string{"file-1", "file-2"})
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		if got["file-1"] != want {
			t.Fatalf("batch re-minted file-1: got %q, want %q", got["file-1"], want)
		}
		if got["file-2"] == "" || got["file-2"] == want {
			t.Fatalf("file-2 should have its own fresh id, got %q", got["file-2"])
		}
	})

	t.Run("batch first, singular observes it", func(t *testing.T) {
		store := newSyncFileTestStore(t)

		batch, err := store.MintOrGetSyncFileIDs("book-1", []string{"file-1", "file-2"})
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		for _, fileID := range []string{"file-1", "file-2"} {
			got, sErr := store.MintOrGetSyncFileID("book-1", fileID)
			if sErr != nil {
				t.Fatalf("singular %s: %v", fileID, sErr)
			}
			if got != batch[fileID] {
				t.Fatalf("singular re-minted %s: got %q, want %q", fileID, got, batch[fileID])
			}
		}
	})

	// Every id the batch returns must be readable back through the ordinary
	// read API. A batch that populated its return map but failed to commit a
	// key would satisfy every assertion above.
	t.Run("batch results are durable and readable", func(t *testing.T) {
		store := newSyncFileTestStore(t)

		got, err := store.MintOrGetSyncFileIDs("book-1", []string{"a", "b", "c"})
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		for fileID, want := range got {
			readBack, found, rErr := store.GetSyncFileID("book-1", fileID)
			if rErr != nil {
				t.Fatalf("GetSyncFileID %s: %v", fileID, rErr)
			}
			if !found {
				t.Fatalf("%s was returned by the batch but is not persisted", fileID)
			}
			if readBack != want {
				t.Fatalf("%s persisted as %q, batch returned %q", fileID, readBack, want)
			}
		}
		list, err := store.ListSyncFilesForBook("book-1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 3 {
			t.Fatalf("expected 3 sync_file records, got %d", len(list))
		}
	})
}

// TestSyncFileBatch_InputHandling covers the argument shapes the mapper can
// actually produce — a book with no files, and (defensively) a repeated file id
// — plus the validation the singular form already had.
func TestSyncFileBatch_InputHandling(t *testing.T) {
	store := newSyncFileTestStore(t)

	t.Run("no file ids touches nothing", func(t *testing.T) {
		got, err := store.MintOrGetSyncFileIDs("book-empty", nil)
		if err != nil {
			t.Fatalf("empty: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected an empty map, got %v", got)
		}
		list, err := store.ListSyncFilesForBook("book-empty")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("a no-file call minted %d records", len(list))
		}
	})

	t.Run("duplicate file ids collapse to one record", func(t *testing.T) {
		got, err := store.MintOrGetSyncFileIDs("book-dup", []string{"f1", "f1", "f1"})
		if err != nil {
			t.Fatalf("dup: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(got))
		}
		list, err := store.ListSyncFilesForBook("book-dup")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("a duplicated file id minted %d records; the dedupe in "+
				"MintOrGetSyncFileIDs is what keeps mintSyncFileIDsLocked from "+
				"minting twice inside one batch", len(list))
		}
	})

	t.Run("empty bookID is rejected", func(t *testing.T) {
		if _, err := store.MintOrGetSyncFileIDs("", []string{"f1"}); err == nil {
			t.Fatal("expected an error for an empty bookID")
		}
	})

	t.Run("empty fileID is rejected", func(t *testing.T) {
		if _, err := store.MintOrGetSyncFileIDs("book-1", []string{"f1", ""}); err == nil {
			t.Fatal("expected an error for an empty fileID")
		}
	})
}

// The sync_item twin of the mixed-path race above is
// TestSyncID_ConcurrentMintRace_SingleWinner in pebble_store_syncid_test.go. It
// predates this change and needs no edit: MintOrGetSyncID gained the same
// unlocked-read-then-double-check shape on 2026-09-08, so that test now covers
// the new path. There is no batch form for sync_item — the ABS mapper mints one
// per BOOK (12 per search page), not one per file (~480), and with the lookup
// off the lock those are ordinary concurrent point-gets.
