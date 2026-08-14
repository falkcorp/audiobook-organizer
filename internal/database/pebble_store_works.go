// file: internal/database/pebble_store_works.go
// version: 1.2.0
// guid: 1d915e6f-133a-4fba-995b-8e4b26b04486
// last-edited: 2026-08-13

package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// GetAllWorks returns all works by iterating the Pebble "work:" prefix.
// Works are intentionally NOT mirrored into memdb — 211K rows × ~590B is
// ~120MB of heap for a query path used in <0.1% of requests. The single
// meaningful hot caller (scanner-side work lookup) batches one call at
// scan start, which Pebble handles in tens of milliseconds.
func (p *PebbleStore) GetAllWorks() ([]Work, error) {
	return p.GetAllWorks_Pebble()
}

// GetAllWorks_Pebble returns all works by iterating the Pebble "work:" prefix.
func (p *PebbleStore) GetAllWorks_Pebble() ([]Work, error) {
	var works []Work
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte("work:0"), UpperBound: []byte("work:;")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":title:") {
			continue
		}
		var w Work
		if err := json.Unmarshal(iter.Value(), &w); err != nil {
			return nil, err
		}
		works = append(works, w)
	}
	return works, nil
}

func (p *PebbleStore) GetWorkByID(id string) (*Work, error) {
	key := []byte(fmt.Sprintf("work:%s", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var w Work
	if err := json.Unmarshal(value, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (p *PebbleStore) CreateWork(work *Work) (*Work, error) {
	if work.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		work.ID = id
	}
	data, err := json.Marshal(work)
	if err != nil {
		return nil, err
	}
	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("work:%s", work.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	// Basic title index (case-insensitive normalized) for future lookup
	normTitle := util.NormalizeString(work.Title)
	if normTitle != "" {
		idxKey := []byte(fmt.Sprintf("work:title:%s:%s", normTitle, work.ID))
		if err := batch.Set(idxKey, []byte(work.ID), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	p.UpsertWorkToMemDB(work)
	return work, nil
}

func (p *PebbleStore) UpdateWork(id string, work *Work) (*Work, error) {
	old, err := p.GetWorkByID(id)
	if err != nil {
		return nil, err
	}
	if old == nil {
		return nil, fmt.Errorf("work not found")
	}
	work.ID = id
	data, err := json.Marshal(work)
	if err != nil {
		return nil, err
	}
	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("work:%s", id))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	oldNorm := util.NormalizeString(old.Title)
	newNorm := util.NormalizeString(work.Title)
	if oldNorm != newNorm {
		if oldNorm != "" {
			if err := batch.Delete([]byte(fmt.Sprintf("work:title:%s:%s", oldNorm, id)), nil); err != nil {
				batch.Close()
				return nil, fmt.Errorf("pebble batch delete old work title index: %w", err)
			}
		}
		if newNorm != "" {
			if err := batch.Set([]byte(fmt.Sprintf("work:title:%s:%s", newNorm, id)), []byte(id), nil); err != nil {
				batch.Close()
				return nil, fmt.Errorf("pebble batch set new work title index: %w", err)
			}
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	p.UpsertWorkToMemDB(work)
	return work, nil
}

func (p *PebbleStore) DeleteWork(id string) error {
	work, err := p.GetWorkByID(id)
	if err != nil {
		return err
	}
	if work == nil {
		return nil
	}
	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("work:%s", id))
	if err := batch.Delete(key, nil); err != nil {
		batch.Close()
		return err
	}
	norm := util.NormalizeString(work.Title)
	if norm != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("work:title:%s:%s", norm, id)), nil); err != nil {
			batch.Close()
			return fmt.Errorf("pebble batch delete work title index: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteWorkFromMemDB(id)
	return nil
}

func (p *PebbleStore) GetBooksByWorkID(workID string) ([]Book, error) {
	// Use book:work:<workID>:<bookID> index to avoid O(50K) full-scan.
	//
	// INDEX-CONSISTENCY: the index VALUE embeds a serialized Book snapshot, but
	// UpdateBook only refreshes that snapshot when the WorkID itself changes — a
	// same-work edit (notably SoftDeleteBook, which sets MarkedForDeletion via
	// UpdateBook without touching WorkID) leaves the embedded copy stale, and a
	// DeleteBook historically left the row dangling. So we treat the index as a
	// POINTER: the trailing key segment is the book ID (a ULID, no nested
	// colons), which we point-look-up against the authoritative book:<id> row.
	// A book that is absent (hard-deleted) or MarkedForDeletion (soft-deleted)
	// is skipped. This can never desync from the source of truth.
	prefix := []byte(fmt.Sprintf("book:work:%s:", workID))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';' // ':' + 1
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var books []Book
	for iter.First(); iter.Valid(); iter.Next() {
		bookID := string(iter.Key()[len(prefix):])
		if bookID == "" {
			continue
		}
		b, err := p.GetBookByID(bookID)
		if err != nil || b == nil {
			continue
		}
		if bookIsSoftDeleted(b) {
			continue
		}
		books = append(books, *b)
	}
	return books, nil
}

// GetAllWorkBookCounts returns map[workID] → count of primary, not-deleted
// books per work. Mirrors GetAllAuthorBookCounts; used to avoid N+1
// GetBooksByWorkID lookups when listing/aggregating works.
func (p *PebbleStore) GetAllWorkBookCounts() (map[string]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllWorkBookCounts()
	}
	counts := make(map[string]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.WorkID == nil || *b.WorkID == "" {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if bookIsSoftDeleted(&b) {
			continue
		}
		counts[*b.WorkID]++
	}

	return counts, nil
}
