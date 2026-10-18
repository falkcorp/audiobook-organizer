// file: internal/database/pebble_store_collections.go
// version: 1.4.0
// guid: 4c9e2b71-6f83-4a15-9d02-7e5b1a83c064
// last-edited: 2026-10-18

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Collections are stored the same way user playlists are:
//
//	col:{id}                    → the Collection JSON
//	idx:col:name:{lcase-name}   → collection ID  (uniqueness + lookup by name)
//
// Deliberately mirroring pebble_store_playlists.go rather than inventing a
// second layout: the two types have the same lifecycle, and a reader who knows
// one should not have to learn another set of conventions to follow the other.
//
// The one structural difference is that there is NO per-user index. Collections
// are server-wide by definition -- every user sees all of them -- so scoping
// them per user would be a bug, not a missing feature.

const (
	collKeyPrefix     = "col:"
	collNameIdxPrefix = "idx:col:name:"
)

// CreateCollection stores a new collection and its name index.
func (p *PebbleStore) CreateCollection(col *Collection) (*Collection, error) {
	if col == nil || strings.TrimSpace(col.Name) == "" {
		return nil, fmt.Errorf("collection: name required")
	}
	if col.Type != CollectionTypeStatic && col.Type != CollectionTypeDynamic {
		return nil, fmt.Errorf("collection: type must be static or dynamic")
	}
	// A dynamic collection with no query would silently behave as a permanently
	// empty one -- the same "looks implemented, renders nothing" shape that hid
	// the collections stub itself. Reject it at the door.
	if col.Type == CollectionTypeDynamic && strings.TrimSpace(col.Query) == "" {
		return nil, fmt.Errorf("collection: a dynamic collection requires a query")
	}
	if col.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		col.ID = id
	}

	lower := util.NormalizeString(col.Name)
	if v, closer, err := p.db.Get([]byte(collNameIdxPrefix + lower)); err == nil {
		existing := string(v)
		closer.Close()
		if existing != col.ID {
			return nil, fmt.Errorf("%w: %q", ErrCollectionNameInUse, col.Name)
		}
	}

	now := time.Now()
	if col.CreatedAt.IsZero() {
		col.CreatedAt = now
	}
	col.UpdatedAt = now
	if col.Version == 0 {
		col.Version = 1
	}

	data, err := json.Marshal(col)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte(collKeyPrefix+col.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte(collNameIdxPrefix+lower), []byte(col.ID), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return col, nil
}

