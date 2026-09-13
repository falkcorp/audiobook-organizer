// file: internal/database/pebble_store_book_path_owner.go
// version: 1.0.0
// guid: 5d8a2f41-7c3e-4b96-a1d0-9e6b3c7f2a18
// last-edited: 2026-09-13

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var bookPathOwnerLog = logger.New("database.book-path-owner")

// bookPathKey is the single-valued book:path:<path> lookup key that
// GetBookByFilePath reads.
func bookPathKey(path string) []byte {
	return []byte("book:path:" + path)
}

// bookPathKeyHeldByOther reports the id of a book other than id that
// book:path:<path> currently names and that still owns it: its row exists,
// is not soft-deleted, and its FilePath is still path. A key naming a gone,
// trashed or moved-away book is stale and does not hold the path.
//
// A row that cannot be decoded is treated as a live owner: the caller then
// leaves the key alone, which loses nothing, where overwriting it would take
// another book's lookup key on a guess.
func (p *PebbleStore) bookPathKeyHeldByOther(path, id string) (string, error) {
	value, closer, err := p.db.Get(bookPathKey(path))
	if errors.Is(err, pebble.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read path key %q: %w", path, err)
	}
	owner := string(value)
	closer.Close()
	if owner == id || owner == "" {
		return "", nil
	}
	row, closer, err := p.db.Get([]byte("book:" + owner))
	if errors.Is(err, pebble.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read book %s holding path key %q: %w", owner, path, err)
	}
	var r bookAtPathRow
	decErr := json.Unmarshal(row, &r)
	closer.Close()
	if decErr != nil {
		return owner, nil
	}
	if r.FilePath != path || markedForDeletionFlag(r.MarkedForDeletion) {
		return "", nil
	}
	return owner, nil
}

// setBookPathKeyIfFree stages book:path:<path> -> id unless another live
// book owns that key (bookPathKeyHeldByOther). The key is single-valued, so
// before this check the last writer took it, and a create or move onto an
// occupied path silently re-pointed every GetBookByFilePath caller (scanner,
// organizer collision checks, iTunes import, autoscan) at the newcomer.
//
// When the key is held, nothing is staged and the newcomer is reachable only
// through the multi-valued book_atpath index (LiveBookIDsAtPath), which the
// caller writes unconditionally. The alternative, refusing the write, was not
// chosen: CreateBook and UpdateBook are called by every scan and import, and
// a refused create loses the book where a skipped key loses only a shortcut.
//
// Check-then-set, not atomic: two writers racing onto one free path can still
// both see it free. Closing that needs a store-wide path lock.
func (p *PebbleStore) setBookPathKeyIfFree(batch *pebble.Batch, path, id string) error {
	owner, err := p.bookPathKeyHeldByOther(path, id)
	if err != nil {
		return err
	}
	if owner != "" {
		bookPathOwnerLog.Warn("book %s moved onto path %q, whose lookup key belongs to live book %s; key left with %s",
			id, logger.SanitizeLogValue(path), owner, owner)
		return nil
	}
	return batch.Set(bookPathKey(path), []byte(id), nil)
}

// deleteBookPathKeyIfOwned stages a delete of book:path:<path> only when the
// key still names id (compare-and-delete). A book leaving a path, by a move
// or a delete, used to delete the key unconditionally, so leaving a path that
// another book had since taken erased that book's lookup key.
func (p *PebbleStore) deleteBookPathKeyIfOwned(batch *pebble.Batch, path, id string) error {
	value, closer, err := p.db.Get(bookPathKey(path))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read path key %q: %w", path, err)
	}
	owned := string(value) == id
	closer.Close()
	if !owned {
		return nil
	}
	return batch.Delete(bookPathKey(path), nil)
}
