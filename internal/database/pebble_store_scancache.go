// file: internal/database/pebble_store_scancache.go
// version: 1.0.0
// guid: 5737e19f-0c4c-4762-a8ea-928619a02862
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// GetScanCacheMap returns a map of file_path -> ScanCacheEntry for all books
// that have a non-empty FilePath and a non-nil LastScanMtime.
func (p *PebbleStore) GetScanCacheMap() (map[string]ScanCacheEntry, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	result := make(map[string]ScanCacheEntry)
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.FilePath == "" || book.LastScanMtime == nil {
			continue
		}
		result[book.FilePath] = ScanCacheEntry{
			Mtime:       derefInt64(book.LastScanMtime),
			Size:        derefInt64(book.LastScanSize),
			NeedsRescan: derefBool(book.NeedsRescan),
		}
	}
	return result, nil
}

// UpdateScanCache sets LastScanMtime, LastScanSize, and clears NeedsRescan for a book.
func (p *PebbleStore) UpdateScanCache(bookID string, mtime int64, size int64) error {
	book, err := p.GetBookByID(bookID)
	if err != nil {
		return err
	}
	if book == nil {
		return nil // non-fatal: book not found
	}
	book.LastScanMtime = &mtime
	book.LastScanSize = &size
	f := false
	book.NeedsRescan = &f
	_, err = p.UpdateBook(bookID, book)
	return err
}

// MarkNeedsRescan sets NeedsRescan = true for the given book.
func (p *PebbleStore) MarkNeedsRescan(bookID string) error {
	book, err := p.GetBookByID(bookID)
	if err != nil {
		return err
	}
	if book == nil {
		return nil // non-fatal: book not found
	}
	t := true
	book.NeedsRescan = &t
	_, err = p.UpdateBook(bookID, book)
	return err
}

// GetDirtyBookFolders returns a deduplicated list of parent directories for all
// books that have NeedsRescan = true.
func (p *PebbleStore) GetDirtyBookFolders() ([]string, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	seen := make(map[string]struct{})
	var dirs []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.FilePath == "" || !derefBool(book.NeedsRescan) {
			continue
		}
		dir := filepath.Dir(book.FilePath)
		if _, ok := seen[dir]; !ok {
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

// RecordPathChange stores a path change record in PebbleDB.
// Key format: path_history:<book_id>:<timestamp>
func (p *PebbleStore) RecordPathChange(change *BookPathChange) error {
	ts := time.Now().UnixNano()
	change.CreatedAt = time.Now()
	change.ID = int(ts)
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("path_history:%s:%019d", change.BookID, ts))
	return p.db.Set(key, data, pebble.Sync)
}

// GetBookPathHistory returns all path changes for a book, newest first.
func (p *PebbleStore) GetBookPathHistory(bookID string) ([]BookPathChange, error) {
	prefix := []byte(fmt.Sprintf("path_history:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []BookPathChange
	for iter.First(); iter.Valid(); iter.Next() {
		var c BookPathChange
		if err := json.Unmarshal(iter.Value(), &c); err != nil {
			continue
		}
		results = append(results, c)
	}
	// Reverse for newest-first
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results, nil
}
