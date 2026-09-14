// file: internal/database/pebble_store_works.go
// version: 1.5.0
// guid: 1d915e6f-133a-4fba-995b-8e4b26b04486
// last-edited: 2026-09-13

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// worksIterCtxEvery is how many work rows ForEachWork visits between context
// checks. A check is a single atomic load, so this only has to keep the gap
// between checks well under a millisecond.
const worksIterCtxEvery = 1024

// WorksGeneration returns a counter that changes whenever any work: row
// changes. Every writer of a work: key (CreateWork, UpdateWork, DeleteWork,
// Reset, WipeByPrefixes) bumps it AFTER its commit, so a reader that records
// the value BEFORE loading the works table and later sees the same value knows
// no write landed in between. The scanner's works lookup cache uses it to
// survive a scan restart without reloading every work (2026-09-13: each
// stand-down re-queue reloaded 156,952 works in ~57s).
//
// The value is seeded per store instance at open, so a cache built against one
// store can never match the generation of another (a reopened or swapped
// store).
func (p *PebbleStore) WorksGeneration() uint64 {
	return p.worksGen.Load()
}

// seedWorksGeneration gives this store instance a starting generation that no
// other instance in the process will have.
func (p *PebbleStore) seedWorksGeneration() {
	p.worksGen.Store(uint64(time.Now().UnixNano()) << 8)
}

// bumpWorksGeneration records that a work: row changed. Call after the commit.
func (p *PebbleStore) bumpWorksGeneration() {
	p.worksGen.Add(1)
}

// GetAllWorks returns all works by iterating the Pebble "work:" prefix.
// Works are intentionally NOT mirrored into memdb — 211K rows × ~590B is
// ~120MB of heap for a query path used in <0.1% of requests. The single
// meaningful hot caller (scanner-side work lookup) now uses ForEachWork so it
// can be canceled.
func (p *PebbleStore) GetAllWorks() ([]Work, error) {
	return p.GetAllWorks_Pebble()
}

// GetAllWorks_Pebble returns all works by iterating the Pebble "work:" prefix.
func (p *PebbleStore) GetAllWorks_Pebble() ([]Work, error) {
	var works []Work
	if err := p.ForEachWork(context.Background(), func(w Work) error {
		works = append(works, w)
		return nil
	}); err != nil {
		return nil, err
	}
	return works, nil
}

// ForEachWork calls visit with every work row, checking ctx every
// worksIterCtxEvery rows and returning ctx's error once it is done. It exists
// because the scanner loads the whole works table at scan start (156,952 rows,
// ~57s on production on 2026-09-13) and GetAllWorks could not be interrupted:
// a scan canceled by a stand-down did not see the cancel until the load
// finished, and the apply op waiting for the scan to park waited that long.
// It also avoids materializing a []Work the caller immediately discards.
func (p *PebbleStore) ForEachWork(ctx context.Context, visit func(Work) error) error {
	n := 0
	return forEachBareRow(p.db, "work:", func(_ string, rowValue []byte) error {
		n++
		if n%worksIterCtxEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var w Work
		if err := json.Unmarshal(rowValue, &w); err != nil {
			return err
		}
		return visit(w)
	})
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
	p.bumpWorksGeneration()
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
	p.bumpWorksGeneration()
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
	p.bumpWorksGeneration()
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
	var books []Book
	if err := forEachKeyInRange(p.db, prefix, upper, func(key, _ []byte) error {
		bookID := string(key[len(prefix):])
		if bookID == "" {
			return nil
		}
		b, err := p.GetBookByID(bookID)
		if err != nil {
			// Only a missing row may be skipped: GetBookByID reports not-found as
			// (nil, nil), which here means a stale index entry for a hard-deleted
			// book. Any other error is an unreadable MEMBER, and skipping it hands
			// the caller a short group that reads as complete (dedup, version-group
			// operations and library-copy lookups all act on the whole group).
			// Until 2026-09-12 every error was skipped.
			return fmt.Errorf("GetBooksByWorkID %s: reading member %s: %w", workID, bookID, err)
		}
		if b == nil {
			return nil
		}
		if bookIsSoftDeleted(b) {
			return nil
		}
		books = append(books, *b)
		return nil
	}); err != nil {
		return nil, err
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

	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {

		var b Book
		if err := json.Unmarshal(rowValue, &b); err != nil {
			return nil
		}
		if b.WorkID == nil || *b.WorkID == "" {
			return nil
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			return nil
		}
		if bookIsSoftDeleted(&b) {
			return nil
		}
		counts[*b.WorkID]++
		return nil
	}); err != nil {
		return nil, err
	}

	return counts, nil
}
