// file: internal/database/soft_deleted_count_test.go
// version: 1.0.0
// guid: 6d1c8b47-2e95-4a3f-8c06-b5e9a7f2d413
// last-edited: 2026-10-06

package database

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// seedTrash writes n trashed books (some sharing a deletion time, some with
// none) and m live ones.
func seedTrash(t *testing.T, n, m int) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	yes := true
	base := time.Now().Truncate(time.Second)
	for i := range n + m {
		b := &Book{ID: fmt.Sprintf("b%03d", i), Title: fmt.Sprintf("T%d", i), FilePath: fmt.Sprintf("/l/%d.m4b", i), Format: "m4b"}
		if i < n {
			b.MarkedForDeletion = &yes
			if i%7 != 0 { // every 7th has no timestamp; others collide in threes
				at := base.Add(-time.Duration(i/3) * time.Minute)
				b.MarkedForDeletionAt = &at
			}
		}
		if _, err := p.CreateBook(b); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// TestListSoftDeletedBooks_MemdbPageMatchesPebbleOrder: the memdb path now
// sorts pointers and copies only the page; every page must equal the Pebble
// scan's page, ties and missing timestamps included.
func TestListSoftDeletedBooks_MemdbPageMatchesPebbleOrder(t *testing.T) {
	p := seedTrash(t, 40, 5)
	for _, pg := range []struct{ limit, offset int }{{1, 0}, {5, 0}, {5, 5}, {7, 35}, {100, 0}, {3, 40}} {
		mem, err := p.ListSoftDeletedBooks(pg.limit, pg.offset, nil)
		if err != nil {
			t.Fatal(err)
		}
		p.UseMemDB = false
		peb, err := p.ListSoftDeletedBooks(pg.limit, pg.offset, nil)
		p.UseMemDB = true
		if err != nil {
			t.Fatal(err)
		}
		if d := firstDiff(bookIDsOf(peb), bookIDsOf(mem)); d >= 0 {
			t.Fatalf("limit=%d offset=%d: memdb %v, pebble %v", pg.limit, pg.offset, bookIDsOf(mem), bookIDsOf(peb))
		}
	}
}

// TestCountSoftDeletedBooks_ShortMemdbFallsThroughToPebble: the count is the
// total the listing paginates, so it must take the same fall-through the
// listing takes when memdb knows it is missing book rows, rather than
// counting the short set.
func TestCountSoftDeletedBooks_ShortMemdbFallsThroughToPebble(t *testing.T) {
	p := seedTrash(t, 12, 4)
	// Make memdb genuinely short (one trashed row gone) and say so.
	txn := p.mem().db.Txn(true)
	if _, err := txn.DeleteAll(memTableBooks, memIdxID, "b001"); err != nil {
		t.Fatal(err)
	}
	txn.Commit()
	if n, err := p.mem().CountSoftDeletedBooks(nil); err != nil || n != 11 {
		t.Fatalf("fixture: short memdb counts %d (%v), want 11", n, err)
	}
	p.mem().recordLostRows(memTableBooks, 1)

	if _, err := p.mem().CountSoftDeletedBooks(nil); err == nil {
		t.Fatal("a memdb known to be short must refuse to count")
	}
	n, err := p.CountSoftDeletedBooks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("count = %d, want the authoritative 12", n)
	}
}
