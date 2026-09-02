// file: internal/maintenance/jobs/dedup_books_softdelete_test.go
// version: 1.0.0
// guid: 9a1f3c5e-7b2d-4e64-a8f0-1c3e5a7b9d2f
// last-edited: 2026-09-02

package jobs

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// softDeleteProbe is the narrowest bookSoftDeleter: it records every write and
// fails UpdateBook on demand. It has no DeleteBook method at all, so this file
// also fails to compile if anyone widens bookSoftDeleter back to include one.
type softDeleteProbe struct {
	book      *database.Book
	failWrite error
	updates   int
	lastWrite *database.Book
}

func (p *softDeleteProbe) GetBookByID(string) (*database.Book, error) {
	if p.book == nil {
		return nil, nil
	}
	cp := *p.book
	return &cp, nil
}

func (p *softDeleteProbe) UpdateBook(_ string, b *database.Book) (*database.Book, error) {
	p.updates++
	if p.failWrite != nil {
		return nil, p.failWrite
	}
	p.lastWrite = b
	return b, nil
}

var _ bookSoftDeleter = (*softDeleteProbe)(nil)

func TestDDSoftDeleteBook_SetsFlagAndTimestamp(t *testing.T) {
	p := &softDeleteProbe{book: &database.Book{ID: "b1", Title: "x"}}
	if err := ddSoftDeleteBook(p, "b1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.updates != 1 || p.lastWrite == nil {
		t.Fatalf("want exactly one write, got %d", p.updates)
	}
	if p.lastWrite.MarkedForDeletion == nil || !*p.lastWrite.MarkedForDeletion || p.lastWrite.MarkedForDeletionAt == nil {
		t.Fatal("soft-delete must set MarkedForDeletion=true and MarkedForDeletionAt")
	}
}

// A failed UpdateBook is returned to the caller — never swallowed, never
// turned into a hard delete. The phase-1 loop in ddDedupBooks logs and skips
// the book on this error instead of counting it as retired.
func TestDDSoftDeleteBook_UpdateFails_ReturnsWrappedError(t *testing.T) {
	boom := errors.New("pebble: write stalled")
	p := &softDeleteProbe{book: &database.Book{ID: "b2"}, failWrite: boom}
	err := ddSoftDeleteBook(p, "b2")
	if err == nil {
		t.Fatal("a failed soft-delete must be reported, not returned as success")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error must wrap the store error; got %v", err)
	}
}

func TestDDSoftDeleteBook_AlreadyGone_IsNoop(t *testing.T) {
	p := &softDeleteProbe{}
	if err := ddSoftDeleteBook(p, "missing"); err != nil {
		t.Fatalf("missing row must be a no-op, got %v", err)
	}
	if p.updates != 0 {
		t.Fatalf("no write expected, got %d", p.updates)
	}
}
