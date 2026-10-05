// file: internal/database/credits_store.go
// version: 1.1.0
// guid: 3ef0e962-a1f0-4d40-a589-c54d7e618523
// last-edited: 2026-10-04

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// The store side of credit lists (see credits.go and
// docs/plans/2026-10-04-author-narrator-credit-lists-audit.md, PR 1).
//
// Locks. A book's credits live under two keys, book_authors:<id> and
// book_narrators:<id>, each with its own stripe set. The full order is
//
//	book -> owner -> book_authors -> book_narrators
//
// and it stays acyclic only while nothing that holds a later stripe takes an
// earlier one. ModifyBookCredits takes book, book_authors and book_narrators
// (no owner: it writes no book_file row); DeleteBook takes all four; the
// Set/Modify calls for one list take only that list's stripe. Callbacks run
// under the stripes and must stay pure: no store writes, no I/O.

// BookCreditsReader reads books' resolved credit lists in one call.
type BookCreditsReader interface {
	// GetBookCredits returns every requested book's credits, keyed by book
	// ID; a book with no credits (or no row) maps to an empty BookCredits.
	GetBookCredits(ctx context.Context, bookIDs []string) (map[string]BookCredits, error)
}

// BookCreditsModifier changes a book's credits without lost updates.
type BookCreditsModifier interface {
	ModifyBookCredits(bookID string, fn func(book *Book, credits *BookCreditsEdit) error) (*Book, BookCreditsEdit, error)
	ModifyBookNarrators(bookID string, fn func([]BookNarrator) ([]BookNarrator, error)) ([]BookNarrator, error)
}

var (
	_ BookCreditsReader   = (*PebbleStore)(nil)
	_ BookCreditsModifier = (*PebbleStore)(nil)
)

// BookCreditsEdit is a book's two credit lists as a ModifyBookCredits
// callback sees and edits them: raw join rows, not resolved records.
type BookCreditsEdit struct {
	Authors   []BookAuthor
	Narrators []BookNarrator
}

// lockBookNarrators takes the book_narrators stripe for bookID; see the lock
// order above.
func (p *PebbleStore) lockBookNarrators(bookID string) func() {
	mu := &p.bookNarratorLocks[stripeFor(bookID)]
	mu.Lock()
	return mu.Unlock
}

// ErrSkipBookNarratorsWrite, returned by a ModifyBookNarrators callback,
// means "nothing to change": nothing is written and the rows are returned as
// read, with a nil error.
var ErrSkipBookNarratorsWrite = errors.New("skip book_narrators write")

// ModifyBookNarrators is the lost-update-safe way to change a book's narrator
// credits, the twin of ModifyBookAuthors. Under the book's book_narrators
// stripe it reads the list, hands fn a copy, and writes what fn returns,
// normalised. Two callers each adding a narrator cannot both read [A] and
// have the second write drop the first's addition, which a caller-side
// GetBookNarrators -> merge -> SetBookNarrators cannot guarantee. fn must not
// call any credit write. An error from fn aborts without writing.
func (p *PebbleStore) ModifyBookNarrators(bookID string, fn func([]BookNarrator) ([]BookNarrator, error)) ([]BookNarrator, error) {
	unlock := p.lockBookNarrators(bookID)
	defer unlock()
	current, err := p.GetBookNarrators(bookID)
	if err != nil {
		return nil, err
	}
	next, err := fn(append([]BookNarrator(nil), current...))
	if err != nil {
		if errors.Is(err, ErrSkipBookNarratorsWrite) {
			return current, nil
		}
		return nil, err
	}
	return p.setBookNarratorsLocked(bookID, next)
}

