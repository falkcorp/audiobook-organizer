// file: internal/database/narrator_bookref.go
// version: 1.0.0
// guid: 36c225c3-b235-4aac-b164-0273255164fd
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Unfiltered NARRATOR reference counting, for the purge-empty-narrators
// deletion decision. The narrator-side twin of author_bookref.go; read that
// file first for why a delete guard must never read a filtered display count.
//
// WHERE A NARRATOR ID CAN BE HELD
//
// A narrator row (narrator:<id>) is referenced by id in exactly one persisted
// place: the per-book credit list stored as a JSON array under
// book_narrators:<bookID>. Every reader of a narrator id in the repo
// (organizer/rename.go, metafetch service_apply.go and service_writeback.go,
// server/server_metadata.go, handlers/audiobooks/handler_crud.go,
// handlers/entities/handler.go) reads it from that junction; there is no
// legacy Book.NarratorID field. The id pass is therefore one keyspace scan,
// and it applies NO book-state filter: trashed books, non-primary versions and
// junction rows whose book row no longer exists all count, because each still
// holds the id and deleting the narrator row would leave it dangling.
//
// THE NAME PASS, AND WHAT IT IS NOT
//
// Book.Narrator is a free-text credit string, not an id reference. Nothing
// resolves an id from it at read time. It IS resolved by name at WRITE time:
// the audiobook update path (audiobooks/service_mutation.go) and the
// optimize-database split (handlers/operations/handler.go) both split it with
// util.SplitCreditNames and call GetNarratorByName, minting a fresh row with
// CreateNarrator when the name has none. So deleting a narrator whose name is
// still in some book's text does not lose the name; it comes back, under a NEW
// id, the next time that book is edited. ByName is therefore kept apart from
// ByID and is only a hold-back signal: a narrator with no junction link whose
// name is still credited in text looks more like a book that lost its
// junction row than like importer junk.
//
// WHY PEBBLE ONLY, AND WHY ONE SNAPSHOT
//
// Unlike the author twin this never answers from the memdb. The memdb
// book_narrators projection has gone stale in production: rows stored without
// a book_id were rejected by its compound primary index and every later memdb
// update of the book failed on them (junction_bookid.go, migration 63). A stale
// projection answers "referenced by nothing" with a nil error, which is the
// permissive answer to a caller that deletes on it. The junction keyspace is
// small (3,480 rows at a recent production warmup), so the authoritative scan
// is cheap. The book pass is the expensive one, and it runs once per op.
//
// Both passes read ONE snapshot, so a book written between them cannot be
// half-seen. See getAllAuthorBookRefCountsPebble for the fail-open direction
// of a torn two-table read.

// NarratorRefs is the answer to "what still refers to each narrator".
type NarratorRefs struct {
	// ByID maps narratorID -> number of distinct books whose book_narrators
	// row names it, in ANY book state, including rows whose book is gone. A
	// narrator absent from the map is referenced by nothing.
	ByID map[int]int
	// ByName maps util.NormalizeAuthor(name) -> number of book rows (any
	// state) whose Narrator text credits that name once split by
	// util.SplitCreditNames. Not an id reference; see the file comment.
	ByName map[string]int
}

// NarratorRefStore is a narrow capability interface, kept OUT of
// database.Store for the same reason as AuthorBookRefStore: widening Store
// forces every implementation and generated mock to grow with it. Reach it
// through AsNarratorRefStore, which looks through the indexedStore decorator.
type NarratorRefStore interface {
	// GetAllNarratorRefs scans the whole book_narrators junction and every
	// book row, unfiltered, from one snapshot.
	GetAllNarratorRefs() (NarratorRefs, error)
	// CountNarratorBookLinks returns how many distinct books' book_narrators
	// rows name narratorID right now. It reads only the junction keyspace, so
	// it is cheap enough to call once per narrator immediately before deleting
	// it; that is its job.
	CountNarratorBookLinks(narratorID int) (int, error)
}

// AsNarratorRefStore returns s as a NarratorRefStore, or nil if the backing
// store cannot answer. Callers MUST nil-check and MUST refuse to delete rather
// than fall back to any filtered count.
//
// It goes through AsCapability, not a bare type assertion, because in
// production the store is wrapped in the Bleve indexedStore decorator and a
// bare `s.(*PebbleStore)` returns nil exactly where the guard matters.
func AsNarratorRefStore(s any) NarratorRefStore {
	if s == nil {
		return nil
	}
	if rs, ok := AsCapability[NarratorRefStore](s); ok {
		return rs
	}
	return nil
}

// NarratorRefCounts returns the unfiltered narrator references for the whole
// library. It fails CLOSED: a store that cannot answer is an error, never an
// empty map, because an empty map reads as "nothing references anything".
func NarratorRefCounts(store any) (NarratorRefs, error) {
	rs := AsNarratorRefStore(store)
	if rs == nil {
		return NarratorRefs{}, fmt.Errorf("store cannot count unfiltered narrator references (got %T); "+
			"refusing to delete narrators from an unverified count", store)
	}
	return rs.GetAllNarratorRefs()
}

// NarratorLinkCount is the per-item re-check: how many books link narratorID
// through book_narrators at this moment. Fails CLOSED like NarratorRefCounts.
func NarratorLinkCount(store any, narratorID int) (int, error) {
	rs := AsNarratorRefStore(store)
	if rs == nil {
		return 0, fmt.Errorf("store cannot re-check narrator %d's book links (got %T); "+
			"refusing to delete it unverified", narratorID, store)
	}
	return rs.CountNarratorBookLinks(narratorID)
}

// narratorRefKey identifies one (book, narrator) attachment, so a credit list
// that repeats a narrator counts that book once.
type narratorRefKey struct {
	bookID     string
	narratorID int
}

