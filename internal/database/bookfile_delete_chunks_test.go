// file: internal/database/bookfile_delete_chunks_test.go
// version: 1.0.0
// guid: 8d41c6a7-3e0b-4f5a-9c72-b1f6e5a0d934
// last-edited: 2026-10-26

package database

import (
	"errors"
	"fmt"
	"testing"
)

// DeleteBookFilesByIDs commits at most deleteBookFilesChunk rows at a time, so
// the bytes it holds to compare at a commit stay bounded. Callers see the same
// result and the same refusals; only a later chunk failing after an earlier one
// committed is new, and is reported WITH the rows already deleted.

// seedChunkRows creates n rows under one book and returns their ids in order.
func seedChunkRows(t *testing.T, f *casFixture, n int) []string {
	t.Helper()
	ids := []string{f.file.ID}
	for i := 1; i < n; i++ {
		row := &BookFile{
			BookID: f.book.ID, FileHash: fmt.Sprintf("gotham-bulk-%03d", i),
			FilePath: fmt.Sprintf("/lib/Gotham/Batman-Year-One/bulk-%03d.m4b", i),
		}
		if err := f.s.CreateBookFile(row); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
	}
	return ids
}

func presentRows(t *testing.T, f *casFixture) int {
	t.Helper()
	files, err := f.s.GetBookFiles(f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func TestCAS_DeleteBookFilesByIDs_LargerThanTheChunkDeletesEverythingInBoundedCommits(t *testing.T) {
	f := newCASFixture(t)
	const total = 150
	ids := seedChunkRows(t, f, total)
	var remaining []int
	bookFileBeforeCommitHook = func() { remaining = append(remaining, presentRows(t, f)) }
	t.Cleanup(func() { bookFileBeforeCommitHook = nil })

	deleted, err := f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, ids...))
	if err != nil {
		t.Fatalf("DeleteBookFilesByIDs: %v", err)
	}
	if len(deleted) != total || presentRows(t, f) != 0 {
		t.Fatalf("returned %d rows, %d left; want %d returned and none left", len(deleted), presentRows(t, f), total)
	}
	wantCommits := (total + deleteBookFilesChunk - 1) / deleteBookFilesChunk
	if len(remaining) != wantCommits {
		t.Fatalf("%d commits (rows left before each: %v), want %d", len(remaining), remaining, wantCommits)
	}
	left := total
	for i, r := range remaining {
		if r != left {
			t.Fatalf("before commit %d %d rows were left, want %d: an earlier commit deleted more than a chunk", i+1, r, left)
		}
		left -= min(deleteBookFilesChunk, left)
	}
	if keys := secondaryKeysOf(t, f.s, ids[len(ids)-1]); len(keys) > 0 {
		t.Errorf("index keys left naming a deleted row: %v", keys)
	}
}

// A repeated ref is one row, even when its twin lands in another chunk: the
// second chunk must not look for a row the first one deleted.
func TestCAS_DeleteBookFilesByIDs_ADuplicateInAnotherChunkIsOneRow(t *testing.T) {
	f := newCASFixture(t)
	ids := seedChunkRows(t, f, deleteBookFilesChunk+10)
	refs := append(refsOf(f.book.ID, ids...), BookFileRef{BookID: f.book.ID, FileID: ids[0]})

	deleted, err := f.s.DeleteBookFilesByIDs(refs)
	if err != nil || len(deleted) != len(ids) {
		t.Fatalf("deleted %d rows, err %v; want %d and no error", len(deleted), err, len(ids))
	}
}

// Every refusal is decided over the whole call before the first chunk commits:
// a bad id, or a row under another book, in the LAST chunk deletes nothing.
func TestCAS_DeleteBookFilesByIDs_RefusalsInALaterChunkDeleteNothing(t *testing.T) {
	f := newCASFixture(t)
	ids := seedChunkRows(t, f, 2*deleteBookFilesChunk+5)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unresolvable id", func(t *testing.T) {
		refs := append(refsOf(f.book.ID, ids...), BookFileRef{BookID: f.book.ID, FileID: "01HGOTHAM0000000000000NONE"})
		deleted, err := f.s.DeleteBookFilesByIDs(refs)
		if err == nil || len(deleted) != 0 || presentRows(t, f) != len(ids) {
			t.Fatalf("deleted=%d err=%v present=%d, want an error and every row untouched", len(deleted), err, presentRows(t, f))
		}
	})
	t.Run("row now under another book", func(t *testing.T) {
		last := ids[len(ids)-1]
		if err := f.s.MoveBookFilesToBook([]string{last}, f.book.ID, dst.ID); err != nil {
			t.Fatal(err)
		}
		deleted, err := f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, ids...))
		if !errors.Is(err, ErrBookFileOwnerChanged) || len(deleted) != 0 || presentRows(t, f) != len(ids)-1 {
			t.Fatalf("deleted=%d err=%v present=%d, want ErrBookFileOwnerChanged and every other row untouched", len(deleted), err, presentRows(t, f))
		}
	})
}

// A later chunk that fails after an earlier one committed reports the rows that
// are gone together with the error, and leaves every other row alone.
func TestCAS_DeleteBookFilesByIDs_ALaterChunkFailingKeepsTheEarlierChunksReported(t *testing.T) {
	f := newCASFixture(t)
	const total = 3 * deleteBookFilesChunk
	ids := seedChunkRows(t, f, total)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	moved := ids[deleteBookFilesChunk+3] // in the second chunk
	injectBeforeCommit(t, 0, func(n int) {
		if n != 2 { // the second chunk's first commit attempt
			return
		}
		if err := f.s.MoveBookFilesToBook([]string{moved}, f.book.ID, dst.ID); err != nil {
			t.Errorf("injected MoveBookFilesToBook: %v", err)
		}
	})

	deleted, err := f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, ids...))
	if !errors.Is(err, ErrBookFileOwnerChanged) {
		t.Fatalf("err = %v, want ErrBookFileOwnerChanged from the second chunk", err)
	}
	if len(deleted) != deleteBookFilesChunk {
		t.Fatalf("returned %d rows, want the %d the first chunk deleted", len(deleted), deleteBookFilesChunk)
	}
	gone := map[string]bool{}
	for _, d := range deleted {
		gone[d.ID] = true
		if row, _ := f.s.GetBookFileByID(f.book.ID, d.ID); row != nil {
			t.Errorf("row %s is reported deleted but still stored", d.ID)
		}
	}
	for _, id := range ids {
		if gone[id] {
			continue
		}
		bookID := f.book.ID
		if id == moved {
			bookID = dst.ID
		}
		if row, _ := f.s.GetBookFileByID(bookID, id); row == nil {
			t.Errorf("row %s was deleted but not reported (or deleted by the refused chunk)", id)
		}
	}
}
