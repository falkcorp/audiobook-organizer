// file: internal/database/author_creation_gate_test.go
// version: 1.0.0
// guid: 5a2e7c14-93b8-4d6f-a1e0-7b4c9d2f8e36
// last-edited: 2026-09-25

package database

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// TestCreateAuthor_RefusesImplausibleNames: CreateAuthor is the last line of
// defence for every creation path, so a junk name must come back as the typed
// error and leave no row behind.
func TestCreateAuthor_RefusesImplausibleNames(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	for _, name := range []string{
		"14 BBY", "13 short stories", "(c) 2001 Stephen Hawking", "- Epigraph",
		"&#169", "read by narrator", "Read by Robin Sachs", "Book 1 (Unabridged)",
		"02-25", "2", "Lords of the Sith_418m_07s_99h", "Epigraph", "Unknown",
		"Various", "n/a",
	} {
		a, err := store.CreateAuthor(name)
		if a != nil {
			t.Errorf("CreateAuthor(%q) returned row %+v; want none", name, a)
		}
		if !errors.Is(err, ErrImplausibleAuthorName) {
			t.Errorf("CreateAuthor(%q) err = %v; want ErrImplausibleAuthorName", name, err)
			continue
		}
		var typed *ImplausibleAuthorNameError
		if !errors.As(err, &typed) || typed.Name != name || typed.Reason == "" {
			t.Errorf("CreateAuthor(%q) err = %#v; want *ImplausibleAuthorNameError naming the input and a reason", name, err)
		}
		if got, _ := store.GetAuthorByName(name); got != nil {
			t.Errorf("CreateAuthor(%q) left a row behind: %+v", name, got)
		}
	}
}

// TestCreateAuthor_CanonicalUnknownAuthorStillWorks: the repair paths
// (author-id-repair, the primary repoint, the entities handler) fall back to
// UnknownAuthorName on purpose. The gate refuses "Unknown Author" as a PARSED
// name, so the store must exempt the canonical placeholder or those paths break.
func TestCreateAuthor_CanonicalUnknownAuthorStillWorks(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	if ok, _ := personname.IsPlausibleAuthorName(UnknownAuthorName); ok {
		t.Fatalf("precondition: the gate is expected to refuse %q as a parsed name", UnknownAuthorName)
	}
	a, err := store.CreateAuthor(UnknownAuthorName)
	if err != nil || a == nil || a.Name != UnknownAuthorName {
		t.Fatalf("CreateAuthor(UnknownAuthorName) = %+v, %v; want the placeholder row", a, err)
	}
	again, err := store.CreateAuthor(UnknownAuthorName)
	if err != nil || again == nil || again.ID != a.ID {
		t.Fatalf("second CreateAuthor(UnknownAuthorName) = %+v, %v; want the same row %d", again, err, a.ID)
	}
}

// TestCreateAuthor_AcceptsRealNames guards the other direction: pen names and
// initials that a shape-based gate could refuse by accident.
func TestCreateAuthor_AcceptsRealNames(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	for _, name := range []string{"Zogarth", "pirateaba", "RavensDagger", "Radclyffe", "Jae", "J. N. Chaney", "M.E. Thorne"} {
		a, err := store.CreateAuthor(name)
		if err != nil || a == nil {
			t.Errorf("CreateAuthor(%q) = %+v, %v; want a row", name, a, err)
		}
	}
}
