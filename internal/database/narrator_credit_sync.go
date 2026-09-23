// file: internal/database/narrator_credit_sync.go
// version: 1.1.0
// guid: 77463a64-b525-48cd-ae77-98e41f13a135
// last-edited: 2026-09-23

package database

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

var narratorSyncLog = logger.New("database.narrator-sync")

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
	rows, err := resolveNarratorCredit(s, bookID, credit)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return setNarratorsIfChanged(s, bookID, rows)
}

// resolveNarratorCredit turns a credit string into junction rows, creating
// narrator entities as needed. It returns nil for an empty credit.
func resolveNarratorCredit(s NarratorCreditWriter, bookID, credit string) ([]BookNarrator, error) {
	if strings.TrimSpace(credit) == "" {
		return nil, nil
	}
	var rows []BookNarrator
	for _, name := range util.SplitCreditNames(credit) {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		n, err := s.CreateNarrator(name)
		if err != nil {
			return nil, fmt.Errorf("resolve narrator %q: %w", name, err)
		}
		if n == nil {
			return nil, fmt.Errorf("resolve narrator %q: store returned no narrator", name)
		}
		role := "narrator"
		if len(rows) > 0 {
			role = "co-narrator"
		}
		rows = append(rows, BookNarrator{BookID: bookID, NarratorID: n.ID, Role: role, Position: len(rows)})
	}
	return rows, nil
}

// setNarratorsIfChanged writes rows unless the junction already credits the
// same narrators in the same order. It returns true when it wrote.
func setNarratorsIfChanged(s NarratorCreditWriter, bookID string, rows []BookNarrator) (bool, error) {
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

// narratorSyncAfterResolveHook, when set by a test, runs between resolving
// the credit and re-taking the stripe: the window a concurrent write can use.
var narratorSyncAfterResolveHook func(bookID string)

// narratorOf returns b's narrator credit, "" when unset.
func narratorOf(b *Book) string {
	if b == nil || b.Narrator == nil {
		return ""
	}
	return *b.Narrator
}

// syncNarratorJunctionAfterWrite keeps book_narrators in step with the
// Narrator column. CreateBook, UpdateBook and ModifyBook call it AFTER
// releasing the book stripe, whenever a write changed the column to a
// non-empty value. Doing it here rather than at each caller is the point: on
// 2026-09-22 only 3 of 17 flows that write the column also wrote the junction,
// and undo paths that restore an old credit would otherwise leave the junction
// on the undone cast, which ABS prefers over the column.
//
// Narrator resolution runs outside the stripe because CreateNarrator takes the
// narrator name-index lock and the ID counter lock. The junction write then
// re-takes the stripe and goes ahead only if the column still holds the credit
// it resolved; if a later write changed it, that write's own sync owns the
// junction. So two racing writes cannot leave the junction on the older cast.
//
// The column stays the truth: a failure here is logged, never returned, since
// the book write it follows has already committed.
func (p *PebbleStore) syncNarratorJunctionAfterWrite(bookID, before, after string) {
	if before == after || strings.TrimSpace(after) == "" {
		return
	}
	rows, err := resolveNarratorCredit(p, bookID, after)
	if err != nil {
		narratorSyncLog.Warn("book %s: narrator credit %q not synced to junction: %v",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(after), err)
		return
	}
	if len(rows) == 0 {
		return
	}
	if narratorSyncAfterResolveHook != nil {
		narratorSyncAfterResolveHook(bookID)
	}
	unlock := p.lockBook(bookID)
	defer unlock()
	cur, err := p.GetBookByID(bookID)
	if err != nil {
		narratorSyncLog.Warn("book %s: re-read before narrator junction sync failed: %v",
			logger.SanitizeLogValue(bookID), err)
		return
	}
	if cur == nil || narratorOf(cur) != after {
		return
	}
	if _, err := setNarratorsIfChanged(p, bookID, rows); err != nil {
		narratorSyncLog.Warn("book %s: narrator junction sync failed: %v",
			logger.SanitizeLogValue(bookID), err)
	}
}
