// file: internal/database/pebble_store_collections.go
// version: 1.0.0
// guid: 4c9e2b71-6f83-4a15-9d02-7e5b1a83c064
// last-edited: 2026-08-16

package database

import (
	"encoding/json"
	"fmt"
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
			return nil, fmt.Errorf("collection name %q already in use", col.Name)
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

	prev, err := p.GetCollection(col.ID)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("collection %s not found", col.ID)
	}

	lower := util.NormalizeString(col.Name)
	prevLower := util.NormalizeString(prev.Name)
	if lower != prevLower {
		if v, closer, gerr := p.db.Get([]byte(collNameIdxPrefix + lower)); gerr == nil {
			existing := string(v)
			closer.Close()
			if existing != col.ID {
				return fmt.Errorf("collection name %q already in use", col.Name)
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
