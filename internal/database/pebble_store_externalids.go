// file: internal/database/pebble_store_externalids.go
// version: 1.1.0
// guid: 8f2e5ddb-e07e-4fbb-98a7-ea24d07ec445
// last-edited: 2026-09-22

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// CreateExternalIDMapping creates or replaces an external ID mapping.
//
// When the replace moves the ID to a different book, the previous owner's
// reverse key is deleted in the same batch. Leaving it behind made the old book
// still list the ID in GetExternalIDsForBook, and a merge that retired that old
// book queued an iTunes removal for a track the surviving book owned.
func (p *PebbleStore) CreateExternalIDMapping(mapping *ExternalIDMapping) error {
	now := time.Now()
	mapping.CreatedAt = now
	mapping.UpdatedAt = now

	data, err := json.Marshal(mapping)
	if err != nil {
		return err
	}

	primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", mapping.Source, mapping.ExternalID))
	reverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", mapping.BookID, mapping.Source, mapping.ExternalID))

	prevOwner, err := p.externalIDOwner(primaryKey)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	if prevOwner != "" && prevOwner != mapping.BookID {
		oldReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", prevOwner, mapping.Source, mapping.ExternalID))
		if err := batch.Delete(oldReverseKey, nil); err != nil {
			return fmt.Errorf("pebble Delete ext_id previous owner reverse: %w", err)
		}
	}
	if err := batch.Set(primaryKey, data, nil); err != nil {
		return fmt.Errorf("pebble Set ext_id primary: %w", err)
	}
	if err := batch.Set(reverseKey, []byte(mapping.ExternalID), nil); err != nil {
		return fmt.Errorf("pebble Set ext_id reverse: %w", err)
	}

	return batch.Commit(pebble.Sync)
}

// externalIDOwner returns the BookID currently stored under primaryKey, or ""
// when there is no mapping. A read failure is an error. An undecodable record
// returns "" with no error: the caller is about to overwrite it, and refusing
// would make a corrupt record impossible to repair through this path. Its old
// reverse key, if any, is then filtered out by GetExternalIDsForBook.
func (p *PebbleStore) externalIDOwner(primaryKey []byte) (string, error) {
	data, closer, err := p.db.Get(primaryKey)
	if err == pebble.ErrNotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pebble Get ext_id primary: %w", err)
	}
	defer closer.Close()
	var existing ExternalIDMapping
	if err := json.Unmarshal(data, &existing); err != nil {
		return "", nil
	}
	return existing.BookID, nil
}

// GetBookByExternalID returns the book_id for a non-tombstoned external ID.
func (p *PebbleStore) GetBookByExternalID(source, externalID string) (string, error) {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closer.Close()

	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return "", err
	}
	if mapping.Tombstoned {
		return "", nil
	}
	return mapping.BookID, nil
}

// GetExternalIDsForBook returns all external ID mappings for a book.
//
// The reverse index is only a pointer; the forward record is the truth. A
// reverse key whose forward mapping now names a different book is stale (left
// by an owner change before CreateExternalIDMapping cleaned them up) and is
// skipped, so a book never reports an ID another book owns.
func (p *PebbleStore) GetExternalIDsForBook(bookID string) ([]ExternalIDMapping, error) {
	results, _, err := p.externalIDsForBook(bookID)
	return results, err
}

// externalIDsForBook walks bookID's reverse index. It returns the mappings the
// book really owns plus the reverse keys found to be stale, so writers that
// rewrite the book's index can delete them in the same batch.
func (p *PebbleStore) externalIDsForBook(bookID string) ([]ExternalIDMapping, [][]byte, error) {
	prefix := []byte(fmt.Sprintf("ext_id:book:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xff),
	})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	var results []ExternalIDMapping
	var stale [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		// Parse source and externalID from key: ext_id:book:<bookID>:<source>:<externalID>
		parts := strings.SplitN(string(iter.Key()), ":", 5)
		if len(parts) < 5 {
			continue
		}
		source := parts[3]
		extID := parts[4]

		primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", source, extID))
		data, closer, err := p.db.Get(primaryKey)
		if err != nil {
			continue
		}
		var mapping ExternalIDMapping
		if err := json.Unmarshal(data, &mapping); err != nil {
			closer.Close()
			continue
		}
		closer.Close()
		if mapping.BookID != bookID {
			stale = append(stale, append([]byte(nil), iter.Key()...))
			continue
		}
		results = append(results, mapping)
	}
	return results, stale, nil
}

// IsExternalIDTombstoned checks whether an external ID is tombstoned.
func (p *PebbleStore) IsExternalIDTombstoned(source, externalID string) (bool, error) {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closer.Close()

	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return false, err
	}
	return mapping.Tombstoned, nil
}

// TombstoneExternalID marks an external ID as tombstoned to prevent reimport.
func (p *PebbleStore) TombstoneExternalID(source, externalID string) error {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err != nil {
		return err
	}
	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		closer.Close()
		return err
	}
	closer.Close()

	mapping.Tombstoned = true
	mapping.UpdatedAt = time.Now()

	updated, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// ReassignExternalIDs moves all external ID mappings from one book to another (for merges).
