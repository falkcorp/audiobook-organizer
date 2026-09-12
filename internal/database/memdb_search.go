// file: internal/database/memdb_search.go
// version: 1.3.0
// guid: 7b1d9c34-2e58-4a07-9f61-3c8ad5e0b742
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// searchIDsPreallocMax bounds the result slice's initial capacity. It is a
// hint, not a cap on results: a no-limit query appends past it.
const searchIDsPreallocMax = 64

// getBookRowForSearch reads one book: row and decodes it, with no book_sig:
// hydration and no memdb involvement.
//
// This is deliberately NOT GetBookByID. That is the full-fidelity read and folds
// in the book_sig: sidecar; SearchBooks' disk scan does a bare json.Unmarshal of
// the row and therefore returns books without signatures. Search results are
// serialized straight into API responses, and BookSigV1 alone is ~22KB of base64
// per book — routing search hits through the hydrating read would have quietly
// re-inflated the payload that PR #3125 had just cut.
//
// A missing row returns (nil, nil): between the memdb match and this read, a
// concurrent delete can land, and a search should skip that book rather than
// fail.
func (p *PebbleStore) getBookRowForSearch(id string) (*Book, error) {
	value, closer, err := p.db.Get([]byte("book:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var book Book
	if err := json.Unmarshal(value, &book); err != nil {
		return nil, err
	}
	return &book, nil
}

// SubstringSearchMatches is THE SearchBooks predicate: a whole-query substring
// match over title, primary-author name and narrator. It is the single copy
// shared by the Pebble disk scan, the memdb scan below, and the audiobooks
// service's author_id/series_id-scoped fallback search, so all three decide
// "does this book match" identically.
//
// authorNames maps author id to util.NormalizeAuthor(name) (lower + TrimSpace);
// lowerQuery is strings.ToLower(query). Title and narrator compare against a
// bare strings.ToLower. That asymmetry is pre-existing and kept on purpose —
// see the quirk list on SearchBookIDs.
func SubstringSearchMatches(title string, narrator *string, authorID *int, authorNames map[int]string, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(title), lowerQuery) {
		return true
	}
	if authorID != nil {
		if name, ok := authorNames[*authorID]; ok && strings.Contains(name, lowerQuery) {
			return true
		}
	}
	return narrator != nil && strings.Contains(strings.ToLower(*narrator), lowerQuery)
}

// SearchBookIDs runs the library search predicate against the in-memory book
// table and returns the matching book IDs, in the same order and with the same
// pagination semantics as PebbleStore.SearchBooks' on-disk scan.
//
// Why IDs and not Books: memdb holds a *projection*. stripBookForMemdb clears
// Description, VersionNotes and the BookSig* family before insertion, so
// returning these rows directly would silently blank a description that the
// Pebble scan returns in full. The strip comment names the contract — callers
// needing the whole record fetch it from Pebble — so matching happens here and
// hydration happens at the call site.
//
// This works only because the three fields the predicate reads (Title,
// Narrator, AuthorID) are exactly the ones stripBookForMemdb does NOT clear. If
// a future strip touches any of them, this fast path starts returning fewer
// results than the disk scan and must be revisited.
//
// The predicate below is a deliberate transcription of the Pebble scan, quirks
// included, because SearchBooks has callers beyond the ABS UI (the iTunes
// handler overfetches through it) and a "tidier" predicate here would silently
// change what they match:
//
//   - Author names compare against util.NormalizeAuthor (lower + TrimSpace)
//     while Title and Narrator compare against a bare strings.ToLower. The
//     query itself is only lowercased. That asymmetry is pre-existing; it is
//     reproduced rather than fixed so this change stays a pure speed-up.
//   - `count` increments only inside the match branch, so `offset` skips the
//     first N *matches*, not the first N books.
//   - Soft-deleted books are NOT excluded. The disk scan does not filter them,
//     so neither does this. (Worth revisiting — separately.)
func (m *MemStore) SearchBookIDs(query string, limit, offset int) ([]string, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	// Author id -> normalized name. The disk scan rebuilds this map from a full
	// author: iteration on every call; here it is a walk of an in-memory table.
	authIter, err := txn.Get(memTableAuthors, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb search: authors: %w", err)
	}
	authorNames := make(map[int]string, 4096)
	for obj := authIter.Next(); obj != nil; obj = authIter.Next() {
		a, ok := obj.(*Author)
		if !ok {
			continue
		}
		authorNames[a.ID] = util.NormalizeAuthor(a.Name)
	}

	// memIdxID orders by the book's ULID, the same lexicographic order the
	// Pebble book: keyspace iterates in. That equivalence is what makes the
	// limit early-exit below select the same rows as the disk scan.
	iter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb search: books: %w", err)
	}

	lowerQuery := strings.ToLower(query)

	// A CONSTANT capacity — deliberately not derived from `limit`.
	//
	// The first cut sized this from limit, which is wrong twice over. `limit`
	// reaches SearchBooks from three call sites and is not validated at all of
	// them, and a negative value makes make([]string, 0, limit) panic with
	// "makeslice: cap out of range" — the Pebble scan never preallocated, so the
	// fast path introduced that. Clamping it fixed the panic but kept the
	// allocation size flowing from request input, which CodeQL still flags
	// (go/uncontrolled-allocation-size) and which is a fair reading: the clamp is
	// one edit away from being wrong again.
	//
	// There is nothing to trade off. The win in this function is not allocating
	// the result slice once instead of a few times — it is not unmarshalling
	// ~121K book rows off disk. A fixed hint gets the same speed with no
	// user-controlled size anywhere; append grows past it when a no-limit query
	// matches more.
	ids := make([]string, 0, searchIDsPreallocMax)
	var count int

	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b, ok := obj.(*Book)
		if !ok {
			continue
		}

		if SubstringSearchMatches(b.Title, b.Narrator, b.AuthorID, authorNames, lowerQuery) {
			// limit == 0 means "no limit" (return all matches).
			if count >= offset && (limit == 0 || len(ids) < limit) {
				ids = append(ids, b.ID)
			}
			count++
			if limit > 0 && len(ids) >= limit {
				break
			}
		}
	}

	return ids, nil
}
