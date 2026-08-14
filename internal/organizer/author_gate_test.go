// file: internal/organizer/author_gate_test.go
// version: 1.0.0
// guid: 9b3e6c50-2f81-4d47-a5c9-8e0d7f2b4a13
// last-edited: 2026-08-14

package organizer

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TestHasResolvedAuthor pins the gate's definition of "resolved": a real
// non-blank name, populated directly or via AuthorID lookup. Everything that
// would make expandPattern fall back to "Unknown Author" must report false.
func TestHasResolvedAuthor(t *testing.T) {
	o := NewOrganizer(&config.Config{})

	if o.HasResolvedAuthor(&database.Book{}) {
		t.Fatal("nil author must be unresolved")
	}
	if o.HasResolvedAuthor(&database.Book{Author: &database.Author{Name: "   "}}) {
		t.Fatal("blank author name must be unresolved")
	}
	if !o.HasResolvedAuthor(&database.Book{Author: &database.Author{Name: "Brandon Sanderson"}}) {
		t.Fatal("real author must be resolved")
	}

	// AuthorID path resolves via the store.
	id := 7
	o.store = &database.MockStore{GetAuthorByIDFunc: func(gotID int) (*database.Author, error) {
		if gotID != 7 {
			t.Fatalf("looked up author %d, want 7", gotID)
		}
		return &database.Author{ID: 7, Name: "N. K. Jemisin"}, nil
	}}
	if !o.HasResolvedAuthor(&database.Book{AuthorID: &id}) {
		t.Fatal("AuthorID-resolvable author must be resolved")
	}
}

// TestReOrganizeInPlace_RefusesUnresolvedAuthor pins the apply-side gate: an
// in-root rename for a placeholder-author book returns ErrAuthorUnresolved
// BEFORE touching the filesystem.
func TestReOrganizeInPlace_RefusesUnresolvedAuthor(t *testing.T) {
	svc := &Service{}
	_, err := svc.ReOrganizeInPlace(&database.Book{Title: "X", FilePath: "/nonexistent/x.m4b"}, logger.New("test"))
	if !errors.Is(err, ErrAuthorUnresolved) {
		t.Fatalf("err = %v, want ErrAuthorUnresolved", err)
	}
}
