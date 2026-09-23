// file: internal/database/narrator_credit_sync.go
// version: 1.0.0
// guid: 77463a64-b525-48cd-ae77-98e41f13a135
// last-edited: 2026-09-22

package database

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// NarratorCreditWriter is the slice of the store SyncBookNarratorsFromCredit
// needs. CreateNarrator is resolve-or-create, so no separate lookup is needed.
type NarratorCreditWriter interface {
	CreateNarrator(name string) (*Narrator, error)
	GetBookNarrators(bookID string) ([]BookNarrator, error)
	SetBookNarrators(bookID string, narrators []BookNarrator) error
}

// SyncBookNarratorsFromCredit makes the book_narrators junction match a
// narrator credit string, one row per person.
//
// Providers and file tags deliver a cast as ONE joined string ("Kate Reading,
// Michael Kramer"). Callers used to store only that string in book.Narrator and
// leave the junction alone, so every cast became a single narrator entity named
// after the whole list (157 of 961 in prod on 2026-09-22) and ABS showed one
// chip for eight people. The string is split with util.SplitCreditNames, the
// same splitter the read side uses, which keeps "Surname, Given" whole.
//
// The junction is REPLACED, not merged: it is the per-person spelling of the
// credit string the caller just wrote, so a narrator the new credit drops must
// go too, or the two disagree. Callers must already have honoured the narrator
// field lock before calling this.
//
// An empty credit is a no-op: no credit is not "no narrators". A credit whose
// people already match the junction in order writes nothing. It returns true
// when it wrote.
func SyncBookNarratorsFromCredit(s NarratorCreditWriter, bookID, credit string) (bool, error) {
	if strings.TrimSpace(credit) == "" {
		return false, nil
	}
	var rows []BookNarrator
	for _, name := range util.SplitCreditNames(credit) {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		n, err := s.CreateNarrator(name)
		if err != nil {
			return false, fmt.Errorf("resolve narrator %q: %w", name, err)
		}
		if n == nil {
			return false, fmt.Errorf("resolve narrator %q: store returned no narrator", name)
		}
		role := "narrator"
		if len(rows) > 0 {
			role = "co-narrator"
		}
		rows = append(rows, BookNarrator{BookID: bookID, NarratorID: n.ID, Role: role, Position: len(rows)})
	}
	if len(rows) == 0 {
		return false, nil
	}
	existing, err := s.GetBookNarrators(bookID)
	if err != nil {
		return false, fmt.Errorf("read book narrators: %w", err)
	}
	if sameNarratorOrder(existing, rows) {
		return false, nil
	}
	if err := s.SetBookNarrators(bookID, rows); err != nil {
		return false, fmt.Errorf("write book narrators: %w", err)
	}
	return true, nil
}

// sameNarratorOrder reports whether two junctions credit the same narrators in
// the same order. Roles follow position, so IDs are all that can differ.
func sameNarratorOrder(a, b []BookNarrator) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].NarratorID != b[i].NarratorID {
			return false
		}
	}
	return true
}
