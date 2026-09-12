// file: internal/database/pebble_store_quarantine.go
// version: 1.3.0
// guid: ace123a3-f577-4065-b41c-ae9de32c9b45
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"
)

// GetQuarantinedBooks returns books with a non-nil QuarantinedAt, newest first.
//
// There is no quarantine index: this walks the entire book:* keyspace and
// json.Unmarshals EVERY book to test one field. Measured on production
// 2026-09-09, it took ~4.2s to find 7 quarantined books, and the paired
// CountQuarantinedBooks below repeats the same walk for the total, so
// GET /api/v1/audiobooks/quarantined costs ~8.5s end to end. That is
// tolerated only because the endpoint has no frontend caller (the UI filters
// the main book list with show_quarantined=true instead) — if anything starts
// calling this on a page load, it needs a real index, not a faster scan.
func (p *PebbleStore) GetQuarantinedBooks(limit, offset int) ([]Book, error) {
	// Scan book:* index and only deserialize books that are quarantined
	var result []Book

	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {
		var b Book
		if err := json.Unmarshal(rowValue, &b); err != nil {
			return nil
		}
		if b.QuarantinedAt != nil {
			result = append(result, b)
		}
		return nil
	}); err != nil {
		return nil, err
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
//
// It carries the same full-keyspace cost as GetQuarantinedBooks above.
//
// This comment used to claim the scan counted "without deserializing the full
// book object". That was false from the first commit: the loop below does a
// plain json.Unmarshal into a full Book, exactly like the list path. The claim
// is corrected rather than deleted because it is the kind that stops the next
// reader from measuring — it describes the optimization someone intended, not
// the code that shipped.
//
// Note that actually honouring the claim would not help much anyway:
// unmarshalling into a one-field struct still makes encoding/json parse the
// whole document, so it saves allocations, not the parse. Allocation volume is
// a proxy for cost, not cost.
func (p *PebbleStore) CountQuarantinedBooks() (int, error) {
	n := 0

	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {
		var b Book
		if err := json.Unmarshal(rowValue, &b); err != nil {
			return nil
		}
		if b.QuarantinedAt != nil {
			n++
		}
		return nil
	}); err != nil {
		return 0, err
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
