// file: internal/database/pebble_store_itunes.go
// version: 1.0.0
// guid: f359d1ff-32ad-45c2-b58c-bd254479a552
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// SetLastWrittenAt stamps the last_written_at timestamp for book id.
func (p *PebbleStore) SetLastWrittenAt(id string, t time.Time) error {
	book, err := p.GetBookByID(id)
	if err != nil {
		return err
	}
	if book == nil {
		return nil // non-fatal: book not found
	}
	book.LastWrittenAt = &t
	_, err = p.UpdateBook(id, book)
	return err
}

// MarkITunesSynced sets itunes_sync_status to "synced" for the given book IDs.
func (p *PebbleStore) MarkITunesSynced(bookIDs []string) (int64, error) {
	var count int64
	synced := "synced"
	for _, id := range bookIDs {
		book, err := p.GetBookByID(id)
		if err != nil || book == nil {
			continue
		}
		book.ITunesSyncStatus = &synced
		if _, err := p.UpdateBook(id, book); err == nil {
			count++
		}
	}
	return count, nil
}

// GetITunesPurgePendingBooks returns books with itunes_sync_status = "purge_pending" and a PID.
func (p *PebbleStore) GetITunesPurgePendingBooks() ([]Book, error) {
	// Scan book:* index and filter by iTunes sync status without loading all books
	var pending []Book

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.ITunesSyncStatus != nil && *b.ITunesSyncStatus == "purge_pending" && b.ITunesPersistentID != nil {
			pending = append(pending, b)
		}
	}
	return pending, nil
}

// GetITunesDirtyBooks returns all primary books with itunes_sync_status = "dirty".
func (p *PebbleStore) GetITunesDirtyBooks() ([]Book, error) {
	// Scan book:* index and filter by iTunes sync status without loading all books
	var dirty []Book

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.ITunesSyncStatus != nil && *b.ITunesSyncStatus == "dirty" {
			if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
				dirty = append(dirty, b)
			}
		}
	}
	return dirty, nil
}

// CreateDeferredITunesUpdate stores a deferred iTunes path update.
func (p *PebbleStore) CreateDeferredITunesUpdate(bookID, persistentID, oldPath, newPath, updateType string) error {
	id := time.Now().UnixNano()
	rec := DeferredITunesUpdate{
		ID:           int(id),
		BookID:       bookID,
		PersistentID: persistentID,
		OldPath:      oldPath,
		NewPath:      newPath,
		UpdateType:   updateType,
		CreatedAt:    time.Now(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("deferred_itunes:%019d", id))
	return p.db.Set(key, data, pebble.Sync)
}

// GetPendingDeferredITunesUpdates returns all deferred updates that haven't been applied yet.
func (p *PebbleStore) GetPendingDeferredITunesUpdates() ([]DeferredITunesUpdate, error) {
	prefix := []byte("deferred_itunes:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []DeferredITunesUpdate
	for iter.First(); iter.Valid(); iter.Next() {
		var rec DeferredITunesUpdate
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.AppliedAt == nil {
			results = append(results, rec)
		}
	}
	return results, nil
}

// MarkDeferredITunesUpdateApplied sets the applied_at timestamp on a deferred update.
func (p *PebbleStore) MarkDeferredITunesUpdateApplied(id int) error {
	key := []byte(fmt.Sprintf("deferred_itunes:%019d", id))
	data, closer, err := p.db.Get(key)
	if err != nil {
		return err
	}
	var rec DeferredITunesUpdate
	if err := json.Unmarshal(data, &rec); err != nil {
		closer.Close()
		return err
	}
	closer.Close()

	now := time.Now()
	rec.AppliedAt = &now
	updated, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// GetDeferredITunesUpdatesByBookID returns all deferred updates for a specific book.
func (p *PebbleStore) GetDeferredITunesUpdatesByBookID(bookID string) ([]DeferredITunesUpdate, error) {
	all, err := p.getPendingAndAppliedDeferredUpdates()
	if err != nil {
		return nil, err
	}
	var results []DeferredITunesUpdate
	for _, rec := range all {
		if rec.BookID == bookID {
			results = append(results, rec)
		}
	}
	return results, nil
}

func (p *PebbleStore) getPendingAndAppliedDeferredUpdates() ([]DeferredITunesUpdate, error) {
	prefix := []byte("deferred_itunes:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []DeferredITunesUpdate
	for iter.First(); iter.Valid(); iter.Next() {
		var rec DeferredITunesUpdate
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		results = append(results, rec)
	}
	return results, nil
}
