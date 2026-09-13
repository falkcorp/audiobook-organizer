// file: internal/database/author_primary_repoint.go
// version: 1.0.0
// guid: 3c9e61d4-8a27-4f5b-b0e3-7d2a4c16f958
// last-edited: 2026-09-13

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"
)

// UnknownAuthorName is the placeholder author a book falls back to when every
// author it credited has been deleted. It is resolved through the author:name
// index (GetAuthorByName), never by scanning for rows with this name: production
// has held more than one row named "Unknown Author", and only the one the index
// resolves is reachable by name.
const UnknownAuthorName = "Unknown Author"

// ErrNoPrimaryAuthorSuccessor is returned when a book's primary author is being
// removed and no successor can be chosen -- not even the Unknown Author
// placeholder (it is the row being deleted, or it could not be resolved or
// created). The caller must refuse the delete: the scalar is never cleared.
var ErrNoPrimaryAuthorSuccessor = errors.New("no successor for book primary author")

// NextPrimaryAuthorID picks the author that replaces excluded as a book's
// denormalized primary (Book.AuthorID), by join-row position: the lowest-position
// row in joins whose author is not excluded and still resolves. resolve maps an
// author id to the live id it should be written as (a tombstoned id resolves to
// its canonical row) and reports false for an id that does not resolve.
//
// It returns (0, false) when no row qualifies; the caller then falls back to the
// Unknown Author placeholder. It never returns 0 with true.
func NextPrimaryAuthorID(joins []BookAuthor, excluded int, resolve func(int) (int, bool)) (int, bool) {
	sorted := make([]BookAuthor, len(joins))
	copy(sorted, joins)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	for _, ba := range sorted {
		if ba.AuthorID <= 0 || ba.AuthorID == excluded {
			continue
		}
		live, ok := resolve(ba.AuthorID)
		if !ok || live <= 0 || live == excluded {
			continue
		}
		return live, true
	}
	return 0, false
}

// resolveLiveAuthorID reports the live author id for id: the row itself, or the
// canonical row a tombstone redirects to. GetAuthorByID reads Pebble directly.
func (p *PebbleStore) resolveLiveAuthorID(id int) (int, bool) {
	a, err := p.GetAuthorByID(id)
	if err != nil || a == nil || a.ID <= 0 {
		return 0, false
	}
	return a.ID, true
}

// repointPrimaryScalarsBeforeDelete moves Book.AuthorID/Book.Author off
// authorID for every book whose scalar still names it, before DeleteAuthor
// removes the row. The successor is the book's next author by join-row position
// (NextPrimaryAuthorID); when none remains it is the Unknown Author placeholder
// the name index resolves, created if absent. The scalar is never cleared.
//
// 🔴 WHY THIS IS NOT IN DeleteAuthor's BATCH. A book write is not one key: it is
// UpdateBook's book_ver: CoW snapshot, the preserve-on-nil guard for nine fields
// stripped from the memdb projection, the path/at-path/hash/versiongroup/work/
// ISBN index families, the signature sidecar, and the memdb upsert -- all staged
// and committed by UpdateBook itself. Hand-staging the book row into the author
// batch would bypass every one of those. So the repoint runs FIRST, through
// UpdateBook, and the delete batch commits after it. The order is the safety
// argument: if the delete then fails, each repointed book names a live author,
// which is a valid state; the reverse order would leave exactly the dangling id
// this exists to prevent. Any repoint failure aborts before the delete.
//
// It runs before DeleteAuthor takes nameIdx.author, because resolving the
// placeholder can call CreateAuthor, which takes that same lock.
//
// The candidate list is GetBooksByAuthorIDForRelinkCore (junction ∪ legacy
// scalar, trash included; memdb when complete, Pebble otherwise), and each
// candidate's scalar is re-read from Pebble via GetBookByID before it is
// written. A book whose scalar memdb does not know about is not seen here;
// maintenance.author-id-repair's phase (a) is the Pebble-authoritative sweep for
// anything that slips past.
func (p *PebbleStore) repointPrimaryScalarsBeforeDelete(authorID int) error {
	cands, err := p.GetBooksByAuthorIDForRelinkCore(authorID)
	if err != nil {
		return fmt.Errorf("list books crediting author %d before delete: %w", authorID, err)
	}
	seen := make(map[string]struct{}, len(cands))
	unknownID := 0
	for _, c := range cands {
		if _, dup := seen[c.ID]; dup {
			continue
		}
		seen[c.ID] = struct{}{}
		// Decided on the Pebble row, not the candidate's projection: a
		// projection stale in either direction must not decide a write.
		full, gErr := p.GetBookByID(c.ID)
		if gErr != nil {
			return fmt.Errorf("read book %s before author delete: %w", c.ID, gErr)
		}
		if full == nil || full.AuthorID == nil || *full.AuthorID != authorID {
			continue
		}
		joins, jErr := p.GetBookAuthors(c.ID)
		if jErr != nil {
			return fmt.Errorf("read authors of book %s before author delete: %w", c.ID, jErr)
		}
		next, ok := NextPrimaryAuthorID(joins, authorID, p.resolveLiveAuthorID)
		if !ok {
			if unknownID == 0 {
				id, uErr := p.resolveUnknownAuthorID(true)
				if uErr != nil {
					return uErr
				}
				unknownID = id
			}
			if unknownID == authorID {
				return fmt.Errorf("%w: book %s has no other author and author %d is the %q placeholder itself; refusing to delete it",
					ErrNoPrimaryAuthorSuccessor, c.ID, authorID, UnknownAuthorName)
			}
			next = unknownID
		}
		target, tErr := p.GetAuthorByID(next)
		if tErr != nil || target == nil {
			return fmt.Errorf("%w: successor author %d for book %s did not resolve (err=%v)",
				ErrNoPrimaryAuthorSuccessor, next, c.ID, tErr)
		}
		full.AuthorID = &target.ID
		full.Author = target
		if _, uErr := p.UpdateBook(c.ID, full); uErr != nil {
			return fmt.Errorf("repoint primary author of book %s from %d to %d: %w", c.ID, authorID, target.ID, uErr)
		}
	}
	return nil
}

