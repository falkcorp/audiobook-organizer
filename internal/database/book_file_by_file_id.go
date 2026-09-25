// file: internal/database/book_file_by_file_id.go
// version: 1.0.0
// guid: 9d734fc6-325a-44cd-99e3-ac55ee626ce8
// last-edited: 2026-09-25

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// BookFileByFileIDReader resolves a book_file row from its file ID alone,
// without knowing the owning book.
//
// It is a capability interface, not part of Store: resolve it with
// AsCapability[BookFileByFileIDReader](store) so it keeps working through the
// indexedStore decorator (see store_capability.go). Its only caller today is
// the admin debug API's "which book owns this file?" route.
type BookFileByFileIDReader interface {
	// GetBookFileByFileID returns the row whose ID is fileID, or (nil, nil)
	// when the book_file_id index has no entry for it or the entry is stale.
	GetBookFileByFileID(fileID string) (*BookFile, error)
}

var _ BookFileByFileIDReader = (*PebbleStore)(nil)

// GetBookFileByFileID reads the book_file_id:<fileID> index and then the row
// it points at: two point lookups.
//
// It deliberately does NOT fall back to a scan of every book_file: key the way
// DeleteBookFile does for rows written before the index existed. That scan
// walks the whole library (~750K rows in production), and this method serves
// an interactive request; a missing index entry is reported as not found. A
// stale entry (its row gone, or a row with a different ID) is also not found.
// Any other read or decode error is returned.
func (s *PebbleStore) GetBookFileByFileID(fileID string) (*BookFile, error) {
	if fileID == "" {
		return nil, nil
	}
	idxVal, idxCloser, err := s.bookFileIDGet([]byte("book_file_id:" + fileID))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetBookFileByFileID: read id index for %s: %w", fileID, err)
	}
	primaryKey := append([]byte(nil), idxVal...) // valid only until Close
	idxCloser.Close()

	val, closer, err := s.db.Get(primaryKey)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetBookFileByFileID: read %s: %w", primaryKey, err)
	}
	defer closer.Close()
	var f BookFile
	if err := json.Unmarshal(val, &f); err != nil {
		return nil, fmt.Errorf("GetBookFileByFileID: decode %s: %w", primaryKey, err)
	}
	if f.ID != fileID {
		return nil, nil
	}
	return &f, nil
}
