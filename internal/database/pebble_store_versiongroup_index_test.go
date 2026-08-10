// file: internal/database/pebble_store_versiongroup_index_test.go
// version: 1.0.0
// guid: c21ecc4a-5ca1-4492-b1a9-c1bf8c4c9d4f
// last-edited: 2026-08-10

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// 🔴 GetBooksByVersionGroup silently under-reports a group whose
// book:versiongroup:<gid>:<id> index is PARTIALLY populated.
//
// The read path trusts a non-empty result: `if len(books) > 0 { return books }`,
// falling back to a full scan only when the index yields NOTHING. So an index
// missing one row returns the other members and never scans — the caller gets a
// short list with no error. The paradox that makes this worth a regression test:
// deleting the WHOLE index returns the CORRECT set (the fallback fires), while
// deleting ONE row returns a WRONG one. More data loss, better answer.
//
// The root cause was on the WRITE side. UpdateBook maintained the index only
// when a book's VersionGroupID *changed* (`if oldVG != newVG { ...set... }`), so
// a row that was missing for any reason could never come back: every later edit
// left the group unchanged and therefore skipped the write. These tests damage
// exactly ONE row — not all of them — because damaging all of them hits the
// fallback and passes even with the bug present.
func TestGetBooksByVersionGroup_PartialIndexUnderReports(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	gid := "vg-partial-index"
	var ids []string
	for i := 1; i <= 3; i++ {
		b, err := s.CreateBook(&Book{Title: fmt.Sprintf("Edition %d", i), VersionGroupID: &gid})
		if err != nil {
			t.Fatalf("CreateBook %d: %v", i, err)
		}
		ids = append(ids, b.ID)
	}

	got, err := s.GetBooksByVersionGroup(gid)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("baseline: want 3 books in group, got %d", len(got))
	}

	// Damage exactly ONE index row, leaving the authoritative book:<id> row
	// intact. This is the shape a real partial index has: the book exists and
	// is in the group, but nothing points at it.
	victim := ids[1]
	victimKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", gid, victim))
	if err := s.db.Delete(victimKey, pebble.Sync); err != nil {
		t.Fatalf("damage index row: %v", err)
	}

	// Confirm the damage reproduces the under-report. This assertion documents
	// the broken behaviour on purpose — it is the bug, not the fix.
	damaged, err := s.GetBooksByVersionGroup(gid)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup after damage: %v", err)
	}
	if len(damaged) != 2 {
		t.Fatalf("expected the partial index to under-report 2 of 3 books, got %d — "+
			"if this is 3, the read path changed and this test's premise is stale", len(damaged))
	}

	// THE FIX: a same-group edit must repair the missing row. Before the fix
	// UpdateBook skipped the index write whenever the group was unchanged, so
	// this returned 2 forever no matter how many times the book was updated.
	b, err := s.GetBookByID(victim)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s): %v", victim, err)
	}
	b.Title = "Edition 2 (edited, same group)"
	if _, err := s.UpdateBook(victim, b); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	healed, err := s.GetBooksByVersionGroup(gid)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup after update: %v", err)
	}
	if len(healed) != 3 {
		t.Fatalf("self-heal failed: want 3 books after a same-group update, got %d — "+
			"UpdateBook is not writing the current-group index row unconditionally", len(healed))
	}
}

// The companion to the test above: with the index entirely gone the full-scan
// fallback fires and the answer is correct. Losing MORE index data returns a
// MORE correct result, which is exactly why the partial case went unnoticed.
//
// This also pins the fallback in place. Gating it on the backfill sentinel was
// considered and rejected: a genuinely missing row would then return EMPTY
// instead of the correct set, trading a silent under-report for a silent zero.
func TestGetBooksByVersionGroup_EmptyIndexFallsBackToCorrectAnswer(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	gid := "vg-empty-index"
	for i := 1; i <= 3; i++ {
		if _, err := s.CreateBook(&Book{Title: fmt.Sprintf("Edition %d", i), VersionGroupID: &gid}); err != nil {
			t.Fatalf("CreateBook %d: %v", i, err)
		}
	}

	prefix := []byte(fmt.Sprintf("book:versiongroup:%s:", gid))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';'
	if err := s.db.DeleteRange(prefix, upper, pebble.Sync); err != nil {
		t.Fatalf("delete whole index range: %v", err)
	}

	got, err := s.GetBooksByVersionGroup(gid)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("empty-index fallback: want 3 books from the full scan, got %d", len(got))
	}
}

// CreateBook must write the index row itself, so a freshly created book is
// discoverable by its group without waiting for the startup backfill.
func TestCreateBook_WritesVersionGroupIndexRow(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	gid := "vg-create-writes"
	b, err := s.CreateBook(&Book{Title: "Only Edition", VersionGroupID: &gid})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	key := []byte(fmt.Sprintf("book:versiongroup:%s:%s", gid, b.ID))
	val, closer, err := s.db.Get(key)
	if err != nil {
		t.Fatalf("index row missing after CreateBook: %v", err)
	}
	got := string(val)
	closer.Close()

	// The index is a POINTER index: the value is the book ID and no reader
	// consumes it. Asserting it here keeps all three writers (CreateBook,
	// UpdateBook, BackfillVersionGroupIndex) on one format, so the row stays
	// cheap enough to rewrite on every update.
	if got != b.ID {
		t.Fatalf("index value: want the book ID %q, got %q", b.ID, got)
	}
}