// resolveUnknownAuthorID returns the id the name index resolves for
// UnknownAuthorName, creating the row when create is true and none exists.
// It must not be called while nameIdx.author is held.
func (p *PebbleStore) resolveUnknownAuthorID(create bool) (int, error) {
	u, err := p.GetAuthorByName(UnknownAuthorName)
	if err != nil {
		return 0, fmt.Errorf("resolve %q author: %w", UnknownAuthorName, err)
	}
	if u == nil && create {
		u, err = p.CreateAuthor(UnknownAuthorName)
		if err != nil {
			return 0, fmt.Errorf("create %q author: %w", UnknownAuthorName, err)
		}
	}
	if u == nil || u.ID <= 0 {
		return 0, fmt.Errorf("%w: %q author does not resolve", ErrNoPrimaryAuthorSuccessor, UnknownAuthorName)
	}
	return u.ID, nil
}

// GetBookIDsCreditingAuthorDurable returns, sorted, the id of every book that
// credits authorID through EITHER a book_authors junction row OR the legacy
// Book.AuthorID scalar, in any state (trashed, non-primary), read straight from
// Pebble and never from memdb. Junction rows whose book row is gone are included:
// they are credits too.
//
// It is the durable half of a delete-when-empty check. The other half is
// GetBooksByAuthorIDForRelinkCore, which answers from memdb when memdb is
// complete; memdb and Pebble can diverge, and a delete must never rest on a count
// only one of them can see. It fails closed: an undecodable row is an error,
// never a skip, because the skipped row may be the credit.
//
// Cost: one scan of the book_authors keyspace plus one of the book: rows.
func (p *PebbleStore) GetBookIDsCreditingAuthorDurable(authorID int) ([]string, error) {
	ids := make(map[string]struct{})
	prefix := []byte("book_authors:")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, fmt.Errorf("durable author credit scan: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		val, vErr := iter.ValueAndErr()
		if vErr != nil {
			_ = iter.Close()
			return nil, fmt.Errorf("durable author credit scan: read book_authors row: %w", vErr)
		}
		var authors []BookAuthor
		if uErr := json.Unmarshal(val, &authors); uErr != nil {
			key := string(iter.Key())
			_ = iter.Close()
			return nil, fmt.Errorf("durable author credit scan: undecodable row %q: %w", key, uErr)
		}
		for _, a := range authors {
			if a.AuthorID == authorID {
				ids[string(iter.Key()[len(prefix):])] = struct{}{}
				break
			}
		}
	}
	if iErr := iter.Error(); iErr != nil {
		_ = iter.Close()
		return nil, fmt.Errorf("durable author credit scan truncated over book_authors: %w", iErr)
	}
	if cErr := iter.Close(); cErr != nil {
		return nil, fmt.Errorf("durable author credit scan: close iterator: %w", cErr)
	}

	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {
		var b Book
		if uErr := json.Unmarshal(rowValue, &b); uErr != nil {
			return fmt.Errorf("durable author credit scan: undecodable book row %q: %w", rowID, uErr)
		}
		if b.AuthorID != nil && *b.AuthorID == authorID {
			id := b.ID
			if id == "" {
				id = rowID
			}
			ids[id] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
