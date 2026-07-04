// file: internal/database/pebble_store_blocklist.go
// version: 1.0.0
// guid: 0099e818-3f11-41d4-850e-e44e387f9f00
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Blocked hash (do-not-import) methods
func (p *PebbleStore) IsHashBlocked(hash string) (bool, error) {
	key := []byte(fmt.Sprintf("blocked:hash:%s", hash))
	_, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	closer.Close()
	return true, nil
}

func (p *PebbleStore) AddBlockedHash(hash, reason string) error {
	item := DoNotImport{
		Hash:      hash,
		Reason:    reason,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal blocked hash: %w", err)
	}

	key := []byte(fmt.Sprintf("blocked:hash:%s", hash))
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}
	p.UpsertBlockedHashToMemDB(&item)
	return nil
}

func (p *PebbleStore) RemoveBlockedHash(hash string) error {
	key := []byte(fmt.Sprintf("blocked:hash:%s", hash))
	if err := p.db.Delete(key, pebble.Sync); err != nil {
		return err
	}
	p.DeleteBlockedHashFromMemDB(hash)
	return nil
}

// GetAllBlockedHashes returns all blocked hashes.
func (p *PebbleStore) GetAllBlockedHashes() ([]DoNotImport, error) {
	return p.GetAllBlockedHashes_Pebble()
}

// GetAllBlockedHashes_Pebble returns all blocked hashes using Pebble prefix iteration.
func (p *PebbleStore) GetAllBlockedHashes_Pebble() ([]DoNotImport, error) {
	var items []DoNotImport
	prefix := []byte("blocked:hash:")

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var item DoNotImport
		if err := json.Unmarshal(iter.Value(), &item); err != nil {
			return nil, fmt.Errorf("failed to unmarshal blocked hash: %w", err)
		}
		items = append(items, item)
	}

	return items, iter.Error()
}

func (p *PebbleStore) GetBlockedHashByHash(hash string) (*DoNotImport, error) {
	key := []byte(fmt.Sprintf("blocked:hash:%s", hash))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var item DoNotImport
	if err := json.Unmarshal(value, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blocked hash: %w", err)
	}

	return &item, nil
}
