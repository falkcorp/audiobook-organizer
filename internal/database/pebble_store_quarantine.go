// file: internal/database/pebble_store_quarantine.go
// version: 1.0.0
// guid: ace123a3-f577-4065-b41c-ae9de32c9b45
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"
)

// GetQuarantinedBooks returns books with a non-nil QuarantinedAt, newest first.
func (p *PebbleStore) GetQuarantinedBooks(limit, offset int) ([]Book, error) {
	// Scan book:* index and only deserialize books that are quarantined
	var result []Book

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
		if b.QuarantinedAt != nil {
			result = append(result, b)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].QuarantinedAt.After(*result[j].QuarantinedAt)
	})

	if offset >= len(result) {
		return nil, nil
	}
	result = result[offset:]
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// CountQuarantinedBooks returns the total number of quarantined books.
func (p *PebbleStore) CountQuarantinedBooks() (int, error) {
	// Scan book:* index and count without deserializing the full book object
	n := 0

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.QuarantinedAt != nil {
			n++
		}
	}
	return n, nil
}

// GetScanFailCount returns the number of consecutive taglib failures for a file path hash.
func (p *PebbleStore) GetScanFailCount(pathHash string) (int, error) {
	key := []byte("scan_fail:" + pathHash)
	val, closer, err := p.db.Get(key)
	if err != nil {
		return 0, nil
	}
	defer closer.Close()
	n := 0
	_, _ = fmt.Sscanf(string(val), "%d", &n)
	return n, nil
}

// IncrScanFailCount increments the scan-fail counter for a file path hash and returns the new count.
func (p *PebbleStore) IncrScanFailCount(pathHash string) (int, error) {
	n, _ := p.GetScanFailCount(pathHash)
	n++
	key := []byte("scan_fail:" + pathHash)
	return n, p.db.Set(key, []byte(fmt.Sprintf("%d", n)), pebble.Sync)
}

// ResetScanFailCount resets the scan-fail counter for a file path hash.
func (p *PebbleStore) ResetScanFailCount(pathHash string) error {
	key := []byte("scan_fail:" + pathHash)
	return p.db.Delete(key, pebble.Sync)
}
