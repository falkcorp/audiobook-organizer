// file: internal/database/bookfile_hash_concurrency_test.go
// version: 1.1.1
// guid: 2f7a9d14-6b3e-4c85-a0d9-8e1c5b7f3a26
// last-edited: 2026-09-14

package database

import (
	"sync"
	"testing"
	"time"
)

// A panic while the row's stripe is held must still release it. Unlocking
// after the call instead of in a defer left the stripe held for good, so every
// later write to the ~1/256 of files on that stripe blocked forever. The hook
// panics inside the locked section; a second hash write on the same row must
// then finish.
func TestUpdateBookFileHashes_PanicReleasesStripe(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Panic"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/P/01.m4b", FileHash: "before"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	f := onlyBookFile(t, s, book.ID)

	updateBookFileHashesReadHook = func() { panic("injected") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the injected panic did not propagate")
			}
		}()
		_ = s.UpdateBookFileHashes(f.ID, "", "post", "mid")
	}()
	updateBookFileHashesReadHook = nil

	done := make(chan error, 1)
	go func() { done <- s.UpdateBookFileHashes(f.ID, "", "post", "after") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UpdateBookFileHashes after the panic: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the book_file stripe was still held after a panic: the second write never finished")
	}
}

// Review round 4, item 2: UpdateBookFileHashes read the row and then wrote
// that row back whole, holding no lock, so a field another writer committed in
// between was reverted by a write meant to change only the hashes. The read
// hook runs a concurrent SkipScan patch inside that window and gives it up to
// 300ms to finish. With the row's stripe held across the read and the write,
// the patch waits, runs after the hash write on the fresh row, and both land.
func TestUpdateBookFileHashes_ConcurrentFieldPatchIsNotReverted(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Concurrent"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/C/01.m4b", FileHash: "before"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	f := onlyBookFile(t, s, book.ID)

	var (
		wg       sync.WaitGroup
		patchErr error
	)
	finished := make(chan struct{})
	skip := true
	updateBookFileHashesReadHook = func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(finished)
			_, _, patchErr = s.PatchBookFileFields(book.ID, f.ID, BookFileFieldPatch{SkipScan: &skip})
		}()
		select {
		case <-finished:
		case <-time.After(300 * time.Millisecond):
		}
	}
	defer func() { updateBookFileHashesReadHook = nil }()

	if err := s.UpdateBookFileHashes(f.ID, "", "post", "after"); err != nil {
		t.Fatalf("UpdateBookFileHashes: %v", err)
	}
	wg.Wait()
	if patchErr != nil {
		t.Fatalf("PatchBookFileFields: %v", patchErr)
	}

	got, err := s.GetBookFileByID(book.ID, f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBookFileByID: row=%v err=%v", got, err)
	}
	if got.FileHash != "after" {
		t.Errorf("FileHash = %q, want the hash write's %q", got.FileHash, "after")
	}
	if !got.SkipScan {
		t.Errorf("the concurrent SkipScan patch was reverted by the hash write")
	}
}

// Review round 4, item 3: deleteSingleOwnerIndexIfOwned decided ownership from the
// committed index entry. In one batch upsert, B's row is staged first and
// points "dup" at B; A's row, whose committed entry still owned "dup", then
// staged a delete of it, so B's fresh entry was gone at commit.
func TestBatchUpsertBookFiles_HashIndexHandedOverWithinOneBatch(t *testing.T) {
	s := openRescanStore(t)
	bookA, err := s.CreateBook(&Book{Title: "Copy A"})
	if err != nil {
		t.Fatalf("CreateBook A: %v", err)
	}
	bookB, err := s.CreateBook(&Book{Title: "Copy B"})
	if err != nil {
		t.Fatalf("CreateBook B: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: bookA.ID, FilePath: "/lib/A/01.m4b", FileHash: "dup"}); err != nil {
		t.Fatalf("CreateBookFile A: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: bookB.ID, FilePath: "/lib/B/01.m4b", FileHash: "b-old"}); err != nil {
		t.Fatalf("CreateBookFile B: %v", err)
	}
	if got, _ := s.GetBookBySegmentFileHash("dup"); got == nil || got.ID != bookA.ID {
		t.Fatalf("precondition: the dup entry should point at book A, got %v", got)
	}

	if err := s.BatchUpsertBookFiles([]*BookFile{
		{BookID: bookB.ID, FilePath: "/lib/B/01.m4b", FileHash: "dup"},
		{BookID: bookA.ID, FilePath: "/lib/A/01.m4b", FileHash: "a-new"},
	}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}

	if got, _ := s.GetBookBySegmentFileHash("dup"); got == nil || got.ID != bookB.ID {
		t.Errorf("book B's entry, staged earlier in the same batch, was deleted by book A's row: got %v", got)
	}
	if got, _ := s.GetBookBySegmentFileHash("a-new"); got == nil || got.ID != bookA.ID {
		t.Errorf("book A's new hash was not indexed: got %v", got)
	}
}