// ModifyBookCredits changes a book row and both of its credit lists as one
// write. Under the book's write stripe and both credit stripes it reads the
// row and the two lists, hands them to fn to edit in place, and commits the
// row and the (normalised) lists in ONE Pebble batch, through the same
// update path as ModifyBook. A crash cannot leave the row and its credits
// from different writes, and no other writer of the row or either list can
// land between the read and the commit.
//
// It composes with ModifyBook rather than duplicating it: the row goes
// through updateBookLockedMode, so whatever that path enforces (the
// memdb-stripped field guard, the series invariant, version snapshots and,
// once the approved ModifyBook migration adds it, the row-version check)
// applies here too. The credits are added to that path's batch through
// bookWriteOpts.stage.
//
// After the commit the credit lists are mirrored into memdb and the book is
// queued for search reindexing (BooksNeedReindex), so a credit-only change
// reaches the search index; ReplaceBook*InMemDB alone only invalidates the
// result cache.
//
// It does NOT run the Narrator-column -> junction sync that ModifyBook runs:
// the narrator list is whatever fn leaves in credits.Narrators. Nor does it
// derive Book.AuthorID or Book.Narrator from the lists; that is PR 4 of the
// plan. A caller that changes one must keep the other in step itself.
//
// It returns (nil, BookCreditsEdit{}, nil) when the book does not exist (fn
// is not called). An error from fn aborts without writing and is returned
// as-is, except ErrSkipBookWrite, which returns the row and lists as read
// with a nil error.
func (p *PebbleStore) ModifyBookCredits(bookID string, fn func(book *Book, credits *BookCreditsEdit) error) (*Book, BookCreditsEdit, error) {
	unlockBook := p.lockBook(bookID)
	defer unlockBook()
	unlockAuthors := p.lockBookAuthors(bookID)
	defer unlockAuthors()
	unlockNarrators := p.lockBookNarrators(bookID)
	defer unlockNarrators()

	fresh, err := p.GetBookByID(bookID)
	if err != nil {
		return nil, BookCreditsEdit{}, err
	}
	if fresh == nil {
		return nil, BookCreditsEdit{}, nil
	}
	authors, err := p.GetBookAuthors(bookID)
	if err != nil {
		return nil, BookCreditsEdit{}, fmt.Errorf("read book authors of %s: %w", bookID, err)
	}
	narrators, err := p.GetBookNarrators(bookID)
	if err != nil {
		return nil, BookCreditsEdit{}, fmt.Errorf("read book narrators of %s: %w", bookID, err)
	}
	asRead := BookCreditsEdit{Authors: authors, Narrators: narrators}
	edit := BookCreditsEdit{
		Authors:   append([]BookAuthor(nil), authors...),
		Narrators: append([]BookNarrator(nil), narrators...),
	}
	if err := fn(fresh, &edit); err != nil {
		if errors.Is(err, ErrSkipBookWrite) {
			return fresh, asRead, nil
		}
		return nil, BookCreditsEdit{}, err
	}

	// The tie-breaker is the row as fn left it: a callback that changes the
	// primary author and the credits together orders them by the new one.
	newAuthors, authorData, err := encodeBookAuthors(bookID, edit.Authors, fresh.AuthorID)
	if err != nil {
		return nil, BookCreditsEdit{}, err
	}
	newNarrators, narratorData, err := encodeBookNarrators(bookID, edit.Narrators)
	if err != nil {
		return nil, BookCreditsEdit{}, err
	}
	stage := func(b *pebble.Batch) error {
		if err := b.Set(bookAuthorsKey(bookID), authorData, nil); err != nil {
			return fmt.Errorf("stage book_authors:%s: %w", bookID, err)
		}
		if err := b.Set(bookNarratorsKey(bookID), narratorData, nil); err != nil {
			return fmt.Errorf("stage book_narrators:%s: %w", bookID, err)
		}
		return nil
	}
	updated, err := p.updateBookLockedMode(bookID, fresh, bookWriteOpts{stage: stage})
	if err != nil {
		return nil, BookCreditsEdit{}, err
	}
	p.ReplaceBookAuthorsInMemDB(bookID, newAuthors)
	p.ReplaceBookNarratorsInMemDB(bookID, newNarrators)
	p.notifyBooksNeedReindex(bookID)
	return updated, BookCreditsEdit{Authors: newAuthors, Narrators: newNarrators}, nil
}

