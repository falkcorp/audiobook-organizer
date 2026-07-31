// file: internal/database/pebble_store_bookmarks.go
// version: 1.1.0
// guid: 35844383-1823-4889-9735-b64bceb2ab17
// last-edited: 2026-07-30

// Package database: named-bookmark keyspace.
//
// This is a genuinely new feature -- the only prior "bookmark" concept in
// this codebase is the single scalar Book.ITunesBookmark (*int64,
// milliseconds, one value per book, imported from iTunes). It is unrelated
// to the ABS named-bookmark concept implemented here: a per-(user, item)
// list of named timestamps a listener can jump back to.
//
// Real ABS keys a bookmark by (libraryItemId, time), not a separate
// bookmark ID -- DELETE /api/me/item/:id/bookmark/:time puts the time value
// itself in the URL path. This store mirrors that: no bookmark ID field
// anywhere, the natural key is (userID, itemID, canonicalized time).
//
// Pebble key: "bookmark:" + userID + ":" + itemID + ":" +
// progress.CanonicalTimeKey(timeSec), prefix-scanned per (userID, itemID)
// using the same LowerBound/UpperBound("~") pattern as GetUserPosition in
// pebble_store_playback.go (that file is read for the pattern but not
// edited -- see the workstream's file-ownership rule).
//
// No HTTP handler is wired here. BookmarkStore is deliberately NOT embedded
// into the composed Store interface in store.go; a later task decides
// whether to embed it or accept it narrowly (the way ReadingStore does in
// internal/server/handlers/reading.go) once it builds the bookmark
// endpoints.
package database

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// BookmarkStore is a narrow interface satisfied structurally by *PebbleStore.
// Deliberately NOT embedded into the composed Store interface in store.go --
// wiring a bookmark HTTP handler (which would need that) is out of scope for
// this task; whichever later task adds the handler decides then whether to
// embed this into Store or accept it narrowly the way ReadingStore does in
// internal/server/handlers/reading.go.
type BookmarkStore interface {
	CreateBookmark(b progress.Bookmark) error
	ListBookmarks(userID, itemID string) ([]progress.Bookmark, error)
	ListBookmarksForUser(userID string) ([]progress.Bookmark, error)
	UpdateBookmarkTitle(userID, itemID string, timeSec float64, newTitle string) error
	DeleteBookmark(userID, itemID string, timeSec float64) error
}

// AsBookmarkStore returns s as a BookmarkStore if it implements the
// interface (true for *PebbleStore), or nil otherwise. Callers MUST nil-check
// the result exactly like AsSyncIdentityStore's callers do.
func AsBookmarkStore(s any) BookmarkStore {
	if s == nil {
		return nil
	}
	// Looks through the indexedStore decorator; see store_capability.go.
	if bs, ok := asCapability[BookmarkStore](s); ok {
		return bs
	}
	return nil
}

// Compile-time assertion that *PebbleStore satisfies BookmarkStore, so a
// future signature drift on either side fails the build instead of only
// surfacing at AsBookmarkStore's first caller.
var _ BookmarkStore = (*PebbleStore)(nil)

func bookmarkKey(userID, itemID string, timeSec float64) []byte {
	return []byte(fmt.Sprintf("bookmark:%s:%s:%s", userID, itemID, progress.CanonicalTimeKey(timeSec)))
}

func bookmarkPrefix(userID, itemID string) []byte {
	return []byte(fmt.Sprintf("bookmark:%s:%s:", userID, itemID))
}

// bookmarkUserPrefix bounds every bookmark for userID across ALL items --
// the "bookmark:<userID>:" prefix is a strict prefix of every
// per-item bookmarkPrefix, so scanning it aggregates across items the same
// way ListUserPositionsSince's "upos:<userID>:" prefix does in
// pebble_store_playback.go.
func bookmarkUserPrefix(userID string) []byte {
	return []byte(fmt.Sprintf("bookmark:%s:", userID))
}

