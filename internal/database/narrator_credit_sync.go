// file: internal/database/narrator_credit_sync.go
// version: 1.2.0
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

// narratorCreditStore is the slice of the store the narrator junction sync
// needs. CreateNarrator is resolve-or-create, so no separate lookup is needed.
type narratorCreditStore interface {
	CreateNarrator(name string) (*Narrator, error)
	GetBookNarrators(bookID string) ([]BookNarrator, error)
	SetBookNarrators(bookID string, narrators []BookNarrator) error
	GetBookAuthors(bookID string) ([]BookAuthor, error)
	GetAuthorByID(id int) (*Author, error)
}

// bookAuthorNames returns the names of every author credited on the book: the
// book_authors join plus the author_id column, which can hold a primary author
// the join lacks. A read failure is an error: CleanNarratorCredit drops
// author names from a credit, and an incomplete list would turn an author into
// a narrator.
func bookAuthorNames(s narratorCreditStore, bookID string, primary *int) ([]string, error) {
	ids := make(map[int]bool)
	if primary != nil && *primary > 0 {
		ids[*primary] = true
	}
	links, err := s.GetBookAuthors(bookID)
	if err != nil {
		return nil, fmt.Errorf("read book authors: %w", err)
	}
	for _, l := range links {
		if l.AuthorID > 0 {
			ids[l.AuthorID] = true
		}
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		a, err := s.GetAuthorByID(id)
		if err != nil {
			return nil, fmt.Errorf("read author %d: %w", id, err)
		}
		if a != nil {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

// resolveNarratorCredit turns a credit into junction rows, creating narrator
// entities as needed. The credit goes through util.CleanNarratorCredit first
// (owner rules 2026-09-23: drop translators/editors, strip "By:", drop the
// book's own authors when a real narrator remains). Any verdict other than
// NarratorCreditPeople returns no rows, and the caller leaves the junction
// alone.
func resolveNarratorCredit(s narratorCreditStore, bookID, credit string, bookAuthors []string) ([]BookNarrator, util.NarratorCreditVerdict, error) {
	people, verdict := util.CleanNarratorCredit(credit, bookAuthors)
	if verdict != util.NarratorCreditPeople {
		return nil, verdict, nil
	}
	var rows []BookNarrator
	for _, name := range people {
		n, err := s.CreateNarrator(name)
		if err != nil {
			return nil, verdict, fmt.Errorf("resolve narrator %q: %w", name, err)
		}
		if n == nil {
			return nil, verdict, fmt.Errorf("resolve narrator %q: store returned no narrator", name)
		}
		role := "narrator"
		if len(rows) > 0 {
			role = "co-narrator"
		}
		rows = append(rows, BookNarrator{BookID: bookID, NarratorID: n.ID, Role: role, Position: len(rows)})
	}
	return rows, verdict, nil
}

// setNarratorsIfChanged writes rows unless the junction already credits the
// same narrators in the same order. It returns true when it wrote.
func setNarratorsIfChanged(s narratorCreditStore, bookID string, rows []BookNarrator) (bool, error) {
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
func (p *PebbleStore) syncNarratorJunctionAfterWrite(bookID, before string, written *Book) {
	after := narratorOf(written)
	if before == after || strings.TrimSpace(after) == "" {
		return
	}
	authors, err := bookAuthorNames(p, bookID, written.AuthorID)
	if err != nil {
		narratorSyncLog.Warn("book %s: narrator credit %q not synced, authors unreadable: %v",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(after), err)
		return
	}
	rows, verdict, err := resolveNarratorCredit(p, bookID, after, authors)
	switch verdict {
	case util.NarratorCreditJunk:
		narratorSyncLog.Info("book %s: narrator credit %q is not a list of people; junction left as is",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(after))
		return
	case util.NarratorCreditAllAuthors:
		narratorSyncLog.Info("book %s: narrator credit %q names only the book's authors (self-read or mis-tag); junction left as is",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(after))
		return
	}
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