// GetBookCredits returns the resolved, position-ordered credit lists of every
// book in bookIDs. Each requested ID is a key in the result (an empty
// BookCredits when the book credits no one).
//
// Rows are read in canonical order (NormalizeBookAuthors /
// NormalizeBookNarrators), the same order a write would store them in: a list
// stored before positions were normalised on write, such as the all-zero
// rows an old copy path wrote, comes back with the book's AuthorID first
// among tied rows and the rest in stored order. An author id that redirects through a
// tombstone resolves to its canonical author; two rows that resolve to the
// same author are listed once, at the first position. A row whose record no
// longer exists is left out, and the positions returned are renumbered
// 0..n-1 over what remains.
//
// There is deliberately no fallback to Book.AuthorID or Book.Narrator: the
// lists are the truth, and the plan's backfill (PR 3) fills the books whose
// lists are empty before any reader moves onto this call.
//
// Each distinct author and narrator is read once per call. A read error is an
// error, never a shorter list.
func (p *PebbleStore) GetBookCredits(ctx context.Context, bookIDs []string) (map[string]BookCredits, error) {
	out := make(map[string]BookCredits, len(bookIDs))
	authorCache := map[int]*Author{}
	narratorCache := map[int]*Narrator{}
	for _, bookID := range bookIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, done := out[bookID]; done {
			continue
		}
		credits, err := p.bookCredits(bookID, authorCache, narratorCache)
		if err != nil {
			return nil, err
		}
		out[bookID] = credits
	}
	return out, nil
}

// bookCredits resolves one book's credits for GetBookCredits, reading each
// author and narrator through the call's caches.
func (p *PebbleStore) bookCredits(bookID string, authorCache map[int]*Author, narratorCache map[int]*Narrator) (BookCredits, error) {
	credits := BookCredits{Authors: []CreditedAuthor{}, Narrators: []CreditedNarrator{}}

	authorRows, err := p.GetBookAuthors(bookID)
	if err != nil {
		return BookCredits{}, fmt.Errorf("GetBookCredits: book authors of %s: %w", bookID, err)
	}
	// Rows stored before writes were normalised can still tie on position;
	// order them exactly as the next write would store them.
	var primary *int
	if len(authorRows) > 1 {
		if primary, err = p.bookPrimaryAuthorID(bookID); err != nil {
			return BookCredits{}, fmt.Errorf("GetBookCredits: %w", err)
		}
	}
	seenAuthor := map[int]bool{}
	for _, row := range NormalizeBookAuthors(authorRows, primary) {
		a, cached := authorCache[row.AuthorID]
		if !cached {
			a, err = p.GetAuthorByID(row.AuthorID)
			if err != nil {
				return BookCredits{}, fmt.Errorf("GetBookCredits: author %d of %s: %w", row.AuthorID, bookID, err)
			}
			authorCache[row.AuthorID] = a
		}
		if a == nil || seenAuthor[a.ID] {
			continue
		}
		seenAuthor[a.ID] = true
		credits.Authors = append(credits.Authors, CreditedAuthor{Author: *a, Role: row.Role, Position: len(credits.Authors)})
	}

	narratorRows, err := p.GetBookNarrators(bookID)
	if err != nil {
		return BookCredits{}, fmt.Errorf("GetBookCredits: book narrators of %s: %w", bookID, err)
	}
	for _, row := range NormalizeBookNarrators(narratorRows) {
		n, cached := narratorCache[row.NarratorID]
		if !cached {
			n, err = p.GetNarratorByID(row.NarratorID)
			if err != nil {
				return BookCredits{}, fmt.Errorf("GetBookCredits: narrator %d of %s: %w", row.NarratorID, bookID, err)
			}
			narratorCache[row.NarratorID] = n
		}
		if n == nil {
			continue
		}
		credits.Narrators = append(credits.Narrators, CreditedNarrator{Narrator: *n, Role: row.Role, Position: len(credits.Narrators)})
	}
	return credits, nil
}