// createBookmarkMu serializes CreateBookmark's get-then-set upsert section so
// two concurrent upserts at the identical (userID, itemID, time) key cannot
// race: without this, both goroutines could read "not found", both decide to
// set CreatedAt to now, and the loser's write could still land last with a
// title that lost the race non-deterministically alongside a doubled
// CreatedAt decision. Mirrors the syncIDMintMu idiom in
// pebble_store_syncid.go. Not held across ListBookmarks/DeleteBookmark/
// UpdateBookmarkTitle -- only the create-upsert path needs this.
var createBookmarkMu sync.Mutex

// CreateBookmark upserts (creates or replaces the title of) a bookmark at
// (b.UserID, b.ItemID, CanonicalTimeKey(b.TimeSec)). Calling it twice with
// the same time and a different title updates the title rather than
// erroring -- real ABS's create endpoint is also the natural "move the
// playhead and re-save at the same spot" path a client might replay.
// CreatedAt is set only if no record exists yet at that key (preserving the
// original creation time across an upsert); UpdatedAt always advances.
func (p *PebbleStore) CreateBookmark(b progress.Bookmark) error {
	if err := progress.ValidateBookmark(b); err != nil {
		return err
	}

	createBookmarkMu.Lock()
	defer createBookmarkMu.Unlock()

	key := bookmarkKey(b.UserID, b.ItemID, b.TimeSec)
	nowMs := time.Now().UnixMilli()

	existingData, closer, err := p.db.Get(key)
	switch {
	case err == nil:
		var existing progress.Bookmark
		if unmarshalErr := json.Unmarshal(existingData, &existing); unmarshalErr != nil {
			closer.Close()
			return unmarshalErr
		}
		closer.Close()
		b.CreatedAt = existing.CreatedAt
	case err == pebble.ErrNotFound:
		b.CreatedAt = nowMs
	default:
		return err
	}
	b.UpdatedAt = nowMs

	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return p.db.Set(key, data, pebble.NoSync)
}

// ListBookmarks returns every bookmark for (userID, itemID), prefix-scanned
// using the same LowerBound/UpperBound("~") pattern as GetUserPosition in
// pebble_store_playback.go.
func (p *PebbleStore) ListBookmarks(userID, itemID string) ([]progress.Bookmark, error) {
	if userID == "" || itemID == "" {
		return nil, nil
	}
	prefix := bookmarkPrefix(userID, itemID)
	upper := append(append([]byte{}, prefix...), '~')
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []progress.Bookmark
	for iter.First(); iter.Valid(); iter.Next() {
		var b progress.Bookmark
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// ListBookmarksForUser returns every bookmark for userID across ALL items --
// the source the `/api/me` `bookmarks[]` array is built from on every login
// and home-screen refresh. Uses the same prefix-scan pattern as
// ListBookmarks, just bounded one segment higher (by userID alone rather
// than userID+itemID).
func (p *PebbleStore) ListBookmarksForUser(userID string) ([]progress.Bookmark, error) {
	if userID == "" {
		return nil, nil
	}
	prefix := bookmarkUserPrefix(userID)
	upper := append(append([]byte{}, prefix...), '~')
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []progress.Bookmark
	for iter.First(); iter.Valid(); iter.Next() {
		var b progress.Bookmark
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// UpdateBookmarkTitle changes the title of an existing bookmark at
// (userID, itemID, timeSec). It returns an error and does not create a new
// bookmark if none exists at that key.
func (p *PebbleStore) UpdateBookmarkTitle(userID, itemID string, timeSec float64, newTitle string) error {
	key := bookmarkKey(userID, itemID, timeSec)

	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return fmt.Errorf("database: no bookmark for user %q item %q time %v", userID, itemID, timeSec)
	}
	if err != nil {
		return err
	}
	var b progress.Bookmark
	unmarshalErr := json.Unmarshal(data, &b)
	closer.Close()
	if unmarshalErr != nil {
		return unmarshalErr
	}

	b.Title = newTitle
	b.UpdatedAt = time.Now().UnixMilli()
	if err := progress.ValidateBookmark(b); err != nil {
		return err
	}

	newData, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return p.db.Set(key, newData, pebble.NoSync)
}

// DeleteBookmark removes the bookmark at (userID, itemID, timeSec), if any.
func (p *PebbleStore) DeleteBookmark(userID, itemID string, timeSec float64) error {
	return p.db.Delete(bookmarkKey(userID, itemID, timeSec), pebble.NoSync)
}