func (p *PebbleStore) ReassignExternalIDs(oldBookID, newBookID string) error {
	mappings, stale, err := p.externalIDsForBook(oldBookID)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	// The old book is being retired; its stale reverse keys point at IDs other
	// books own and must not be moved to newBookID, only dropped.
	for _, k := range stale {
		if err := batch.Delete(k, nil); err != nil {
			return fmt.Errorf("pebble Delete ext_id stale reverse: %w", err)
		}
	}

	now := time.Now()
	for _, m := range mappings {
		// Delete old reverse key
		oldReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", oldBookID, m.Source, m.ExternalID))
		if err := batch.Delete(oldReverseKey, nil); err != nil {
			return fmt.Errorf("pebble Delete ext_id old reverse: %w", err)
		}

		// Update mapping
		m.BookID = newBookID
		m.UpdatedAt = now
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", m.Source, m.ExternalID))
		if err := batch.Set(primaryKey, data, nil); err != nil {
			return fmt.Errorf("pebble Set ext_id primary: %w", err)
		}

		// Add new reverse key
		newReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", newBookID, m.Source, m.ExternalID))
		if err := batch.Set(newReverseKey, []byte(m.ExternalID), nil); err != nil {
			return fmt.Errorf("pebble Set ext_id new reverse: %w", err)
		}
	}

	return batch.Commit(pebble.Sync)
}

// ReassignExternalID moves a SINGLE external-ID mapping to another book. The
// in-place re-group heal needs per-mapping granularity: an over-merged book holds
// PIDs belonging to several target books, so the whole-book ReassignExternalIDs
// would wrongly drag unrelated PIDs along. No-ops if the mapping already points at
// newBookID; errors if the mapping does not exist.
func (p *PebbleStore) ReassignExternalID(source, externalID, newBookID string) error {
	primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(primaryKey)
	if err != nil {
		return fmt.Errorf("ReassignExternalID: ext_id %s:%s not found: %w", source, externalID, err)
	}
	var m ExternalIDMapping
	unmErr := json.Unmarshal(data, &m)
	closer.Close()
	if unmErr != nil {
		return fmt.Errorf("ReassignExternalID unmarshal: %w", unmErr)
	}
	if m.BookID == newBookID {
		return nil
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	oldReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", m.BookID, m.Source, m.ExternalID))
	if err := batch.Delete(oldReverseKey, nil); err != nil {
		return fmt.Errorf("ReassignExternalID delete old reverse: %w", err)
	}

	m.BookID = newBookID
	m.UpdatedAt = time.Now()
	updated, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := batch.Set(primaryKey, updated, nil); err != nil {
		return fmt.Errorf("ReassignExternalID set primary: %w", err)
	}

	newReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", newBookID, m.Source, m.ExternalID))
	if err := batch.Set(newReverseKey, []byte(m.ExternalID), nil); err != nil {
		return fmt.Errorf("ReassignExternalID set new reverse: %w", err)
	}

	return batch.Commit(pebble.Sync)
}

// BulkCreateExternalIDMappings inserts multiple external ID mappings.
// Existing mappings are not overwritten (ignore semantics).
func (p *PebbleStore) BulkCreateExternalIDMappings(mappings []ExternalIDMapping) error {
	batch := p.db.NewBatch()
	defer batch.Close()

	now := time.Now()
	// The existence check reads the committed DB, which cannot see this batch's
	// own writes. A repeated ID in one call would otherwise be written twice, and
	// if the repeats named different books the first book's reverse key would be
	// left stale. First occurrence wins, matching the skip-existing semantics.
	seen := make(map[string]struct{}, len(mappings))
	for _, m := range mappings {
		primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", m.Source, m.ExternalID))
		if _, dup := seen[string(primaryKey)]; dup {
			continue
		}
		seen[string(primaryKey)] = struct{}{}
		// Check if already exists
		if _, closer, err := p.db.Get(primaryKey); err == nil {
			closer.Close()
			continue // skip existing
		}

		m.CreatedAt = now
		m.UpdatedAt = now
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if err := batch.Set(primaryKey, data, nil); err != nil {
			return fmt.Errorf("pebble Set ext_id primary: %w", err)
		}

		reverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", m.BookID, m.Source, m.ExternalID))
		if err := batch.Set(reverseKey, []byte(m.ExternalID), nil); err != nil {
			return fmt.Errorf("pebble Set ext_id reverse: %w", err)
		}
	}

	return batch.Commit(pebble.Sync)
}

// MarkExternalIDRemoved marks an external ID mapping as tombstoned and records
// the removal timestamp. The primary ext_id:<source>:<externalID> record is
// updated in-place so provenance and other fields are preserved.
func (p *PebbleStore) MarkExternalIDRemoved(source, externalID string) error {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pebble Get ext_id for removal: %w", err)
	}
	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		closer.Close()
		return fmt.Errorf("unmarshal ext_id for removal: %w", err)
	}
	closer.Close()

	now := time.Now()
	mapping.Tombstoned = true
	mapping.RemovedAt = &now
	updated, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshal ext_id after removal: %w", err)
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// SetExternalIDProvenance updates the provenance field on an existing external
// ID mapping record. No-ops silently if the record does not exist.
func (p *PebbleStore) SetExternalIDProvenance(source, externalID, provenance string) error {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pebble Get ext_id for provenance: %w", err)
	}
	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		closer.Close()
		return fmt.Errorf("unmarshal ext_id for provenance: %w", err)
	}
	closer.Close()

	mapping.Provenance = provenance
	updated, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshal ext_id after provenance: %w", err)
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// GetRemovedExternalIDs returns all tombstoned external ID mappings for the
// given source (i.e. records where MarkExternalIDRemoved was called).
func (p *PebbleStore) GetRemovedExternalIDs(source string) ([]ExternalIDMapping, error) {
	prefix := []byte(fmt.Sprintf("ext_id:%s:", source))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []ExternalIDMapping
	for iter.First(); iter.Valid(); iter.Next() {
		var mapping ExternalIDMapping
		if err := json.Unmarshal(iter.Value(), &mapping); err != nil {
			continue
		}
		if mapping.Tombstoned {
			results = append(results, mapping)
		}
	}
	return results, nil
}
