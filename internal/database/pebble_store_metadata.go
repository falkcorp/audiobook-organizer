// file: internal/database/pebble_store_metadata.go
// version: 1.0.0
// guid: aa5896b5-07dd-4825-ba04-5b2dac36a0f3
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func (p *PebbleStore) metadataStateKey(bookID, field string) []byte {
	return []byte(fmt.Sprintf("metadata_state:%s:%s", bookID, field))
}

func (p *PebbleStore) GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error) {
	var states []MetadataFieldState
	prefix := []byte(fmt.Sprintf("metadata_state:%s:", bookID))

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		if !strings.HasPrefix(string(iter.Key()), string(prefix)) {
			break
		}
		var state MetadataFieldState
		if err := json.Unmarshal(iter.Value(), &state); err != nil {
			return nil, err
		}
		states = append(states, state)
	}

	return states, nil
}

func (p *PebbleStore) UpsertMetadataFieldState(state *MetadataFieldState) error {
	if state == nil {
		return fmt.Errorf("metadata state cannot be nil")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	return p.db.Set(p.metadataStateKey(state.BookID, state.Field), data, pebble.Sync)
}

func (p *PebbleStore) DeleteMetadataFieldState(bookID, field string) error {
	return p.db.Delete(p.metadataStateKey(bookID, field), pebble.Sync)
}

func (p *PebbleStore) RecordMetadataChange(record *MetadataChangeRecord) error {
	if record.ChangedAt.IsZero() {
		record.ChangedAt = time.Now().UTC()
	}
	// Use UnixNano as the synthetic integer ID so callers can distinguish
	// records (ID == 0 is treated as "not stored").
	if record.ID == 0 {
		record.ID = int(record.ChangedAt.UnixNano())
	}
	key := fmt.Sprintf("metadata_change:%s:%s:%d", record.BookID, record.Field, record.ChangedAt.UnixNano())
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func (p *PebbleStore) GetMetadataChangeHistory(bookID string, field string, limit int) ([]MetadataChangeRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	prefix := fmt.Sprintf("metadata_change:%s:%s:", bookID, field)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []MetadataChangeRecord
	for iter.First(); iter.Valid(); iter.Next() {
		var r MetadataChangeRecord
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		all = append(all, r)
	}
	// Reverse for newest-first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (p *PebbleStore) GetBookChangeHistory(bookID string, limit int) ([]MetadataChangeRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	prefix := fmt.Sprintf("metadata_change:%s:", bookID)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []MetadataChangeRecord
	for iter.First(); iter.Valid(); iter.Next() {
		var r MetadataChangeRecord
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		all = append(all, r)
	}
	// Reverse for newest-first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// AddMetadataRejection is not supported on PebbleStore.
// AddMetadataRejection is a no-op on PebbleStore (rejections not persisted).
// AddMetadataRejection persists a rejected metadata candidate under
// key metadata_rejection:<bookID>:<ULID>.
func (p *PebbleStore) AddMetadataRejection(r MetadataRejection) error {
	if r.ID == "" {
		id, err := newULID()
		if err != nil {
			return err
		}
		r.ID = id
	}
	if r.RejectedAt.IsZero() {
		r.RejectedAt = time.Now().UTC()
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("metadata_rejection:%s:%s", r.BookID, r.ID))
	return p.db.Set(key, data, pebble.Sync)
}

// GetMetadataRejections returns all rejection records for a book.
func (p *PebbleStore) GetMetadataRejections(bookID string) ([]MetadataRejection, error) {
	prefix := []byte(fmt.Sprintf("metadata_rejection:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []MetadataRejection
	for iter.First(); iter.Valid(); iter.Next() {
		var r MetadataRejection
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	if out == nil {
		out = []MetadataRejection{}
	}
	return out, nil
}

// DeleteMetadataRejections removes all rejection records for a book.
func (p *PebbleStore) DeleteMetadataRejections(bookID string) error {
	prefix := []byte(fmt.Sprintf("metadata_rejection:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return err
	}
	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	iter.Close()
	for _, k := range keys {
		if err := p.db.Delete(k, pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}
