// file: internal/database/pebble_book_file_errors.go
// version: 1.2.0
// guid: a1b2c3d4-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// BookFileError represents a persisted ffmpeg/fingerprint error for a specific file.
type BookFileError struct {
	FilePath    string    `json:"file_path"`
	BookID      string    `json:"book_id"`
	ErrorClass  string    `json:"error_class"`
	LastMessage string    `json:"last_message"`
	Occurrences int       `json:"occurrences"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// RecordFileError records or updates a BookFileError entry and maintains a
// secondary index keyed by book ID so book-level lookups are efficient.
func (p *PebbleStore) RecordFileError(filePath, bookID, errClass, message string) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("pebble store not initialized")
	}
	key := []byte("book_file_error:" + filePath)
	now := time.Now().UTC()
	var e BookFileError
	val, closer, err := p.db.Get(key)
	if err == nil {
		closer.Close()
		if err := json.Unmarshal(val, &e); err != nil {
			// Corrupt value: overwrite with fresh record
			e = BookFileError{}
		}
		e.Occurrences += 1
		e.LastMessage = message
		e.LastSeen = now
	} else if err == pebble.ErrNotFound {
		e = BookFileError{
			FilePath:    filePath,
			BookID:      bookID,
			ErrorClass:  errClass,
			LastMessage: message,
			Occurrences: 1,
			FirstSeen:   now,
			LastSeen:    now,
		}
	} else {
		return fmt.Errorf("pebble Get: %w", err)
	}
	data, jerr := json.Marshal(e)
	if jerr != nil {
		return fmt.Errorf("json marshal: %w", jerr)
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return fmt.Errorf("pebble Set: %w", err)
	}
	indexKey := []byte("book_file_errors_by_book:" + bookID + ":" + filePath)
	if err := p.db.Set(indexKey, []byte("1"), pebble.Sync); err != nil {
		return fmt.Errorf("pebble Set index: %w", err)
	}
	p.InvalidateLibraryStats()
	return nil
}

// ClearFileError removes a persisted file error and the associated index entry.
func (p *PebbleStore) ClearFileError(filePath string) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("pebble store not initialized")
	}
	key := []byte("book_file_error:" + filePath)
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil
		}
		return fmt.Errorf("pebble Get: %w", err)
	}
	closer.Close()
	var e BookFileError
	if err := json.Unmarshal(val, &e); err != nil {
		// Best-effort delete the primary key and return
		_ = p.db.Delete(key, pebble.Sync)
		return nil
	}
	if err := p.db.Delete(key, pebble.Sync); err != nil {
		return fmt.Errorf("pebble Delete: %w", err)
	}
	indexKey := []byte("book_file_errors_by_book:" + e.BookID + ":" + filePath)
	if err := p.db.Delete(indexKey, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return fmt.Errorf("pebble Delete index: %w", err)
	}
	p.InvalidateLibraryStats()
	return nil
}

// ListBooksWithFileErrors returns a list of book IDs that have at least one file error.
func (p *PebbleStore) ListBooksWithFileErrors() ([]string, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("pebble store not initialized")
	}
	lower := []byte("book_file_errors_by_book:")
	upper := prefixEnd(lower)
	books := map[string]struct{}{}
	if err := forEachKeyInRange(p.db, lower, upper, func(key, _ []byte) error {
		// key format: book_file_errors_by_book:{bookID}:{filePath}
		r := strings.TrimPrefix(string(key), "book_file_errors_by_book:")
		parts := strings.SplitN(r, ":", 2)
		if len(parts) >= 1 && parts[0] != "" {
			books[parts[0]] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(books))
	for id := range books {
		result = append(result, id)
	}
	return result, nil
}

// GetBrokenFileCount returns the number of distinct books that have at least one recorded file error.
func (p *PebbleStore) GetBrokenFileCount() (int, error) {
	ids, err := p.ListBooksWithFileErrors()
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}
