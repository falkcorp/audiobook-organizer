// file: internal/server/entity_assign_lost_update_test.go
// version: 1.0.0
// guid: 878f4dbf-a0fb-4112-be16-7ed225a85aa6
// last-edited: 2026-09-14

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestAssignPublisherPreservingRecord_DoesNotRevertConcurrentColumns pins the
// lost-update fix on the resolve-production-author publisher write (audit
// A1#15): the helper must set only Publisher, so a Duration another writer
// commits between its read and its write survives. Against the old
// GetBookByID -> UpdateBook(whole row) it fails with "Duration reverted".
// lostUpdateBookStore and setDurationConcurrently live in
// series_merge_lost_update_test.go.
func TestAssignPublisherPreservingRecord_DoesNotRevertConcurrentColumns(t *testing.T) {
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "Book"})
	store.concurrent = setDurationConcurrently

	if err := assignPublisherPreservingRecord(store, "b1", "SomeProduction LLC"); err != nil {
		t.Fatalf("assignPublisherPreservingRecord: %v", err)
	}
	got := store.stored("b1")
	if got == nil || got.Publisher == nil || *got.Publisher != "SomeProduction LLC" {
		t.Fatalf("publisher not written: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the publisher write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}

// TestAssignResolvedAuthorPreservingRecord_DoesNotRevertConcurrentColumns is
// the same pin for the AuthorID write of the same op.
func TestAssignResolvedAuthorPreservingRecord_DoesNotRevertConcurrentColumns(t *testing.T) {
	prod := 7
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "Book", AuthorID: &prod})
	store.concurrent = setDurationConcurrently

	if err := assignResolvedAuthorPreservingRecord(store, "b1", 42, prod); err != nil {
		t.Fatalf("assignResolvedAuthorPreservingRecord: %v", err)
	}
	got := store.stored("b1")
	if got == nil || got.AuthorID == nil || *got.AuthorID != 42 {
		t.Fatalf("author not written: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the author write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
