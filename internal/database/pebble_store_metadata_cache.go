// file: internal/database/pebble_store_metadata_cache.go
// version: 1.6.0
// guid: 3f8b41d7-9e26-4c05-b1a8-7d0e5c26f934
// last-edited: 2026-10-06

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// metadataCacheKeyPrefix is the prefix every per-book cache key shares.
const metadataCacheKeyPrefix = "metadata_cache:"

func metadataCacheKey(bookID string) []byte {
	return []byte(metadataCacheKeyPrefix + bookID)
}

// metadataCacheLog is the process log of the metadata_cache keyspace.
var metadataCacheLog = logger.New("database.metadata-cache")

// metadataCacheLock is bookID's stripe of PebbleStore.metadataCacheLocks.
func (p *PebbleStore) metadataCacheLock(bookID string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bookID))
	return &p.metadataCacheLocks[h.Sum32()%uint32(len(p.metadataCacheLocks))]
}

// GetMetadataCache reads the cache entry for bookID, or returns
// (nil, nil) when the key is absent.
func (p *PebbleStore) GetMetadataCache(bookID string) (*MetadataCandidateCache, error) {
	val, closer, err := p.db.Get(metadataCacheKey(bookID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("pebble get metadata_cache:%s: %w", bookID, err)
	}
	defer closer.Close()

	var entry MetadataCandidateCache
	if err := json.Unmarshal(val, &entry); err != nil {
		return nil, fmt.Errorf("decode metadata_cache:%s: %w", bookID, err)
	}
	return &entry, nil
}

// PutMetadataCache writes (or replaces) the cache entry for entry.BookID.
func (p *PebbleStore) PutMetadataCache(entry *MetadataCandidateCache) error {
	if entry == nil || entry.BookID == "" {
		return fmt.Errorf("PutMetadataCache: nil entry or empty BookID")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode metadata_cache:%s: %w", entry.BookID, err)
	}
	mu := p.metadataCacheLock(entry.BookID)
	mu.Lock()
	defer mu.Unlock()
	if err := p.db.Set(metadataCacheKey(entry.BookID), data, pebble.Sync); err != nil {
		return fmt.Errorf("pebble set metadata_cache:%s: %w", entry.BookID, err)
	}
	p.bumpMetadataCacheGeneration(entry.BookID)
	return nil
}

// DeleteMetadataCache removes the cache entry for bookID. Missing
// keys are not an error.
func (p *PebbleStore) DeleteMetadataCache(bookID string) error {
	mu := p.metadataCacheLock(bookID)
	mu.Lock()
	defer mu.Unlock()
	if err := p.db.Delete(metadataCacheKey(bookID), pebble.Sync); err != nil {
		return fmt.Errorf("pebble delete metadata_cache:%s: %w", bookID, err)
	}
	p.bumpMetadataCacheGeneration(bookID)
	return nil
}

// ListMetadataCacheKeys lives in pebble_store_metadata_cache_summaries.go: it
// is served from an in-process index kept current by the change log below.

// MetadataCacheGeneration is the metadata-cache generation: the number of
// writes to the "metadata_cache:" keyspace since this store was opened. Every
// writer moves it, after its commit, and records the book it wrote
// (MetadataCacheChangedSince):
//
//   - PutMetadataCache and DeleteMetadataCache;
//   - UpdateBook, when an identity change (title, author, narrator, series,
//     ISBN, ASIN) deletes the book's cache row in the book's own batch --
//     an ASIN/ISBN backfill does this on every book it writes;
//   - DeleteBook, which deletes the row with the book's other sidecars;
//   - SetRaw, DeleteRaw and DeleteRawBatch on a key under the prefix;
//   - Reset, which wipes everything (and so records no book: a reader behind
//     it is told to rebuild).
//
// A reader holding something derived from the cache -- the review listing's
// snapshot -- compares it to the value it read when it began building, to
// learn for the cost of an atomic load whether anything it read can have
// changed, and asks MetadataCacheChangedSince which rows. It is per process
// and starts at 0 on every open: a value is only comparable with another from
// the same store.
func (p *PebbleStore) MetadataCacheGeneration() uint64 {
	return p.cacheGen.Value()
}

// asinReplacedOnWrite reports whether a book write takes oldBook's non-empty
// ASIN away: replaced by another value or cleared. Filling an empty ASIN, or
// rewriting it with a different case or spacing, is not.
func asinReplacedOnWrite(oldBook, newBook *Book) (old string, replaced bool) {
	if oldBook == nil || oldBook.ASIN == nil {
		return "", false
	}
	old = strings.TrimSpace(*oldBook.ASIN)
	if old == "" {
		return "", false
	}
	cur := ""
	if newBook != nil && newBook.ASIN != nil {
		cur = strings.TrimSpace(*newBook.ASIN)
	}
	return old, !strings.EqualFold(old, cur)
}

// stageCacheASINStamp records, in the book write's batch, that the book's
// cached candidates were fetched for oldASIN (FetchedForASIN), when the row
// exists and has no value yet. A row that already records one keeps it:
// that is still the ASIN its candidates were fetched for, so an ASIN
// replaced and later restored reads as current again.
//
// The caller must hold the book's metadataCacheLock until the batch commits;
// staged reports whether the row was rewritten (the caller then bumps the
// cache generation after the commit).
func (p *PebbleStore) stageCacheASINStamp(batch *pebble.Batch, bookID, oldASIN string) (staged bool, err error) {
	entry, err := p.GetMetadataCache(bookID)
	if err != nil {
		// A row that cannot be read cannot be stamped. Keeping it unstamped
		// would let candidates fetched for the old ASIN read as current, so
		// it is dropped instead: the next fetch rewrites it.
		metadataCacheLog.Warn("metadata_cache:%s unreadable while its book's ASIN was replaced; dropping it: %v",
			logger.SanitizeLogValue(bookID), err)
		return p.stageDeleteIfPresent(batch, metadataCacheKey(bookID))
	}
	if entry == nil || strings.TrimSpace(entry.FetchedForASIN) != "" {
		return false, nil
	}
	entry.FetchedForASIN = oldASIN
	data, err := json.Marshal(entry)
	if err != nil {
		return false, fmt.Errorf("encode metadata_cache:%s: %w", bookID, err)
	}
	if err := batch.Set(metadataCacheKey(bookID), data, nil); err != nil {
		return false, err
	}
	return true, nil
}
