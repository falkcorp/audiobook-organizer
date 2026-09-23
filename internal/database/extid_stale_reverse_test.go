// file: internal/database/extid_stale_reverse_test.go
// version: 1.0.0
// guid: 4c183811-eda8-4a4f-b762-58bae6229260
// last-edited: 2026-09-22

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// These pin the external-ID reverse index against the prod shape found on
// 2026-09-22: an iTunes PID whose forward record named book A while book B
// still held a reverse key for it. B listed the PID as its own, and merging B
// away queued an iTunes removal of the track A kept.

func newExtIDTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func reverseKeyExists(t *testing.T, s *PebbleStore, bookID, source, extID string) bool {
	t.Helper()
	_, closer, err := s.db.Get([]byte(fmt.Sprintf("ext_id:book:%s:%s:%s", bookID, source, extID)))
	if err == pebble.ErrNotFound {
		return false
	}
	if err != nil {
		t.Fatalf("Get reverse key: %v", err)
	}
	closer.Close()
	return true
}

// Re-creating a mapping under a new owner must retire the old owner's reverse key.
func TestCreateExternalIDMapping_OwnerChangeDropsOldReverseKey(t *testing.T) {
	s := newExtIDTestStore(t)
	for _, owner := range []string{"bookB", "bookA"} {
		if err := s.CreateExternalIDMapping(&ExternalIDMapping{Source: "itunes", ExternalID: "PID1", BookID: owner}); err != nil {
			t.Fatalf("CreateExternalIDMapping(%s): %v", owner, err)
		}
	}
	if reverseKeyExists(t, s, "bookB", "itunes", "PID1") {
		t.Error("bookB kept its reverse key after the PID moved to bookA")
	}
	if !reverseKeyExists(t, s, "bookA", "itunes", "PID1") {
		t.Error("bookA has no reverse key for the PID it now owns")
	}
	if got, _ := s.GetExternalIDsForBook("bookB"); len(got) != 0 {
		t.Errorf("bookB lists %+v, want none", got)
	}
}

// Re-creating under the SAME owner must keep that owner's reverse key.
func TestCreateExternalIDMapping_SameOwnerKeepsReverseKey(t *testing.T) {
	s := newExtIDTestStore(t)
	for i := 0; i < 2; i++ {
		if err := s.CreateExternalIDMapping(&ExternalIDMapping{Source: "itunes", ExternalID: "PID1", BookID: "bookA"}); err != nil {
			t.Fatalf("CreateExternalIDMapping: %v", err)
		}
	}
	got, err := s.GetExternalIDsForBook("bookA")
	if err != nil || len(got) != 1 || got[0].ExternalID != "PID1" {
		t.Fatalf("GetExternalIDsForBook(bookA) = %+v, %v; want [PID1]", got, err)
	}
}

// A stale reverse key already on disk (written before the fix) must not make a
// book report an ID another book owns.
func TestGetExternalIDsForBook_SkipsStaleReverseKey(t *testing.T) {
	s := newExtIDTestStore(t)
	if err := s.CreateExternalIDMapping(&ExternalIDMapping{Source: "itunes", ExternalID: "PID1", BookID: "bookA"}); err != nil {
		t.Fatalf("CreateExternalIDMapping: %v", err)
	}
	if err := s.CreateExternalIDMapping(&ExternalIDMapping{Source: "audible", ExternalID: "ASIN1", BookID: "bookB"}); err != nil {
		t.Fatalf("CreateExternalIDMapping: %v", err)
	}
	if err := s.db.Set([]byte("ext_id:book:bookB:itunes:PID1"), []byte("PID1"), pebble.Sync); err != nil {
		t.Fatalf("seed stale reverse key: %v", err)
	}

	got, err := s.GetExternalIDsForBook("bookB")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook: %v", err)
	}
	if len(got) != 1 || got[0].ExternalID != "ASIN1" {
		t.Fatalf("bookB mappings = %+v, want only ASIN1", got)
	}
}

// Merging away a book with a stale reverse key must drop that key and leave the
// real owner's mapping alone.
func TestReassignExternalIDs_DropsStaleReverseKeyWithoutMovingIt(t *testing.T) {
	s := newExtIDTestStore(t)
	if err := s.CreateExternalIDMapping(&ExternalIDMapping{Source: "itunes", ExternalID: "PID1", BookID: "bookA"}); err != nil {
		t.Fatalf("CreateExternalIDMapping: %v", err)
	}
	if err := s.db.Set([]byte("ext_id:book:bookB:itunes:PID1"), []byte("PID1"), pebble.Sync); err != nil {
		t.Fatalf("seed stale reverse key: %v", err)
	}

	if err := s.ReassignExternalIDs("bookB", "bookC"); err != nil {
		t.Fatalf("ReassignExternalIDs: %v", err)
	}
	if reverseKeyExists(t, s, "bookB", "itunes", "PID1") {
		t.Error("stale reverse key survived the reassign")
	}
	if reverseKeyExists(t, s, "bookC", "itunes", "PID1") {
		t.Error("stale reverse key was carried to the new book")
	}
	if owner, _ := s.GetBookByExternalID("itunes", "PID1"); owner != "bookA" {
		t.Errorf("PID1 owner = %q, want bookA", owner)
	}
}

// A repeated ID within one bulk call keeps the first occurrence and leaves no
// reverse key for the second.
func TestBulkCreateExternalIDMappings_InBatchDuplicateKeepsFirst(t *testing.T) {
	s := newExtIDTestStore(t)
	err := s.BulkCreateExternalIDMappings([]ExternalIDMapping{
		{Source: "itunes", ExternalID: "PID1", BookID: "bookA"},
		{Source: "itunes", ExternalID: "PID1", BookID: "bookB"},
	})
	if err != nil {
		t.Fatalf("BulkCreateExternalIDMappings: %v", err)
	}
	if owner, _ := s.GetBookByExternalID("itunes", "PID1"); owner != "bookA" {
		t.Errorf("PID1 owner = %q, want bookA (first occurrence)", owner)
	}
	if reverseKeyExists(t, s, "bookB", "itunes", "PID1") {
		t.Error("the losing duplicate left a reverse key on bookB")
	}
}
