// file: internal/database/pebble_store_syncid.go
// version: 1.2.1
// guid: 5b9bd4e0-2ee2-436d-ac81-16b93de80eb3
// last-edited: 2026-07-31

// Package database: sync_item keyspace — durable ABS `libraryItemId` identity.
//
// libraryItemId is the key every Audiobookshelf-compatible client stores
// progress and bookmarks against. Book ULIDs churn under untagged moves
// (version-link mints a new ULID) and dedup merges (the surviving ULID
// changes), so the id exposed to clients must be a separate, never-reused
// identity that we can repoint underneath a churning ULID.
//
// See docs/specs/2026-07-29-abs-sync-api-design.md §1.7.1, §4 for the full
// design. In short: Absorb (a target client) splits compound podcast keys by
// FIXED BYTE OFFSET (substring(0,36) / substring(37)) at 4+ call sites. Our
// Book IDs are 26-char ULIDs -- too short, and would break episode splitting.
// The syncID minted here MUST be exactly 36 characters in canonical
// hyphenated UUID form (8-4-4-4-12 hex groups) or Absorb mis-truncates it
// into the wrong /api/me/progress/... path.
//
// Two keys:
//   - sync_item:<syncID>        -> JSON-encoded SyncItem record.
//   - sync_item:book:<bookID>   -> raw bytes of the syncID (reverse index,
//     same "value is the id as raw bytes" convention as book:path:<path>).
//
// This file does not wire up any live HTTP path -- nothing reads or writes
// these keys yet. It is the foundation TASK-03 (merge-follow), TASK-04
// (backfill), and TASK-05 (survival tests) build on.
package database

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// SyncItem is the durable identity record behind an ABS-visible
// libraryItemId. RedirectTo empty means this is a live, client-visible
// record; non-empty means this syncID was a merge loser and now redirects to
// the winner's syncID (see RecordSyncMerge). CurrentBookID is meaningless on
// a redirect record -- it is left as whatever it was at merge time; readers
// must follow RedirectTo first (see ResolveSyncItem).
type SyncItem struct {
	SyncID        string    `json:"sync_id"`
	CurrentBookID string    `json:"current_book_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	MergedFrom    []string  `json:"merged_from,omitempty"`
	RedirectTo    string    `json:"redirect_to,omitempty"`
}

// SyncIdentityStore is implemented by PebbleStore. Consumers that only have a
// database.Store value obtain it via AsSyncIdentityStore's type assertion --
// this keeps store.go untouched (repo rule: nobody edits it; every new
// capability lives in its own file) and means MemStore/mocks are not required
// to implement it.
type SyncIdentityStore interface {
	MintOrGetSyncID(bookID string) (string, error)
	GetSyncIDForBook(bookID string) (string, bool, error)
	ResolveSyncItem(syncID string) (*SyncItem, error)
	RepointSyncItem(oldBookID, newBookID string) error
	RecordSyncMerge(loserBookID, winnerBookID string) error
}

// AsSyncIdentityStore returns s as a SyncIdentityStore if it implements the
// interface (true for *PebbleStore), or nil otherwise. Callers MUST nil-check
// the result exactly like internal/merge/service.go's eidStore != nil checks.
func AsSyncIdentityStore(s any) SyncIdentityStore {
	if s == nil {
		return nil
	}
	// asCapability, not a bare type assertion: the server decorates s.store with
	// indexedStore (which embeds the Store INTERFACE and so hides every
	// capability method) during Start(). See store_capability.go.
	if ss, ok := AsCapability[SyncIdentityStore](s); ok {
		return ss
	}
	return nil
}

// Compile-time assertion that *PebbleStore satisfies SyncIdentityStore, so a
// future signature drift on either side fails the build instead of only
// surfacing at AsSyncIdentityStore's first caller.
var _ SyncIdentityStore = (*PebbleStore)(nil)

// syncIDMintMu serializes the check-then-mint-then-write section of
// MintOrGetSyncID so two concurrent first-encounters of the same book cannot
// both mint a syncID and race the reverse-index write (mirrors the
// mergeSerializeMu idiom in internal/merge/serialize.go). It is not held
// across any Pebble iteration or I/O beyond the point-read/point-writes that
// method needs.
var syncIDMintMu sync.Mutex

// newSyncID mints a random UUIDv4 by hand via crypto/rand -- deliberately not
// using google/uuid, which is an indirect-only dependency today; promoting it
// to direct is reserved for a different task. The bit-twiddling below is what
// makes the string a valid UUIDv4 (version 4, variant 10) that satisfies
// Absorb's fixed-offset length check.
func newSyncID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func syncItemKey(syncID string) []byte {
	return []byte(fmt.Sprintf("sync_item:%s", syncID))
}

func syncItemBookKey(bookID string) []byte {
	return []byte(fmt.Sprintf("sync_item:book:%s", bookID))
}

// getSyncItem reads and unmarshals the sync_item:<syncID> record. Returns
// (nil, nil) on not-found, mirroring GetBookByID's convention.
func (p *PebbleStore) getSyncItem(syncID string) (*SyncItem, error) {
	value, closer, err := p.db.Get(syncItemKey(syncID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var item SyncItem
	if err := json.Unmarshal(value, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// MintOrGetSyncID returns the durable syncID for bookID, minting a fresh one
// on first encounter and persisting both the SyncItem record and the reverse
// index in a single batch. Subsequent calls for the same bookID return the
// same syncID. This store is a pure key-value layer -- it does not validate
// that bookID refers to a real Book; that is the caller's job.
func (p *PebbleStore) MintOrGetSyncID(bookID string) (string, error) {
	if bookID == "" {
		return "", fmt.Errorf("bookID required")
	}

	syncIDMintMu.Lock()
	defer syncIDMintMu.Unlock()

	existing, closer, err := p.db.Get(syncItemBookKey(bookID))
	if err == nil {
		id := string(existing)
		closer.Close()
		return id, nil
	}
	if err != pebble.ErrNotFound {
		return "", err
	}

	id, err := newSyncID()
	if err != nil {
		return "", err
	}
	item := SyncItem{
		SyncID:        id,
		CurrentBookID: bookID,
		CreatedAt:     time.Now(),
	}
	data, err := json.Marshal(item)
	if err != nil {
		return "", err
	}

	batch := p.db.NewBatch()
	if err := batch.Set(syncItemKey(id), data, nil); err != nil {
		batch.Close()
		return "", err
	}
	if err := batch.Set(syncItemBookKey(bookID), []byte(id), nil); err != nil {
		batch.Close()
		return "", err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return "", err
	}
	return id, nil
}

// GetSyncIDForBook is a read-only point-get of the reverse index. It returns
// ("", false, nil) when bookID has no sync item yet -- "no sync item yet" is
// a normal state, not an error.
func (p *PebbleStore) GetSyncIDForBook(bookID string) (string, bool, error) {
	value, closer, err := p.db.Get(syncItemBookKey(bookID))
	if err == pebble.ErrNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer closer.Close()
	return string(value), true, nil
}

// ResolveSyncItem reads the sync_item:<syncID> record and follows RedirectTo
// chains left behind by merges until it reaches a live record (RedirectTo ==
// ""). It caps at 10 hops and tracks visited ids to guard against a cycle or
// runaway chain; hitting either returns an error instead of looping forever
// or returning stale data. Returns (nil, nil) if the starting syncID itself
// does not exist, mirroring GetBookByID's not-found convention.
func (p *PebbleStore) ResolveSyncItem(syncID string) (*SyncItem, error) {
	const maxHops = 10
	visited := make(map[string]bool, maxHops)

	current := syncID
	for hop := 0; hop < maxHops; hop++ {
		if visited[current] {
			return nil, fmt.Errorf("sync item redirect chain too long or cyclic starting at %s", syncID)
		}
		visited[current] = true

		item, err := p.getSyncItem(current)
		if err != nil {
			return nil, err
		}
		if item == nil {
			if current == syncID {
				return nil, nil
			}
			return nil, fmt.Errorf("sync item redirect chain too long or cyclic starting at %s", syncID)
		}
		if item.RedirectTo == "" {
			return item, nil
		}
		current = item.RedirectTo
	}
	return nil, fmt.Errorf("sync item redirect chain too long or cyclic starting at %s", syncID)
}

// RepointSyncItem moves the reverse index from oldBookID to newBookID and
// updates the SyncItem record's CurrentBookID -- the untagged-move case,
// where a version-link mints a new Book ULID but the client-visible identity
// must survive. If oldBookID has no sync item yet, there is nothing to carry
// forward and this is a no-op (returns nil). This method has no caller yet;
// wiring it into the scanner's untagged-move/version-link path is out of
// scope for this task.
func (p *PebbleStore) RepointSyncItem(oldBookID, newBookID string) error {
	syncID, has, err := p.GetSyncIDForBook(oldBookID)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}

	item, err := p.getSyncItem(syncID)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("sync item %s referenced by reverse index but record missing", syncID)
	}
	item.CurrentBookID = newBookID
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	if err := batch.Delete(syncItemBookKey(oldBookID), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(syncItemBookKey(newBookID), []byte(syncID), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(syncItemKey(syncID), data, nil); err != nil {
		batch.Close()
		return err
	}
	return batch.Commit(pebble.Sync)
}

// RecordSyncMerge is the primitive the merge hook calls when loserBookID
// merges into winnerBookID. It ensures the winner has a syncID (merges can
// involve two books that never had one minted yet), redirects the loser's
// syncID to the winner's, and appends the loser's syncID to the winner's
// MergedFrom (deduplicated -- a merge can be retried).
//
// If the loser never had a client-visible identity (no sync item minted),
// this is a no-op: nothing to redirect.
//
// Idempotent: if the loser's record already redirects to the winner's
// syncID, this returns nil without touching either record again -- safe to
// re-run on a retried merge or a backfill sweep that re-touches the same
// pair.
//
// sync_item:book:<loserBookID> is deliberately left untouched, still
// pointing at loserSyncID -- a future lookup of the loser's (soft-deleted)
// book resolves to loserSyncID, and ResolveSyncItem follows the redirect to
// the winner. Deleting it would make a stale client request 404 instead of
// correctly resolving.
func (p *PebbleStore) RecordSyncMerge(loserBookID, winnerBookID string) error {
	winnerSyncID, err := p.MintOrGetSyncID(winnerBookID)
	if err != nil {
		return err
	}

	loserSyncID, has, err := p.GetSyncIDForBook(loserBookID)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}

	loserItem, err := p.getSyncItem(loserSyncID)
	if err != nil {
		return err
	}
	if loserItem == nil {
		return fmt.Errorf("sync item %s referenced by reverse index but record missing", loserSyncID)
	}
	if loserItem.RedirectTo == winnerSyncID {
		// Already recorded.
		return nil
	}

	winnerItem, err := p.getSyncItem(winnerSyncID)
	if err != nil {
		return err
	}
	if winnerItem == nil {
		return fmt.Errorf("sync item %s just minted/looked-up but record missing", winnerSyncID)
	}

	loserItem.RedirectTo = winnerSyncID
	loserData, err := json.Marshal(loserItem)
	if err != nil {
		return err
	}

	alreadyMerged := false
	for _, id := range winnerItem.MergedFrom {
		if id == loserSyncID {
			alreadyMerged = true
			break
		}
	}
	if !alreadyMerged {
		winnerItem.MergedFrom = append(winnerItem.MergedFrom, loserSyncID)
	}
	winnerData, err := json.Marshal(winnerItem)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	if err := batch.Set(syncItemKey(loserSyncID), loserData, nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(syncItemKey(winnerSyncID), winnerData, nil); err != nil {
		batch.Close()
		return err
	}
	return batch.Commit(pebble.Sync)
}
