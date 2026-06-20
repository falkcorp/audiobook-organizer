// file: internal/database/reassign_external_id_test.go
// version: 1.0.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-06-20

package database

import "testing"

func TestReassignExternalID(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	if err := s.CreateExternalIDMapping(&ExternalIDMapping{
		Source: "itunes", ExternalID: "PIDX", BookID: "bookA",
	}); err != nil {
		t.Fatalf("CreateExternalIDMapping: %v", err)
	}

	// Reassign A -> B.
	if err := s.ReassignExternalID("itunes", "PIDX", "bookB"); err != nil {
		t.Fatalf("ReassignExternalID: %v", err)
	}

	got, err := s.GetBookByExternalID("itunes", "PIDX")
	if err != nil || got != "bookB" {
		t.Fatalf("GetBookByExternalID = %q,%v; want bookB", got, err)
	}

	// Reverse index must follow: A loses it, B gains it.
	if a, _ := s.GetExternalIDsForBook("bookA"); len(a) != 0 {
		t.Errorf("bookA still has %d mappings, want 0", len(a))
	}
	b, _ := s.GetExternalIDsForBook("bookB")
	if len(b) != 1 || b[0].ExternalID != "PIDX" {
		t.Errorf("bookB mappings = %+v, want [PIDX]", b)
	}

	// Same-book reassign is a no-op (no error).
	if err := s.ReassignExternalID("itunes", "PIDX", "bookB"); err != nil {
		t.Errorf("same-book reassign should no-op, got %v", err)
	}

	// Missing mapping errors (caller must not silently drag a non-existent PID).
	if err := s.ReassignExternalID("itunes", "NOPE", "bookB"); err == nil {
		t.Errorf("reassign of missing mapping should error")
	}
}