// GetCollection returns one collection by id, or (nil, nil) when absent.
func (p *PebbleStore) GetCollection(id string) (*Collection, error) {
	data, closer, err := p.db.Get([]byte(collKeyPrefix + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var col Collection
	if err := json.Unmarshal(data, &col); err != nil {
		return nil, err
	}
	return &col, nil
}

// GetCollectionByName resolves a collection through the name index.
func (p *PebbleStore) GetCollectionByName(name string) (*Collection, error) {
	lower := util.NormalizeString(name)
	v, closer, err := p.db.Get([]byte(collNameIdxPrefix + lower))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(v)
	closer.Close()
	return p.GetCollection(id)
}

// ListCollections returns collections ordered by name, with the TOTAL count
// before paging.
//
// The total is the full count, not len(page): clients decide whether another
// page exists from it, so returning the slice length would tell them page 0 is
// everything. That is the same rule the ABS series route documents.
func (p *PebbleStore) ListCollections(collectionType string, limit, offset int) ([]Collection, int, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(collKeyPrefix),
		UpperBound: []byte(collKeyPrefix + "~"),
	})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()

	var all []Collection
	for iter.First(); iter.Valid(); iter.Next() {
		var col Collection
		if err := json.Unmarshal(iter.Value(), &col); err != nil {
			// One unreadable row must not take out the whole list.
			continue
		}
		if collectionType != "" && col.Type != collectionType {
			continue
		}
		all = append(all, col)
	}

	// Stable, total ordering: name first, id as tie-break so pages partition the
	// set exactly instead of overlapping and skipping.
	sort.SliceStable(all, func(i, j int) bool {
		ni, nj := util.NormalizeString(all[i].Name), util.NormalizeString(all[j].Name)
		if ni != nj {
			return ni < nj
		}
		return all[i].ID < all[j].ID
	})

	total := len(all)
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < total {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

// ErrCollectionVersionConflict is returned by UpdateCollection when the
// caller's Collection.Version does not match the currently-stored row's
// Version -- i.e. the row changed between the caller's read and its write.
//
// Callers detect it with errors.Is, not by matching the message: the wrapped
// error carries the collection id and both versions for the log, and that
// wording is free to change without silently breaking every call site.
var ErrCollectionVersionConflict = errors.New("collection version conflict")

// ErrCollectionNameInUse is returned by CreateCollection and UpdateCollection
// when another collection already holds the requested name (compared after
// util.NormalizeString). The wrapped error names the collection; callers map
// it to 409 with errors.Is, never by matching the text.
var ErrCollectionNameInUse = errors.New("collection name already in use")

// ErrCollectionNotFound is wrapped by every collection write that targets an id
// with no row. Its text is deliberately just "not found" so the wrapped message
// keeps the "collection <id> not found" wording UpdateCollection has always
// produced; callers use errors.Is, never the text.
var ErrCollectionNotFound = errors.New("not found")

// ErrCollectionNotStatic is returned by the membership operations
// (UpdateCollectionBooks, AddBooksToCollection, RemoveBookFromCollection) for a
// dynamic collection: its members are the result of its query, so an explicit
// add or remove has nothing to act on and would be a write that changes nothing.
var ErrCollectionNotStatic = errors.New("collection is not static")

// ErrCollectionBookNotMember is returned by RemoveBookFromCollection when the
// book is not a member of the collection.
var ErrCollectionBookNotMember = errors.New("book is not a member of the collection")

// UpdateCollection rewrites a collection, moving the name index if the name
// changed.
func (p *PebbleStore) UpdateCollection(col *Collection) error {
	if col == nil || col.ID == "" {
		return fmt.Errorf("collection: id required")
	}
	if strings.TrimSpace(col.Name) == "" {
		return fmt.Errorf("collection: name required")
	}
	if col.Type != CollectionTypeStatic && col.Type != CollectionTypeDynamic {
		return fmt.Errorf("collection: type must be static or dynamic")
	}
	if col.Type == CollectionTypeDynamic && strings.TrimSpace(col.Query) == "" {
		return fmt.Errorf("collection: a dynamic collection requires a query")
	}

	// Held across the read, the compare and the commit. This is what makes the
	// Version check below an atomic compare-and-swap against the membership
	// operations as well as against another UpdateCollection: without it a
	// membership write could commit between this call's read and its commit and
	// be overwritten, with no conflict raised to either side.
	p.collMu.Lock()
	defer p.collMu.Unlock()

	prev, err := p.GetCollection(col.ID)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("collection %s %w", col.ID, ErrCollectionNotFound)
	}

	// Compare-and-swap on Version: without this, two concurrent
	// read-modify-write cycles (e.g. two AddBookToCollection calls racing on the
	// same collection) silently clobber one another — the second write always
	// wins with no signal that the first one's change was lost. Requiring the
	// caller's col.Version to match what is currently stored turns that into a
	// rejected write the caller can retry against fresh data.
	//
	// Plain equality, not "skip the check when col.Version == 0": a caller that
	// read the current row first has col.Version == prev.Version by
	// construction (all production call sites do — see the callers audited in
	// this change's PR body), so this never rejects a normal sequential update.
	// A caller that never read the row (Version left at its Go zero value) is
	// exactly the blind-overwrite case this guard exists to catch, so it must
	// conflict too, not be waved through as "unconditional write" — treating
	// zero as a bypass would silently recreate the last-write-wins bug for any
	// caller that merely forgets to populate the field. A collection whose
	// stored JSON predates the Version field unmarshals prev.Version as 0 too,
	// so a first read-then-write of a legacy row satisfies 0 == 0 and proceeds
	// normally — no separate legacy-compat branch is needed.
	//
	// The check is atomic with the write below: collMu is held from the read of
	// prev to the commit, and every other writer of this row takes it too.
	if col.Version != prev.Version {
		return fmt.Errorf("collection %s: %w: expected %d, got %d", col.ID, ErrCollectionVersionConflict, prev.Version, col.Version)
	}

	lower := util.NormalizeString(col.Name)
	prevLower := util.NormalizeString(prev.Name)
	if lower != prevLower {
		if v, closer, gerr := p.db.Get([]byte(collNameIdxPrefix + lower)); gerr == nil {
			existing := string(v)
			closer.Close()
			if existing != col.ID {
				return fmt.Errorf("%w: %q", ErrCollectionNameInUse, col.Name)
			}
		}
	}

	col.CreatedAt = prev.CreatedAt
	col.UpdatedAt = time.Now()
	col.Version = prev.Version + 1

	data, err := json.Marshal(col)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte(collKeyPrefix+col.ID), data, nil); err != nil {
		b.Close()
		return err
	}
	if lower != prevLower {
		// Delete the OLD name key before writing the new one. Leaving it behind
		// would keep the old name permanently reserved by a collection that no
		// longer answers to it.
		if err := b.Delete([]byte(collNameIdxPrefix+prevLower), nil); err != nil {
			b.Close()
			return err
		}
		if err := b.Set([]byte(collNameIdxPrefix+lower), []byte(col.ID), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// DeleteCollection removes a collection and its name index entry.
func (p *PebbleStore) DeleteCollection(id string) error {
	// Locked so a membership write that has already re-read the row cannot
	// commit after the delete and resurrect it.
	p.collMu.Lock()
	defer p.collMu.Unlock()

	prev, err := p.GetCollection(id)
	if err != nil {
		return err
	}
	if prev == nil {
		return nil // already gone; deleting twice is not an error
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte(collKeyPrefix+id), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte(collNameIdxPrefix+util.NormalizeString(prev.Name)), nil); err != nil {
		b.Close()
		return err
	}
	return b.Commit(pebble.Sync)
}

// UpdateCollectionBooks atomically rewrites the member list of one static
// collection.
//
// collMu is held across the whole operation and the row is re-read INSIDE the
// lock, so fn always sees the membership that is actually stored and its result
// is committed on top of exactly that row. That is the lost-update fix: the old
// shape was read in the handler, mutate, UpdateCollection, which let two racing
// requests both start from the same row and one change vanish.
//
// fn receives a private copy of the current membership and returns the new one;
// returning an error aborts with nothing written and the error returned as is.
// When fn returns the membership unchanged the call is a no-op: the stored row
// is returned without bumping Version or UpdatedAt, so a repeated add does not
// look like an edit to cache invalidation (the ABS DTO exposes UpdatedAt).
// Otherwise Version and UpdatedAt are bumped and the row is committed in one
// batch.
//
// Errors: an id with no row wraps ErrCollectionNotFound; a dynamic collection
// returns ErrCollectionNotStatic. Callers needing a membership rule that depends
// on the current list (the ABS remove maps a survivor id to the stored merge
// loser) use this directly; the common cases are the two wrappers below.
func (p *PebbleStore) UpdateCollectionBooks(id string, fn func(current []string) ([]string, error)) (*Collection, error) {
	if id == "" {
		return nil, fmt.Errorf("collection: id required")
	}
	if fn == nil {
		return nil, fmt.Errorf("collection: membership function required")
	}

	p.collMu.Lock()
	defer p.collMu.Unlock()

	cur, err := p.GetCollection(id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("collection %s %w", id, ErrCollectionNotFound)
	}
	if cur.Type == CollectionTypeDynamic {
		return nil, fmt.Errorf("collection %s: %w", id, ErrCollectionNotStatic)
	}

	next, err := fn(append([]string(nil), cur.BookIDs...))
	if err != nil {
		return nil, err
	}
	if slices.Equal(next, cur.BookIDs) {
		return cur, nil
	}

	cur.BookIDs = next
	cur.UpdatedAt = time.Now()
	cur.Version++

	data, err := json.Marshal(cur)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte(collKeyPrefix+cur.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return cur, nil
}

// AddBooksToCollection appends the given book ids to a static collection,
// skipping ids already present (and repeats within bookIDs) and keeping the
// existing order, new ids landing in the order given. See UpdateCollectionBooks
// for the locking, no-op and error rules.
func (p *PebbleStore) AddBooksToCollection(id string, bookIDs []string) (*Collection, error) {
	return p.UpdateCollectionBooks(id, func(current []string) ([]string, error) {
		have := make(map[string]struct{}, len(current)+len(bookIDs))
		for _, b := range current {
			have[b] = struct{}{}
		}
		for _, b := range bookIDs {
			if _, dup := have[b]; dup {
				continue
			}
			have[b] = struct{}{}
			current = append(current, b)
		}
		return current, nil
	})
}

// RemoveBookFromCollection removes one book id from a static collection. A book
// that is not a member returns ErrCollectionBookNotMember with nothing written.
// See UpdateCollectionBooks for the locking and the other errors.
func (p *PebbleStore) RemoveBookFromCollection(id, bookID string) (*Collection, error) {
	return p.UpdateCollectionBooks(id, func(current []string) ([]string, error) {
		i := slices.Index(current, bookID)
		if i < 0 {
			return nil, fmt.Errorf("collection %s: %w", id, ErrCollectionBookNotMember)
		}
		return slices.Delete(current, i, i+1), nil
	})
}