// pebbleIterSource is what both *pebble.DB and *pebble.Snapshot provide, so the
// junction scan below serves the snapshot-based bulk count and the live
// per-item re-check from one body.
type pebbleIterSource interface {
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
}

// scanNarratorJunction calls fn for every book_narrators:<bookID> row.
//
// Every guard here fails CLOSED, because an undercount is fail-OPEN for the
// caller (the narrator gets deleted while a book still names it):
//   - UpperBound is prefixUpperBound, never a hand-written "book_narrators:~",
//     which would exclude every non-ASCII book id (author_bookref.go has the
//     full argument; sweepNarratorFromBookNarrators uses the same bound).
//   - An undecodable row is an ERROR, not a skip. It may hold the only link to
//     a narrator. (DeleteNarrator's sweep skips such rows, which is right for
//     a rewrite and wrong for a count.) The key is captured before Close.
//   - iter.Error() is checked after the loop, because the loop exits the same
//     way on end-of-range and on an iteration error.
func scanNarratorJunction(src pebbleIterSource, fn func(bookID string, rows []BookNarrator)) error {
	prefix := []byte("book_narrators:")
	iter, err := src.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return fmt.Errorf("narrator ref scan: open book_narrators iterator: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		val, vErr := iter.ValueAndErr()
		if vErr != nil {
			_ = iter.Close()
			return fmt.Errorf("narrator ref scan: reading book_narrators row %q: %w", key, vErr)
		}
		var rows []BookNarrator
		if err := json.Unmarshal(val, &rows); err != nil {
			_ = iter.Close()
			return fmt.Errorf("narrator ref scan: undecodable book_narrators row %q: %w", key, err)
		}
		fn(strings.TrimPrefix(key, "book_narrators:"), rows)
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return fmt.Errorf("narrator ref scan truncated over book_narrators, refusing to answer from a partial count: %w", err)
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("narrator ref scan: closing book_narrators iterator: %w", err)
	}
	return nil
}

// narratorTextOnly decodes just the field the name pass needs. The tag must
// match Book.Narrator's; TestGetAllNarratorRefs_NameIndexFromBookText writes
// through CreateBook, so a drifted tag fails that test.
type narratorTextOnly struct {
	Narrator *string `json:"narrator"`
}

// GetAllNarratorRefs counts every narrator reference in Pebble with no
// filtering. See the file comment for the two passes and why they share one
// snapshot.
func (p *PebbleStore) GetAllNarratorRefs() (NarratorRefs, error) {
	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	refs := NarratorRefs{ByID: make(map[int]int), ByName: make(map[string]int)}
	seen := make(map[narratorRefKey]bool)
	if err := scanNarratorJunction(snap, func(bookID string, rows []BookNarrator) {
		for _, r := range rows {
			k := narratorRefKey{bookID: bookID, narratorID: r.NarratorID}
			if seen[k] {
				continue
			}
			seen[k] = true
			refs.ByID[r.NarratorID]++
		}
	}); err != nil {
		return NarratorRefs{}, err
	}

	// Every book row, in any state. Bounds are the true "book:" prefix range,
	// and the one-colon filter below is what makes that range safe: it also
	// admits book:path:, book:hash: and book:versiongroup:, whose values are
	// bare ids rather than book JSON. Bounds and filter are one change.
	iter, err := snap.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:"),
		UpperBound: []byte("book;"),
	})
	if err != nil {
		return NarratorRefs{}, fmt.Errorf("narrator ref scan: open book iterator: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Count(key, ":") != 1 {
			continue
		}
		val, vErr := iter.ValueAndErr()
		if vErr != nil {
			_ = iter.Close()
			return NarratorRefs{}, fmt.Errorf("narrator ref scan: reading book row %q: %w", key, vErr)
		}
		var b narratorTextOnly
		if err := json.Unmarshal(val, &b); err != nil {
			// Fatal for the same reason as the junction pass: an unreadable
			// book row may credit a candidate's name.
			_ = iter.Close()
			return NarratorRefs{}, fmt.Errorf("narrator ref scan: undecodable book row %q: %w", key, err)
		}
		if b.Narrator == nil || strings.TrimSpace(*b.Narrator) == "" {
			continue
		}
		// The same splitter the write paths use, so a name counts here exactly
		// when a book edit would resolve it back into a narrator row.
		counted := make(map[string]bool)
		for _, piece := range util.SplitCreditNames(*b.Narrator) {
			norm := util.NormalizeAuthor(piece)
			if norm == "" || counted[norm] {
				continue
			}
			counted[norm] = true
			refs.ByName[norm]++
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return NarratorRefs{}, fmt.Errorf("narrator ref scan truncated over books, refusing to answer from a partial count: %w", err)
	}
	if err := iter.Close(); err != nil {
		return NarratorRefs{}, fmt.Errorf("narrator ref scan: closing book iterator: %w", err)
	}
	return refs, nil
}

// CountNarratorBookLinks scans the LIVE junction (not a snapshot: the point is
// to see writes that landed since the bulk count) for rows naming narratorID.
// Its cost is one pass over book_narrators, the same pass DeleteNarrator's own
// sweep makes, so re-checking before each delete at most doubles a cost the
// delete already pays.
func (p *PebbleStore) CountNarratorBookLinks(narratorID int) (int, error) {
	books := make(map[string]bool)
	err := scanNarratorJunction(p.db, func(bookID string, rows []BookNarrator) {
		for _, r := range rows {
			if r.NarratorID == narratorID {
				books[bookID] = true
				return
			}
		}
	})
	if err != nil {
		return 0, err
	}
	return len(books), nil
}
