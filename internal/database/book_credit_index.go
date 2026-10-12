// file: internal/database/book_credit_index.go
// version: 1.1.0
// guid: 5b3e9d17-42c8-4f60-9a1e-7d0c6b284fa3
// last-edited: 2026-10-12

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// BookCreditIndexStore is a narrow capability interface, deliberately kept OUT
// of database.Store (widening Store forces every implementation and generated
// mock to grow with it). Reach it through AsCapability, which looks through the
// search-index decorator.
type BookCreditIndexStore interface {
	// GetAllBookAuthorRoleCredits returns bookID -> that book's author-role
	// rows in the book_authors junction (IsAuthorCreditRole: narrators,
	// editors, translators and the like are left out). A book with no
	// author-role row is absent from the map.
	//
	// The rows are NOT in credit order and may repeat a person under two
	// author roles: order them with NormalizeBookAuthors(rows, book.AuthorID),
	// which breaks position ties with the book's primary author the same way
	// every credit write does. Leaving that to the caller means both the
	// memdb and the Pebble answer are ordered by one function with one
	// primary.
	//
	// It reads every credit row once, so a caller filtering a whole library
	// pays one load per query rather than one store read per book. An
	// undecodable stored row costs only that book its credits; it is logged,
	// never silently dropped, and never fails the whole load.
	GetAllBookAuthorRoleCredits() (map[string][]BookAuthor, error)
}

var bookCreditIndexLog = logger.New("database.book-credit-index")

// memdbShortOnce keeps the "memdb is short, scanning Pebble" error to one line
// per process: while memdb stays short every author: query takes the scan,
// and a line per query would bury everything else in the log.
var memdbShortOnce sync.Once

// undecodableCreditKeys holds the book_authors keys already reported as
// undecodable, so each bad key is logged once per process rather than once
// per author: query.
var undecodableCreditKeys sync.Map

// creditGroupResolvedInMemdb reports whether memdb's copy of one book's
// junction rows can answer "which of these people are credited as an author".
//
// memdb's book_authors primary index is {BookID, AuthorID}, so it holds ONE
// row per person: of two rows for the same person (an author who also
// narrates keeps an "author" and a "narrator" row; see NormalizeBookAuthors)
// the later insert replaces the earlier. Two shapes show the copy may have
// lost a row:
//   - a non-author role. The person's dropped row may have been the author
//     one, and only Pebble still has it.
//   - a position gap. Every credit write renumbers positions 0..n-1, so n
//     rows whose highest position is n or more are missing one.
//
// Either way the book is answered from Pebble instead. Rows from writers
// that predate position renumbering can also show a gap; they take the same
// Pebble read, which is correct and costs one key read per such book.
func creditGroupResolvedInMemdb(rows []BookAuthor) bool {
	maxPos := -1
	for _, r := range rows {
		if !IsAuthorCreditRole(r.Role) {
			return false
		}
		maxPos = max(maxPos, r.Position)
	}
	return maxPos < len(rows)
}

// authorRoleCredits answers GetAllBookAuthorRoleCredits from memdb in one pass.
// unresolved lists the books memdb cannot answer for (see
// creditGroupResolvedInMemdb); the PebbleStore reads those from Pebble. It
// refuses (ErrMemdbIncomplete) when the table is known to be short.
//
// The memIdxID index is keyed BookID first, so each book's rows arrive
// together: they are grouped as they stream, into one shared backing array,
// with no per-book intermediate map.
func (m *MemStore) authorRoleCredits() (credits map[string][]BookAuthor, unresolved []string, err error) {
	if err := m.requireTablesComplete("book credit index", memTableBookAuthors); err != nil {
		return nil, nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()

	it, err := txn.Get(memTableBookAuthors, memIdxID)
	if err != nil {
		return nil, nil, fmt.Errorf("memdb book_authors scan: %w", err)
	}
	// Sized from the previous load: the library barely changes between
	// queries, and growing a 10k-entry map from empty spent about a third of
	// this pass rehashing.
	credits = make(map[string][]BookAuthor, creditIndexBooksHint.Load())
	all := make([]BookAuthor, 0, creditIndexRowsHint.Load())
	cur, start, rowsSeen := "", 0, 0
	flush := func() {
		if cur == "" || start == len(all) {
			return
		}
		group := all[start:len(all):len(all)]
		if creditGroupResolvedInMemdb(group) {
			credits[cur] = group
			start = len(all)
			return
		}
		unresolved = append(unresolved, cur)
		all = all[:start]
	}
	for obj := it.Next(); obj != nil; obj = it.Next() {
		ba := obj.(*BookAuthor)
		if ba.BookID != cur {
			flush()
			cur = ba.BookID
		}
		all = append(all, *ba)
		rowsSeen++
	}
	flush()
	creditIndexBooksHint.Store(int64(len(credits) + len(unresolved)))
	creditIndexRowsHint.Store(int64(rowsSeen))
	return credits, unresolved, nil
}

// creditIndexBooksHint and creditIndexRowsHint carry the size of the last
// memdb credit load to the next one (see authorRoleCredits).
var creditIndexBooksHint, creditIndexRowsHint atomic.Int64

// GetAllBookAuthorRoleCredits is memdb-first. Books whose memdb rows cannot
// settle the author roles are read from Pebble (readAuthorRoleCreditsPebble); a memdb known to
// be short drops to the full Pebble scan; any other memdb error propagates.
func (p *PebbleStore) GetAllBookAuthorRoleCredits() (map[string][]BookAuthor, error) {
	if m := p.memOrFallback("GetAllBookAuthorRoleCredits"); m != nil {
		credits, unresolved, err := m.authorRoleCredits()
		if err == nil {
			if len(unresolved) > 0 {
				if err := p.readAuthorRoleCreditsPebble(credits, unresolved); err != nil {
					return nil, err
				}
			}
			return credits, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		memdbShortOnce.Do(func() {
			bookCreditIndexLog.Error("book credit index: memdb is missing rows and will stay short until restart; every author: filter scans Pebble instead (logged once per process): err=%v lost_rows=%v", err, m.LostRows())
		})
	}
	return p.authorRoleCreditsPebbleScan()
}

// decodeAuthorRoleCredits decodes one book_authors value and keeps its
// author-role rows.
func decodeAuthorRoleCredits(val []byte) ([]BookAuthor, error) {
	var rows []BookAuthor
	if err := json.Unmarshal(val, &rows); err != nil {
		return nil, err
	}
	kept := rows[:0]
	for _, r := range rows {
		if IsAuthorCreditRole(r.Role) {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

// reportUndecodableCredits logs, once per process each, the book_authors keys
// a credit load skipped because their value would not decode. Those books
// lose their co-credits in an author: filter (their primary author still
// matches) until the row is repaired; every other book is unaffected.
func reportUndecodableCredits(skipped map[string]error) {
	if len(skipped) == 0 {
		return
	}
	var fresh []string
	var firstErr error
	for key, err := range skipped {
		if _, seen := undecodableCreditKeys.LoadOrStore(key, struct{}{}); !seen {
			fresh = append(fresh, key)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if len(fresh) == 0 {
		return
	}
	shown := fresh
	if len(shown) > 10 {
		shown = shown[:10]
	}
	bookCreditIndexLog.Error("book credit index: skipped %d undecodable book_authors row(s) (%d newly seen); those books match author: on their primary author only until repaired: keys=%q err=%v", len(skipped), len(fresh), shown, firstErr)
}

// readAuthorRoleCreditsPebble reads the named books' junction rows from Pebble
// on one snapshot and stores their author-role rows in credits. A book with
// no stored row or no author-role row is left out; an undecodable row costs
// only that book (reportUndecodableCredits).
//
// The books are visited in key order with ONE iterator, seeking forward from
// key to key, rather than one point Get each: neighbouring keys share sstable
// blocks, and a forward seek reuses the block the iterator already holds
// where a fresh Get looks it up again. The walk is a single forward pass over
// a bounded key range, the same shape as the full scan below, so it is not
// split across workers. Its cost grows with the number of books that carry a
// non-author credit row: about 2 ms for 500 such books in the 10k-book
// benchmark (BenchmarkAuthorFilterCredits_10k).
func (p *PebbleStore) readAuthorRoleCreditsPebble(credits map[string][]BookAuthor, bookIDs []string) error {
	if len(bookIDs) == 0 {
		return nil
	}
	ids := slices.Clone(bookIDs)
	slices.Sort(ids)

	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()
	prefix := []byte("book_authors:")
	iter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: bookAuthorsKey(ids[0]),
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	skipped := map[string]error{}
	for _, bookID := range ids {
		key := bookAuthorsKey(bookID)
		if !iter.SeekGE(key) || !bytes.Equal(iter.Key(), key) {
			if err := iter.Error(); err != nil {
				return fmt.Errorf("book credit index: read %s: %w", key, err)
			}
			continue // no stored row
		}
		rows, err := decodeAuthorRoleCredits(iter.Value())
		if err != nil {
			skipped[string(key)] = err
			continue
		}
		if len(rows) > 0 {
			stampBookAuthorsInPlace(bookID, rows)
			credits[bookID] = rows
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("book credit index: read of unresolved books truncated: %w", err)
	}
	reportUndecodableCredits(skipped)
	return nil
}

// authorRoleCreditsPebbleScan scans every book_authors:<bookID> row on one
// snapshot. An undecodable row is skipped and reported, so one bad row costs
// only its own book; an iterator error fails the load, because a truncated
// scan would make an author filter miss co-credited books without a word.
func (p *PebbleStore) authorRoleCreditsPebbleScan() (map[string][]BookAuthor, error) {
	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	prefix := []byte("book_authors:")
	iter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	out := make(map[string][]BookAuthor)
	skipped := map[string]error{}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		rows, err := decodeAuthorRoleCredits(iter.Value())
		if err != nil {
			skipped[key] = err
			continue
		}
		if len(rows) > 0 {
			bookID := strings.TrimPrefix(key, "book_authors:")
			stampBookAuthorsInPlace(bookID, rows)
			out[bookID] = rows
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("book credit index: scan over book_authors truncated: %w", err)
	}
	reportUndecodableCredits(skipped)
	return out, nil
}
