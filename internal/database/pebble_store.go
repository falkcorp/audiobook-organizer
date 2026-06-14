// file: internal/database/pebble_store.go
// version: 1.88.0
// guid: 0c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-06-14

package database

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	ulid "github.com/oklog/ulid/v2"
)

// prefixEnd returns an upper-bound key for Pebble range iteration
// over all keys sharing `prefix`. It creates a separate byte slice
// so the increment doesn't mutate the original prefix.
func prefixEnd(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	upper[len(upper)-1]++
	return upper
}

// serializeBookForIndex marshals a Book to JSON for index storage.
// This enables GetBooksBySeriesID and GetBooksByAuthorID to deserialize
// directly from the index without secondary point lookups.
func serializeBookForIndex(book *Book) ([]byte, error) {
	return json.Marshal(book)
}

// deserializeBookFromIndex unmarshals a Book from index storage.
// Handles both new format (full Book JSON) and old format (just book ID).
// If the value looks like a ULID (32 chars, alphanumeric), treat it as a legacy format
// that still needs a point lookup. Otherwise, unmarshal as Book JSON.
func deserializeBookFromIndex(value []byte, fallbackLookup func(string) (*Book, error)) (*Book, error) {
	if len(value) == 0 {
		return nil, nil
	}

	// Check if this looks like a legacy format (just a ULID)
	valueStr := string(value)
	if len(valueStr) == 26 && isValidULID(valueStr) {
		// Legacy format: just the book ID. Fall back to point lookup.
		return fallbackLookup(valueStr)
	}

	// New format: full Book JSON
	var book Book
	if err := json.Unmarshal(value, &book); err != nil {
		return nil, err
	}
	return &book, nil
}

// isValidULID checks if a string looks like a ULID (26 alphanumeric chars)
func isValidULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// PebbleStore implements the Store interface using PebbleDB (LSM key-value store)
//
// Key Schema:
// - author:<id>                -> Author JSON
// - author:name:<name>         -> author_id (for lookups)
// - series:<id>                -> Series JSON
// - series:name:<name>:<author_id> -> series_id (for lookups)
// - book:<id>                  -> Book JSON
// - book:path:<path>           -> book_id (for lookups)
// NOTE: book:series and book:author prefix indexes were removed in Task 3.4.
//       GetBooksBySeriesID and GetBooksByAuthorID fall back to a full Pebble scan
//       (the in-memory query layer covers the hot paths).
// - import_path:<id>           -> ImportPath JSON
// - import_path:path:<path>    -> import_path_id (for lookups)
// - operation:<id>             -> Operation JSON
// - operationlog:<operation_id>:<timestamp>:<seq> -> OperationLog JSON
// - preference:<key>           -> UserPreference JSON
// - playlist:<id>              -> Playlist JSON
// - playlist:series:<series_id> -> playlist_id
// - playlistitem:<playlist_id>:<position> -> PlaylistItem JSON
// - author_alias:<id>           -> AuthorAlias JSON
// - author_alias:author:<author_id>:<alias_id> -> alias_id (for author queries)
// - author_alias:name:<name>    -> alias_id (for name lookups)
// - counter:author              -> next author ID
// - counter:author_alias        -> next author alias ID
// - counter:series             -> next series ID
// - counter:book               -> next book ID
// - counter:import_path        -> next import path ID
// - counter:operationlog       -> next operation log ID
// - counter:playlist           -> next playlist ID
// - counter:playlistitem       -> next playlist item ID
// - metadata_state:<book_id>:<field> -> MetadataFieldState JSON
// - author_tombstone:<old_id>        -> canonical_id (merged author redirect)

type PebbleStore struct {
	db *pebble.DB
	// memPtr atomically holds the warm in-memory query layer (or nil while
	// warmup is in progress / failed). Reads load it lock-free; warmup
	// stores it once it completes. Use the mem() helper to read; never
	// touch memPtr directly outside the warmup path.
	memPtr                   atomic.Pointer[MemStore]
	counterMu                sync.Mutex // protects nextID read-modify-write
	opsMu                    sync.Mutex // serializes v2 op CAS operations (SetOperationV2StatusIfQueued)
	opsLogSeq                int64      // monotonic counter for log key uniqueness; accessed via atomic
	rootDir                  string     // organized library root; set via SetRootDir after config load
	libraryCountsRecomputeMu sync.Mutex // gates recompute to prevent stampede when N callers see dirty cache
	UseMemDB                 bool       // feature flag: use in-memory query layer for aggregations / filtered reads

	// warmupCancel cancels the async memdb warmup goroutine; warmupDone is
	// closed when that goroutine exits. Close() cancels and waits on these
	// before closing the underlying Pebble DB — otherwise the warmup's db.NewIter
	// races the close and panics ("pebble: closed" / nil-pointer deref). Set once
	// in NewPebbleStore before the store is returned, so no mutex is needed.
	warmupCancel context.CancelFunc
	warmupDone   chan struct{}
}

// mem returns the active in-memory query layer or nil if warmup hasn't
// completed yet. Read paths that check `p.mem() != nil` should use
// `p.mem() != nil` instead.
func (p *PebbleStore) mem() *MemStore { return p.memPtr.Load() }

// IsMemReady reports whether the in-memory query layer is published and
// serving reads. Used by the server-side list-cache warmer to gate on
// memdb readiness so warm-up queries hit the fast O(log n) memdb path
// instead of the slow Pebble JSON-unmarshal path.
func (p *PebbleStore) IsMemReady() bool { return p.memPtr.Load() != nil }

const statsLibraryKey = "stats:library"
const statsLibraryTTL = 10 * time.Minute
const defaultLibraryCountsMinIntervalSeconds = 600 // 10 minutes

func getLibraryCountsMinIntervalSeconds() int {
	if s := os.Getenv("LIBRARY_COUNTS_CACHE_MIN_INTERVAL_SECONDS"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			return v
		}
	}
	return defaultLibraryCountsMinIntervalSeconds
}

// SetRootDir updates the organized-library root used when computing LibraryStats
// and invalidates the cache so the next GetDashboardStats recomputes with the new path.
func (p *PebbleStore) SetRootDir(rootDir string) {
	if p.rootDir != rootDir {
		p.rootDir = rootDir
		p.InvalidateLibraryStats()
	}
}

// InvalidateLibraryStats drops the cached stats:library key so the next
// GetDashboardStats call triggers a fresh full recompute.
// NoSync is intentional: a crash before the delete flushes leaves a stale
// cache that expires within statsLibraryTTL — identical to the pre-cache
// behaviour. The benefit is avoiding a sync flush on every book/file mutation.
func (p *PebbleStore) InvalidateLibraryStats() {
	if err := p.db.Delete([]byte(statsLibraryKey), pebble.NoSync); err != nil {
		slog.Warn("pebble Delete stats:library", "error", err)
	}
	slog.Debug("library_counts marked dirty", "reason", "invalidated")
}

func (p *PebbleStore) readCachedLibraryStats() *LibraryStats {
	val, closer, err := p.db.Get([]byte(statsLibraryKey))
	if err != nil {
		return nil
	}
	defer closer.Close()
	var s LibraryStats
	if err := json.Unmarshal(val, &s); err != nil {
		return nil
	}
	if time.Since(s.ComputedAt) > statsLibraryTTL {
		return nil
	}
	return &s
}

func (p *PebbleStore) writeCachedLibraryStats(s *LibraryStats) {
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := p.db.Set([]byte(statsLibraryKey), data, pebble.Sync); err != nil {
		slog.Error("pebble Set stats:library", "error", err)
	}
}

// NewPebbleStore creates a new PebbleDB store
func NewPebbleStore(path string) (*PebbleStore, error) {
	db, err := pebble.Open(path, &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open PebbleDB: %w", err)
	}

	store := &PebbleStore{
		db:       db,
		UseMemDB: true, // in-memory query layer is the default after Phase 3
	}

	slog.Info("PebbleDB opened", "path", path, "format_version", db.FormatMajorVersion())

	if err := store.migrateImportPathKeys(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate import path keys: %w", err)
	}

	// Initialize counters if they don't exist
	counters := []string{"author", "author_alias", "series", "book", "import_path", "operationlog", "playlist", "playlistitem", "preference"}
	for _, counter := range counters {
		key := fmt.Sprintf("counter:%s", counter)
		if _, closer, err := db.Get([]byte(key)); err == pebble.ErrNotFound {
			if err := db.Set([]byte(key), []byte("1"), pebble.Sync); err != nil {
				db.Close()
				return nil, fmt.Errorf("failed to initialize counter %s: %w", counter, err)
			}
		} else if err == nil {
			closer.Close()
		} else {
			db.Close()
			return nil, fmt.Errorf("failed to check counter %s: %w", counter, err)
		}
	}

	// Initialize in-memory query layer. Warmup runs in a goroutine so the
	// server is available immediately — reads transparently fall back to
	// Pebble until memdb is ready (couple of minutes for ~50K books).
	// store.memPtr is only published once warmup completes successfully.
	//
	// Started LAST — after every early-return error path that calls db.Close()
	// directly — so a construction failure can never close the DB while the
	// warmup goroutine is iterating it. The goroutine iterates store.db, so
	// Close() must cancel it and wait for it to exit before closing the DB (else
	// NewIter races the close and panics). warmupDone is always non-nil so Close
	// can wait unconditionally; it is closed immediately when there is no warmup
	// goroutine to wait for.
	store.warmupDone = make(chan struct{})
	if memStore, memErr := NewMemStore(); memErr != nil {
		slog.Warn("memdb init failed, in-memory queries disabled", "error", memErr)
		close(store.warmupDone)
	} else {
		warmupCtx, warmupCancel := context.WithCancel(context.Background())
		store.warmupCancel = warmupCancel
		go func() {
			defer close(store.warmupDone)
			started := time.Now()
			slog.Info("memdb warmup starting (async)")
			if warmErr := memStore.WarmFromPebble(warmupCtx, store); warmErr != nil {
				slog.Warn("memdb warmup failed, will stay on Pebble for reads",
					"error", warmErr, "duration_ms", time.Since(started).Milliseconds())
				return
			}
			store.memPtr.Store(memStore)
			slog.Info("memdb warmup published",
				"duration_ms", time.Since(started).Milliseconds())
		}()
	}

	return store, nil
}

// Close closes the database
func (p *PebbleStore) Close() error {
	// Stop the async memdb warmup before closing the DB. The warmup goroutine
	// iterates p.db; closing the DB out from under it races warmIter's NewIter
	// and panics ("pebble: closed" / nil-pointer deref). Cancel it and wait for
	// it to fully exit first.
	if p.warmupCancel != nil {
		p.warmupCancel()
	}
	if p.warmupDone != nil {
		<-p.warmupDone
	}
	return p.db.Close()
}

// DB returns the underlying *pebble.DB. Used by AIScanStore to share the DB.
func (p *PebbleStore) DB() *pebble.DB {
	return p.db
}

// Helper functions

func (p *PebbleStore) nextID(counter string) (int, error) {
	p.counterMu.Lock()
	defer p.counterMu.Unlock()

	key := []byte(fmt.Sprintf("counter:%s", counter))

	value, closer, err := p.db.Get(key)
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, err
	}

	nextID := id + 1
	if err := p.db.Set(key, []byte(strconv.Itoa(nextID)), pebble.Sync); err != nil {
		return 0, err
	}

	return id, nil
}

func newULID() (string, error) {
	entropy := ulid.Monotonic(rand.Reader, 0)
	id, err := ulid.New(ulid.Timestamp(time.Now()), entropy)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// migrateImportPathKeys renames legacy library* keys and counters to import_path* equivalents.
// Safe to run multiple times and before counter initialization.
func (p *PebbleStore) migrateImportPathKeys() error {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("library:"),
		UpperBound: []byte("library;"),
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator for legacy keys: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		oldKey := string(iter.Key())
		newKey := strings.Replace(oldKey, "library:path:", "import_path:path:", 1)
		if newKey == oldKey {
			newKey = strings.Replace(oldKey, "library:", "import_path:", 1)
		}
		if newKey == oldKey {
			continue
		}

		value := append([]byte(nil), iter.Value()...)
		if err := p.db.Set([]byte(newKey), value, pebble.Sync); err != nil {
			return fmt.Errorf("failed to write migrated key %s: %w", newKey, err)
		}
		if err := p.db.Delete([]byte(oldKey), pebble.Sync); err != nil {
			return fmt.Errorf("failed to delete legacy key %s: %w", oldKey, err)
		}
	}

	if value, closer, err := p.db.Get([]byte("counter:library")); err == nil {
		defer closer.Close()

		if _, counterCloser, counterErr := p.db.Get([]byte("counter:import_path")); counterErr == nil {
			counterCloser.Close()
			_ = value // already migrated; keep existing value
		} else if counterErr != pebble.ErrNotFound {
			return fmt.Errorf("failed to read import path counter: %w", counterErr)
		} else if err := p.db.Set([]byte("counter:import_path"), value, pebble.Sync); err != nil {
			return fmt.Errorf("failed to migrate import path counter: %w", err)
		}

		if err := p.db.Delete([]byte("counter:library"), pebble.Sync); err != nil {
			return fmt.Errorf("failed to remove legacy library counter: %w", err)
		}
	} else if err != nil && err != pebble.ErrNotFound {
		return fmt.Errorf("failed to read legacy library counter: %w", err)
	}

	return nil
}

// Author operations

func (p *PebbleStore) GetAllAuthors() ([]Author, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthors()
	}
	var authors []Author
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author:0"),
		UpperBound: []byte("author:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}

		var author Author
		if err := json.Unmarshal(iter.Value(), &author); err != nil {
			return nil, err
		}
		authors = append(authors, author)
	}

	return authors, nil
}

func (p *PebbleStore) GetAuthorByID(id int) (*Author, error) {
	key := []byte(fmt.Sprintf("author:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		// Check for tombstone redirect
		canonicalID, tErr := p.GetAuthorTombstone(id)
		if tErr != nil || canonicalID == 0 {
			return nil, nil
		}
		return p.GetAuthorByID(canonicalID)
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var author Author
	if err := json.Unmarshal(value, &author); err != nil {
		return nil, err
	}
	return &author, nil
}

// GetAuthorsByIDs returns a map from authorID → *Author for the given IDs.
// Deduplicates IDs before fetching; missing IDs are absent from the result map.
func (p *PebbleStore) GetAuthorsByIDs(ids []int) (map[int]*Author, error) {
	result := make(map[int]*Author, len(ids))
	for _, id := range ids {
		if _, already := result[id]; already {
			continue
		}
		a, err := p.GetAuthorByID(id)
		if err != nil {
			return nil, err
		}
		if a != nil {
			result[id] = a
		}
	}
	return result, nil
}

func (p *PebbleStore) GetAuthorByName(name string) (*Author, error) {
	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("author:name:%s", strings.ToLower(name)))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetAuthorByID(id)
}

func (p *PebbleStore) CreateAuthor(name string) (*Author, error) {
	// Check if author already exists
	existing, err := p.GetAuthorByName(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	id, err := p.nextID("author")
	if err != nil {
		return nil, err
	}

	author := &Author{ID: id, Name: name}
	data, err := json.Marshal(author)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("author:%d", id))
	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("author:name:%s", strings.ToLower(name)))

	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	p.UpsertAuthorToMemDB(author)
	return author, nil
}

func (p *PebbleStore) DeleteAuthor(id int) error {
	// Get the author to find name for index cleanup
	author, err := p.GetAuthorByID(id)
	if err != nil {
		return err
	}
	if author == nil {
		return nil
	}

	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("author:%d", id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:%d: %w", id, err)
	}
	if err := batch.Delete([]byte(fmt.Sprintf("author:name:%s", strings.ToLower(author.Name))), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:name: %w", err)
	}

	// Delete aliases for this author (cascade)
	if err := p.deleteAuthorAliases(batch, id); err != nil {
		batch.Close()
		return fmt.Errorf("delete author aliases: %w", err)
	}

	// Delete book_author entries for this author
	iter, iterErr := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_author:"),
		UpperBound: []byte("book_author;"),
	})
	if iterErr == nil {
		defer iter.Close()
		for iter.First(); iter.Valid(); iter.Next() {
			val, valErr := iter.ValueAndErr()
			if valErr != nil {
				continue
			}
			var ba BookAuthor
			if json.Unmarshal(val, &ba) == nil && ba.AuthorID == id {
				if err := batch.Delete(iter.Key(), nil); err != nil {
					batch.Close()
					return fmt.Errorf("pebble Delete book_author entry: %w", err)
				}
			}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteAuthorFromMemDB(id)
	p.DeleteAuthorAliasesByAuthorIDFromMemDB(id)
	return nil
}

func (p *PebbleStore) UpdateAuthorName(id int, name string) error {
	author, err := p.GetAuthorByID(id)
	if err != nil {
		return err
	}
	if author == nil {
		return fmt.Errorf("author %d not found", id)
	}

	batch := p.db.NewBatch()
	// Remove old name index
	if err := batch.Delete([]byte(fmt.Sprintf("author:name:%s", strings.ToLower(author.Name))), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author:name: %w", err)
	}

	// Update author record
	author.Name = name
	data, err := json.Marshal(author)
	if err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set([]byte(fmt.Sprintf("author:%d", id)), data, nil); err != nil {
		batch.Close()
		return err
	}
	// Add new name index
	if err := batch.Set([]byte(fmt.Sprintf("author:name:%s", strings.ToLower(name))), []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.UpsertAuthorToMemDB(author)
	return nil
}

// Author Alias operations
//
// Key schema:
//   author_alias:<id>                              → AuthorAlias JSON
//   author_alias:author:<author_id>:<alias_id>     → alias_id (iterate by author)
//   author_alias:name:<lowercase_alias_name>       → alias_id (lookup by name)

func (p *PebbleStore) GetAuthorAliases(authorID int) ([]AuthorAlias, error) {
	prefix := []byte(fmt.Sprintf("author_alias:author:%d:", authorID))
	upper := []byte(fmt.Sprintf("author_alias:author:%d;", authorID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var aliases []AuthorAlias
	for iter.First(); iter.Valid(); iter.Next() {
		var alias AuthorAlias
		if err := json.Unmarshal(iter.Value(), &alias); err != nil {
			// Fallback for legacy format: iter.Value() is just an alias ID
			if aliasID, err := strconv.Atoi(string(iter.Value())); err == nil {
				if legacyAlias, err := p.getAuthorAliasByID(aliasID); err == nil && legacyAlias != nil {
					alias = *legacyAlias
				} else {
					continue
				}
			} else {
				continue
			}
		}
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].AliasName < aliases[j].AliasName })
	return aliases, nil
}

func (p *PebbleStore) GetAllAuthorAliases() ([]AuthorAlias, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorAliases()
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author_alias:0"),
		UpperBound: []byte("author_alias:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var aliases []AuthorAlias
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Only match primary records (author_alias:<digits>), skip index keys
		if strings.Contains(key, ":author:") || strings.Contains(key, ":name:") {
			continue
		}
		var a AuthorAlias
		if err := json.Unmarshal(iter.Value(), &a); err != nil {
			return nil, err
		}
		aliases = append(aliases, a)
	}
	return aliases, nil
}

func (p *PebbleStore) CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*AuthorAlias, error) {
	if aliasType == "" {
		aliasType = "alias"
	}

	// Check for duplicate
	nameKey := fmt.Sprintf("author_alias:name:%s", strings.ToLower(aliasName))
	if _, closer, err := p.db.Get([]byte(nameKey)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("alias %q already exists", aliasName)
	}

	id, err := p.nextID("author_alias")
	if err != nil {
		return nil, err
	}

	alias := AuthorAlias{
		ID:        id,
		AuthorID:  authorID,
		AliasName: aliasName,
		AliasType: aliasType,
		CreatedAt: time.Now(),
	}

	data, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	if err := batch.Set([]byte(fmt.Sprintf("author_alias:%d", id)), data, nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set author_alias:%d: %w", id, err)
	}
	if err := batch.Set([]byte(fmt.Sprintf("author_alias:author:%d:%d", authorID, id)), data, nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set author_alias:author index: %w", err)
	}
	if err := batch.Set([]byte(nameKey), []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, fmt.Errorf("pebble Set author_alias name index: %w", err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		batch.Close()
		return nil, err
	}
	p.UpsertAuthorAliasToMemDB(&alias)
	return &alias, nil
}

func (p *PebbleStore) DeleteAuthorAlias(id int) error {
	alias, err := p.getAuthorAliasByID(id)
	if err != nil {
		return err
	}
	if alias == nil {
		return nil
	}

	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("author_alias:%d", id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author_alias:%d: %w", id, err)
	}
	if err := batch.Delete([]byte(fmt.Sprintf("author_alias:author:%d:%d", alias.AuthorID, id)), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author_alias:author index: %w", err)
	}
	if err := batch.Delete([]byte(fmt.Sprintf("author_alias:name:%s", strings.ToLower(alias.AliasName))), nil); err != nil {
		batch.Close()
		return fmt.Errorf("pebble Delete author_alias:name index: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteAuthorAliasFromMemDB(id)
	return nil
}

func (p *PebbleStore) FindAuthorByAlias(aliasName string) (*Author, error) {
	nameKey := []byte(fmt.Sprintf("author_alias:name:%s", strings.ToLower(aliasName)))
	value, closer, err := p.db.Get(nameKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	aliasID, _ := strconv.Atoi(string(value))
	closer.Close()

	alias, err := p.getAuthorAliasByID(aliasID)
	if err != nil || alias == nil {
		return nil, err
	}
	return p.GetAuthorByID(alias.AuthorID)
}

func (p *PebbleStore) getAuthorAliasByID(id int) (*AuthorAlias, error) {
	key := []byte(fmt.Sprintf("author_alias:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var alias AuthorAlias
	if err := json.Unmarshal(value, &alias); err != nil {
		return nil, err
	}
	return &alias, nil
}

// deleteAuthorAliases removes all aliases for an author (cascade on delete).
func (p *PebbleStore) deleteAuthorAliases(batch *pebble.Batch, authorID int) error {
	prefix := []byte(fmt.Sprintf("author_alias:author:%d:", authorID))
	upper := []byte(fmt.Sprintf("author_alias:author:%d;", authorID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		aliasID, _ := strconv.Atoi(string(iter.Value()))
		alias, err := p.getAuthorAliasByID(aliasID)
		if err != nil {
			return err
		}
		if alias != nil {
			if err := batch.Delete([]byte(fmt.Sprintf("author_alias:%d", aliasID)), nil); err != nil {
				return fmt.Errorf("pebble Delete author_alias:%d: %w", aliasID, err)
			}
			if err := batch.Delete([]byte(fmt.Sprintf("author_alias:name:%s", strings.ToLower(alias.AliasName))), nil); err != nil {
				return fmt.Errorf("pebble Delete author_alias:name index: %w", err)
			}
		}
		if err := batch.Delete(iter.Key(), nil); err != nil {
			return fmt.Errorf("pebble Delete author_alias:author index: %w", err)
		}
	}
	return nil
}

// Series operations

func (p *PebbleStore) GetAllSeries() ([]Series, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeries()
	}
	return p.GetAllSeries_Pebble()
}

// GetAllSeries_Pebble returns all series using Pebble key-range iteration.
func (p *PebbleStore) GetAllSeries_Pebble() ([]Series, error) {
	var series []Series
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("series:0"),
		UpperBound: []byte("series:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}

		var s Series
		if err := json.Unmarshal(iter.Value(), &s); err != nil {
			return nil, err
		}
		series = append(series, s)
	}

	return series, nil
}

func (p *PebbleStore) GetSeriesByID(id int) (*Series, error) {
	key := []byte(fmt.Sprintf("series:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var series Series
	if err := json.Unmarshal(value, &series); err != nil {
		return nil, err
	}
	return &series, nil
}

// GetSeriesByIDs returns a map from seriesID → *Series for the given IDs.
// Deduplicates IDs before fetching; missing IDs are absent from the result map.
func (p *PebbleStore) GetSeriesByIDs(ids []int) (map[int]*Series, error) {
	result := make(map[int]*Series, len(ids))
	for _, id := range ids {
		if _, already := result[id]; already {
			continue
		}
		s, err := p.GetSeriesByID(id)
		if err != nil {
			return nil, err
		}
		if s != nil {
			result[id] = s
		}
	}
	return result, nil
}

func (p *PebbleStore) GetSeriesByName(name string, authorID *int) (*Series, error) {
	authorIDStr := "nil"
	if authorID != nil {
		authorIDStr = strconv.Itoa(*authorID)
	}

	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("series:name:%s:%s", strings.ToLower(name), authorIDStr))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetSeriesByID(id)
}

func (p *PebbleStore) CreateSeries(name string, authorID *int) (*Series, error) {
	// Check if series already exists
	existing, err := p.GetSeriesByName(name, authorID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	id, err := p.nextID("series")
	if err != nil {
		return nil, err
	}

	series := &Series{ID: id, Name: name, AuthorID: authorID}
	data, err := json.Marshal(series)
	if err != nil {
		return nil, err
	}

	authorIDStr := "nil"
	if authorID != nil {
		authorIDStr = strconv.Itoa(*authorID)
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("series:%d", id))
	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("series:name:%s:%s", strings.ToLower(name), authorIDStr))

	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	p.UpsertSeriesToMemDB(series)
	return series, nil
}

func (p *PebbleStore) DeleteSeries(id int) error {
	key := []byte(fmt.Sprintf("series:%d", id))

	// Read the series first to clean up the name index
	val, closer, err := p.db.Get(key)
	if err == nil {
		var series Series
		if json.Unmarshal(val, &series) == nil {
			authorIDStr := "nil"
			if series.AuthorID != nil {
				authorIDStr = strconv.Itoa(*series.AuthorID)
			}
			indexKey := []byte(fmt.Sprintf("series:name:%s:%s", strings.ToLower(series.Name), authorIDStr))
			if err := p.db.Delete(indexKey, pebble.Sync); err != nil {
				slog.Warn("pebble Delete series name index", "key", string(indexKey), "error", err)
			}
		}
		closer.Close()
	}

	if err := p.db.Delete(key, pebble.Sync); err != nil {
		return err
	}
	p.DeleteSeriesFromMemDB(id)
	return nil
}

func (p *PebbleStore) UpdateSeriesName(id int, name string) error {
	key := []byte(fmt.Sprintf("series:%d", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		return fmt.Errorf("series %d not found: %w", id, err)
	}
	var series Series
	if err := json.Unmarshal(val, &series); err != nil {
		closer.Close()
		return err
	}
	closer.Close()

	// Delete old name index
	oldAuthorIDStr := "nil"
	if series.AuthorID != nil {
		oldAuthorIDStr = strconv.Itoa(*series.AuthorID)
	}
	oldIndexKey := []byte(fmt.Sprintf("series:name:%s:%s", strings.ToLower(series.Name), oldAuthorIDStr))
	if err := p.db.Delete(oldIndexKey, pebble.Sync); err != nil {
		slog.Warn("pebble Delete old series name index", "key", string(oldIndexKey), "error", err)
	}

	// Update name
	series.Name = name
	data, err := json.Marshal(series)
	if err != nil {
		return err
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}

	// Create new name index
	newIndexKey := []byte(fmt.Sprintf("series:name:%s:%s", strings.ToLower(name), oldAuthorIDStr))
	idBytes := []byte(fmt.Sprintf("%d", id))
	if err := p.db.Set(newIndexKey, idBytes, pebble.Sync); err != nil {
		return err
	}
	if updated, err := p.GetSeriesByID(id); err == nil && updated != nil {
		p.UpsertSeriesToMemDB(updated)
	}
	return nil
}

func (p *PebbleStore) GetAllSeriesBookCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeriesBookCounts()
	}
	return p.GetAllSeriesBookCounts_Pebble()
}

// GetAllSeriesBookCounts_Pebble returns the number of books per series using Pebble iteration
func (p *PebbleStore) GetAllSeriesBookCounts_Pebble() (map[int]int, error) {
	counts := make(map[int]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) < 2 || len(parts) > 2 {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.SeriesID == nil || (b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion) {
			continue
		}
		counts[*b.SeriesID]++
	}
	return counts, nil
}

// GetAllSeriesFileCounts returns the number of audio files per series.
func (p *PebbleStore) GetAllSeriesFileCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeriesFileCounts()
	}
	bookIDToSeriesID := make(map[string]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.SeriesID != nil && (b.IsPrimaryVersion == nil || *b.IsPrimaryVersion) {
			bookIDToSeriesID[b.ID] = *b.SeriesID
		}
	}
	iter.Close()

	// Count actual BookFile records per book.
	bookFileCounts := make(map[string]int) // bookID → actual file count
	fileIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:0"),
		UpperBound: []byte("book_file:;"),
	})
	if err != nil {
		return nil, err
	}
	for fileIter.First(); fileIter.Valid(); fileIter.Next() {
		key := string(fileIter.Key())
		if !strings.HasPrefix(key, "book_file:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) < 3 {
			continue
		}
		bookID := parts[1]
		if _, inSeries := bookIDToSeriesID[bookID]; inSeries {
			var f BookFile
			if err := json.Unmarshal(fileIter.Value(), &f); err != nil {
				continue
			}
			if !f.Missing {
				bookFileCounts[bookID]++
			}
		}
	}
	fileIter.Close()

	// Aggregate into series counts.
	// Books with no files count as 1 (matches SQLite behaviour).
	counts := make(map[int]int)
	for bookID, seriesID := range bookIDToSeriesID {
		n := bookFileCounts[bookID]
		if n == 0 {
			n = 1
		}
		counts[seriesID] += n
	}
	return counts, nil
}

// ---- Work operations (logical title-level grouping) ----

// GetAllWorks returns all works by iterating the Pebble "work:" prefix.
// Works are intentionally NOT mirrored into memdb — 211K rows × ~590B is
// ~120MB of heap for a query path used in <0.1% of requests. The single
// meaningful hot caller (scanner-side work lookup) batches one call at
// scan start, which Pebble handles in tens of milliseconds.
func (p *PebbleStore) GetAllWorks() ([]Work, error) {
	return p.GetAllWorks_Pebble()
}

// GetAllWorks_Pebble returns all works by iterating the Pebble "work:" prefix.
func (p *PebbleStore) GetAllWorks_Pebble() ([]Work, error) {
	var works []Work
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte("work:0"), UpperBound: []byte("work:;")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":title:") {
			continue
		}
		var w Work
		if err := json.Unmarshal(iter.Value(), &w); err != nil {
			return nil, err
		}
		works = append(works, w)
	}
	return works, nil
}

func (p *PebbleStore) GetWorkByID(id string) (*Work, error) {
	key := []byte(fmt.Sprintf("work:%s", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var w Work
	if err := json.Unmarshal(value, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (p *PebbleStore) CreateWork(work *Work) (*Work, error) {
	if work.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		work.ID = id
	}
	data, err := json.Marshal(work)
	if err != nil {
		return nil, err
	}
	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("work:%s", work.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	// Basic title index (case-insensitive normalized) for future lookup
	normTitle := util.NormalizeString(work.Title)
	if normTitle != "" {
		idxKey := []byte(fmt.Sprintf("work:title:%s:%s", normTitle, work.ID))
		if err := batch.Set(idxKey, []byte(work.ID), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	p.UpsertWorkToMemDB(work)
	return work, nil
}

func (p *PebbleStore) UpdateWork(id string, work *Work) (*Work, error) {
	old, err := p.GetWorkByID(id)
	if err != nil {
		return nil, err
	}
	if old == nil {
		return nil, fmt.Errorf("work not found")
	}
	work.ID = id
	data, err := json.Marshal(work)
	if err != nil {
		return nil, err
	}
	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("work:%s", id))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	oldNorm := util.NormalizeString(old.Title)
	newNorm := util.NormalizeString(work.Title)
	if oldNorm != newNorm {
		if oldNorm != "" {
			if err := batch.Delete([]byte(fmt.Sprintf("work:title:%s:%s", oldNorm, id)), nil); err != nil {
				batch.Close()
				return nil, fmt.Errorf("pebble batch delete old work title index: %w", err)
			}
		}
		if newNorm != "" {
			if err := batch.Set([]byte(fmt.Sprintf("work:title:%s:%s", newNorm, id)), []byte(id), nil); err != nil {
				batch.Close()
				return nil, fmt.Errorf("pebble batch set new work title index: %w", err)
			}
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	p.UpsertWorkToMemDB(work)
	return work, nil
}

func (p *PebbleStore) DeleteWork(id string) error {
	work, err := p.GetWorkByID(id)
	if err != nil {
		return err
	}
	if work == nil {
		return nil
	}
	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("work:%s", id))
	if err := batch.Delete(key, nil); err != nil {
		batch.Close()
		return err
	}
	norm := util.NormalizeString(work.Title)
	if norm != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("work:title:%s:%s", norm, id)), nil); err != nil {
			batch.Close()
			return fmt.Errorf("pebble batch delete work title index: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteWorkFromMemDB(id)
	return nil
}

func (p *PebbleStore) GetBooksByWorkID(workID string) ([]Book, error) {
	// Use book:work:<workID>:<bookID> index to avoid O(50K) full-scan
	prefix := []byte(fmt.Sprintf("book:work:%s:", workID))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';' // ':' + 1
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var books []Book
	for iter.First(); iter.Valid(); iter.Next() {
		b, err := deserializeBookFromIndex(iter.Value(), func(id string) (*Book, error) {
			return p.GetBookByID(id)
		})
		if err != nil || b == nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		books = append(books, *b)
	}
	return books, nil
}

// Book operations

func (p *PebbleStore) GetAllBooks(limit, offset int) ([]Book, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllBooks(limit, offset, nil)
	}
	var books []Book
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	skipped := 0
	count := 0

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip path index keys (book:series and book:author indexes removed in Task 3.4)
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return nil, err
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && count >= limit {
			break
		}
		books = append(books, book)
		count++
	}

	return books, nil
}

// ListBookIDs returns the IDs of all books, without materializing Book
// structs. When memdb is available, delegates to the memdb fast path
// (which also filters MarkedForDeletion). Otherwise walks Pebble keys in
// the "book:" prefix range and strips the prefix — no iter.Value() call,
// so no JSON unmarshal cost. Saves ~50x memory vs GetAllBooks(0,0) when
// the caller only needs the ID set.
func (p *PebbleStore) ListBookIDs() ([]string, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().ListBookIDs()
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	ids := make([]string, 0, 1024)
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Skip path/secondary-index keys (e.g., book:path:..., book:series:..., book:author:...).
		if strings.Contains(key, ":path:") {
			continue
		}
		// Primary key form is "book:<id>". Split on ':' and take the suffix.
		idx := strings.IndexByte(key, ':')
		if idx < 0 || idx == len(key)-1 {
			continue
		}
		id := key[idx+1:]
		// Defensive: skip any other secondary-index key forms that may slip
		// through (anything containing another ':' is not a primary key).
		if strings.IndexByte(id, ':') >= 0 {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetAllBookSummaries returns lightweight BookSummary records for the library list view.
// When memdb is available, takes the indexed-iteration fast path that
// avoids materializing the full Book slice. Falls back to Pebble otherwise.
func (p *PebbleStore) GetAllBookSummaries(limit, offset int) ([]BookSummary, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetBookSummaries(limit, offset, BookSummaryFilter{})
	}
	return p.GetAllBookSummaries_Pebble(limit, offset)
}

// CountBookSummariesFiltered returns the count of rows that would match
// the given filter. Memdb-backed when available (O(matches) without
// projection allocations); Pebble fallback materializes summaries and
// counts (slow but correct, only hit during cold start before memdb
// publishes).
func (p *PebbleStore) CountBookSummariesFiltered(f BookSummaryFilter) (int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().CountBookSummaries(f)
	}
	summaries, err := p.GetAllBookSummariesFiltered(0, 0, f)
	if err != nil {
		return 0, err
	}
	return len(summaries), nil
}

// GetAllBookSummariesFiltered is the filtered variant used by the library
// list when post-filters can be pushed down to memdb (most common case:
// is_primary_version=true). Bypasses the "fetch all books then filter in
// Go" pattern that was making /audiobooks?is_primary_version=true scan 68K
// rows on every page load.
func (p *PebbleStore) GetAllBookSummariesFiltered(limit, offset int, f BookSummaryFilter) ([]BookSummary, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetBookSummaries(limit, offset, f)
	}
	// Pebble fallback: filter manually after a full scan. Matches the
	// historical service behavior so we never regress correctness when
	// memdb is unavailable.
	summaries, err := p.GetAllBookSummaries_Pebble(0, 0)
	if err != nil {
		return nil, err
	}
	filtered := summaries[:0]
	excludeDeleted := f.MarkedForDeletion == nil
	for _, s := range summaries {
		if excludeDeleted {
			if s.IsPrimaryVersion != nil && false { /* IsPrimaryVersion on BookSummary, MarkedForDeletion is not — handle conservatively below */
			}
		}
		if f.IsPrimaryVersion != nil {
			eff := s.IsPrimaryVersion == nil || *s.IsPrimaryVersion
			if eff != *f.IsPrimaryVersion {
				continue
			}
		}
		filtered = append(filtered, s)
	}
	if offset >= len(filtered) {
		return nil, nil
	}
	end := len(filtered)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return filtered[offset:end], nil
}

// GetAllBookSummaries_Pebble is the Pebble-backed implementation.
// It fetches all books via full iteration, then projects each Book into a BookSummary,
// skipping books marked for deletion.
func (p *PebbleStore) GetAllBookSummaries_Pebble(limit, offset int) ([]BookSummary, error) {
	if limit <= 0 {
		limit = 1_000_000
	}
	if offset < 0 {
		offset = 0
	}
	books, err := p.GetAllBooks(limit, offset)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	summaries := make([]BookSummary, 0, len(books))
	for _, b := range books {
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		summaries = append(summaries, BookSummary{
			ID:                   b.ID,
			Title:                b.Title,
			AuthorID:             b.AuthorID,
			SeriesID:             b.SeriesID,
			SeriesSequence:       b.SeriesSequence,
			FilePath:             b.FilePath,
			Format:               b.Format,
			Duration:             b.Duration,
			OriginalFilename:     b.OriginalFilename,
			FileSize:             b.FileSize,
			FileHash:             b.FileHash,
			OriginalFileHash:     b.OriginalFileHash,
			OrganizedFileHash:    b.OrganizedFileHash,
			LibraryState:         b.LibraryState,
			QuarantinedAt:        b.QuarantinedAt,
			QuarantineReason:     b.QuarantineReason,
			CoverURL:             b.CoverURL,
			Narrator:             b.Narrator,
			CreatedAt:            b.CreatedAt,
			UpdatedAt:            b.UpdatedAt,
			MetadataUpdatedAt:    b.MetadataUpdatedAt,
			IsPrimaryVersion:     b.IsPrimaryVersion,
			VersionGroupID:       b.VersionGroupID,
			MetadataReviewStatus: b.MetadataReviewStatus,
		})
	}
	return summaries, nil
}

func (p *PebbleStore) GetBookByID(id string) (*Book, error) {
	key := []byte(fmt.Sprintf("book:%s", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var book Book
	if err := json.Unmarshal(value, &book); err != nil {
		return nil, err
	}
	return &book, nil
}

func (p *PebbleStore) GetBookByFilePath(path string) (*Book, error) {
	indexKey := []byte(fmt.Sprintf("book:path:%s", path))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id := string(value) // ULID string

	return p.GetBookByID(id)
}

// GetBookByITunesPersistentID scans all books to find one matching the given
// iTunes persistent ID. This is O(n) but syncs are infrequent.
func (p *PebbleStore) GetBookByITunesPersistentID(persistentID string) (*Book, error) {
	if persistentID == "" {
		return nil, nil
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":hash:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.ITunesPersistentID != nil && *book.ITunesPersistentID == persistentID {
			return &book, nil
		}
	}
	return nil, nil
}

// ListBooksByITunesPID returns books that have a non-empty iTunes persistent
// ID, paginated. Fast path: memdb has an itunes_persistent_id index, so this
// is O(matches) instead of O(50K books × JSON unmarshal) — the prior
// implementation in handleListITunesBooks and the writeback-preview handler
// called GetAllBooks(0,0) and post-filtered, allocating the full book set
// on every request. With ~50K total books and ~3K iTunes-mapped, that's a
// >15× reduction in allocations and ~100ms→<1ms latency.
//
// Pebble fallback preserved for cold-start (before memdb publishes) and
// tests with no memdb.
func (p *PebbleStore) ListBooksByITunesPID(limit, offset int) ([]Book, error) {
	if mem := p.mem(); mem != nil {
		return mem.ListBooksByITunesPID(limit, offset)
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var books []Book
	skipped := 0
	collected := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":hash:") ||
			strings.Contains(key, ":version:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.ITunesPersistentID == nil || *book.ITunesPersistentID == "" {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && collected >= limit {
			break
		}
		books = append(books, book)
		collected++
	}
	return books, nil
}

func (p *PebbleStore) GetBookByFileHash(hash string) (*Book, error) {
	indexKey := []byte(fmt.Sprintf("book:hash:%s", hash))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id := string(value) // ULID string

	return p.GetBookByID(id)
}

func (p *PebbleStore) GetBookByOriginalHash(hash string) (*Book, error) {
	indexKey := []byte(fmt.Sprintf("book:originalhash:%s", hash))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id := string(value)
	return p.GetBookByID(id)
}

func (p *PebbleStore) GetBookByOrganizedHash(hash string) (*Book, error) {
	indexKey := []byte(fmt.Sprintf("book:organizedhash:%s", hash))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id := string(value)
	return p.GetBookByID(id)
}

// GetBookBySegmentFileHash returns the parent Book of the first book_file whose
// file_hash or original_file_hash matches hash. Tries the book_file_hash: and
// book_file_orig_hash: secondary indexes in order.
func (p *PebbleStore) GetBookBySegmentFileHash(hash string) (*Book, error) {
	if hash == "" {
		return nil, nil
	}
	for _, prefix := range []string{"book_file_hash:", "book_file_orig_hash:"} {
		key := []byte(fmt.Sprintf("%s%s", prefix, hash))
		value, closer, err := p.db.Get(key)
		if err == pebble.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		ref := string(value)
		closer.Close()

		parts := strings.SplitN(ref, ":", 2)
		if len(parts) != 2 {
			continue
		}
		return p.GetBookByID(parts[0])
	}
	return nil, nil
}

// GetDuplicateBooks returns groups of books with identical file hashes
// Only returns groups with 2+ books (actual duplicates)
func (p *PebbleStore) GetDuplicateBooks() ([][]Book, error) {
	// Map to group books by hash (preferring organized_file_hash over file_hash)

	hashGroups := make(map[string][]Book)

	// Iterate through all books to find duplicates.
	// Book data keys are "book:{ULID}" (2 colon-separated parts).
	// Index keys ("book:path:", "book:hash:", etc.) have 3+ parts and are filtered out.
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Only process data keys: "book:<ULID>" has exactly 2 parts.
		if len(strings.Split(key, ":")) != 2 {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return nil, fmt.Errorf("failed to unmarshal book: %w", err)
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}

		// Use organized_file_hash if available, otherwise file_hash
		var hash string
		if book.OrganizedFileHash != nil && *book.OrganizedFileHash != "" {
			hash = *book.OrganizedFileHash
		} else if book.FileHash != nil && *book.FileHash != "" {
			hash = *book.FileHash
		}

		// Only track books with valid hashes
		if hash != "" {
			hashGroups[hash] = append(hashGroups[hash], book)
		}
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	// Extract groups with 2+ books (actual duplicates), sorted by file_path
	var duplicateGroups [][]Book
	for _, group := range hashGroups {
		if len(group) >= 2 {
			// Sort by file_path within each group
			sort.Slice(group, func(i, j int) bool {
				return group[i].FilePath < group[j].FilePath
			})
			duplicateGroups = append(duplicateGroups, group)
		}
	}

	return duplicateGroups, nil
}

// GetBooksByTitleInDir returns books whose lowercased title matches normalizedTitle
// and whose FilePath lives directly under dirPath (same directory, any filename).
// Always scans Pebble — MemStore has no title+dir index.
func (p *PebbleStore) GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]Book, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []Book
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
		if strings.ToLower(book.Title) != strings.ToLower(normalizedTitle) {
			continue
		}
		if filepath.Dir(book.FilePath) != dirPath {
			continue
		}
		results = append(results, book)
	}
	return results, nil
}

func (p *PebbleStore) GetFolderDuplicates() ([][]Book, error) {
	// PebbleStore doesn't support folder-based duplicate detection efficiently.
	return nil, nil
}

// GetDuplicateBooksByMetadata is not efficiently supported in PebbleStore.
func (p *PebbleStore) GetDuplicateBooksByMetadata(threshold float64) ([][]Book, error) {
	return nil, nil
}

func (p *PebbleStore) GetBooksBySeriesID(seriesID int) ([]Book, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetBooksBySeriesID(seriesID, 0, 0)
	}
	return p.GetBooksBySeriesID_Pebble(seriesID)
}

// GetBooksBySeriesID_Pebble performs a full Pebble book scan filtered by series ID.
// Fallback path after Task 3.4 index removal.
func (p *PebbleStore) GetBooksBySeriesID_Pebble(seriesID int) ([]Book, error) {
	var books []Book
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Skip non-primary book keys (path index etc.)
		if strings.Contains(key, ":path:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.SeriesID == nil || *book.SeriesID != seriesID {
			continue
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		books = append(books, book)
	}

	return books, nil
}

func (p *PebbleStore) GetBooksByAuthorID(authorID int) ([]Book, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetBooksByAuthorID(authorID, 0, 0)
	}
	return p.GetBooksByAuthorID_Pebble(authorID)
}

// GetBooksByAuthorID_Pebble performs a full Pebble book scan filtered by author ID.
// Fallback path after Task 3.4 index removal.
func (p *PebbleStore) GetBooksByAuthorID_Pebble(authorID int) ([]Book, error) {
	var books []Book
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Skip non-primary book keys (path index etc.)
		if strings.Contains(key, ":path:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.AuthorID == nil || *book.AuthorID != authorID {
			continue
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		books = append(books, book)
	}

	return books, nil
}

func (p *PebbleStore) GetBookAuthors(bookID string) ([]BookAuthor, error) {
	key := []byte(fmt.Sprintf("book_authors:%s", bookID))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var authors []BookAuthor
	if err := json.Unmarshal(val, &authors); err != nil {
		return nil, err
	}
	return authors, nil
}

func (p *PebbleStore) SetBookAuthors(bookID string, authors []BookAuthor) error {
	key := []byte(fmt.Sprintf("book_authors:%s", bookID))
	data, err := json.Marshal(authors)
	if err != nil {
		return err
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}
	p.ReplaceBookAuthorsInMemDB(bookID, authors)
	return nil
}

// GetBooksByAuthorIDWithRole returns all books where the author appears in
// the book_authors junction table (any role). It also falls back to the
// legacy AuthorID field for books not yet migrated to the junction table.
func (p *PebbleStore) GetBooksByAuthorIDWithRole(authorID int) ([]Book, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetBooksByAuthorID(authorID, 0, 0)
	}
	// Collect book IDs from the book_authors junction table.
	bookIDSet := make(map[string]struct{})
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_authors:"),
		UpperBound: []byte("book_authors:~"),
	})
	if err != nil {
		return nil, err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var authors []BookAuthor
		if err := json.Unmarshal(iter.Value(), &authors); err != nil {
			continue
		}
		for _, a := range authors {
			if a.AuthorID == authorID {
				// Extract bookID from key "book_authors:<bookID>"
				key := string(iter.Key())
				bookID := strings.TrimPrefix(key, "book_authors:")
				bookIDSet[bookID] = struct{}{}
			}
		}
	}
	iter.Close()

	// Also include books matched via legacy AuthorID field.
	var books []Book
	bookIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer bookIter.Close()
	for bookIter.First(); bookIter.Valid(); bookIter.Next() {
		key := string(bookIter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(bookIter.Value(), &book); err != nil {
			continue
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		if _, inJunction := bookIDSet[book.ID]; inJunction {
			books = append(books, book)
			delete(bookIDSet, book.ID) // avoid duplicates
		} else if book.AuthorID != nil && *book.AuthorID == authorID {
			books = append(books, book)
		}
	}
	return books, nil
}

func (p *PebbleStore) GetAllAuthorBookCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorBookCounts()
	}
	// Full Pebble book scan combined with junction table scan.
	counts := make(map[int]int)

	// Pass 1: scan book_authors junction table (multi-author associations).
	// Track which books have junction entries so we don't double-count.
	bookHasJunction := make(map[string]bool)
	jIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_authors:"),
		UpperBound: []byte("book_authors:~"),
	})
	if err != nil {
		return nil, err
	}
	for jIter.First(); jIter.Valid(); jIter.Next() {
		var authors []BookAuthor
		if json.Unmarshal(jIter.Value(), &authors) != nil {
			continue
		}
		key := string(jIter.Key())
		bookID := strings.TrimPrefix(key, "book_authors:")
		// Look up the book to check primary/deletion flags.
		book, _ := p.GetBookByID(bookID)
		if book == nil {
			continue
		}
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		bookHasJunction[bookID] = true
		for _, a := range authors {
			counts[a.AuthorID]++
		}
	}
	jIter.Close()

	// Pass 2: scan books for the legacy AuthorID field (for books without junction entries).
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if bookHasJunction[b.ID] {
			continue // already counted via junction
		}
		if b.AuthorID == nil {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		counts[*b.AuthorID]++
	}

	return counts, nil
}

// GetAllWorkBookCounts returns map[workID] → count of primary, not-deleted
// books per work. Mirrors GetAllAuthorBookCounts; used to avoid N+1
// GetBooksByWorkID lookups when listing/aggregating works.
func (p *PebbleStore) GetAllWorkBookCounts() (map[string]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllWorkBookCounts()
	}
	counts := make(map[string]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.WorkID == nil || *b.WorkID == "" {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		counts[*b.WorkID]++
	}

	return counts, nil
}

// GetAllAuthorFileCounts returns the number of audio files per author.
// Uses the in-memory query layer when enabled, otherwise the Pebble fallback.
func (p *PebbleStore) GetAllAuthorFileCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllAuthorFileCounts()
	}
	return p.GetAllAuthorFileCounts_Pebble()
}

// GetAllAuthorFileCounts_Pebble returns the number of audio files per author using Pebble iteration.
// Full book scan fallback after book:author index removal (Task 3.4).
func (p *PebbleStore) GetAllAuthorFileCounts_Pebble() (map[int]int, error) {
	counts := make(map[int]int)

	// Phase 1: Full scan of primary book records to collect author-book relationships.
	// book:author prefix index removed in Task 3.4; must iterate all books instead.
	type AuthorBook struct {
		AuthorID int
		BookID   string
	}
	var authorBooks []AuthorBook

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.AuthorID == nil {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		authorBooks = append(authorBooks, AuthorBook{AuthorID: *b.AuthorID, BookID: b.ID})
	}
	iter.Close()

	// Phase 2: Batch-load all files for all books at once
	bookIDs := make([]string, len(authorBooks))
	for i, ab := range authorBooks {
		bookIDs[i] = ab.BookID
	}

	filesMap := make(map[string][]BookFile)
	if len(bookIDs) > 0 {
		if bfm, err := p.GetBookFilesForIDs(bookIDs); err == nil {
			filesMap = bfm
		}
	}

	// Phase 3: Count files per author
	for _, ab := range authorBooks {
		files := filesMap[ab.BookID]
		if len(files) == 0 {
			counts[ab.AuthorID]++
			continue
		}
		activeCount := 0
		for _, f := range files {
			if !f.Missing {
				activeCount++
			}
		}
		if activeCount > 0 {
			counts[ab.AuthorID] += activeCount
		} else {
			counts[ab.AuthorID]++
		}
	}

	return counts, nil
}

func (p *PebbleStore) CreateNarrator(name string) (*Narrator, error) {
	// Check if narrator already exists
	existing, err := p.GetNarratorByName(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Generate a new ID by incrementing a counter
	counterKey := []byte("narrator_counter")
	var nextID int
	if val, closer, err := p.db.Get(counterKey); err == nil {
		json.Unmarshal(val, &nextID)
		closer.Close()
	}
	nextID++

	narrator := &Narrator{ID: nextID, Name: name, CreatedAt: time.Now()}
	data, err := json.Marshal(narrator)
	if err != nil {
		return nil, err
	}

	key := []byte(fmt.Sprintf("narrator:%d", nextID))
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return nil, err
	}

	// Save name index
	nameKey := []byte(fmt.Sprintf("narrator_name:%s", strings.ToLower(name)))
	idData, _ := json.Marshal(nextID)
	if err := p.db.Set(nameKey, idData, pebble.Sync); err != nil {
		return nil, fmt.Errorf("pebble Set narrator name index: %w", err)
	}

	// Update counter
	counterData, _ := json.Marshal(nextID)
	if err := p.db.Set(counterKey, counterData, pebble.Sync); err != nil {
		return nil, fmt.Errorf("pebble Set narrator counter: %w", err)
	}

	p.UpsertNarratorToMemDB(narrator)
	return narrator, nil
}

func (p *PebbleStore) GetNarratorByID(id int) (*Narrator, error) {
	key := []byte(fmt.Sprintf("narrator:%d", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var narrator Narrator
	if err := json.Unmarshal(val, &narrator); err != nil {
		return nil, err
	}
	return &narrator, nil
}

func (p *PebbleStore) GetNarratorByName(name string) (*Narrator, error) {
	nameKey := []byte(fmt.Sprintf("narrator_name:%s", strings.ToLower(name)))
	val, closer, err := p.db.Get(nameKey)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var id int
	if err := json.Unmarshal(val, &id); err != nil {
		return nil, err
	}
	return p.GetNarratorByID(id)
}

func (p *PebbleStore) ListNarrators() ([]Narrator, error) {
	var narrators []Narrator
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("narrator:"),
		UpperBound: []byte("narrator;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var n Narrator
		if err := json.Unmarshal(iter.Value(), &n); err == nil {
			narrators = append(narrators, n)
		}
	}
	// Sort alphabetically by name to match SQLiteStore ordering behaviour.
	sort.Slice(narrators, func(i, j int) bool {
		return narrators[i].Name < narrators[j].Name
	})
	return narrators, nil
}

func (p *PebbleStore) GetBookNarrators(bookID string) ([]BookNarrator, error) {
	key := []byte(fmt.Sprintf("book_narrators:%s", bookID))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	var narrators []BookNarrator
	if err := json.Unmarshal(val, &narrators); err != nil {
		return nil, err
	}
	return narrators, nil
}

func (p *PebbleStore) SetBookNarrators(bookID string, narrators []BookNarrator) error {
	key := []byte(fmt.Sprintf("book_narrators:%s", bookID))
	data, err := json.Marshal(narrators)
	if err != nil {
		return err
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}
	p.ReplaceBookNarratorsInMemDB(bookID, narrators)
	return nil
}

func (p *PebbleStore) CreateBook(book *Book) (*Book, error) {
	// Generate ULID if not provided
	if book.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		book.ID = id
	}

	// Set timestamps
	now := time.Now()
	book.CreatedAt = &now
	book.UpdatedAt = &now

	data, err := json.Marshal(book)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()

	// Main key
	key := []byte(fmt.Sprintf("book:%s", book.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}

	// Path index
	pathKey := []byte(fmt.Sprintf("book:path:%s", book.FilePath))
	if err := batch.Set(pathKey, []byte(book.ID), nil); err != nil {
		batch.Close()
		return nil, err
	}

	// Hash index (if hash provided)
	if book.FileHash != nil && *book.FileHash != "" {
		hashKey := []byte(fmt.Sprintf("book:hash:%s", *book.FileHash))
		if err := batch.Set(hashKey, []byte(book.ID), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	if book.OriginalFileHash != nil && *book.OriginalFileHash != "" {
		origKey := []byte(fmt.Sprintf("book:originalhash:%s", *book.OriginalFileHash))
		if err := batch.Set(origKey, []byte(book.ID), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	if book.OrganizedFileHash != nil && *book.OrganizedFileHash != "" {
		orgKey := []byte(fmt.Sprintf("book:organizedhash:%s", *book.OrganizedFileHash))
		if err := batch.Set(orgKey, []byte(book.ID), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	// Version-group index (PERF-VERSIONS): O(N) full-scan in
	// GetBooksByVersionGroup was costing ~15s on a 10K-book library.
	// Indexed by group_id so the read can iterate only matching keys.
	// Also store full Book JSON to eliminate point lookups.
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		vgKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", *book.VersionGroupID, book.ID))
		bookJSON, err := serializeBookForIndex(book)
		if err != nil {
			batch.Close()
			return nil, err
		}
		if err := batch.Set(vgKey, bookJSON, nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	// Work ID index: avoid O(50K) full-scan in GetBooksByWorkID
	if book.WorkID != nil && *book.WorkID != "" {
		workKey := []byte(fmt.Sprintf("book:work:%s:%s", *book.WorkID, book.ID))
		bookJSON, err := serializeBookForIndex(book)
		if err != nil {
			batch.Close()
			return nil, err
		}
		if err := batch.Set(workKey, bookJSON, nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	// ISBN/ASIN secondary index: set-layout rows so GetBookIDsByISBNASIN can
	// do a prefix scan instead of a full O(N) book scan.
	{
		isbn10 := derefStrISBN(book.ISBN10)
		isbn13 := derefStrISBN(book.ISBN13)
		asin := derefStrISBN(book.ASIN)
		if err := writeISBNIndexRows(batch, book.ID, isbn10, isbn13, asin); err != nil {
			batch.Close()
			return nil, err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	// Record the original import path so full provenance is preserved forever.
	if err := p.RecordPathChange(&BookPathChange{
		BookID:     book.ID,
		OldPath:    "",
		NewPath:    book.FilePath,
		ChangeType: "import",
	}); err != nil {
		slog.Warn("pebble RecordPathChange on create", "book_id", book.ID, "error", err)
	}

	p.InvalidateLibraryStats()
	p.MarkAllQuickQueriesDirty("create_book")

	// memdb write-through (always on when initialized)
	p.UpsertBookToMemDB(context.Background(), book)

	return book, nil
}

func (p *PebbleStore) UpdateBook(id string, book *Book) (*Book, error) {
	// Get old book to clean up old indexes
	oldBook, err := p.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if oldBook == nil {
		return nil, fmt.Errorf("book not found")
	}

	book.ID = id

	// Preserve created_at from old book, update updated_at
	if oldBook.CreatedAt != nil {
		book.CreatedAt = oldBook.CreatedAt
	}
	now := time.Now()
	book.UpdatedAt = &now

	data, err := json.Marshal(book)
	if err != nil {
		return nil, err
	}

	// CoW: snapshot old state before overwriting
	oldData, marshalErr := json.Marshal(oldBook)
	if marshalErr != nil {
		return nil, fmt.Errorf("failed to marshal old book for version: %w", marshalErr)
	}
	versionKey := []byte(fmt.Sprintf("book_ver:%s:%d", id, time.Now().UnixNano()))

	batch := p.db.NewBatch()

	// Write version snapshot before main key
	if err := batch.Set(versionKey, oldData, nil); err != nil {
		batch.Close()
		return nil, err
	}

	// Update main key
	key := []byte(fmt.Sprintf("book:%s", id))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}

	// Update path index if changed
	if oldBook.FilePath != book.FilePath {
		oldPathKey := []byte(fmt.Sprintf("book:path:%s", oldBook.FilePath))
		if err := batch.Delete(oldPathKey, nil); err != nil {
			batch.Close()
			return nil, err
		}
		newPathKey := []byte(fmt.Sprintf("book:path:%s", book.FilePath))
		if err := batch.Set(newPathKey, []byte(id), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	updateHashIndex := func(oldVal, newVal *string, prefix string) error {
		var oldStr, newStr string
		if oldVal != nil {
			oldStr = *oldVal
		}
		if newVal != nil {
			newStr = *newVal
		}
		if oldStr == newStr {
			return nil
		}
		if oldStr != "" {
			oldKey := []byte(fmt.Sprintf("book:%s:%s", prefix, oldStr))
			if err := batch.Delete(oldKey, nil); err != nil {
				return err
			}
		}
		if newStr != "" {
			newKey := []byte(fmt.Sprintf("book:%s:%s", prefix, newStr))
			if err := batch.Set(newKey, []byte(id), nil); err != nil {
				return err
			}
		}
		return nil
	}

	if err := updateHashIndex(oldBook.FileHash, book.FileHash, "hash"); err != nil {
		batch.Close()
		return nil, err
	}
	if err := updateHashIndex(oldBook.OriginalFileHash, book.OriginalFileHash, "originalhash"); err != nil {
		batch.Close()
		return nil, err
	}
	if err := updateHashIndex(oldBook.OrganizedFileHash, book.OrganizedFileHash, "organizedhash"); err != nil {
		batch.Close()
		return nil, err
	}

	// Update version-group index if changed (PERF-VERSIONS).
	oldVG := ""
	if oldBook.VersionGroupID != nil {
		oldVG = *oldBook.VersionGroupID
	}
	newVG := ""
	if book.VersionGroupID != nil {
		newVG = *book.VersionGroupID
	}
	if oldVG != newVG {
		if oldVG != "" {
			oldVGKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", oldVG, id))
			if err := batch.Delete(oldVGKey, nil); err != nil {
				batch.Close()
				return nil, err
			}
		}
		if newVG != "" {
			newVGKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", newVG, id))
			bookJSON, err := serializeBookForIndex(book)
			if err != nil {
				batch.Close()
				return nil, err
			}
			if err := batch.Set(newVGKey, bookJSON, nil); err != nil {
				batch.Close()
				return nil, err
			}
		}
	}

	// Update work ID index if changed.
	oldWorkID := ""
	if oldBook.WorkID != nil {
		oldWorkID = *oldBook.WorkID
	}
	newWorkID := ""
	if book.WorkID != nil {
		newWorkID = *book.WorkID
	}
	if oldWorkID != newWorkID {
		if oldWorkID != "" {
			oldWorkKey := []byte(fmt.Sprintf("book:work:%s:%s", oldWorkID, id))
			if err := batch.Delete(oldWorkKey, nil); err != nil {
				batch.Close()
				return nil, err
			}
		}
		if newWorkID != "" {
			newWorkKey := []byte(fmt.Sprintf("book:work:%s:%s", newWorkID, id))
			bookJSON, err := serializeBookForIndex(book)
			if err != nil {
				batch.Close()
				return nil, err
			}
			if err := batch.Set(newWorkKey, bookJSON, nil); err != nil {
				batch.Close()
				return nil, err
			}
		}
	}

	// METADATA-CACHED-MATCHER: invalidate cached candidates when any
	// identity-bearing field (title, author, narrator, series, ISBN,
	// ASIN) changes. The cache stored top-N matches for the prior
	// identity; they may no longer apply. Done as a batch.Delete so
	// the same transaction that updates the book row also clears the
	// cache. Missing-key is a no-op in pebble.
	if identityChanged(oldBook, book) {
		_ = batch.Delete(metadataCacheKey(id), nil)
	}

	// ISBN/ASIN secondary index: delete stale rows for old values and write
	// new rows for changed values, all in the same atomic batch.
	{
		oldISBN10 := derefStrISBN(oldBook.ISBN10)
		newISBN10 := derefStrISBN(book.ISBN10)
		oldISBN13 := derefStrISBN(oldBook.ISBN13)
		newISBN13 := derefStrISBN(book.ISBN13)
		oldASIN := derefStrISBN(oldBook.ASIN)
		newASIN := derefStrISBN(book.ASIN)
		if err := updateISBNIndex(batch, id, oldISBN10, newISBN10, oldISBN13, newISBN13, oldASIN, newASIN); err != nil {
			batch.Close()
			return nil, err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	p.InvalidateLibraryStats()
	// UpdateBook may change cover, isbn, or path; mark targeted queries dirty.
	p.MarkQuickQueryDirty("missing_covers", "update_book")
	p.MarkQuickQueryDirty("no_isbn", "update_book")
	p.MarkQuickQueryDirty("in_import_path", "update_book")

	// memdb write-through
	p.UpsertBookToMemDB(context.Background(), book)

	return book, nil
}

// identityChanged reports whether any field that drives the metadata
// cache key has changed between the two book snapshots. Limited to the
// fields the search chain inspects.
func identityChanged(oldBook, newBook *Book) bool {
	if oldBook == nil || newBook == nil {
		return true
	}
	if oldBook.Title != newBook.Title {
		return true
	}
	if !intPtrEq(oldBook.AuthorID, newBook.AuthorID) {
		return true
	}
	if !intPtrEq(oldBook.SeriesID, newBook.SeriesID) {
		return true
	}
	if !strPtrEq(oldBook.ISBN10, newBook.ISBN10) {
		return true
	}
	if !strPtrEq(oldBook.ISBN13, newBook.ISBN13) {
		return true
	}
	if !strPtrEq(oldBook.ASIN, newBook.ASIN) {
		return true
	}
	return false
}

func intPtrEq(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func strPtrEq(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// UpdateBookRating updates only the user rating fields for the given book.
// Fields are applied selectively: nil pointer = no change, Clear* = set to nil.
func (p *PebbleStore) UpdateBookRating(id string, req UpdateBookRatingRequest) error {
	book, err := p.GetBookByID(id)
	if err != nil {
		return err
	}
	if book == nil {
		return fmt.Errorf("book not found")
	}

	if req.ClearOverall {
		book.UserRatingOverall = nil
	} else if req.Overall != nil {
		book.UserRatingOverall = req.Overall
	}

	if req.ClearStory {
		book.UserRatingStory = nil
	} else if req.Story != nil {
		book.UserRatingStory = req.Story
	}

	if req.ClearPerf {
		book.UserRatingPerformance = nil
	} else if req.Performance != nil {
		book.UserRatingPerformance = req.Performance
	}

	if req.ClearNotes {
		book.UserRatingNotes = nil
	} else if req.Notes != nil {
		book.UserRatingNotes = req.Notes
	}

	_, err = p.UpdateBook(id, book)
	return err
}

// SetLastWrittenAt stamps the last_written_at timestamp for book id.
func (p *PebbleStore) SetLastWrittenAt(id string, t time.Time) error {
	book, err := p.GetBookByID(id)
	if err != nil {
		return err
	}
	if book == nil {
		return nil // non-fatal: book not found
	}
	book.LastWrittenAt = &t
	_, err = p.UpdateBook(id, book)
	return err
}

// MarkITunesSynced sets itunes_sync_status to "synced" for the given book IDs.
func (p *PebbleStore) MarkITunesSynced(bookIDs []string) (int64, error) {
	var count int64
	synced := "synced"
	for _, id := range bookIDs {
		book, err := p.GetBookByID(id)
		if err != nil || book == nil {
			continue
		}
		book.ITunesSyncStatus = &synced
		if _, err := p.UpdateBook(id, book); err == nil {
			count++
		}
	}
	return count, nil
}

// GetITunesPurgePendingBooks returns books with itunes_sync_status = "purge_pending" and a PID.
func (p *PebbleStore) GetITunesPurgePendingBooks() ([]Book, error) {
	// Scan book:* index and filter by iTunes sync status without loading all books
	var pending []Book

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
		if b.ITunesSyncStatus != nil && *b.ITunesSyncStatus == "purge_pending" && b.ITunesPersistentID != nil {
			pending = append(pending, b)
		}
	}
	return pending, nil
}

// GetITunesDirtyBooks returns all primary books with itunes_sync_status = "dirty".
func (p *PebbleStore) GetITunesDirtyBooks() ([]Book, error) {
	// Scan book:* index and filter by iTunes sync status without loading all books
	var dirty []Book

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
		if b.ITunesSyncStatus != nil && *b.ITunesSyncStatus == "dirty" {
			if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
				dirty = append(dirty, b)
			}
		}
	}
	return dirty, nil
}

// GetBookSnapshots returns CoW version snapshots for a book, newest-first.
func (p *PebbleStore) GetBookSnapshots(id string, limit int) ([]BookSnapshot, error) {
	prefix := fmt.Sprintf("book_ver:%s:", id)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var versions []BookSnapshot
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 {
			continue
		}
		nsec, parseErr := strconv.ParseInt(parts[2], 10, 64)
		if parseErr != nil {
			continue
		}
		dataCopy := make([]byte, len(iter.Value()))
		copy(dataCopy, iter.Value())
		versions = append(versions, BookSnapshot{
			BookID:    id,
			Timestamp: time.Unix(0, nsec),
			Data:      dataCopy,
		})
	}
	// Reverse for newest-first
	for i, j := 0, len(versions)-1; i < j; i, j = i+1, j-1 {
		versions[i], versions[j] = versions[j], versions[i]
	}
	if limit > 0 && len(versions) > limit {
		versions = versions[:limit]
	}
	return versions, nil
}

// GetBookAtVersion retrieves a book snapshot at a specific version timestamp.
func (p *PebbleStore) GetBookAtVersion(id string, ts time.Time) (*Book, error) {
	key := []byte(fmt.Sprintf("book_ver:%s:%d", id, ts.UnixNano()))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("version not found")
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var book Book
	if err := json.Unmarshal(value, &book); err != nil {
		return nil, err
	}
	return &book, nil
}

// RevertBookToVersion restores a book to a previous version snapshot.
func (p *PebbleStore) RevertBookToVersion(id string, ts time.Time) (*Book, error) {
	oldBook, err := p.GetBookAtVersion(id, ts)
	if err != nil {
		return nil, fmt.Errorf("failed to get version: %w", err)
	}
	oldBook.ID = id
	return p.UpdateBook(id, oldBook)
}

// PruneBookSnapshots keeps the newest keepCount versions and deletes the rest.
func (p *PebbleStore) PruneBookSnapshots(id string, keepCount int) (int, error) {
	if keepCount < 0 {
		keepCount = 0
	}
	versions, err := p.GetBookSnapshots(id, 0)
	if err != nil {
		return 0, err
	}
	if len(versions) <= keepCount {
		return 0, nil
	}
	toDelete := versions[keepCount:]
	batch := p.db.NewBatch()
	for _, v := range toDelete {
		key := []byte(fmt.Sprintf("book_ver:%s:%d", id, v.Timestamp.UnixNano()))
		if err := batch.Delete(key, nil); err != nil {
			batch.Close()
			return 0, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return len(toDelete), nil
}

func (p *PebbleStore) DeleteBook(id string) error {
	book, err := p.GetBookByID(id)
	if err != nil {
		return err
	}
	if book == nil {
		return nil
	}

	batch := p.db.NewBatch()

	// Delete main key
	key := []byte(fmt.Sprintf("book:%s", id))
	if err := batch.Delete(key, nil); err != nil {
		batch.Close()
		return err
	}

	// Delete path index
	pathKey := []byte(fmt.Sprintf("book:path:%s", book.FilePath))
	if err := batch.Delete(pathKey, nil); err != nil {
		batch.Close()
		return err
	}

	// Delete version-group index (PERF-VERSIONS).
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		vgKey := []byte(fmt.Sprintf("book:versiongroup:%s:%s", *book.VersionGroupID, id))
		if err := batch.Delete(vgKey, nil); err != nil {
			batch.Close()
			return err
		}
	}

	statePrefix := []byte(fmt.Sprintf("metadata_state:%s:", id))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: statePrefix,
		UpperBound: append(statePrefix, 0xFF),
	})
	if err != nil {
		batch.Close()
		return err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		if err := batch.Delete(iter.Key(), nil); err != nil {
			batch.Close()
			return err
		}
	}

	// Delete ISBN/ASIN secondary index rows for this book.
	{
		isbn10 := derefStrISBN(book.ISBN10)
		isbn13 := derefStrISBN(book.ISBN13)
		asin := derefStrISBN(book.ASIN)
		if err := deleteISBNIndexRows(batch, id, isbn10, isbn13, asin); err != nil {
			batch.Close()
			return err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.InvalidateLibraryStats()
	p.MarkAllQuickQueriesDirty("delete_book")

	// memdb write-through
	p.DeleteBookFromMemDB(context.Background(), id)

	return nil
}

func (p *PebbleStore) SearchBooks(query string, limit, offset int) ([]Book, error) {
	// Scan book:* index directly instead of loading all books into memory
	// Pre-load author names for author field matching during iteration
	authorNames := make(map[int]string)
	authIter, authErr := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author:0"),
		UpperBound: []byte("author:;"),
	})
	if authErr == nil {
		defer authIter.Close()
		for authIter.First(); authIter.Valid(); authIter.Next() {
			key := string(authIter.Key())
			if strings.Contains(key, ":name:") || strings.Contains(key, ":book:") {
				continue
			}
			var a Author
			if err := json.Unmarshal(authIter.Value(), &a); err == nil {
				authorNames[a.ID] = strings.ToLower(a.Name)
			}
		}
	}

	lowerQuery := strings.ToLower(query)
	var filtered []Book
	var count int

	// Scan book:* index and filter during iteration
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Skip non-primary book entries
		if strings.Contains(key, ":") && !strings.HasPrefix(key, "book:") {
			continue
		}

		// Check title match first (cheapest operation)
		value := iter.Value()
		var book Book
		if err := json.Unmarshal(value, &book); err != nil {
			continue
		}

		titleMatch := strings.Contains(strings.ToLower(book.Title), lowerQuery)
		authorMatch := false
		if book.AuthorID != nil {
			if name, ok := authorNames[*book.AuthorID]; ok {
				authorMatch = strings.Contains(name, lowerQuery)
			}
		}
		narratorMatch := book.Narrator != nil && strings.Contains(strings.ToLower(*book.Narrator), lowerQuery)

		if titleMatch || authorMatch || narratorMatch {
			// Apply pagination: only collect results in the requested range
			if count >= offset && len(filtered) < limit {
				filtered = append(filtered, book)
			}
			count++
			if len(filtered) >= limit {
				break
			}
		}
	}

	return filtered, nil
}

func (p *PebbleStore) CountBooks() (int, error) {
	count := 0
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return 0, err
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		// Skip non-primary versions so duplicate editions don't inflate counts
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		count++
	}

	return count, nil
}

// GetDistinctGenres returns sorted distinct non-empty genre values across all primary books.
func (p *PebbleStore) GetDistinctGenres() ([]string, error) {
	// Scan book:* index directly without loading all books
	seen := map[string]bool{}
	var out []string

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys (same as GetAllBooks)
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.Genre != nil && *b.Genre != "" {
			if !seen[*b.Genre] {
				seen[*b.Genre] = true
				out = append(out, *b.Genre)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// GetDistinctLanguages returns sorted distinct non-empty language values across all primary books.
func (p *PebbleStore) GetDistinctLanguages() ([]string, error) {
	// Scan book:* index directly without loading all books
	seen := map[string]bool{}
	var out []string

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys (same as GetAllBooks)
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.Language != nil && *b.Language != "" {
			if !seen[*b.Language] {
				seen[*b.Language] = true
				out = append(out, *b.Language)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// CountFiles returns the total number of audio files across all books.
// Books with active segments count their segments; books without segments count as 1 file each.
// Uses two range scans instead of per-book GetBookFiles calls to avoid N+1 queries.
func (p *PebbleStore) CountFiles() (int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().CountFiles()
	}
	// Pass 1: collect IDs of all primary, non-deleted books (key scan + JSON decode)
	primaryBookIDs := make(map[string]struct{})
	bookIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return 0, err
	}
	for bookIter.First(); bookIter.Valid(); bookIter.Next() {
		key := string(bookIter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(bookIter.Value(), &book); err != nil {
			return 0, err
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		primaryBookIDs[book.ID] = struct{}{}
	}
	bookIter.Close()

	// Pass 2: single range scan over book_file: space — count active files per book
	fileIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:"),
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return 0, err
	}
	defer fileIter.Close()

	bookActiveFiles := make(map[string]int, len(primaryBookIDs))
	for fileIter.First(); fileIter.Valid(); fileIter.Next() {
		// Primary keys are book_file:<bookID>:<fileID> (3 colon-delimited segments).
		// The upper bound book_file; already excludes secondary indexes (book_file_pid:, etc.)
		// but SplitN guards against any edge cases.
		parts := strings.SplitN(string(fileIter.Key()), ":", 4)
		if len(parts) != 3 {
			continue
		}
		bookID := parts[1]
		if _, ok := primaryBookIDs[bookID]; !ok {
			continue
		}
		var f BookFile
		if err := json.Unmarshal(fileIter.Value(), &f); err != nil {
			continue
		}
		if !f.Missing {
			bookActiveFiles[bookID]++
		}
	}

	total := 0
	for id := range primaryBookIDs {
		if n := bookActiveFiles[id]; n > 0 {
			total += n
		} else {
			total++ // no file records or all missing → count as 1
		}
	}
	return total, nil
}

func (p *PebbleStore) CountAuthors() (int, error) {
	count := 0
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author:0"),
		UpperBound: []byte("author:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}
		count++
	}
	return count, nil
}

func (p *PebbleStore) CountSeries() (int, error) {
	count := 0
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("series:0"),
		UpperBound: []byte("series:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}
		count++
	}
	return count, nil
}

// GetBookCountsByLocation counts primary, non-deleted books whose file path
// starts with rootDir (organized) vs. those that don't (import/unorganized).
// When rootDir is empty, all books are counted as unorganized.
func (p *PebbleStore) GetBookCountsByLocation(rootDir string) (library, import_ int, err error) {
	books, err := p.GetAllBooks(0, 0)
	if err != nil {
		return 0, 0, err
	}
	for _, b := range books {
		if rootDir != "" && strings.HasPrefix(b.FilePath, rootDir) {
			library++
		} else {
			import_++
		}
	}
	return library, import_, nil
}

// GetBookSizesByLocation sums file sizes for books in the library root vs. outside.
// When rootDir is empty, all sizes go to the import (unorganized) bucket.
func (p *PebbleStore) GetBookSizesByLocation(rootDir string) (librarySize, importSize int64, err error) {
	books, err := p.GetAllBooks(0, 0)
	if err != nil {
		return 0, 0, err
	}
	for _, b := range books {
		var sz int64
		if b.FileSize != nil {
			sz = *b.FileSize
		}
		if rootDir != "" && strings.HasPrefix(b.FilePath, rootDir) {
			librarySize += sz
		} else {
			importSize += sz
		}
	}
	return librarySize, importSize, nil
}

// GetDashboardStats returns LibraryStats, serving from the PebbleDB cache when fresh.
// Even if the cache is marked dirty via InvalidateLibraryStats, returns the cached value
// if it was recomputed within the min-interval (default 10 minutes) to prevent thrashing
// during background operations like fingerprinting.
//
// Only when a cache miss (TTL expiry, or recent compute + dirty but outside min-interval)
// requires recompute, a per-process mutex gates the work to prevent concurrent stampedes.
func (p *PebbleStore) GetDashboardStats() (*DashboardStats, error) {
	// Stale-while-revalidate. ANY cached value — even a week-old one —
	// is returned immediately; if stale beyond the min-interval, kick a
	// background recompute for next time. This eliminates the cold-start
	// 87s spike where memdb wasn't warm yet and computeLibraryStats fell
	// through to a slow Pebble scan blocking the dashboard.
	cached := p.readCachedLibraryStats()

	if cached != nil {
		ageSec := time.Since(cached.ComputedAt).Seconds()
		minIntervalSec := float64(getLibraryCountsMinIntervalSeconds())
		if ageSec >= minIntervalSec {
			// Stale: kick off background recompute. TryLock so we don't
			// queue duplicate recomputes — one in flight is enough.
			if p.libraryCountsRecomputeMu.TryLock() {
				go func() {
					defer p.libraryCountsRecomputeMu.Unlock()
					start := time.Now()
					stats, err := p.computeLibraryStats()
					if err != nil {
						slog.Warn("library_counts background recompute failed",
							"component", "library_counts_cache", "error", err)
						return
					}
					p.writeCachedLibraryStats(stats)
					slog.Info("library_counts cache recomputed (background)",
						"component", "library_counts_cache",
						"total_books", stats.TotalBooks,
						"duration_ms", time.Since(start).Milliseconds(),
						"reason", "stale-while-revalidate",
					)
				}()
			}
		} else {
			slog.Debug("library_counts cache hit (fresh)",
				"component", "library_counts_cache",
				"age_seconds", ageSec,
				"min_interval_seconds", minIntervalSec,
			)
		}
		return cached, nil
	}

	// No cache at all (first boot or post-Invalidate restart). Block on
	// recompute — nothing to serve in the meantime.
	p.libraryCountsRecomputeMu.Lock()
	defer p.libraryCountsRecomputeMu.Unlock()

	// Double-check: a peer goroutine may have populated the cache while
	// we waited for the lock.
	if cached := p.readCachedLibraryStats(); cached != nil {
		return cached, nil
	}

	start := time.Now()
	stats, err := p.computeLibraryStats()
	if err != nil {
		return nil, err
	}
	p.writeCachedLibraryStats(stats)
	slog.Info("library_counts cache recomputed (cold)",
		"component", "library_counts_cache",
		"total_books", stats.TotalBooks,
		"organized_books", stats.OrganizedBooks,
		"unorganized_books", stats.UnorganizedBooks,
		"broken_files", stats.BrokenFiles,
		"duration_ms", time.Since(start).Milliseconds(),
		"reason", "cold-cache",
	)
	return stats, nil
}

// computeLibraryStats builds a fresh LibraryStats in two sequential range scans.
// Pass 1 (book:): counts/sums all fields, splits organized vs unorganized, per-import-path counts.
// Pass 2 (book_file:): counts active files without any per-book point lookups.
func (p *PebbleStore) computeLibraryStats() (*LibraryStats, error) {
	// Fast path: when memdb is published, aggregate from RAM. ~150× faster
	// than the Pebble scan below (no JSON unmarshal, no disk I/O). Memdb
	// can't see the book_file_errors_by_book: index, so we still need a
	// short Pebble call for BrokenFiles.
	if mem := p.mem(); mem != nil {
		importPaths, _ := p.GetAllImportPaths()
		stats, err := mem.ComputeLibraryStats(p.rootDir, importPaths)
		if err == nil {
			if booksWithErrors, berr := p.ListBooksWithFileErrors(); berr == nil {
				stats.BrokenFiles = len(booksWithErrors)
			}
			return stats, nil
		}
		// Fall through to Pebble scan on memdb error (shouldn't happen).
		slog.Warn("memdb ComputeLibraryStats failed, falling back to Pebble scan",
			"error", err)
	}

	stats := &LibraryStats{
		StateDistribution:  make(map[string]int),
		FormatDistribution: make(map[string]int),
		BooksByImportPath:  make(map[int]int),
		SizeByImportPath:   make(map[int]int64),
		ComputedAt:         time.Now(),
	}

	// Load import paths upfront (typically just a handful, not worth a separate scan).
	importPaths, _ := p.GetAllImportPaths()

	// Pass 1: book: range
	primaryBookIDs := make(map[string]struct{}, 12000)
	bookIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	for bookIter.First(); bookIter.Valid(); bookIter.Next() {
		key := string(bookIter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var b Book
		if err := json.Unmarshal(bookIter.Value(), &b); err != nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		stats.TotalBooks++
		if b.Duration != nil {
			stats.TotalDuration += int64(*b.Duration)
		}
		size := int64(0)
		if b.FileSize != nil {
			size = *b.FileSize
			stats.TotalSize += size
		}
		state := "imported"
		if b.LibraryState != nil {
			state = *b.LibraryState
		}
		stats.StateDistribution[state]++
		codec := "unknown"
		if b.Codec != nil {
			codec = *b.Codec
		}
		stats.FormatDistribution[codec]++

		// Organized vs unorganized + per-import-path (primary versions only)
		if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
			primaryBookIDs[b.ID] = struct{}{}
			if p.rootDir != "" && strings.HasPrefix(b.FilePath, p.rootDir) {
				stats.OrganizedBooks++
				stats.OrganizedSize += size
			} else {
				stats.UnorganizedBooks++
				stats.UnorganizedSize += size
				for _, ip := range importPaths {
					if strings.HasPrefix(b.FilePath, ip.Path) {
						stats.BooksByImportPath[ip.ID]++
						stats.SizeByImportPath[ip.ID] += size
						break
					}
				}
			}
		}
	}
	bookIter.Close()

	// Pass 2: book_file: range — active file count per primary book
	// Optimized: key-only scan to count files without deserializing
	fileIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:"),
		UpperBound: []byte("book_file;"),
	})
	if err == nil {
		bookActiveFiles := make(map[string]int, len(primaryBookIDs))
		for fileIter.First(); fileIter.Valid(); fileIter.Next() {
			parts := strings.SplitN(string(fileIter.Key()), ":", 4)
			if len(parts) != 3 {
				continue
			}
			bookID := parts[1]
			if _, ok := primaryBookIDs[bookID]; !ok {
				continue
			}
			// Count all files for primary books (fast path: no deserialization)
			// Missing files are rare; counting them all is still much faster than deserializing each one
			bookActiveFiles[bookID]++
		}
		fileIter.Close()
		for id := range primaryBookIDs {
			if n := bookActiveFiles[id]; n > 0 {
				stats.TotalFiles += n
			} else {
				stats.TotalFiles++ // no file records → count as 1
			}
		}
	}

	// Key-only scans — no JSON deserialization
	if ac, err := p.CountAuthors(); err == nil {
		stats.TotalAuthors = ac
	}
	if sc, err := p.CountSeries(); err == nil {
		stats.TotalSeries = sc
	}

	// Pass 3: book_file_errors_by_book: key-only scan — count distinct books with errors.
	// Reuses the secondary index written by RecordFileError so no JSON deserialization needed.
	if booksWithErrors, err := p.ListBooksWithFileErrors(); err == nil {
		stats.BrokenFiles = len(booksWithErrors)
	}

	return stats, nil
}

func (p *PebbleStore) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]Book, error) {
	// Fast path: memdb has a marked_for_deletion index, so this is O(deleted)
	// instead of O(total) — typically the soft-deleted set is tiny relative
	// to 393K total books, so this turns a 20s Pebble scan into <50ms.
	if mem := p.mem(); mem != nil {
		return mem.ListSoftDeletedBooks(limit, offset, olderThan)
	}
	var books []Book
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	skipped := 0
	collected := 0

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":version:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return nil, err
		}
		if book.MarkedForDeletion == nil || !*book.MarkedForDeletion {
			continue
		}
		if olderThan != nil && book.MarkedForDeletionAt != nil && book.MarkedForDeletionAt.After(*olderThan) {
			continue
		}

		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && collected >= limit {
			break
		}
		books = append(books, book)
		collected++
	}

	return books, nil
}

// GetBooksByVersionGroup returns all books in a version group
func (p *PebbleStore) GetBooksByVersionGroup(groupID string) ([]Book, error) {
	// Fast path: use the book:versiongroup:<gid>:<id> index added in
	// PERF-VERSIONS so we touch O(|group|) keys instead of full-scanning
	// the entire books table (was ~15s on 10K books).
	prefix := []byte(fmt.Sprintf("book:versiongroup:%s:", groupID))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';' // ':' + 1
	idxIter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	var books []Book
	for idxIter.First(); idxIter.Valid(); idxIter.Next() {
		b, err := deserializeBookFromIndex(idxIter.Value(), func(id string) (*Book, error) {
			return p.GetBookByID(id)
		})
		if err != nil || b == nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		books = append(books, *b)
	}
	idxIter.Close()

	if len(books) > 0 {
		sortVersions(books)
		return books, nil
	}

	// Fallback: full scan for groups whose index hasn't been backfilled
	// yet. The backfill goroutine writes index entries on startup; this
	// path keeps the API correct in the meantime.
	books = nil // Reset for fallback scan
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":version:") ||
			strings.Contains(key, ":versiongroup:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}

		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}

		if book.VersionGroupID != nil && *book.VersionGroupID == groupID {
			books = append(books, book)
		}
	}

	sortVersions(books)
	return books, nil
}

// sortVersions orders books with the primary version first, then by title.
func sortVersions(books []Book) {
	sort.Slice(books, func(i, j int) bool {
		if books[i].IsPrimaryVersion != nil && *books[i].IsPrimaryVersion {
			return true
		}
		if books[j].IsPrimaryVersion != nil && *books[j].IsPrimaryVersion {
			return false
		}
		return books[i].Title < books[j].Title
	})
}

// GetBooksByMetadataSourceHash returns all books with the given metadata source hash.
func (p *PebbleStore) GetBooksByMetadataSourceHash(hash string) ([]Book, error) {
	var books []Book
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") || strings.Contains(key, ":version:") {
			continue
		}

		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}

		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}

		if book.MetadataSourceHash != nil && *book.MetadataSourceHash == hash {
			if book.MergedIntoBookID == nil {
				books = append(books, book)
			}
		}
	}

	return books, nil
}

// FlagMetadataHashDuplicate marks duplicateID as absorbed into primaryID.
// PebbleStore stub — metadata dedup is only performed by SQLiteStore in production.
func (p *PebbleStore) FlagMetadataHashDuplicate(primaryID, duplicateID string) error {
	book, err := p.GetBookByID(duplicateID)
	if err != nil {
		return err
	}
	if book == nil {
		return nil
	}
	f := false
	book.MergedIntoBookID = &primaryID
	book.IsPrimaryVersion = &f
	_, err = p.UpdateBook(duplicateID, book)
	return err
}

// Import path operations

// GetAllImportPaths returns all managed import paths.
func (p *PebbleStore) GetAllImportPaths() ([]ImportPath, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllImportPaths()
	}
	return p.GetAllImportPaths_Pebble()
}

// GetAllImportPaths_Pebble returns all import paths using Pebble KV iteration.
func (p *PebbleStore) GetAllImportPaths_Pebble() ([]ImportPath, error) {
	var importPaths []ImportPath
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("import_path:0"),
		UpperBound: []byte("import_path:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":path:") {
			continue
		}

		var importPath ImportPath
		if err := json.Unmarshal(iter.Value(), &importPath); err != nil {
			return nil, err
		}
		importPaths = append(importPaths, importPath)
	}

	return importPaths, nil
}

// CountBooksByPathPrefix returns the number of books that originated from the
// given import path. It checks SourceImportPath first (set on books discovered
// after the source-import-path change), then falls back to FilePath for older
// records. This keeps counts correct after auto-organize relocates books.
func (p *PebbleStore) CountBooksByPathPrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, nil
	}
	// Fast path: memdb scan is ~200× faster than Pebble + JSON unmarshal.
	if mem := p.mem(); mem != nil {
		return mem.CountBooksByPathPrefix(prefix)
	}
	count := 0
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
		if json.Unmarshal(iter.Value(), &b) != nil {
			continue
		}
		if b.SourceImportPath != nil && *b.SourceImportPath != "" {
			if strings.HasPrefix(*b.SourceImportPath, prefix) {
				count++
			}
		} else if strings.HasPrefix(b.FilePath, prefix) {
			count++
		}
	}
	return count, nil
}

func (p *PebbleStore) GetImportPathByID(id int) (*ImportPath, error) {
	key := []byte(fmt.Sprintf("import_path:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var importPath ImportPath
	if err := json.Unmarshal(value, &importPath); err != nil {
		return nil, err
	}
	return &importPath, nil
}

func (p *PebbleStore) GetImportPathByPath(path string) (*ImportPath, error) {
	indexKey := []byte(fmt.Sprintf("import_path:path:%s", path))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetImportPathByID(id)
}

func (p *PebbleStore) CreateImportPath(path, name string) (*ImportPath, error) {
	existing, err := p.GetImportPathByPath(path)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("import path with path %s already exists", path)
	}

	id, err := p.nextID("import_path")
	if err != nil {
		return nil, err
	}

	importPath := &ImportPath{
		ID:        id,
		Path:      path,
		Name:      name,
		Enabled:   true,
		CreatedAt: time.Now(),
		BookCount: 0,
	}

	data, err := json.Marshal(importPath)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("import_path:%d", id))
	indexKey := []byte(fmt.Sprintf("import_path:path:%s", path))

	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	p.UpsertImportPathToMemDB(importPath)
	return importPath, nil
}

func (p *PebbleStore) UpdateImportPath(id int, importPath *ImportPath) error {
	importPath.ID = id

	// If the path changed, update the index accordingly
	current, err := p.GetImportPathByID(id)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("import path %d not found", id)
	}

	batch := p.db.NewBatch()

	if current.Path != importPath.Path {
		oldIndexKey := []byte(fmt.Sprintf("import_path:path:%s", current.Path))
		if err := batch.Delete(oldIndexKey, nil); err != nil {
			batch.Close()
			return err
		}
		newIndexKey := []byte(fmt.Sprintf("import_path:path:%s", importPath.Path))
		if err := batch.Set(newIndexKey, []byte(strconv.Itoa(id)), nil); err != nil {
			batch.Close()
			return err
		}
	}

	data, err := json.Marshal(importPath)
	if err != nil {
		batch.Close()
		return err
	}

	key := []byte(fmt.Sprintf("import_path:%d", id))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.UpsertImportPathToMemDB(importPath)
	return nil
}

func (p *PebbleStore) DeleteImportPath(id int) error {
	importPath, err := p.GetImportPathByID(id)
	if err != nil {
		return err
	}
	if importPath == nil {
		return nil
	}

	batch := p.db.NewBatch()

	key := []byte(fmt.Sprintf("import_path:%d", id))
	if err := batch.Delete(key, nil); err != nil {
		batch.Close()
		return err
	}

	indexKey := []byte(fmt.Sprintf("import_path:path:%s", importPath.Path))
	if err := batch.Delete(indexKey, nil); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	p.DeleteImportPathFromMemDB(id)
	return nil
}

// Operation operations

func (p *PebbleStore) CreateOperation(id, opType string, folderPath *string) (*Operation, error) {
	op := &Operation{
		ID:         id,
		Type:       opType,
		Status:     "pending",
		Progress:   0,
		Total:      0,
		Message:    "",
		FolderPath: folderPath,
		CreatedAt:  time.Now(),
	}

	data, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}

	key := []byte(fmt.Sprintf("operation:%s", id))
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return nil, err
	}

	return op, nil
}

func (p *PebbleStore) GetOperationByID(id string) (*Operation, error) {
	key := []byte(fmt.Sprintf("operation:%s", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var op Operation
	if err := json.Unmarshal(value, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (p *PebbleStore) GetRecentOperations(limit int) ([]Operation, error) {
	var operations []Operation
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		operations = append(operations, op)
	}

	sort.Slice(operations, func(i, j int) bool {
		return operations[i].CreatedAt.After(operations[j].CreatedAt)
	})

	if len(operations) > limit {
		operations = operations[:limit]
	}

	return operations, nil
}

func (p *PebbleStore) ListOperations(limit, offset int) ([]Operation, int, error) {
	var operations []Operation
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		operations = append(operations, op)
	}

	sort.Slice(operations, func(i, j int) bool {
		return operations[i].CreatedAt.After(operations[j].CreatedAt)
	})

	total := len(operations)
	if offset >= len(operations) {
		return []Operation{}, total, nil
	}
	end := offset + limit
	if end > len(operations) {
		end = len(operations)
	}
	return operations[offset:end], total, nil
}

func (p *PebbleStore) UpdateOperationStatus(id, status string, progress, total int, message string) error {
	op, err := p.GetOperationByID(id)
	if err != nil {
		return err
	}
	if op == nil {
		return fmt.Errorf("operation not found")
	}

	op.Status = status
	op.Progress = progress
	op.Total = total
	op.Message = message

	now := time.Now()
	if status == "running" && op.StartedAt == nil {
		op.StartedAt = &now
	} else if (status == "completed" || status == "failed") && op.CompletedAt == nil {
		op.CompletedAt = &now
	}

	data, err := json.Marshal(op)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("operation:%s", id))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) UpdateOperationError(id, errorMessage string) error {
	op, err := p.GetOperationByID(id)
	if err != nil {
		return err
	}
	if op == nil {
		return fmt.Errorf("operation not found")
	}

	op.Status = "failed"
	op.ErrorMessage = &errorMessage
	now := time.Now()
	op.CompletedAt = &now

	data, err := json.Marshal(op)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("operation:%s", id))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) UpdateOperationResultData(id string, resultData string) error {
	op, err := p.GetOperationByID(id)
	if err != nil {
		return err
	}
	if op == nil {
		return fmt.Errorf("operation not found: %s", id)
	}
	op.ResultData = &resultData
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("operation:%s", id)), data, pebble.Sync)
}

// Operation Log operations

func (p *PebbleStore) AddOperationLog(operationID, level, message string, details *string) error {
	id, err := p.nextID("operationlog")
	if err != nil {
		return err
	}

	log := &OperationLog{
		ID:          id,
		OperationID: operationID,
		Level:       level,
		Message:     message,
		Details:     details,
		CreatedAt:   time.Now(),
	}

	data, err := json.Marshal(log)
	if err != nil {
		return err
	}

	// Key format: operationlog:<operation_id>:<timestamp>:<seq>
	key := []byte(fmt.Sprintf("operationlog:%s:%d:%d", operationID, log.CreatedAt.UnixNano(), id))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetOperationLogs(operationID string) ([]OperationLog, error) {
	var logs []OperationLog
	prefix := []byte(fmt.Sprintf("operationlog:%s:", operationID))

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var log OperationLog
		if err := json.Unmarshal(iter.Value(), &log); err != nil {
			continue
		}
		logs = append(logs, log)
	}

	return logs, nil
}

// Book Tombstones (safe deletion pattern)

func (p *PebbleStore) CreateBookTombstone(book *Book) error {
	data, err := json.Marshal(book)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("tombstone:%s", book.ID))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetBookTombstone(id string) (*Book, error) {
	key := []byte(fmt.Sprintf("tombstone:%s", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()
	var book Book
	if err := json.Unmarshal(val, &book); err != nil {
		return nil, err
	}
	return &book, nil
}

func (p *PebbleStore) DeleteBookTombstone(id string) error {
	key := []byte(fmt.Sprintf("tombstone:%s", id))
	return p.db.Delete(key, pebble.Sync)
}

func (p *PebbleStore) ListBookTombstones(limit int) ([]Book, error) {
	var books []Book
	prefix := []byte("tombstone:")

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		books = append(books, book)
		if limit > 0 && len(books) >= limit {
			break
		}
	}
	return books, nil
}

// Operation Summary Logs (persistent across restarts)

func (p *PebbleStore) SaveOperationSummaryLog(op *OperationSummaryLog) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("opsummary:%s", op.ID))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetOperationSummaryLog(id string) (*OperationSummaryLog, error) {
	key := []byte(fmt.Sprintf("opsummary:%s", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()
	var op OperationSummaryLog
	if err := json.Unmarshal(val, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (p *PebbleStore) ListOperationSummaryLogs(limit, offset int) ([]OperationSummaryLog, error) {
	var logs []OperationSummaryLog
	prefix := []byte("opsummary:")

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op OperationSummaryLog
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		logs = append(logs, op)
	}

	// Sort by created_at descending
	for i := 0; i < len(logs)-1; i++ {
		for j := i + 1; j < len(logs); j++ {
			if logs[j].CreatedAt.After(logs[i].CreatedAt) {
				logs[i], logs[j] = logs[j], logs[i]
			}
		}
	}

	// Apply offset and limit
	if offset >= len(logs) {
		return nil, nil
	}
	logs = logs[offset:]
	if limit > 0 && len(logs) > limit {
		logs = logs[:limit]
	}

	return logs, nil
}

// Metadata provenance operations

func (p *PebbleStore) metadataStateKey(bookID, field string) []byte {
	return []byte(fmt.Sprintf("metadata_state:%s:%s", bookID, field))
}

func (p *PebbleStore) GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error) {
	var states []MetadataFieldState
	prefix := []byte(fmt.Sprintf("metadata_state:%s:", bookID))

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		if !strings.HasPrefix(string(iter.Key()), string(prefix)) {
			break
		}
		var state MetadataFieldState
		if err := json.Unmarshal(iter.Value(), &state); err != nil {
			return nil, err
		}
		states = append(states, state)
	}

	return states, nil
}

func (p *PebbleStore) UpsertMetadataFieldState(state *MetadataFieldState) error {
	if state == nil {
		return fmt.Errorf("metadata state cannot be nil")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	return p.db.Set(p.metadataStateKey(state.BookID, state.Field), data, pebble.Sync)
}

func (p *PebbleStore) DeleteMetadataFieldState(bookID, field string) error {
	return p.db.Delete(p.metadataStateKey(bookID, field), pebble.Sync)
}

// Metadata change history operations

func (p *PebbleStore) RecordMetadataChange(record *MetadataChangeRecord) error {
	if record.ChangedAt.IsZero() {
		record.ChangedAt = time.Now().UTC()
	}
	// Use UnixNano as the synthetic integer ID so callers can distinguish
	// records (ID == 0 is treated as "not stored").
	if record.ID == 0 {
		record.ID = int(record.ChangedAt.UnixNano())
	}
	key := fmt.Sprintf("metadata_change:%s:%s:%d", record.BookID, record.Field, record.ChangedAt.UnixNano())
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func (p *PebbleStore) GetMetadataChangeHistory(bookID string, field string, limit int) ([]MetadataChangeRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	prefix := fmt.Sprintf("metadata_change:%s:%s:", bookID, field)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []MetadataChangeRecord
	for iter.First(); iter.Valid(); iter.Next() {
		var r MetadataChangeRecord
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		all = append(all, r)
	}
	// Reverse for newest-first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (p *PebbleStore) GetBookChangeHistory(bookID string, limit int) ([]MetadataChangeRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	prefix := fmt.Sprintf("metadata_change:%s:", bookID)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []MetadataChangeRecord
	for iter.First(); iter.Valid(); iter.Next() {
		var r MetadataChangeRecord
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		all = append(all, r)
	}
	// Reverse for newest-first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// User Preference operations

func (p *PebbleStore) GetUserPreference(key string) (*UserPreference, error) {
	dbKey := []byte(fmt.Sprintf("preference:%s", key))
	value, closer, err := p.db.Get(dbKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var pref UserPreference
	if err := json.Unmarshal(value, &pref); err != nil {
		return nil, err
	}
	return &pref, nil
}

func (p *PebbleStore) SetUserPreference(key, value string) error {
	existing, err := p.GetUserPreference(key)
	if err != nil {
		return err
	}

	var pref *UserPreference
	if existing != nil {
		pref = existing
		pref.Value = &value
		pref.UpdatedAt = time.Now()
	} else {
		id, err := p.nextID("preference")
		if err != nil {
			return err
		}
		pref = &UserPreference{
			ID:        id,
			Key:       key,
			Value:     &value,
			UpdatedAt: time.Now(),
		}
	}

	data, err := json.Marshal(pref)
	if err != nil {
		return err
	}

	dbKey := []byte(fmt.Sprintf("preference:%s", key))
	return p.db.Set(dbKey, data, pebble.Sync)
}

// GetAllUserPreferences returns all user preferences.
func (p *PebbleStore) GetAllUserPreferences() ([]UserPreference, error) {
	return p.GetAllUserPreferences_Pebble()
}

// GetAllUserPreferences_Pebble returns all user preferences using Pebble key iteration.
// Iterates over "preference:*" keys and unmarshals each JSON-encoded UserPreference.
func (p *PebbleStore) GetAllUserPreferences_Pebble() ([]UserPreference, error) {
	var preferences []UserPreference
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("preference:"),
		UpperBound: []byte("preference:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var pref UserPreference
		if err := json.Unmarshal(iter.Value(), &pref); err != nil {
			continue
		}
		preferences = append(preferences, pref)
	}

	return preferences, nil
}

// Playlist operations

func (p *PebbleStore) CreatePlaylist(name string, seriesID *int, filePath string) (*Playlist, error) {
	id, err := p.nextID("playlist")
	if err != nil {
		return nil, err
	}

	playlist := &Playlist{
		ID:       id,
		Name:     name,
		SeriesID: seriesID,
		FilePath: filePath,
	}

	data, err := json.Marshal(playlist)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("playlist:%d", id))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}

	if seriesID != nil {
		indexKey := []byte(fmt.Sprintf("playlist:series:%d", *seriesID))
		if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	return playlist, nil
}

func (p *PebbleStore) GetPlaylistByID(id int) (*Playlist, error) {
	key := []byte(fmt.Sprintf("playlist:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var playlist Playlist
	if err := json.Unmarshal(value, &playlist); err != nil {
		return nil, err
	}
	return &playlist, nil
}

func (p *PebbleStore) GetPlaylistBySeriesID(seriesID int) (*Playlist, error) {
	indexKey := []byte(fmt.Sprintf("playlist:series:%d", seriesID))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetPlaylistByID(id)
}

func (p *PebbleStore) AddPlaylistItem(playlistID, bookID, position int) error {
	id, err := p.nextID("playlistitem")
	if err != nil {
		return err
	}

	item := &PlaylistItem{
		ID:         id,
		PlaylistID: playlistID,
		BookID:     bookID,
		Position:   position,
	}

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("playlistitem:%d:%d", playlistID, position))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetPlaylistItems(playlistID int) ([]PlaylistItem, error) {
	var items []PlaylistItem
	prefix := []byte(fmt.Sprintf("playlistitem:%d:", playlistID))

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var item PlaylistItem
		if err := json.Unmarshal(iter.Value(), &item); err != nil {
			continue
		}
		items = append(items, item)
	}

	return items, nil
}

// ---- Extended keyspace implementation ----

// Users & Auth
func (p *PebbleStore) CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error) {
	lowerUser := strings.ToLower(username)
	lowerEmail := strings.ToLower(email)

	// uniqueness checks
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("username already exists")
	}
	if _, closer, err := p.db.Get([]byte("idx:user:email:" + lowerEmail)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("email already exists")
	}

	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &User{
		ID: id, Username: username, Email: email,
		PasswordHashAlgo: passwordHashAlgo, PasswordHash: passwordHash,
		Roles: roles, Status: status, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	data, _ := json.Marshal(user)
	b := p.db.NewBatch()
	if err := b.Set([]byte("u:"+id), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:user:username:"+lowerUser), []byte(id), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:user:email:"+lowerEmail), []byte(id), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return user, nil
}

func (p *PebbleStore) GetUserByID(id string) (*User, error) {
	v, closer, err := p.db.Get([]byte("u:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var u User
	if err := json.Unmarshal(v, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (p *PebbleStore) getUserByIndex(idx string) (*User, error) {
	v, closer, err := p.db.Get([]byte(idx))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	id := string(v)
	return p.GetUserByID(id)
}

func (p *PebbleStore) GetUserByUsername(username string) (*User, error) {
	return p.getUserByIndex("idx:user:username:" + strings.ToLower(username))
}

func (p *PebbleStore) GetUserByEmail(email string) (*User, error) {
	return p.getUserByIndex("idx:user:email:" + strings.ToLower(email))
}

func (p *PebbleStore) UpdateUser(user *User) error {
	user.UpdatedAt = time.Now()
	data, _ := json.Marshal(user)
	return p.db.Set([]byte("u:"+user.ID), data, pebble.Sync)
}

func (p *PebbleStore) ListUsers() ([]User, error) {
	prefix := []byte("u:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var users []User
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":idx:") || strings.Contains(key, ":username:") || strings.Contains(key, ":email:") {
			continue
		}
		var u User
		if err := json.Unmarshal(iter.Value(), &u); err != nil {
			continue
		}
		users = append(users, u)
	}
	return users, nil
}

// Roles

func (p *PebbleStore) GetRoleByID(id string) (*Role, error) {
	v, closer, err := p.db.Get([]byte("role:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var r Role
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *PebbleStore) GetRoleByName(name string) (*Role, error) {
	lower := strings.ToLower(name)
	v, closer, err := p.db.Get([]byte("idx:role:name:" + lower))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return p.GetRoleByID(string(v))
}

func (p *PebbleStore) ListRoles() ([]Role, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("role:"),
		UpperBound: []byte("role:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []Role
	for iter.First(); iter.Valid(); iter.Next() {
		// Skip name-index entries (separate prefix already, but be defensive).
		var r Role
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (p *PebbleStore) CreateRole(role *Role) (*Role, error) {
	if role == nil || role.Name == "" {
		return nil, fmt.Errorf("role name required")
	}
	if role.ID == "" {
		// Seed roles use their name as ID; others get a ULID.
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		role.ID = id
	}
	// Uniqueness check on name.
	lower := strings.ToLower(role.Name)
	if existing, closer, err := p.db.Get([]byte("idx:role:name:" + lower)); err == nil {
		closer.Close()
		if string(existing) != role.ID {
			return nil, fmt.Errorf("role name already exists")
		}
	}
	now := time.Now()
	if role.CreatedAt.IsZero() {
		role.CreatedAt = now
	}
	role.UpdatedAt = now
	if role.Version == 0 {
		role.Version = 1
	}
	data, err := json.Marshal(role)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("role:"+role.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:role:name:"+lower), []byte(role.ID), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return role, nil
}

func (p *PebbleStore) UpdateRole(role *Role) error {
	if role == nil || role.ID == "" {
		return fmt.Errorf("role id required")
	}
	role.UpdatedAt = time.Now()
	role.Version++
	data, err := json.Marshal(role)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("role:"+role.ID), data, pebble.Sync)
}

func (p *PebbleStore) DeleteRole(id string) error {
	r, err := p.GetRoleByID(id)
	if err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	if r.IsSeed {
		return fmt.Errorf("cannot delete seed role %q", r.Name)
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("role:"+id), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:role:name:"+strings.ToLower(r.Name)), nil); err != nil {
		b.Close()
		return err
	}
	return b.Commit(pebble.Sync)
}

// User playlists (spec 3.4)
//
// Key schema:
//   upl:{id}                    → UserPlaylist JSON
//   idx:upl:name:{lcase-name}   → playlist ID
//   idx:upl:itunes:{pid}        → playlist ID
//   idx:upl:dirty:{id}          → "1" (pending-push set)

func (p *PebbleStore) CreateUserPlaylist(pl *UserPlaylist) (*UserPlaylist, error) {
	if pl == nil || pl.Name == "" {
		return nil, fmt.Errorf("playlist: name required")
	}
	if pl.Type != UserPlaylistTypeStatic && pl.Type != UserPlaylistTypeSmart {
		return nil, fmt.Errorf("playlist: type must be static or smart")
	}
	if pl.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		pl.ID = id
	}
	lower := strings.ToLower(pl.Name)
	if v, closer, err := p.db.Get([]byte("idx:upl:name:" + lower)); err == nil {
		existing := string(v)
		closer.Close()
		if existing != pl.ID {
			return nil, fmt.Errorf("playlist name %q already in use", pl.Name)
		}
	}
	now := time.Now()
	if pl.CreatedAt.IsZero() {
		pl.CreatedAt = now
	}
	pl.UpdatedAt = now
	if pl.Version == 0 {
		pl.Version = 1
	}
	data, err := json.Marshal(pl)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("upl:"+pl.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:upl:name:"+lower), []byte(pl.ID), nil); err != nil {
		b.Close()
		return nil, err
	}
	if pl.ITunesPersistentID != "" {
		if err := b.Set([]byte("idx:upl:itunes:"+pl.ITunesPersistentID), []byte(pl.ID), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if pl.Dirty {
		if err := b.Set([]byte("idx:upl:dirty:"+pl.ID), []byte("1"), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return pl, nil
}

func (p *PebbleStore) GetUserPlaylist(id string) (*UserPlaylist, error) {
	data, closer, err := p.db.Get([]byte("upl:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var pl UserPlaylist
	if err := json.Unmarshal(data, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

func (p *PebbleStore) GetUserPlaylistByName(name string) (*UserPlaylist, error) {
	v, closer, err := p.db.Get([]byte("idx:upl:name:" + strings.ToLower(name)))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(v)
	closer.Close()
	return p.GetUserPlaylist(id)
}

func (p *PebbleStore) GetUserPlaylistByITunesPID(pid string) (*UserPlaylist, error) {
	if pid == "" {
		return nil, nil
	}
	v, closer, err := p.db.Get([]byte("idx:upl:itunes:" + pid))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(v)
	closer.Close()
	return p.GetUserPlaylist(id)
}

func (p *PebbleStore) ListUserPlaylists(playlistType string, limit, offset int) ([]UserPlaylist, int, error) {
	// matchUser=false → all users.
	return p.listUserPlaylists(playlistType, "", false, limit, offset)
}

// ListUserPlaylistsForUser returns only playlists created by userID.
func (p *PebbleStore) ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]UserPlaylist, int, error) {
	return p.listUserPlaylists(playlistType, userID, true, limit, offset)
}

// listUserPlaylists scans all user playlists, optionally filtering by type and
// (when matchUser is true) by CreatedByUserID == userFilter, then paginates.
func (p *PebbleStore) listUserPlaylists(playlistType, userFilter string, matchUser bool, limit, offset int) ([]UserPlaylist, int, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("upl:"),
		UpperBound: []byte("upl:~"),
	})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()
	var all []UserPlaylist
	for iter.First(); iter.Valid(); iter.Next() {
		var pl UserPlaylist
		if err := json.Unmarshal(iter.Value(), &pl); err != nil {
			continue
		}
		if playlistType != "" && pl.Type != playlistType {
			continue
		}
		// When scoping to a user, include that user's playlists AND legacy
		// unowned rows (empty CreatedByUserID, e.g. pre-ownership iTunes
		// imports) so the refactor doesn't hide pre-existing data. This mirrors
		// the handler's ownedByCaller semantics.
		if matchUser && pl.CreatedByUserID != "" && pl.CreatedByUserID != userFilter {
			continue
		}
		all = append(all, pl)
	}
	total := len(all)
	if offset >= total {
		return []UserPlaylist{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return all[offset:end], total, nil
}

func (p *PebbleStore) UpdateUserPlaylist(pl *UserPlaylist) error {
	if pl == nil || pl.ID == "" {
		return fmt.Errorf("playlist id required")
	}
	prev, err := p.GetUserPlaylist(pl.ID)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("playlist %s not found", pl.ID)
	}
	pl.UpdatedAt = time.Now()
	pl.Version = prev.Version + 1
	data, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("upl:"+pl.ID), data, nil); err != nil {
		b.Close()
		return err
	}
	if !strings.EqualFold(prev.Name, pl.Name) {
		if err := b.Delete([]byte("idx:upl:name:"+strings.ToLower(prev.Name)), nil); err != nil {
			b.Close()
			return err
		}
		if err := b.Set([]byte("idx:upl:name:"+strings.ToLower(pl.Name)), []byte(pl.ID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if prev.ITunesPersistentID != pl.ITunesPersistentID {
		if prev.ITunesPersistentID != "" {
			if err := b.Delete([]byte("idx:upl:itunes:"+prev.ITunesPersistentID), nil); err != nil {
				b.Close()
				return err
			}
		}
		if pl.ITunesPersistentID != "" {
			if err := b.Set([]byte("idx:upl:itunes:"+pl.ITunesPersistentID), []byte(pl.ID), nil); err != nil {
				b.Close()
				return err
			}
		}
	}
	if pl.Dirty {
		if err := b.Set([]byte("idx:upl:dirty:"+pl.ID), []byte("1"), nil); err != nil {
			b.Close()
			return err
		}
	} else if prev.Dirty {
		if err := b.Delete([]byte("idx:upl:dirty:"+pl.ID), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) DeleteUserPlaylist(id string) error {
	pl, err := p.GetUserPlaylist(id)
	if err != nil {
		return err
	}
	if pl == nil {
		return nil
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("upl:"+id), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:upl:name:"+strings.ToLower(pl.Name)), nil); err != nil {
		b.Close()
		return err
	}
	if pl.ITunesPersistentID != "" {
		if err := b.Delete([]byte("idx:upl:itunes:"+pl.ITunesPersistentID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if pl.Dirty {
		if err := b.Delete([]byte("idx:upl:dirty:"+id), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) ListDirtyUserPlaylists() ([]UserPlaylist, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("idx:upl:dirty:"),
		UpperBound: []byte("idx:upl:dirty:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserPlaylist
	prefix := []byte("idx:upl:dirty:")
	for iter.First(); iter.Valid(); iter.Next() {
		id := string(iter.Key()[len(prefix):])
		pl, err := p.GetUserPlaylist(id)
		if err != nil || pl == nil {
			continue
		}
		out = append(out, *pl)
	}
	return out, nil
}

// User positions + book state (spec 3.6)
//
// Key schema:
//   upos:{userID}:{bookID}:{segmentID}  → UserPosition JSON
//   ubs:{userID}:{bookID}               → UserBookState JSON
//   idx:ubs:status:{userID}:{status}:{bookID} → "1"

func (p *PebbleStore) SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error {
	if userID == "" || bookID == "" || segmentID == "" {
		return fmt.Errorf("user/book/segment required")
	}
	pos := UserPosition{
		UserID: userID, BookID: bookID, SegmentID: segmentID,
		PositionSeconds: positionSeconds, UpdatedAt: time.Now(),
	}
	data, err := json.Marshal(pos)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("upos:"+userID+":"+bookID+":"+segmentID), data, pebble.NoSync)
}

func (p *PebbleStore) GetUserPosition(userID, bookID string) (*UserPosition, error) {
	if userID == "" || bookID == "" {
		return nil, nil
	}
	prefix := []byte("upos:" + userID + ":" + bookID + ":")
	upper := []byte("upos:" + userID + ":" + bookID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var latest *UserPosition
	for iter.First(); iter.Valid(); iter.Next() {
		var pos UserPosition
		if err := json.Unmarshal(iter.Value(), &pos); err != nil {
			continue
		}
		if latest == nil || pos.UpdatedAt.After(latest.UpdatedAt) {
			posCopy := pos
			latest = &posCopy
		}
	}
	return latest, nil
}

func (p *PebbleStore) ListUserPositionsForBook(userID, bookID string) ([]UserPosition, error) {
	if userID == "" || bookID == "" {
		return nil, nil
	}
	prefix := []byte("upos:" + userID + ":" + bookID + ":")
	upper := []byte("upos:" + userID + ":" + bookID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserPosition
	for iter.First(); iter.Valid(); iter.Next() {
		var pos UserPosition
		if err := json.Unmarshal(iter.Value(), &pos); err != nil {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

func (p *PebbleStore) ClearUserPositions(userID, bookID string) error {
	positions, err := p.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return err
	}
	if len(positions) == 0 {
		return nil
	}
	b := p.db.NewBatch()
	for _, pos := range positions {
		if err := b.Delete([]byte("upos:"+pos.UserID+":"+pos.BookID+":"+pos.SegmentID), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) SetUserBookState(state *UserBookState) error {
	if state == nil || state.UserID == "" || state.BookID == "" {
		return fmt.Errorf("user and book required")
	}
	state.UpdatedAt = time.Now()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	prev, _ := p.GetUserBookState(state.UserID, state.BookID)
	b := p.db.NewBatch()
	if err := b.Set([]byte("ubs:"+state.UserID+":"+state.BookID), data, nil); err != nil {
		b.Close()
		return err
	}
	if prev != nil && prev.Status != "" && prev.Status != state.Status {
		if err := b.Delete([]byte("idx:ubs:status:"+state.UserID+":"+prev.Status+":"+state.BookID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if state.Status != "" {
		if err := b.Set([]byte("idx:ubs:status:"+state.UserID+":"+state.Status+":"+state.BookID), []byte("1"), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) GetUserBookState(userID, bookID string) (*UserBookState, error) {
	if userID == "" || bookID == "" {
		return nil, nil
	}
	data, closer, err := p.db.Get([]byte("ubs:" + userID + ":" + bookID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s UserBookState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (p *PebbleStore) ListUserBookStatesByStatus(userID, status string, limit, offset int) ([]UserBookState, error) {
	if userID == "" || status == "" {
		return nil, nil
	}
	prefix := []byte("idx:ubs:status:" + userID + ":" + status + ":")
	upper := []byte("idx:ubs:status:" + userID + ":" + status + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserBookState
	skipped := 0
	prefixLen := len(prefix)
	for iter.First(); iter.Valid(); iter.Next() {
		if limit > 0 && len(out) >= limit {
			break
		}
		bookID := string(iter.Key()[prefixLen:])
		if skipped < offset {
			skipped++
			continue
		}
		state, err := p.GetUserBookState(userID, bookID)
		if err != nil || state == nil {
			continue
		}
		out = append(out, *state)
	}
	return out, nil
}

func (p *PebbleStore) ListUserPositionsSince(userID string, t time.Time) ([]UserPosition, error) {
	if userID == "" {
		return nil, nil
	}
	prefix := []byte("upos:" + userID + ":")
	upper := []byte("upos:" + userID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserPosition
	for iter.First(); iter.Valid(); iter.Next() {
		var pos UserPosition
		if err := json.Unmarshal(iter.Value(), &pos); err != nil {
			continue
		}
		if pos.UpdatedAt.After(t) {
			out = append(out, pos)
		}
	}
	return out, nil
}

// Book versions (spec 3.1)

func (p *PebbleStore) CreateBookVersion(v *BookVersion) (*BookVersion, error) {
	if v == nil || v.BookID == "" {
		return nil, fmt.Errorf("book version: book_id required")
	}
	if v.Status == "" {
		return nil, fmt.Errorf("book version: status required")
	}
	if v.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		v.ID = id
	}
	now := time.Now()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	if v.IngestDate.IsZero() {
		v.IngestDate = now
	}
	v.UpdatedAt = now
	if v.Version == 0 {
		v.Version = 1
	}

	// Single-active invariant: callers swapping primary go through
	// the version_swap tracked op per spec 3.1 §4.
	if v.Status == BookVersionStatusActive {
		if existing, err := p.GetActiveVersionForBook(v.BookID); err == nil && existing != nil && existing.ID != v.ID {
			return nil, fmt.Errorf("book %s already has an active version (%s); use version_swap to change primary", v.BookID, existing.ID)
		}
	}

	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("bv:"+v.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:bv:book:"+v.BookID+":"+v.ID), []byte("1"), nil); err != nil {
		b.Close()
		return nil, err
	}
	if v.Status == BookVersionStatusActive {
		if err := b.Set([]byte("idx:bv:active:"+v.BookID), []byte(v.ID), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if v.TorrentHash != "" {
		if err := b.Set([]byte("idx:bv:torrent:"+v.TorrentHash), []byte(v.ID), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return v, nil
}

func (p *PebbleStore) GetBookVersion(id string) (*BookVersion, error) {
	data, closer, err := p.db.Get([]byte("bv:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var v BookVersion
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (p *PebbleStore) GetBookVersionsByBookID(bookID string) ([]BookVersion, error) {
	prefix := []byte("idx:bv:book:" + bookID + ":")
	upper := []byte("idx:bv:book:" + bookID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []BookVersion
	for iter.First(); iter.Valid(); iter.Next() {
		versionID := strings.TrimPrefix(string(iter.Key()), string(prefix))
		v, err := p.GetBookVersion(versionID)
		if err != nil || v == nil {
			continue
		}
		out = append(out, *v)
	}
	return out, nil
}

func (p *PebbleStore) GetActiveVersionForBook(bookID string) (*BookVersion, error) {
	data, closer, err := p.db.Get([]byte("idx:bv:active:" + bookID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	versionID := string(data)
	closer.Close()
	return p.GetBookVersion(versionID)
}

func (p *PebbleStore) UpdateBookVersion(v *BookVersion) error {
	if v == nil || v.ID == "" {
		return fmt.Errorf("book version: id required")
	}
	prev, err := p.GetBookVersion(v.ID)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("book version %s not found", v.ID)
	}
	v.UpdatedAt = time.Now()
	v.Version = prev.Version + 1
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("bv:"+v.ID), data, nil); err != nil {
		b.Close()
		return err
	}
	if prev.Status == BookVersionStatusActive && v.Status != BookVersionStatusActive {
		if err := b.Delete([]byte("idx:bv:active:"+v.BookID), nil); err != nil {
			b.Close()
			return err
		}
	} else if v.Status == BookVersionStatusActive {
		if err := b.Set([]byte("idx:bv:active:"+v.BookID), []byte(v.ID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if prev.TorrentHash != v.TorrentHash {
		if prev.TorrentHash != "" {
			if err := b.Delete([]byte("idx:bv:torrent:"+prev.TorrentHash), nil); err != nil {
				b.Close()
				return err
			}
		}
		if v.TorrentHash != "" {
			if err := b.Set([]byte("idx:bv:torrent:"+v.TorrentHash), []byte(v.ID), nil); err != nil {
				b.Close()
				return err
			}
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) DeleteBookVersion(id string) error {
	v, err := p.GetBookVersion(id)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("bv:"+id), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:bv:book:"+v.BookID+":"+id), nil); err != nil {
		b.Close()
		return err
	}
	if v.Status == BookVersionStatusActive {
		if err := b.Delete([]byte("idx:bv:active:"+v.BookID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if v.TorrentHash != "" {
		if err := b.Delete([]byte("idx:bv:torrent:"+v.TorrentHash), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) GetBookVersionByTorrentHash(hash string) (*BookVersion, error) {
	if hash == "" {
		return nil, nil
	}
	data, closer, err := p.db.Get([]byte("idx:bv:torrent:" + hash))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	versionID := string(data)
	closer.Close()
	return p.GetBookVersion(versionID)
}

func (p *PebbleStore) ListTrashedBookVersions() ([]BookVersion, error) {
	return p.listBookVersionsByStatus(BookVersionStatusTrash)
}

func (p *PebbleStore) ListPurgedBookVersions() ([]BookVersion, error) {
	purged, err := p.listBookVersionsByStatus(BookVersionStatusInactivePurged)
	if err != nil {
		return nil, err
	}
	blocked, err := p.listBookVersionsByStatus(BookVersionStatusBlockedForRedownload)
	if err != nil {
		return nil, err
	}
	return append(purged, blocked...), nil
}

func (p *PebbleStore) listBookVersionsByStatus(status string) ([]BookVersion, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("bv:"),
		UpperBound: []byte("bv:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []BookVersion
	for iter.First(); iter.Valid(); iter.Next() {
		var v BookVersion
		if err := json.Unmarshal(iter.Value(), &v); err != nil {
			continue
		}
		if v.Status == status {
			out = append(out, v)
		}
	}
	return out, nil
}

// API keys

func (p *PebbleStore) CreateAPIKey(key *APIKey) (*APIKey, error) {
	if key == nil || key.UserID == "" {
		return nil, fmt.Errorf("api key: user_id required")
	}
	if key.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		key.ID = id
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now()
	}
	if key.Status == "" {
		key.Status = "active"
	}
	data, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("apikey:"+key.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:apikey:user:"+key.UserID+":"+key.ID), []byte("1"), nil); err != nil {
		b.Close()
		return nil, err
	}
	if key.TokenHash != "" {
		if err := b.Set([]byte("idx:apikey:hash:"+key.TokenHash), []byte(key.ID), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return key, nil
}

func (p *PebbleStore) GetAPIKey(id string) (*APIKey, error) {
	v, closer, err := p.db.Get([]byte("apikey:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var k APIKey
	if err := json.Unmarshal(v, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

func (p *PebbleStore) GetAPIKeyByHash(hash string) (*APIKey, error) {
	v, closer, err := p.db.Get([]byte("idx:apikey:hash:" + hash))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keyID := string(v)
	closer.Close()
	return p.GetAPIKey(keyID)
}

func (p *PebbleStore) ListAPIKeysForUser(userID string) ([]APIKey, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("idx:apikey:user:" + userID + ":"),
		UpperBound: []byte("idx:apikey:user:" + userID + ":~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []APIKey
	prefix := "idx:apikey:user:" + userID + ":"
	for iter.First(); iter.Valid(); iter.Next() {
		keyID := strings.TrimPrefix(string(iter.Key()), prefix)
		k, err := p.GetAPIKey(keyID)
		if err != nil || k == nil {
			continue
		}
		out = append(out, *k)
	}
	return out, nil
}

func (p *PebbleStore) ListAllAPIKeys() ([]APIKey, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("apikey:"),
		UpperBound: []byte("apikey:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []APIKey
	for iter.First(); iter.Valid(); iter.Next() {
		var k APIKey
		if err := json.Unmarshal(iter.Value(), &k); err != nil {
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

func (p *PebbleStore) RevokeAPIKey(id string) error {
	return p.SetAPIKeyStatus(id, "revoked", time.Now())
}

func (p *PebbleStore) SetAPIKeyStatus(id, status string, at time.Time) error {
	k, err := p.GetAPIKey(id)
	if err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	k.Status = status
	switch status {
	case "revoked":
		k.RevokedAt = &at
	case "inactive":
		k.DeactivatedAt = &at
	case "active":
		k.DeactivatedAt = nil
	}
	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("apikey:"+id), data, pebble.Sync)
}

func (p *PebbleStore) TouchAPIKeyLastUsed(id string, at time.Time, ip string) error {
	k, err := p.GetAPIKey(id)
	if err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	k.LastUsedAt = &at
	k.LastUsedIP = ip
	k.UseCount++
	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("apikey:"+id), data, pebble.NoSync)
}

// Invites

func (p *PebbleStore) CreateInvite(invite *Invite) (*Invite, error) {
	if invite == nil || invite.Token == "" {
		return nil, fmt.Errorf("invite: token required")
	}
	if invite.Username == "" {
		return nil, fmt.Errorf("invite: username required")
	}
	if invite.RoleID == "" {
		return nil, fmt.Errorf("invite: role_id required")
	}
	if invite.CreatedAt.IsZero() {
		invite.CreatedAt = time.Now()
	}
	if invite.ExpiresAt.IsZero() {
		invite.ExpiresAt = invite.CreatedAt.Add(7 * 24 * time.Hour)
	}
	lower := strings.ToLower(invite.Username)
	if v, closer, err := p.db.Get([]byte("idx:invite:username:" + lower)); err == nil {
		existingToken := string(v)
		closer.Close()
		if existingToken != invite.Token {
			return nil, fmt.Errorf("invite already pending for username %q", invite.Username)
		}
	}
	data, err := json.Marshal(invite)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("invite:"+invite.Token), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:invite:username:"+lower), []byte(invite.Token), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return invite, nil
}

func (p *PebbleStore) GetInvite(token string) (*Invite, error) {
	v, closer, err := p.db.Get([]byte("invite:" + token))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var i Invite
	if err := json.Unmarshal(v, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

func (p *PebbleStore) ListActiveInvites() ([]Invite, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("invite:"),
		UpperBound: []byte("invite:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	now := time.Now()
	var out []Invite
	for iter.First(); iter.Valid(); iter.Next() {
		var i Invite
		if err := json.Unmarshal(iter.Value(), &i); err != nil {
			continue
		}
		if i.UsedAt != nil {
			continue
		}
		if now.After(i.ExpiresAt) {
			continue
		}
		out = append(out, i)
	}
	return out, nil
}

func (p *PebbleStore) DeleteInvite(token string) error {
	inv, err := p.GetInvite(token)
	if err != nil {
		return err
	}
	if inv == nil {
		return nil
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("invite:"+token), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:invite:username:"+strings.ToLower(inv.Username)), nil); err != nil {
		b.Close()
		return err
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error) {
	inv, err := p.GetInvite(token)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invite not found")
	}
	if inv.UsedAt != nil {
		return nil, fmt.Errorf("invite already used")
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, fmt.Errorf("invite expired")
	}
	lowerUser := strings.ToLower(inv.Username)
	if _, closer, err := p.db.Get([]byte("idx:user:username:" + lowerUser)); err == nil {
		closer.Close()
		return nil, fmt.Errorf("username %q taken since invite was created", inv.Username)
	}

	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &User{
		ID: id, Username: inv.Username, Email: inv.Email,
		PasswordHashAlgo: passwordHashAlgo, PasswordHash: passwordHash,
		Roles: []string{inv.RoleID}, Status: "active",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	inv.UsedAt = &now

	userData, err := json.Marshal(user)
	if err != nil {
		return nil, err
	}
	invData, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}

	b := p.db.NewBatch()
	if err := b.Set([]byte("u:"+id), userData, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:user:username:"+lowerUser), []byte(id), nil); err != nil {
		b.Close()
		return nil, err
	}
	if inv.Email != "" {
		if err := b.Set([]byte("idx:user:email:"+strings.ToLower(inv.Email)), []byte(id), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Set([]byte("invite:"+token), invData, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Delete([]byte("idx:invite:username:"+lowerUser), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return user, nil
}

// Sessions
func (p *PebbleStore) CreateSession(userID, ip, userAgent string, ttl time.Duration) (*Session, error) {
	id, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{ID: id, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(ttl), IP: ip, UserAgent: userAgent, Revoked: false, Version: 1}
	data, _ := json.Marshal(sess)
	b := p.db.NewBatch()
	if err := b.Set([]byte("sess:"+id), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:sess:user:"+userID+":"+id), []byte("1"), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return sess, nil
}

func (p *PebbleStore) GetSession(id string) (*Session, error) {
	v, closer, err := p.db.Get([]byte("sess:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s Session
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (p *PebbleStore) RevokeSession(id string) error {
	s, err := p.GetSession(id)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.Revoked = true
	data, _ := json.Marshal(s)
	return p.db.Set([]byte("sess:"+id), data, pebble.Sync)
}

func (p *PebbleStore) ListUserSessions(userID string) ([]Session, error) {
	prefix := []byte("idx:sess:user:" + userID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(prefix, 0xFF)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var res []Session
	for iter.First(); iter.Valid(); iter.Next() {
		sessID := strings.TrimPrefix(string(iter.Key()), "idx:sess:user:"+userID+":")
		s, err := p.GetSession(sessID)
		if err == nil && s != nil {
			res = append(res, *s)
		}
	}
	return res, nil
}

func (p *PebbleStore) DeleteExpiredSessions(now time.Time) (int, error) {
	prefix := []byte("sess:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	batch := p.db.NewBatch()
	defer batch.Close()

	deleted := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		value := append([]byte(nil), iter.Value()...)

		var sess Session
		if err := json.Unmarshal(value, &sess); err != nil {
			continue
		}
		if !sess.Revoked && sess.ExpiresAt.After(now) {
			continue
		}

		if err := batch.Delete(key, nil); err != nil {
			return deleted, err
		}
		if err := batch.Delete([]byte("idx:sess:user:"+sess.UserID+":"+sess.ID), nil); err != nil {
			return deleted, err
		}
		deleted++
	}

	if deleted == 0 {
		return 0, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (p *PebbleStore) CountUsers() (int, error) {
	prefix := []byte("u:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	return count, nil
}

// Per-user preferences
func (p *PebbleStore) SetUserPreferenceForUser(userID, key, value string) error {
	kv := &UserPreferenceKV{UserID: userID, Key: key, Value: value, UpdatedAt: time.Now(), Version: 1}
	data, _ := json.Marshal(kv)
	return p.db.Set([]byte("pref:"+userID+":"+key), data, pebble.Sync)
}
func (p *PebbleStore) GetUserPreferenceForUser(userID, key string) (*UserPreferenceKV, error) {
	v, closer, err := p.db.Get([]byte("pref:" + userID + ":" + key))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var kv UserPreferenceKV
	if err := json.Unmarshal(v, &kv); err != nil {
		return nil, err
	}
	return &kv, nil
}
func (p *PebbleStore) GetAllPreferencesForUser(userID string) ([]UserPreferenceKV, error) {
	return p.getAllPreferencesForUser_Pebble(userID)
}

// getAllPreferencesForUser_Pebble is the original Pebble-scan implementation.
func (p *PebbleStore) getAllPreferencesForUser_Pebble(userID string) ([]UserPreferenceKV, error) {
	prefix := []byte("pref:" + userID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(prefix, 0xFF)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var res []UserPreferenceKV
	for iter.First(); iter.Valid(); iter.Next() {
		var kv UserPreferenceKV
		if err := json.Unmarshal(iter.Value(), &kv); err == nil {
			res = append(res, kv)
		}
	}
	return res, nil
}

// Book segments & merge
func (p *PebbleStore) CreateBookSegment(bookNumericID int, segment *BookSegment) (*BookSegment, error) {
	segID, err := newULID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	segment.ID = segID
	segment.BookID = bookNumericID
	segment.Active = true
	segment.CreatedAt = now
	segment.UpdatedAt = now
	segment.Version = 1
	data, _ := json.Marshal(segment)
	b := p.db.NewBatch()
	if err := b.Set([]byte("bf:"+segID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte(fmt.Sprintf("bfs:%d:%s", bookNumericID, segID)), []byte("1"), nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	// recompute duration map
	if err := p.recomputeDurationMap(bookNumericID); err != nil {
		slog.Warn("pebble recomputeDurationMap on segment create", "book_id", bookNumericID, "error", err)
	}
	return segment, nil
}

func (p *PebbleStore) UpdateBookSegment(segment *BookSegment) error {
	segment.UpdatedAt = time.Now()
	segment.Version++
	key := []byte(fmt.Sprintf("bfs:%d:%s", segment.BookID, segment.ID))
	data, err := json.Marshal(segment)
	if err != nil {
		return err
	}
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) ListBookSegments(bookNumericID int) ([]BookSegment, error) {
	prefix := []byte(fmt.Sprintf("bfs:%d:", bookNumericID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(prefix, 0xFF)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var segs []BookSegment
	for iter.First(); iter.Valid(); iter.Next() {
		segID := strings.TrimPrefix(string(iter.Key()), fmt.Sprintf("bfs:%d:", bookNumericID))
		v, closer, err := p.db.Get([]byte("bf:" + segID))
		if err == nil {
			var s BookSegment
			if err := json.Unmarshal(v, &s); err == nil {
				segs = append(segs, s)
			}
			closer.Close()
		}
	}
	return segs, nil
}

func (p *PebbleStore) MergeBookSegments(bookNumericID int, newSegment *BookSegment, supersedeIDs []string) error {
	// Create new segment
	_, err := p.CreateBookSegment(bookNumericID, newSegment)
	if err != nil {
		return err
	}
	// Mark old segments
	b := p.db.NewBatch()
	for _, id := range supersedeIDs {
		v, closer, err := p.db.Get([]byte("bf:" + id))
		if err == nil {
			var s BookSegment
			if err := json.Unmarshal(v, &s); err == nil {
				closer.Close()
				s.Active = false
				sid := newSegment.ID
				s.SupersededBy = &sid
				s.UpdatedAt = time.Now()
				data, _ := json.Marshal(&s)
				if err := b.Set([]byte("bf:"+id), data, nil); err != nil {
					b.Close()
					return err
				}
			} else {
				closer.Close()
			}
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return err
	}
	// recompute duration map
	return p.recomputeDurationMap(bookNumericID)
}

// GetBookSegmentByID retrieves a single segment by its ULID.
func (p *PebbleStore) GetBookSegmentByID(segmentID string) (*BookSegment, error) {
	v, closer, err := p.db.Get([]byte("bf:" + segmentID))
	if err != nil {
		return nil, fmt.Errorf("segment not found: %s", segmentID)
	}
	defer closer.Close()
	var seg BookSegment
	if err := json.Unmarshal(v, &seg); err != nil {
		return nil, err
	}
	return &seg, nil
}

// MoveSegmentsToBook reassigns segments to a different book (by numeric ID).
func (p *PebbleStore) MoveSegmentsToBook(segmentIDs []string, targetBookNumericID int) error {
	b := p.db.NewBatch()
	for _, segID := range segmentIDs {
		v, closer, err := p.db.Get([]byte("bf:" + segID))
		if err != nil {
			b.Close()
			return fmt.Errorf("segment not found: %s", segID)
		}
		var seg BookSegment
		if err := json.Unmarshal(v, &seg); err != nil {
			closer.Close()
			b.Close()
			return err
		}
		closer.Close()

		// Delete old index key
		oldKey := []byte(fmt.Sprintf("bfs:%d:%s", seg.BookID, seg.ID))
		if err := b.Delete(oldKey, nil); err != nil {
			b.Close()
			return err
		}

		// Update segment
		seg.BookID = targetBookNumericID
		seg.UpdatedAt = time.Now()
		seg.Version++

		data, _ := json.Marshal(&seg)
		if err := b.Set([]byte("bf:"+segID), data, nil); err != nil {
			b.Close()
			return err
		}
		// Create new index key
		newKey := []byte(fmt.Sprintf("bfs:%d:%s", targetBookNumericID, seg.ID))
		if err := b.Set(newKey, []byte("1"), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) recomputeDurationMap(bookNumericID int) error {
	segs, err := p.ListBookSegments(bookNumericID)
	if err != nil {
		return err
	}
	// simple stable ordering: by TrackNumber(if present) then FilePath
	// bubble sort (small lists expected)
	for i := 0; i < len(segs)-1; i++ {
		for j := i + 1; j < len(segs); j++ {
			less := false
			if segs[i].TrackNumber != nil && segs[j].TrackNumber != nil {
				less = *segs[i].TrackNumber > *segs[j].TrackNumber
			} else if segs[i].TrackNumber != nil {
				less = false
			} else if segs[j].TrackNumber != nil {
				less = true
			} else {
				less = segs[i].FilePath > segs[j].FilePath
			}
			if less {
				segs[i], segs[j] = segs[j], segs[i]
			}
		}
	}
	type segMap struct {
		ID          string `json:"id"`
		Duration    int    `json:"duration"`
		Active      bool   `json:"active"`
		OffsetStart int    `json:"offset_start"`
	}
	var arr []segMap
	total := 0
	for _, s := range segs {
		arr = append(arr, segMap{ID: s.ID, Duration: s.DurationSec, Active: s.Active, OffsetStart: total})
		total += s.DurationSec
	}
	m := map[string]any{"segments": arr, "total_duration": total, "version": 1}
	data, _ := json.Marshal(m)
	return p.db.Set([]byte(fmt.Sprintf("b:duration_map:%d", bookNumericID)), data, pebble.Sync)
}

// Playback events & progress
func (p *PebbleStore) AddPlaybackEvent(event *PlaybackEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	event.Version = 1
	data, _ := json.Marshal(event)
	key := fmt.Sprintf("playe:%s:%d:%d", event.UserID, event.BookID, event.CreatedAt.UnixNano())
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func (p *PebbleStore) ListPlaybackEvents(userID string, bookNumericID int, limit int) ([]PlaybackEvent, error) {
	prefix := []byte(fmt.Sprintf("playe:%s:%d:", userID, bookNumericID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(prefix, 0xFF)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var events []PlaybackEvent
	for iter.First(); iter.Valid(); iter.Next() {
		var ev PlaybackEvent
		if err := json.Unmarshal(iter.Value(), &ev); err == nil {
			events = append(events, ev)
		}
	}
	// reverse chronological and cap to limit
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (p *PebbleStore) UpdatePlaybackProgress(progress *PlaybackProgress) error {
	if progress.UpdatedAt.IsZero() {
		progress.UpdatedAt = time.Now()
	}
	progress.Version = 1
	data, _ := json.Marshal(progress)
	key := fmt.Sprintf("playp:%s:%d", progress.UserID, progress.BookID)
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func (p *PebbleStore) GetPlaybackProgress(userID string, bookNumericID int) (*PlaybackProgress, error) {
	v, closer, err := p.db.Get([]byte(fmt.Sprintf("playp:%s:%d", userID, bookNumericID)))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var pr PlaybackProgress
	if err := json.Unmarshal(v, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// Stats aggregation
func (p *PebbleStore) IncrementBookPlayStats(bookNumericID int, seconds int) error {
	// increment counters stored as decimal strings
	if err := p.incrementIntKey(fmt.Sprintf("stats:book:plays:%d", bookNumericID), 1); err != nil {
		return err
	}
	return p.incrementIntKey(fmt.Sprintf("stats:book:listen_seconds:%d", bookNumericID), seconds)
}
func (p *PebbleStore) GetBookStats(bookNumericID int) (*BookStats, error) {
	playsKey := fmt.Sprintf("stats:book:plays:%d", bookNumericID)
	secsKey := fmt.Sprintf("stats:book:listen_seconds:%d", bookNumericID)
	plays, playsErr := p.readIntKey(playsKey)
	secs, secsErr := p.readIntKey(secsKey)
	// Return nil,nil when no stats have been recorded for this book (both keys absent).
	if playsErr == nil && secsErr == nil && plays == 0 && secs == 0 {
		// Check if either key actually exists (zero is a valid recorded value).
		if _, _, err := p.db.Get([]byte(playsKey)); err == pebble.ErrNotFound {
			if _, _, err2 := p.db.Get([]byte(secsKey)); err2 == pebble.ErrNotFound {
				return nil, nil
			}
		}
	}
	return &BookStats{BookID: bookNumericID, PlayCount: plays, ListenSeconds: secs, Version: 1}, nil
}
func (p *PebbleStore) IncrementUserListenStats(userID string, seconds int) error {
	return p.incrementIntKey("stats:user:listen_seconds:"+userID, seconds)
}
func (p *PebbleStore) GetUserStats(userID string) (*UserStats, error) {
	secsKey := "stats:user:listen_seconds:" + userID
	secs, _ := p.readIntKey(secsKey)
	if secs == 0 {
		if _, _, err := p.db.Get([]byte(secsKey)); err == pebble.ErrNotFound {
			return nil, nil
		}
	}
	return &UserStats{UserID: userID, ListenSeconds: secs, Version: 1}, nil
}

func (p *PebbleStore) readIntKey(key string) (int, error) {
	v, closer, err := p.db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	return strconv.Atoi(string(v))
}
func (p *PebbleStore) incrementIntKey(key string, delta int) error {
	cur, _ := p.readIntKey(key)
	cur += delta
	return p.db.Set([]byte(key), []byte(strconv.Itoa(cur)), pebble.Sync)
}

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

// ---- Operation State Persistence (resumable operations) ----

func (p *PebbleStore) SaveOperationState(opID string, state []byte) error {
	key := []byte(fmt.Sprintf("opstate:%s", opID))
	return p.db.Set(key, state, pebble.Sync)
}

func (p *PebbleStore) GetOperationState(opID string) ([]byte, error) {
	key := []byte(fmt.Sprintf("opstate:%s", opID))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

func (p *PebbleStore) SaveOperationParams(opID string, params []byte) error {
	key := []byte(fmt.Sprintf("opstate:%s:params", opID))
	return p.db.Set(key, params, pebble.Sync)
}

func (p *PebbleStore) GetOperationParams(opID string) ([]byte, error) {
	key := []byte(fmt.Sprintf("opstate:%s:params", opID))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

func (p *PebbleStore) DeleteOperationState(opID string) error {
	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("opstate:%s", opID)), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Delete([]byte(fmt.Sprintf("opstate:%s:params", opID)), nil); err != nil {
		batch.Close()
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (p *PebbleStore) DeleteOperationsByStatus(statuses []string) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	statusSet := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		statusSet[s] = true
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		if statusSet[op.Status] {
			if err := batch.Delete(iter.Key(), nil); err != nil {
				batch.Close()
				return 0, fmt.Errorf("pebble batch delete operation: %w", err)
			}
			deleted++
		}
	}
	if deleted > 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return 0, err
		}
	} else {
		batch.Close()
	}
	return deleted, nil
}

// DeleteOperationWithLogs removes the operation record (operation:<id>) plus all
// associated log entries (operationlog:<id>:*) in a single atomic Pebble batch.
//
// Why atomic: orphaning log lines under a deleted operation wastes disk space and
// confuses diagnostics. Grouping both deletions into one batch ensures they succeed
// or fail together with no partially-deleted state visible to readers.
func (p *PebbleStore) DeleteOperationWithLogs(id string) error {
	batch := p.db.NewBatch()
	defer batch.Close()

	// Delete the operation record itself.
	opKey := []byte(fmt.Sprintf("operation:%s", id))
	if err := batch.Delete(opKey, nil); err != nil {
		return fmt.Errorf("batch delete operation key: %w", err)
	}

	// Delete all associated log lines via prefix range iteration.
	// Key format: operationlog:<operation_id>:<timestamp_nano>:<seq>
	logPrefix := []byte(fmt.Sprintf("operationlog:%s:", id))
	logUpper := prefixEnd(logPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: logPrefix,
		UpperBound: logUpper,
	})
	if err != nil {
		return fmt.Errorf("open log iterator: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		if err := batch.Delete(k, nil); err != nil {
			iter.Close()
			return fmt.Errorf("batch delete log key: %w", err)
		}
	}
	if iterErr := iter.Error(); iterErr != nil {
		iter.Close()
		return fmt.Errorf("log iterator: %w", iterErr)
	}
	iter.Close()

	return batch.Commit(pebble.Sync)
}

func (p *PebbleStore) GetInterruptedOperations() ([]Operation, error) {
	var ops []Operation
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		if op.Status == "running" || op.Status == "queued" || op.Status == "interrupted" {
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// SaveLibraryFingerprint stores or updates the fingerprint for an iTunes library file.
func (p *PebbleStore) SaveLibraryFingerprint(path string, size int64, modTime time.Time, crc32val uint32) error {
	rec := LibraryFingerprintRecord{
		Path:      path,
		Size:      size,
		ModTime:   modTime,
		CRC32:     crc32val,
		UpdatedAt: time.Now(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("itunes:fingerprint:%s", path))
	return p.db.Set(key, data, pebble.Sync)
}

// GetLibraryFingerprint retrieves the stored fingerprint for an iTunes library file.
func (p *PebbleStore) GetLibraryFingerprint(path string) (*LibraryFingerprintRecord, error) {
	key := []byte(fmt.Sprintf("itunes:fingerprint:%s", path))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var rec LibraryFingerprintRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// CreateDeferredITunesUpdate stores a deferred iTunes path update.
func (p *PebbleStore) CreateDeferredITunesUpdate(bookID, persistentID, oldPath, newPath, updateType string) error {
	id := time.Now().UnixNano()
	rec := DeferredITunesUpdate{
		ID:           int(id),
		BookID:       bookID,
		PersistentID: persistentID,
		OldPath:      oldPath,
		NewPath:      newPath,
		UpdateType:   updateType,
		CreatedAt:    time.Now(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("deferred_itunes:%019d", id))
	return p.db.Set(key, data, pebble.Sync)
}

// GetPendingDeferredITunesUpdates returns all deferred updates that haven't been applied yet.
func (p *PebbleStore) GetPendingDeferredITunesUpdates() ([]DeferredITunesUpdate, error) {
	prefix := []byte("deferred_itunes:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []DeferredITunesUpdate
	for iter.First(); iter.Valid(); iter.Next() {
		var rec DeferredITunesUpdate
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.AppliedAt == nil {
			results = append(results, rec)
		}
	}
	return results, nil
}

// MarkDeferredITunesUpdateApplied sets the applied_at timestamp on a deferred update.
func (p *PebbleStore) MarkDeferredITunesUpdateApplied(id int) error {
	key := []byte(fmt.Sprintf("deferred_itunes:%019d", id))
	data, closer, err := p.db.Get(key)
	if err != nil {
		return err
	}
	var rec DeferredITunesUpdate
	if err := json.Unmarshal(data, &rec); err != nil {
		closer.Close()
		return err
	}
	closer.Close()

	now := time.Now()
	rec.AppliedAt = &now
	updated, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// GetDeferredITunesUpdatesByBookID returns all deferred updates for a specific book.
func (p *PebbleStore) GetDeferredITunesUpdatesByBookID(bookID string) ([]DeferredITunesUpdate, error) {
	all, err := p.getPendingAndAppliedDeferredUpdates()
	if err != nil {
		return nil, err
	}
	var results []DeferredITunesUpdate
	for _, rec := range all {
		if rec.BookID == bookID {
			results = append(results, rec)
		}
	}
	return results, nil
}

func (p *PebbleStore) getPendingAndAppliedDeferredUpdates() ([]DeferredITunesUpdate, error) {
	prefix := []byte("deferred_itunes:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []DeferredITunesUpdate
	for iter.First(); iter.Valid(); iter.Next() {
		var rec DeferredITunesUpdate
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		results = append(results, rec)
	}
	return results, nil
}

// CreateExternalIDMapping creates or replaces an external ID mapping.
func (p *PebbleStore) CreateExternalIDMapping(mapping *ExternalIDMapping) error {
	now := time.Now()
	mapping.CreatedAt = now
	mapping.UpdatedAt = now

	data, err := json.Marshal(mapping)
	if err != nil {
		return err
	}

	primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", mapping.Source, mapping.ExternalID))
	reverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", mapping.BookID, mapping.Source, mapping.ExternalID))

	batch := p.db.NewBatch()
	defer batch.Close()

	if err := batch.Set(primaryKey, data, nil); err != nil {
		return fmt.Errorf("pebble Set ext_id primary: %w", err)
	}
	if err := batch.Set(reverseKey, []byte(mapping.ExternalID), nil); err != nil {
		return fmt.Errorf("pebble Set ext_id reverse: %w", err)
	}

	return batch.Commit(pebble.Sync)
}

// GetBookByExternalID returns the book_id for a non-tombstoned external ID.
func (p *PebbleStore) GetBookByExternalID(source, externalID string) (string, error) {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closer.Close()

	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return "", err
	}
	if mapping.Tombstoned {
		return "", nil
	}
	return mapping.BookID, nil
}

// GetExternalIDsForBook returns all external ID mappings for a book.
func (p *PebbleStore) GetExternalIDsForBook(bookID string) ([]ExternalIDMapping, error) {
	prefix := []byte(fmt.Sprintf("ext_id:book:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []ExternalIDMapping
	for iter.First(); iter.Valid(); iter.Next() {
		// Parse source and externalID from key: ext_id:book:<bookID>:<source>:<externalID>
		parts := strings.SplitN(string(iter.Key()), ":", 5)
		if len(parts) < 5 {
			continue
		}
		source := parts[3]
		extID := parts[4]

		primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", source, extID))
		data, closer, err := p.db.Get(primaryKey)
		if err != nil {
			continue
		}
		var mapping ExternalIDMapping
		if err := json.Unmarshal(data, &mapping); err != nil {
			closer.Close()
			continue
		}
		closer.Close()
		results = append(results, mapping)
	}
	return results, nil
}

// IsExternalIDTombstoned checks whether an external ID is tombstoned.
func (p *PebbleStore) IsExternalIDTombstoned(source, externalID string) (bool, error) {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closer.Close()

	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return false, err
	}
	return mapping.Tombstoned, nil
}

// TombstoneExternalID marks an external ID as tombstoned to prevent reimport.
func (p *PebbleStore) TombstoneExternalID(source, externalID string) error {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err != nil {
		return err
	}
	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		closer.Close()
		return err
	}
	closer.Close()

	mapping.Tombstoned = true
	mapping.UpdatedAt = time.Now()

	updated, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// ReassignExternalIDs moves all external ID mappings from one book to another (for merges).
func (p *PebbleStore) ReassignExternalIDs(oldBookID, newBookID string) error {
	mappings, err := p.GetExternalIDsForBook(oldBookID)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	now := time.Now()
	for _, m := range mappings {
		// Delete old reverse key
		oldReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", oldBookID, m.Source, m.ExternalID))
		if err := batch.Delete(oldReverseKey, nil); err != nil {
			return fmt.Errorf("pebble Delete ext_id old reverse: %w", err)
		}

		// Update mapping
		m.BookID = newBookID
		m.UpdatedAt = now
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", m.Source, m.ExternalID))
		if err := batch.Set(primaryKey, data, nil); err != nil {
			return fmt.Errorf("pebble Set ext_id primary: %w", err)
		}

		// Add new reverse key
		newReverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", newBookID, m.Source, m.ExternalID))
		if err := batch.Set(newReverseKey, []byte(m.ExternalID), nil); err != nil {
			return fmt.Errorf("pebble Set ext_id new reverse: %w", err)
		}
	}

	return batch.Commit(pebble.Sync)
}

// BulkCreateExternalIDMappings inserts multiple external ID mappings.
// Existing mappings are not overwritten (ignore semantics).
func (p *PebbleStore) BulkCreateExternalIDMappings(mappings []ExternalIDMapping) error {
	batch := p.db.NewBatch()
	defer batch.Close()

	now := time.Now()
	for _, m := range mappings {
		primaryKey := []byte(fmt.Sprintf("ext_id:%s:%s", m.Source, m.ExternalID))
		// Check if already exists
		if _, closer, err := p.db.Get(primaryKey); err == nil {
			closer.Close()
			continue // skip existing
		}

		m.CreatedAt = now
		m.UpdatedAt = now
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if err := batch.Set(primaryKey, data, nil); err != nil {
			return fmt.Errorf("pebble Set ext_id primary: %w", err)
		}

		reverseKey := []byte(fmt.Sprintf("ext_id:book:%s:%s:%s", m.BookID, m.Source, m.ExternalID))
		if err := batch.Set(reverseKey, []byte(m.ExternalID), nil); err != nil {
			return fmt.Errorf("pebble Set ext_id reverse: %w", err)
		}
	}

	return batch.Commit(pebble.Sync)
}

// MarkExternalIDRemoved marks an external ID mapping as tombstoned and records
// the removal timestamp. The primary ext_id:<source>:<externalID> record is
// updated in-place so provenance and other fields are preserved.
func (p *PebbleStore) MarkExternalIDRemoved(source, externalID string) error {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pebble Get ext_id for removal: %w", err)
	}
	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		closer.Close()
		return fmt.Errorf("unmarshal ext_id for removal: %w", err)
	}
	closer.Close()

	now := time.Now()
	mapping.Tombstoned = true
	mapping.RemovedAt = &now
	updated, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshal ext_id after removal: %w", err)
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// SetExternalIDProvenance updates the provenance field on an existing external
// ID mapping record. No-ops silently if the record does not exist.
func (p *PebbleStore) SetExternalIDProvenance(source, externalID, provenance string) error {
	key := []byte(fmt.Sprintf("ext_id:%s:%s", source, externalID))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pebble Get ext_id for provenance: %w", err)
	}
	var mapping ExternalIDMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		closer.Close()
		return fmt.Errorf("unmarshal ext_id for provenance: %w", err)
	}
	closer.Close()

	mapping.Provenance = provenance
	updated, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshal ext_id after provenance: %w", err)
	}
	return p.db.Set(key, updated, pebble.Sync)
}

// GetRemovedExternalIDs returns all tombstoned external ID mappings for the
// given source (i.e. records where MarkExternalIDRemoved was called).
func (p *PebbleStore) GetRemovedExternalIDs(source string) ([]ExternalIDMapping, error) {
	prefix := []byte(fmt.Sprintf("ext_id:%s:", source))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []ExternalIDMapping
	for iter.First(); iter.Valid(); iter.Next() {
		var mapping ExternalIDMapping
		if err := json.Unmarshal(iter.Value(), &mapping); err != nil {
			continue
		}
		if mapping.Tombstoned {
			results = append(results, mapping)
		}
	}
	return results, nil
}

func (p *PebbleStore) SetRaw(key string, value []byte) error {
	return p.db.Set([]byte(key), value, pebble.Sync)
}

// GetRaw reads a single key. Returns (nil, nil) on miss so callers
// can handle cache-style lookups with a two-valued result instead
// of a sentinel error.
func (p *PebbleStore) GetRaw(key string) ([]byte, error) {
	val, closer, err := p.db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	// Copy because the closer frees the underlying bytes.
	out := make([]byte, len(val))
	copy(out, val)
	return out, nil
}

func (p *PebbleStore) DeleteRaw(key string) error {
	return p.db.Delete([]byte(key), pebble.Sync)
}

func (p *PebbleStore) ScanPrefix(prefix string) ([]KVPair, error) {
	prefixBytes := []byte(prefix)
	upperBound := make([]byte, len(prefixBytes))
	copy(upperBound, prefixBytes)
	upperBound[len(upperBound)-1]++
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var pairs []KVPair
	for iter.First(); iter.Valid(); iter.Next() {
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())
		pairs = append(pairs, KVPair{Key: string(iter.Key()), Value: val})
	}
	return pairs, nil
}

func (p *PebbleStore) CountPrefix(prefix string) (int64, error) {
	prefixBytes := []byte(prefix)
	upperBound := make([]byte, len(prefixBytes))
	copy(upperBound, prefixBytes)
	upperBound[len(upperBound)-1]++
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: upperBound,
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	var n int64
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n, nil
}

func (p *PebbleStore) CreateOperationResult(result *OperationResult) error {
	result.CreatedAt = time.Now()
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("op_result:%s:%s", result.OperationID, result.BookID))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetOperationResults(operationID string) ([]OperationResult, error) {
	prefix := []byte(fmt.Sprintf("op_result:%s:", operationID))
	upperBound := make([]byte, len(prefix))
	copy(upperBound, prefix)
	upperBound[len(upperBound)-1]++
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []OperationResult
	for iter.First(); iter.Valid(); iter.Next() {
		var r OperationResult
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// GetOperationResultsPage returns a page of results and the total count.
// PebbleDB has no SQL so we load all keys (key-only scan for count) then
// read only the needed slice. For typical operation sizes this is fast;
// very large operations (5 000+) still benefit because the caller no
// longer marshals and transmits the entire payload to the client.
func (p *PebbleStore) GetOperationResultsPage(operationID string, limit, offset int) ([]OperationResult, int, error) {
	all, err := p.GetOperationResults(operationID)
	if err != nil {
		return nil, 0, err
	}
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := total
	if limit > 0 && offset+limit < total {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

func (p *PebbleStore) GetRecentCompletedOperations(limit int) ([]Operation, error) {
	// Scan all operations, collect completed/failed, sort by time, take limit
	prefix := []byte("operation:")
	upperBound := make([]byte, len(prefix))
	copy(upperBound, prefix)
	upperBound[len(upperBound)-1]++
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var ops []Operation
	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		if op.Status == "completed" || op.Status == "failed" {
			ops = append(ops, op)
		}
	}

	// Sort by CreatedAt descending
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].CreatedAt.After(ops[j].CreatedAt)
	})

	if len(ops) > limit {
		ops = ops[:limit]
	}
	return ops, nil
}

// --- User Tags (free-form labels on books) ---

// GetBookUserTags returns all user-defined tags for a book.
func (p *PebbleStore) GetBookUserTags(bookID string) ([]string, error) {
	dbKey := []byte(fmt.Sprintf("user_tag:book:%s", bookID))
	value, closer, err := p.db.Get(dbKey)
	if err == pebble.ErrNotFound {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var tags []string
	if err := json.Unmarshal(value, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// SetBookUserTags replaces all user-defined tags for a book.
func (p *PebbleStore) SetBookUserTags(bookID string, tags []string) error {
	dbKey := []byte(fmt.Sprintf("user_tag:book:%s", bookID))
	data, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return p.db.Set(dbKey, data, pebble.Sync)
}

// AddBookUserTag adds a single user-defined tag to a book (idempotent).
func (p *PebbleStore) AddBookUserTag(bookID string, tag string) error {
	existing, err := p.GetBookUserTags(bookID)
	if err != nil {
		return err
	}
	for _, t := range existing {
		if t == tag {
			return nil // already present
		}
	}
	existing = append(existing, tag)
	return p.SetBookUserTags(bookID, existing)
}

// RemoveBookUserTag removes a single user-defined tag from a book.
func (p *PebbleStore) RemoveBookUserTag(bookID string, tag string) error {
	existing, err := p.GetBookUserTags(bookID)
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(existing))
	for _, t := range existing {
		if t != tag {
			filtered = append(filtered, t)
		}
	}
	return p.SetBookUserTags(bookID, filtered)
}

// --- Book Alternative Titles ---
//
// Stored as one JSON blob per book under key `alt_titles:book:<id>`.
// The Pebble store doesn't persist to any SQL table so the schema
// from migration 046 is irrelevant here — this is the Pebble-native
// representation used by production. The SQLite implementation is
// only for the test-backed sidecar path.

// GetBookAlternativeTitles returns every alt title for a book.
func (p *PebbleStore) GetBookAlternativeTitles(bookID string) ([]BookAlternativeTitle, error) {
	dbKey := []byte(fmt.Sprintf("alt_titles:book:%s", bookID))
	value, closer, err := p.db.Get(dbKey)
	if err == pebble.ErrNotFound {
		return []BookAlternativeTitle{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var alts []BookAlternativeTitle
	if err := json.Unmarshal(value, &alts); err != nil {
		return nil, err
	}
	return alts, nil
}

// SetBookAlternativeTitles replaces every alt title for a book.
func (p *PebbleStore) SetBookAlternativeTitles(bookID string, titles []BookAlternativeTitle) error {
	dbKey := []byte(fmt.Sprintf("alt_titles:book:%s", bookID))
	// Normalize: make sure every row has book_id populated + a
	// created_at, and default source="user" when omitted.
	now := time.Now().UTC()
	normalized := make([]BookAlternativeTitle, 0, len(titles))
	for _, alt := range titles {
		if alt.Title == "" {
			continue
		}
		if alt.BookID == "" {
			alt.BookID = bookID
		}
		if alt.Source == "" {
			alt.Source = "user"
		}
		if alt.CreatedAt.IsZero() {
			alt.CreatedAt = now
		}
		normalized = append(normalized, alt)
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return p.db.Set(dbKey, data, pebble.Sync)
}

// AddBookAlternativeTitle appends one alt title. Idempotent on (book_id,
// title) — if the same title already exists, the call is a no-op and
// the existing source/language/created_at are preserved.
func (p *PebbleStore) AddBookAlternativeTitle(bookID, title, source, language string) error {
	if title == "" {
		return fmt.Errorf("alternative title cannot be empty")
	}
	existing, err := p.GetBookAlternativeTitles(bookID)
	if err != nil {
		return err
	}
	for _, alt := range existing {
		if alt.Title == title {
			return nil // already present
		}
	}
	existing = append(existing, BookAlternativeTitle{
		BookID:    bookID,
		Title:     title,
		Source:    source,
		Language:  language,
		CreatedAt: time.Now().UTC(),
	})
	return p.SetBookAlternativeTitles(bookID, existing)
}

// RemoveBookAlternativeTitle deletes one variant. No-op if absent.
func (p *PebbleStore) RemoveBookAlternativeTitle(bookID, title string) error {
	existing, err := p.GetBookAlternativeTitles(bookID)
	if err != nil {
		return err
	}
	filtered := make([]BookAlternativeTitle, 0, len(existing))
	for _, alt := range existing {
		if alt.Title != title {
			filtered = append(filtered, alt)
		}
	}
	return p.SetBookAlternativeTitles(bookID, filtered)
}

// Reset clears all data from the store and resets all counters to initial state
func (p *PebbleStore) Reset() error {
	// Use DeleteRange to wipe the entire keyspace in one operation.
	// The range ["\x00", "\xff\xff") covers all possible keys.
	batch := p.db.NewBatch()
	if err := batch.DeleteRange([]byte{0x00}, []byte{0xff, 0xff}, pebble.NoSync); err != nil {
		batch.Close()
		return fmt.Errorf("failed to delete all keys: %w", err)
	}

	// Reinitialize counters to their initial state
	counters := []string{"author", "author_alias", "series", "book", "import_path", "operationlog", "playlist", "playlistitem", "preference"}
	for _, counter := range counters {
		key := fmt.Sprintf("counter:%s", counter)
		if err := batch.Set([]byte(key), []byte("1"), pebble.NoSync); err != nil {
			batch.Close()
			return fmt.Errorf("failed to initialize counter %s: %w", counter, err)
		}
	}

	// Commit with sync for durability
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit reset batch: %w", err)
	}

	// Force flush to ensure deletes are persisted to disk
	if err := p.db.Flush(); err != nil {
		return fmt.Errorf("failed to flush after reset: %w", err)
	}

	// Clear the in-memory query layer so it no longer returns stale rows
	// that were wiped from Pebble. Reads check `p.mem() != nil`, so swapping
	// in a fresh empty MemStore (or nil-ing the pointer if construction
	// fails) immediately bypasses the stale snapshot. Without this, reads
	// from memdb-backed paths (e.g. GetAllAuthors) would continue to return
	// entries that were just deleted from Pebble.
	if fresh, err := NewMemStore(); err == nil {
		p.memPtr.Store(fresh)
	} else {
		p.memPtr.Store(nil)
	}

	return nil
}

// CountByPrefix counts keys that start with the given prefix.
func (p *PebbleStore) CountByPrefix(prefix string) (int, error) {
	lb := []byte(prefix)
	ub := make([]byte, len(lb))
	copy(ub, lb)
	ub[len(ub)-1]++

	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return 0, fmt.Errorf("CountByPrefix %q: %w", prefix, err)
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	return count, iter.Error()
}

// WipeByPrefixes deletes all keys that start with any of the given prefix strings.
// Returns the total number of keys deleted.
func (p *PebbleStore) WipeByPrefixes(prefixes []string) (int, error) {
	total := 0
	for _, prefix := range prefixes {
		lb := []byte(prefix)
		// Upper bound: increment the last byte to cover all keys with this prefix.
		ub := make([]byte, len(lb))
		copy(ub, lb)
		ub[len(ub)-1]++

		iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
		if err != nil {
			return total, fmt.Errorf("wipe prefix %q: iter: %w", prefix, err)
		}

		var keys [][]byte
		for iter.First(); iter.Valid(); iter.Next() {
			k := make([]byte, len(iter.Key()))
			copy(k, iter.Key())
			keys = append(keys, k)
		}
		if err := iter.Close(); err != nil {
			return total, fmt.Errorf("wipe prefix %q: iter close: %w", prefix, err)
		}

		if len(keys) == 0 {
			continue
		}

		batch := p.db.NewBatch()
		for _, k := range keys {
			if err := batch.Delete(k, nil); err != nil {
				batch.Close()
				return total, fmt.Errorf("wipe prefix %q: delete: %w", prefix, err)
			}
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return total, fmt.Errorf("wipe prefix %q: commit: %w", prefix, err)
		}
		total += len(keys)
	}
	return total, nil
}

// Optimize compacts the PebbleDB database to reclaim space.
func (p *PebbleStore) Optimize() error {
	return p.db.Compact(context.Background(), nil, []byte{0xff}, false)
}

// CreateOperationChange stores an operation change in PebbleDB.
func (p *PebbleStore) CreateOperationChange(change *OperationChange) error {
	if change.ID == "" {
		change.ID = ulid.Make().String()
	}
	change.CreatedAt = time.Now()
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("opchange:%s:%s", change.OperationID, change.ID)
	return p.db.Set([]byte(key), data, pebble.Sync)
}

// GetOperationChanges returns all changes for a given operation.
func (p *PebbleStore) GetOperationChanges(operationID string) ([]*OperationChange, error) {
	prefix := []byte(fmt.Sprintf("opchange:%s:", operationID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var changes []*OperationChange
	for iter.First(); iter.Valid(); iter.Next() {
		var c OperationChange
		if err := json.Unmarshal(iter.Value(), &c); err != nil {
			return nil, err
		}
		changes = append(changes, &c)
	}
	return changes, iter.Error()
}

// GetBookChanges returns all changes for a given book.
func (p *PebbleStore) GetBookChanges(bookID string) ([]*OperationChange, error) {
	prefix := []byte("opchange:")
	upperBound := []byte("opchange;") // ':' + 1 = ';'
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var changes []*OperationChange
	for iter.First(); iter.Valid(); iter.Next() {
		var c OperationChange
		if err := json.Unmarshal(iter.Value(), &c); err != nil {
			return nil, err
		}
		if c.BookID == bookID {
			changes = append(changes, &c)
		}
	}
	return changes, iter.Error()
}

// RevertOperationChanges marks all changes for an operation as reverted.
func (p *PebbleStore) RevertOperationChanges(operationID string) error {
	changes, err := p.GetOperationChanges(operationID)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, c := range changes {
		if c.RevertedAt == nil {
			c.RevertedAt = &now
			data, err := json.Marshal(c)
			if err != nil {
				return err
			}
			key := fmt.Sprintf("opchange:%s:%s", c.OperationID, c.ID)
			if err := p.db.Set([]byte(key), data, pebble.Sync); err != nil {
				return err
			}
		}
	}
	return nil
}

// CreateAuthorTombstone writes a tombstone that redirects oldID to canonicalID.
func (p *PebbleStore) CreateAuthorTombstone(oldID, canonicalID int) error {
	key := []byte(fmt.Sprintf("author_tombstone:%d", oldID))
	value := []byte(strconv.Itoa(canonicalID))
	return p.db.Set(key, value, pebble.Sync)
}

// GetAuthorTombstone returns the canonical author ID for a tombstoned author.
// Returns 0 if no tombstone exists.
func (p *PebbleStore) GetAuthorTombstone(oldID int) (int, error) {
	key := []byte(fmt.Sprintf("author_tombstone:%d", oldID))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	canonicalID, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, fmt.Errorf("invalid tombstone value for author %d: %w", oldID, err)
	}
	return canonicalID, nil
}

// ResolveTombstoneChains finds chains like A→B→C and collapses them so A→C, B→C.
// Returns the number of tombstones updated.
func (p *PebbleStore) ResolveTombstoneChains() (int, error) {
	// Collect all tombstones
	tombstones := make(map[int]int) // oldID → canonicalID
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author_tombstone:"),
		UpperBound: []byte("author_tombstone;"),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create tombstone iterator: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := strings.SplitN(keyStr, ":", 2)
		if len(parts) != 2 {
			continue
		}
		oldID, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		val, valErr := iter.ValueAndErr()
		if valErr != nil {
			continue
		}
		canonicalID, err := strconv.Atoi(string(val))
		if err != nil {
			continue
		}
		tombstones[oldID] = canonicalID
	}

	// Resolve chains: follow each tombstone to its final destination
	updated := 0
	for oldID, canonicalID := range tombstones {
		finalID := canonicalID
		visited := map[int]bool{oldID: true}
		for {
			nextID, exists := tombstones[finalID]
			if !exists {
				break
			}
			if visited[finalID] {
				break // cycle detection
			}
			visited[finalID] = true
			finalID = nextID
		}
		if finalID != canonicalID {
			// Update the tombstone to point directly to the final destination
			key := []byte(fmt.Sprintf("author_tombstone:%d", oldID))
			if err := p.db.Set(key, []byte(strconv.Itoa(finalID)), pebble.Sync); err != nil {
				return updated, fmt.Errorf("failed to update tombstone %d: %w", oldID, err)
			}
			updated++
		}
	}

	return updated, nil
}

// AddSystemActivityLog stores a log entry from a housekeeping goroutine.
func (p *PebbleStore) AddSystemActivityLog(source, level, message string) error {
	key := fmt.Sprintf("syslog:%s:%s", time.Now().Format(time.RFC3339Nano), source)
	val := SystemActivityLog{
		Source:    source,
		Level:     level,
		Message:   message,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(key), data, pebble.Sync)
}

// GetSystemActivityLogs retrieves recent system activity log entries.
func (p *PebbleStore) GetSystemActivityLogs(source string, limit int) ([]SystemActivityLog, error) {
	prefix := []byte("syslog:")
	upperBound := append(append([]byte{}, prefix...), 0xFF)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var logs []SystemActivityLog
	for iter.Last(); iter.Valid(); iter.Prev() {
		var l SystemActivityLog
		if err := json.Unmarshal(iter.Value(), &l); err != nil {
			continue
		}
		if source != "" && l.Source != source {
			continue
		}
		logs = append(logs, l)
		if len(logs) >= limit {
			break
		}
	}
	return logs, nil
}

// PruneOperationLogs deletes operation log entries older than the given time.
// Key format: operationlog:<operation_id>:<timestamp_nanos>:<seq>
func (p *PebbleStore) PruneOperationLogs(olderThan time.Time) (int, error) {
	prefix := "operationlog:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	olderThanNanos := olderThan.UnixNano()
	for iter.First(); iter.Valid(); iter.Next() {
		// Key: operationlog:<opID>:<nanos>:<seq>
		// Parse the JSON value to get CreatedAt.
		var logEntry OperationLog
		if jsonErr := json.Unmarshal(iter.Value(), &logEntry); jsonErr != nil {
			continue
		}
		if logEntry.CreatedAt.UnixNano() < olderThanNanos {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete operationlog: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

// PruneOperationChanges deletes operation change entries older than the given time.
// Key format: opchange:<operation_id>:<ulid>
func (p *PebbleStore) PruneOperationChanges(olderThan time.Time) (int, error) {
	prefix := "opchange:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var change OperationChange
		if jsonErr := json.Unmarshal(iter.Value(), &change); jsonErr != nil {
			continue
		}
		if change.CreatedAt.Before(olderThan) {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete opchange: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

// PruneSystemActivityLogs deletes system activity log entries older than the given time.
// Key format: syslog:<RFC3339Nano>:<source>
func (p *PebbleStore) PruneSystemActivityLogs(olderThan time.Time) (int, error) {
	prefix := "syslog:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	// Key format: syslog:<RFC3339Nano>:<source>
	// RFC3339Nano contains colons (e.g. "2006-01-02T15:04:05.999999999Z07:00"),
	// so we parse the JSON value to get CreatedAt rather than parsing the key.
	for iter.First(); iter.Valid(); iter.Next() {
		var entry struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if jsonErr := json.Unmarshal(iter.Value(), &entry); jsonErr != nil {
			continue
		}
		if entry.CreatedAt.Before(olderThan) {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete syslog: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

// derefInt64 safely dereferences a *int64, returning 0 for nil.
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// derefBool safely dereferences a *bool, returning false for nil.
func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

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

// AddBookTag adds a user-sourced tag to a book. Server code that
// auto-applies tags should use AddBookTagWithSource so provenance
// is preserved.
func (p *PebbleStore) AddBookTag(bookID, tag string) error {
	return p.AddBookTagWithSource(bookID, tag, "user")
}

// AddBookTagWithSource adds a tag with an explicit source. Typical
// sources: "user" (default), "system" (auto-applied by the server).
// Upserts when the row already exists — later writes overwrite the
// source field so a user-claimed tag can promote to system or vice
// versa without needing a delete-first step.
func (p *PebbleStore) AddBookTagWithSource(bookID, tag, source string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	if source == "" {
		source = "user"
	}

	bt := BookTag{
		BookID:    bookID,
		Tag:       tag,
		Source:    source,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(bt)
	if err != nil {
		return err
	}

	// Primary key: book_tag:<bookID>:<tag>
	bookTagKey := []byte(fmt.Sprintf("book_tag:%s:%s", bookID, tag))
	if err := p.db.Set(bookTagKey, data, pebble.Sync); err != nil {
		return err
	}

	// Reverse index: tag_idx:<tag>:<bookID>
	tagIdxKey := []byte(fmt.Sprintf("tag_idx:%s:%s", tag, bookID))
	return p.db.Set(tagIdxKey, []byte{}, pebble.Sync)
}

// RemoveBookTag removes a tag from a book regardless of source.
func (p *PebbleStore) RemoveBookTag(bookID, tag string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}

	bookTagKey := []byte(fmt.Sprintf("book_tag:%s:%s", bookID, tag))
	if err := p.db.Delete(bookTagKey, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}

	tagIdxKey := []byte(fmt.Sprintf("tag_idx:%s:%s", tag, bookID))
	if err := p.db.Delete(tagIdxKey, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}

	return nil
}

// RemoveBookTagsByPrefix removes every tag on a book whose name
// begins with `prefix`, optionally scoped to a specific source.
// Used to clear a namespace before writing a fresh system tag —
// e.g., re-applying metadata from a new source removes any
// existing `metadata:source:*` system tags first so each book has
// exactly one source tag at a time.
//
// If `source` is empty, all sources match.
func (p *PebbleStore) RemoveBookTagsByPrefix(bookID, prefix, source string) error {
	prefix = util.NormalizeString(prefix)
	if prefix == "" {
		return fmt.Errorf("prefix cannot be empty")
	}

	detailed, err := p.GetBookTagsDetailed(bookID)
	if err != nil {
		return err
	}
	for _, bt := range detailed {
		if !strings.HasPrefix(bt.Tag, prefix) {
			continue
		}
		if source != "" && bt.Source != source {
			continue
		}
		if err := p.RemoveBookTag(bookID, bt.Tag); err != nil {
			return err
		}
	}
	return nil
}

// GetBookTags returns all tag strings for a book, sorted alphabetically.
func (p *PebbleStore) GetBookTags(bookID string) ([]string, error) {
	prefix := []byte(fmt.Sprintf("book_tag:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var tags []string
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		tags = append(tags, bt.Tag)
	}
	sort.Strings(tags)
	return tags, nil
}

// GetBookTagsDetailed returns tags with their source attribution.
// Rows written before migration 47 deserialize with source="" which
// we promote to "user" so downstream filters treat them as user
// tags (the sensible default for legacy data).
func (p *PebbleStore) GetBookTagsDetailed(bookID string) ([]BookTag, error) {
	prefix := []byte(fmt.Sprintf("book_tag:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []BookTag
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		if bt.Source == "" {
			bt.Source = "user"
		}
		out = append(out, bt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// SetBookTags replaces all USER tags on a book with the given set.
// System tags (dedup:*, metadata:source:*, ...) are preserved so the
// user-facing bulk-replace doesn't clobber server-applied provenance.
func (p *PebbleStore) SetBookTags(bookID string, tags []string) error {
	detailed, err := p.GetBookTagsDetailed(bookID)
	if err != nil {
		return err
	}

	// Normalize incoming tags.
	normalized := make(map[string]bool)
	for _, t := range tags {
		t = util.NormalizeString(t)
		if t != "" {
			normalized[t] = true
		}
	}

	// Existing user tags we may need to drop.
	existingUser := make(map[string]bool)
	for _, bt := range detailed {
		if bt.Source == "user" || bt.Source == "" {
			existingUser[bt.Tag] = true
		}
	}

	// Remove user tags not in new set.
	for t := range existingUser {
		if !normalized[t] {
			if err := p.RemoveBookTag(bookID, t); err != nil {
				return err
			}
		}
	}

	// Add user tags not already present.
	for t := range normalized {
		if !existingUser[t] {
			if err := p.AddBookTagWithSource(bookID, t, "user"); err != nil {
				return err
			}
		}
	}

	return nil
}

// ListAllTags returns all unique tags with their usage counts.
func (p *PebbleStore) ListAllTags() ([]TagWithCount, error) {
	prefix := []byte("tag_idx:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	counts := make(map[string]int)
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: tag_idx:<tag>:<bookID>
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) >= 2 {
			counts[parts[1]]++
		}
	}

	result := make([]TagWithCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagWithCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Tag < result[j].Tag
	})
	return result, nil
}

// GetBooksByTag returns all book IDs that have the given tag.
func (p *PebbleStore) GetBooksByTag(tag string) ([]string, error) {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag cannot be empty")
	}

	prefix := []byte(fmt.Sprintf("tag_idx:%s:", tag))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var bookIDs []string
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: tag_idx:<tag>:<bookID>
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) == 3 {
			bookIDs = append(bookIDs, parts[2])
		}
	}
	return bookIDs, nil
}

// ---------- Author / Series tag storage ----------
//
// Authors and series follow the same tag shape as books. Pebble
// keys are parameterized by a keyspace prefix so the same helper
// functions serve all three entity types:
//
//	Books:   book_tag:<bookID>:<tag>       tag_idx:<tag>:<bookID>
//	Authors: author_tag:<authorID>:<tag>   author_tag_idx:<tag>:<authorID>
//	Series:  series_tag:<seriesID>:<tag>   series_tag_idx:<tag>:<seriesID>
//
// Entity IDs are string-formatted for author/series (integer → string)
// because Pebble keys are flat bytes — the caller provides the ID
// formatting and the helper never has to care about the type.

// pebbleTagKeyspace bundles the prefixes for one entity type.
type pebbleTagKeyspace struct {
	tagPrefix   string // e.g. "author_tag:"
	indexPrefix string // e.g. "author_tag_idx:"
	entityLabel string // for error messages / logging
}

var (
	authorTagKeyspace = pebbleTagKeyspace{
		tagPrefix:   "author_tag:",
		indexPrefix: "author_tag_idx:",
		entityLabel: "author",
	}
	seriesTagKeyspace = pebbleTagKeyspace{
		tagPrefix:   "series_tag:",
		indexPrefix: "series_tag_idx:",
		entityLabel: "series",
	}
)

// pebbleAddTag upserts a tag for any entity type. Serializes a
// BookTag with the source field so it survives round-trips.
func (p *PebbleStore) pebbleAddTag(ks pebbleTagKeyspace, entityID, tag, source string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	if source == "" {
		source = "user"
	}
	bt := BookTag{
		BookID:    entityID, // reused as the generic entity ID
		Tag:       tag,
		Source:    source,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(bt)
	if err != nil {
		return err
	}
	primary := []byte(fmt.Sprintf("%s%s:%s", ks.tagPrefix, entityID, tag))
	if err := p.db.Set(primary, data, pebble.Sync); err != nil {
		return err
	}
	idx := []byte(fmt.Sprintf("%s%s:%s", ks.indexPrefix, tag, entityID))
	return p.db.Set(idx, []byte{}, pebble.Sync)
}

func (p *PebbleStore) pebbleRemoveTag(ks pebbleTagKeyspace, entityID, tag string) error {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	primary := []byte(fmt.Sprintf("%s%s:%s", ks.tagPrefix, entityID, tag))
	if err := p.db.Delete(primary, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}
	idx := []byte(fmt.Sprintf("%s%s:%s", ks.indexPrefix, tag, entityID))
	if err := p.db.Delete(idx, pebble.Sync); err != nil && err != pebble.ErrNotFound {
		return err
	}
	return nil
}

func (p *PebbleStore) pebbleGetTags(ks pebbleTagKeyspace, entityID string) ([]string, error) {
	prefix := []byte(fmt.Sprintf("%s%s:", ks.tagPrefix, entityID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var tags []string
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		tags = append(tags, bt.Tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func (p *PebbleStore) pebbleGetTagsDetailed(ks pebbleTagKeyspace, entityID string) ([]BookTag, error) {
	prefix := []byte(fmt.Sprintf("%s%s:", ks.tagPrefix, entityID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []BookTag
	for iter.First(); iter.Valid(); iter.Next() {
		var bt BookTag
		if err := json.Unmarshal(iter.Value(), &bt); err != nil {
			continue
		}
		if bt.Source == "" {
			bt.Source = "user"
		}
		out = append(out, bt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

func (p *PebbleStore) pebbleRemoveTagsByPrefix(ks pebbleTagKeyspace, entityID, prefix, source string) error {
	prefix = util.NormalizeString(prefix)
	if prefix == "" {
		return fmt.Errorf("prefix cannot be empty")
	}
	detailed, err := p.pebbleGetTagsDetailed(ks, entityID)
	if err != nil {
		return err
	}
	for _, bt := range detailed {
		if !strings.HasPrefix(bt.Tag, prefix) {
			continue
		}
		if source != "" && bt.Source != source {
			continue
		}
		if err := p.pebbleRemoveTag(ks, entityID, bt.Tag); err != nil {
			return err
		}
	}
	return nil
}

func (p *PebbleStore) pebbleSetTags(ks pebbleTagKeyspace, entityID string, tags []string) error {
	detailed, err := p.pebbleGetTagsDetailed(ks, entityID)
	if err != nil {
		return err
	}
	normalized := make(map[string]bool)
	for _, t := range tags {
		t = util.NormalizeString(t)
		if t != "" {
			normalized[t] = true
		}
	}
	existingUser := make(map[string]bool)
	for _, bt := range detailed {
		if bt.Source == "user" || bt.Source == "" {
			existingUser[bt.Tag] = true
		}
	}
	for t := range existingUser {
		if !normalized[t] {
			if err := p.pebbleRemoveTag(ks, entityID, t); err != nil {
				return err
			}
		}
	}
	for t := range normalized {
		if !existingUser[t] {
			if err := p.pebbleAddTag(ks, entityID, t, "user"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *PebbleStore) pebbleListAllTags(ks pebbleTagKeyspace) ([]TagWithCount, error) {
	prefix := []byte(ks.indexPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	counts := make(map[string]int)
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: <indexPrefix><tag>:<entityID>
		key := string(iter.Key())
		rest := strings.TrimPrefix(key, ks.indexPrefix)
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) >= 1 {
			counts[parts[0]]++
		}
	}

	result := make([]TagWithCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagWithCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Tag < result[j].Tag
	})
	return result, nil
}

func (p *PebbleStore) pebbleEntitiesByTag(ks pebbleTagKeyspace, tag string) ([]string, error) {
	tag = util.NormalizeString(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag cannot be empty")
	}
	prefix := []byte(fmt.Sprintf("%s%s:", ks.indexPrefix, tag))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var ids []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		rest := strings.TrimPrefix(key, fmt.Sprintf("%s%s:", ks.indexPrefix, tag))
		if rest != "" {
			ids = append(ids, rest)
		}
	}
	return ids, nil
}

// ---------- Author tag wrappers (PebbleStore) ----------

func (p *PebbleStore) AddAuthorTag(authorID int, tag string) error {
	return p.pebbleAddTag(authorTagKeyspace, strconv.Itoa(authorID), tag, "user")
}
func (p *PebbleStore) AddAuthorTagWithSource(authorID int, tag, source string) error {
	return p.pebbleAddTag(authorTagKeyspace, strconv.Itoa(authorID), tag, source)
}
func (p *PebbleStore) RemoveAuthorTag(authorID int, tag string) error {
	return p.pebbleRemoveTag(authorTagKeyspace, strconv.Itoa(authorID), tag)
}
func (p *PebbleStore) RemoveAuthorTagsByPrefix(authorID int, prefix, source string) error {
	return p.pebbleRemoveTagsByPrefix(authorTagKeyspace, strconv.Itoa(authorID), prefix, source)
}
func (p *PebbleStore) GetAuthorTags(authorID int) ([]string, error) {
	return p.pebbleGetTags(authorTagKeyspace, strconv.Itoa(authorID))
}
func (p *PebbleStore) GetAuthorTagsDetailed(authorID int) ([]BookTag, error) {
	return p.pebbleGetTagsDetailed(authorTagKeyspace, strconv.Itoa(authorID))
}
func (p *PebbleStore) SetAuthorTags(authorID int, tags []string) error {
	return p.pebbleSetTags(authorTagKeyspace, strconv.Itoa(authorID), tags)
}
func (p *PebbleStore) ListAllAuthorTags() ([]TagWithCount, error) {
	return p.pebbleListAllTags(authorTagKeyspace)
}
func (p *PebbleStore) GetAuthorsByTag(tag string) ([]int, error) {
	raw, err := p.pebbleEntitiesByTag(authorTagKeyspace, tag)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(raw))
	for _, s := range raw {
		n, err := strconv.Atoi(s)
		if err != nil {
			continue // skip malformed entries
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// ---------- Series tag wrappers (PebbleStore) ----------

func (p *PebbleStore) AddSeriesTag(seriesID int, tag string) error {
	return p.pebbleAddTag(seriesTagKeyspace, strconv.Itoa(seriesID), tag, "user")
}
func (p *PebbleStore) AddSeriesTagWithSource(seriesID int, tag, source string) error {
	return p.pebbleAddTag(seriesTagKeyspace, strconv.Itoa(seriesID), tag, source)
}
func (p *PebbleStore) RemoveSeriesTag(seriesID int, tag string) error {
	return p.pebbleRemoveTag(seriesTagKeyspace, strconv.Itoa(seriesID), tag)
}
func (p *PebbleStore) RemoveSeriesTagsByPrefix(seriesID int, prefix, source string) error {
	return p.pebbleRemoveTagsByPrefix(seriesTagKeyspace, strconv.Itoa(seriesID), prefix, source)
}
func (p *PebbleStore) GetSeriesTags(seriesID int) ([]string, error) {
	return p.pebbleGetTags(seriesTagKeyspace, strconv.Itoa(seriesID))
}
func (p *PebbleStore) GetSeriesTagsDetailed(seriesID int) ([]BookTag, error) {
	return p.pebbleGetTagsDetailed(seriesTagKeyspace, strconv.Itoa(seriesID))
}
func (p *PebbleStore) SetSeriesTags(seriesID int, tags []string) error {
	return p.pebbleSetTags(seriesTagKeyspace, strconv.Itoa(seriesID), tags)
}
func (p *PebbleStore) ListAllSeriesTags() ([]TagWithCount, error) {
	return p.pebbleListAllTags(seriesTagKeyspace)
}
func (p *PebbleStore) GetSeriesByTag(tag string) ([]int, error) {
	raw, err := p.pebbleEntitiesByTag(seriesTagKeyspace, tag)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(raw))
	for _, s := range raw {
		n, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// ---- BookFile CRUD ----

// bookFilePathCRC returns the lowercase hex CRC32 of the file path, used as
// the secondary index key suffix for book_file_path lookups.
func bookFilePathCRC(filePath string) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(filePath)))
}

// marshalBookFileDropSegs serialises f to JSON with AcoustIDSeg0..6 removed.
// It operates on a shallow copy so the caller's struct (used afterwards by
// writeBookFileSecondaryIndexes and UpsertBookFileToMemDB) is never mutated.
//
// T020: segment fields are deprecated — they waste 200–400 MB of Pebble space
// and are no longer needed after LSH replaced the last consumer. New rows are
// written without them; a background sweep (dedup.bookfile-seg-drop) rewrites
// old rows on-demand. The struct fields remain for decode of legacy rows.
func marshalBookFileDropSegs(f *BookFile) ([]byte, error) {
	c := *f // shallow copy — all fields copied, slice headers duplicated
	c.AcoustIDSeg0 = ""
	c.AcoustIDSeg1 = ""
	c.AcoustIDSeg2 = ""
	c.AcoustIDSeg3 = ""
	c.AcoustIDSeg4 = ""
	c.AcoustIDSeg5 = ""
	c.AcoustIDSeg6 = ""
	return json.Marshal(&c)
}

// getBookFileByID fetches a BookFile by its primary key (book_file:<bookID>:<fileID>).
func (s *PebbleStore) getBookFileByID(bookID, fileID string) (*BookFile, error) {
	key := []byte(fmt.Sprintf("book_file:%s:%s", bookID, fileID))
	value, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var f BookFile
	if err := json.Unmarshal(value, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// writeBookFileSecondaryIndexes adds the PID and path secondary index entries
// to the batch. Either index is only written when the relevant field is non-empty.
func writeBookFileSecondaryIndexes(batch *pebble.Batch, f *BookFile) error {
	ref := []byte(fmt.Sprintf("%s:%s", f.BookID, f.ID))

	// book_file_id:<fileID> → "<bookID>:<fileID>" — allows ID-only lookups.
	idxKey := []byte(fmt.Sprintf("book_file_id:%s", f.ID))
	if err := batch.Set(idxKey, []byte(fmt.Sprintf("book_file:%s:%s", f.BookID, f.ID)), nil); err != nil {
		return err
	}

	if f.ITunesPersistentID != "" {
		pidKey := []byte(fmt.Sprintf("book_file_pid:%s", f.ITunesPersistentID))
		if err := batch.Set(pidKey, ref, nil); err != nil {
			return err
		}
	}

	if f.FilePath != "" {
		pathKey := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(f.FilePath)))
		if err := batch.Set(pathKey, ref, nil); err != nil {
			return err
		}
	}

	if f.FileHash != "" {
		hashKey := []byte(fmt.Sprintf("book_file_hash:%s", f.FileHash))
		if err := batch.Set(hashKey, ref, nil); err != nil {
			return err
		}
	}

	if f.OriginalFileHash != "" && f.OriginalFileHash != f.FileHash {
		origKey := []byte(fmt.Sprintf("book_file_orig_hash:%s", f.OriginalFileHash))
		if err := batch.Set(origKey, ref, nil); err != nil {
			return err
		}
	}

	// Write secondary index for each non-empty fingerprint segment.
	for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
		if seg != "" {
			acoustKey := []byte(fmt.Sprintf("book_file_acoustid:%s", seg))
			if err := batch.Set(acoustKey, ref, nil); err != nil {
				return err
			}
		}
	}

	// LSH secondary index over the whole-file AcoustID fingerprint.
	if err := writeFingerprintLSHIndexes(batch, f); err != nil {
		return err
	}
	return nil
}

// deleteBookFileSecondaryIndexes removes PID, path, and acoustid secondary
// index entries from the batch for the given BookFile. The store pointer
// is used to read the LSH meta-row from the committed DB state — Pebble's
// non-indexed batch can't satisfy that lookup itself.
func (s *PebbleStore) deleteBookFileSecondaryIndexes(batch *pebble.Batch, f *BookFile) error {
	// Delete book_file_id secondary index.
	if f.ID != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("book_file_id:%s", f.ID)), nil); err != nil {
			return err
		}
	}

	if f.ITunesPersistentID != "" {
		pidKey := []byte(fmt.Sprintf("book_file_pid:%s", f.ITunesPersistentID))
		if err := batch.Delete(pidKey, nil); err != nil {
			return err
		}
	}

	if f.FilePath != "" {
		pathKey := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(f.FilePath)))
		if err := batch.Delete(pathKey, nil); err != nil {
			return err
		}
	}

	if f.FileHash != "" {
		hashKey := []byte(fmt.Sprintf("book_file_hash:%s", f.FileHash))
		if err := batch.Delete(hashKey, nil); err != nil {
			return err
		}
	}

	if f.OriginalFileHash != "" && f.OriginalFileHash != f.FileHash {
		origKey := []byte(fmt.Sprintf("book_file_orig_hash:%s", f.OriginalFileHash))
		if err := batch.Delete(origKey, nil); err != nil {
			return err
		}
	}

	// Delete secondary index for each non-empty fingerprint segment.
	for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
		if seg != "" {
			acoustKey := []byte(fmt.Sprintf("book_file_acoustid:%s", seg))
			if err := batch.Delete(acoustKey, nil); err != nil {
				return err
			}
		}
	}

	// LSH index — best-effort delete via the fpidx_meta side-table.
	if err := deleteFingerprintLSHIndexesByIDWithStore(s, batch, f.ID); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// LSH (locality-sensitive hashing) secondary index over BookFile whole-file
// fingerprints. See internal/fingerprint/lsh.go for the parameter choices
// and PLAN-LSH.md for the design.
//
// Two key prefixes:
//   fpidx:<band:1B><subprint:8B>:<bookFileID>        -> 1 byte LSHIndexVersion
//   fpidx_meta:<bookFileID>                          -> 1B version + N×(1B band + 8B subprint)
//
// The meta row makes deletes O(1) — we don't have to re-derive subprints
// from the (possibly stale) fp bytes when UpdateBookFile changes them.
// ---------------------------------------------------------------------------

const (
	lshKeyPrefix     = "fpidx:"
	lshMetaKeyPrefix = "fpidx_meta:"
)

// lshIndexKey builds an fpidx: row key for one (band, subprint, bookFileID).
func lshIndexKey(band byte, sub fingerprint.Subprint, bookFileID string) []byte {
	// 6 (prefix) + 1 (band) + 8 (subprint) + 1 (sep) + len(id)
	out := make([]byte, 0, 6+1+8+1+len(bookFileID))
	out = append(out, lshKeyPrefix...)
	out = append(out, band)
	out = append(out, sub[:]...)
	out = append(out, ':')
	out = append(out, bookFileID...)
	return out
}

// lshMetaKey builds the per-BookFile meta row key.
func lshMetaKey(bookFileID string) []byte {
	out := make([]byte, 0, len(lshMetaKeyPrefix)+len(bookFileID))
	out = append(out, lshMetaKeyPrefix...)
	out = append(out, bookFileID...)
	return out
}

// encodeLSHMeta packs version + (band,subprint) tuples for the meta row.
func encodeLSHMeta(subs []fingerprint.Subprint, bands []byte) []byte {
	out := make([]byte, 1+9*len(subs))
	out[0] = fingerprint.LSHIndexVersion
	for i := range subs {
		off := 1 + 9*i
		out[off] = bands[i]
		copy(out[off+1:off+9], subs[i][:])
	}
	return out
}

// decodeLSHMeta unpacks a meta-row value. Returns nil, nil on empty or
// version mismatch (caller treats as "no entries").
func decodeLSHMeta(v []byte) ([]fingerprint.Subprint, []byte) {
	if len(v) == 0 || v[0] != fingerprint.LSHIndexVersion {
		return nil, nil
	}
	body := v[1:]
	n := len(body) / 9
	if n == 0 {
		return nil, nil
	}
	subs := make([]fingerprint.Subprint, n)
	bands := make([]byte, n)
	for i := 0; i < n; i++ {
		off := 9 * i
		bands[i] = body[off]
		copy(subs[i][:], body[off+1:off+9])
	}
	return subs, bands
}

// writeFingerprintLSHIndexes derives subprints from f.AcoustIDFingerprint and
// writes the fpidx + fpidx_meta rows in the supplied batch. No-op when the
// fingerprint is empty or too short to sample.
//
// Value stored in each fpidx: row is the BookID (UTF-8 bytes), matching the
// spec (SPEC 1 §5) so T013's probe-collector can retrieve the candidate
// book without a secondary BookFile lookup.
func writeFingerprintLSHIndexes(batch *pebble.Batch, f *BookFile) error {
	if len(f.AcoustIDFingerprint) == 0 {
		return nil
	}
	subs, bands, err := fingerprint.Subprints(f.AcoustIDFingerprint)
	if err != nil {
		// Misaligned bytes — treat as no index. Don't poison the batch.
		return nil
	}
	if len(subs) == 0 {
		return nil
	}
	bookIDVal := []byte(f.BookID)
	for i := range subs {
		if err := batch.Set(lshIndexKey(bands[i], subs[i], f.ID), bookIDVal, nil); err != nil {
			return err
		}
	}
	return batch.Set(lshMetaKey(f.ID), encodeLSHMeta(subs, bands), nil)
}

// deleteFingerprintLSHIndexesByID looks up the meta row from the underlying
// DB (not the batch — UpdateBookFile uses a non-indexed batch), deletes every
// fpidx row it points at, then deletes the meta row itself. Safe to call on
// a BookFile that never had an LSH entry. The reader must be a *PebbleStore
// passed via package-level helper, but to keep the function batch-only we
// instead route through the DB handle pinned at module level — see
// deleteFingerprintLSHIndexesByIDWithDB.
//
// The hook-callers in this file have access to the *PebbleStore receiver via
// the batch's parent, but Pebble doesn't expose that. Instead, the entry
// points (UpdateBookFile, DeleteBookFile) wrap this in a closure that holds
// the store.
//
// Callers should use deleteFingerprintLSHIndexesByIDWithStore below.
func deleteFingerprintLSHIndexesByIDWithStore(s *PebbleStore, batch *pebble.Batch, bookFileID string) error {
	metaKey := lshMetaKey(bookFileID)
	v, closer, err := s.db.Get(metaKey)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil
		}
		return err
	}
	val := append([]byte(nil), v...)
	_ = closer.Close()

	subs, bands := decodeLSHMeta(val)
	for i := range subs {
		if err := batch.Delete(lshIndexKey(bands[i], subs[i], bookFileID), nil); err != nil {
			return err
		}
	}
	return batch.Delete(metaKey, nil)
}

// deleteFingerprintLSHIndexesByID is a no-op placeholder kept for the
// generic secondary-index-delete hook signature. The actual delete needs
// access to the *PebbleStore handle (see deleteFingerprintLSHIndexesByIDWithStore)
// because UpdateBookFile uses a non-indexed batch and we have to read the
// meta row from the committed DB state.
func deleteFingerprintLSHIndexesByID(_ *pebble.Batch, _ string) error {
	return nil
}

// LookupAcoustIDCandidates returns BookFile IDs whose LSH subprints collide
// with fp on ≥ fingerprint.LSHMinBandHits bands. Sorted by hit-count desc,
// capped at maxCandidates. Empty fp ⇒ ([], nil).
func (s *PebbleStore) LookupAcoustIDCandidates(fp []byte, maxCandidates int) ([]string, error) {
	if len(fp) == 0 {
		return nil, nil
	}
	subs, bands, err := fingerprint.Subprints(fp)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, nil
	}
	if maxCandidates <= 0 {
		maxCandidates = 200
	}

	hits := make(map[string]int, 256)
	for i := range subs {
		// Iterate fpidx:<band><subprint>:* — the trailing ':' anchors us at
		// the start of the per-(band,subprint) range; upper bound is the
		// next subprint by lexicographic order.
		lower := lshIndexKey(bands[i], subs[i], "")
		upper := append([]byte{}, lower...)
		// Bump the last byte (':') to ';' to form the exclusive upper bound.
		upper[len(upper)-1] = ';'
		iter, ierr := s.db.NewIter(&pebble.IterOptions{
			LowerBound: lower,
			UpperBound: upper,
		})
		if ierr != nil {
			return nil, ierr
		}
		for iter.First(); iter.Valid(); iter.Next() {
			key := iter.Key()
			// key = fpidx:<band:1><subprint:8>:<bookFileID>
			// id starts after prefix(6) + 1 + 8 + 1 = 16
			if len(key) <= 16 {
				continue
			}
			id := string(key[16:])
			hits[id]++
		}
		_ = iter.Close()
	}

	type scoredID struct {
		id  string
		hit int
	}
	scored := make([]scoredID, 0, len(hits))
	for id, n := range hits {
		if n < fingerprint.LSHMinBandHits {
			continue
		}
		scored = append(scored, scoredID{id, n})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].hit != scored[j].hit {
			return scored[i].hit > scored[j].hit
		}
		return scored[i].id < scored[j].id
	})
	if len(scored) > maxCandidates {
		scored = scored[:maxCandidates]
	}
	out := make([]string, len(scored))
	for i, s := range scored {
		out[i] = s.id
	}
	return out, nil
}

// HasLSHIndex reports whether a BookFile has an LSH meta entry that matches
// the current LSHIndexVersion. Returns false for missing entries AND for
// entries written by an older index version, so the build op rewrites stale
// rows whenever LSHIndexVersion or LSHBandCount changes.
func (s *PebbleStore) HasLSHIndex(bookFileID string) bool {
	val, closer, err := s.db.Get(lshMetaKey(bookFileID))
	if err != nil {
		return false
	}
	defer closer.Close()
	return len(val) > 0 && val[0] == fingerprint.LSHIndexVersion
}

// CreateBookFile stores a new BookFile, generating a ULID if the ID is empty.
// It writes the primary key book_file:<bookID>:<fileID> and secondary indexes
// for iTunes PID and file path (when non-empty) atomically in a single batch.
func (s *PebbleStore) CreateBookFile(file *BookFile) error {
	if file.ID == "" {
		id, err := newULID()
		if err != nil {
			return err
		}
		file.ID = id
	}

	now := time.Now()
	if file.CreatedAt.IsZero() {
		file.CreatedAt = now
	}
	file.UpdatedAt = now

	// T020: drop AcoustIDSeg0..6 from the stored value via a copy; the
	// original struct is preserved for writeBookFileSecondaryIndexes and
	// UpsertBookFileToMemDB below.
	data, err := marshalBookFileDropSegs(file)
	if err != nil {
		return err
	}

	batch := s.db.NewBatch()

	key := []byte(fmt.Sprintf("book_file:%s:%s", file.BookID, file.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return err
	}

	if err := writeBookFileSecondaryIndexes(batch, file); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "create_book_file")
	s.UpsertBookFileToMemDB(file)
	// Recompute book-level Duration/FileSize aggregates now that a file was added.
	// Best-effort: the file write already committed; don't fail on aggregate errors.
	s.notifyBookFileChange(file.BookID)
	return nil
}

// UpdateBookFile replaces an existing BookFile, cleaning up stale secondary
// indexes when the PID or path changes.
func (s *PebbleStore) UpdateBookFile(id string, file *BookFile) error {
	// We need the bookID to build the primary key; it must be set on file.
	old, err := s.getBookFileByID(file.BookID, id)
	if err != nil {
		return err
	}
	if old == nil {
		return fmt.Errorf("book file not found: %s", id)
	}

	file.ID = id
	file.CreatedAt = old.CreatedAt
	file.UpdatedAt = time.Now()

	// T020: drop AcoustIDSeg0..6 from the stored value via a copy.
	data, err := marshalBookFileDropSegs(file)
	if err != nil {
		return err
	}

	batch := s.db.NewBatch()

	// Remove stale secondary indexes before writing new ones.
	if err := s.deleteBookFileSecondaryIndexes(batch, old); err != nil {
		batch.Close()
		return err
	}

	key := []byte(fmt.Sprintf("book_file:%s:%s", file.BookID, file.ID))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return err
	}

	if err := writeBookFileSecondaryIndexes(batch, file); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "update_book_file")
	s.UpsertBookFileToMemDB(file)
	// Recompute book-level Duration/FileSize aggregates now that a file was updated.
	// Best-effort: the file write already committed; don't fail on aggregate errors.
	s.notifyBookFileChange(file.BookID)
	return nil
}

// GetBookFiles returns all BookFile records for the given bookID by iterating
// the prefix book_file:<bookID>:.
func (s *PebbleStore) GetBookFiles(bookID string) ([]BookFile, error) {
	prefix := []byte(fmt.Sprintf("book_file:%s:", bookID))
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var files []BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	// Sort: disc ASC, track ASC, file_path ASC — matches SQLite ORDER BY.
	sort.Slice(files, func(i, j int) bool {
		if files[i].DiscNumber != files[j].DiscNumber {
			return files[i].DiscNumber < files[j].DiscNumber
		}
		if files[i].TrackNumber != files[j].TrackNumber {
			return files[i].TrackNumber < files[j].TrackNumber
		}
		return files[i].FilePath < files[j].FilePath
	})
	return files, nil
}

// GetBookFilesForIDs returns book files grouped by bookID. When memdb is
// published, uses the memdb book_id index — O(sum of files per ID), not
// O(all 308K book_files) like the Pebble full-scan fallback. For a
// 500-book page query, this drops the call from ~15s to <5ms; for a
// 20-book query, from ~15s to <1ms. The Pebble full-scan was the actual
// killer behind 500-per-page taking 3m51s pre-fix.
//
// Pebble full-scan retained as fallback for cold-start (before memdb
// publishes) and tests with no memdb.
func (s *PebbleStore) GetBookFilesForIDs(bookIDs []string) (map[string][]BookFile, error) {
	if s.UseMemDB && s.mem() != nil {
		return s.mem().GetBookFilesForIDs(bookIDs)
	}
	return s.getBookFilesForIDsPebbleScan(bookIDs)
}

func (s *PebbleStore) getBookFilesForIDsPebbleScan(bookIDs []string) (map[string][]BookFile, error) {
	result := make(map[string][]BookFile)
	if len(bookIDs) == 0 {
		return result, nil
	}
	idSet := make(map[string]bool)
	for _, id := range bookIDs {
		idSet[id] = true
	}
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			return nil, err
		}
		if idSet[f.BookID] {
			result[f.BookID] = append(result[f.BookID], f)
		}
	}
	return result, nil
}

// GetAllBookFiles returns every BookFile in the database. When memdb is
// published, iterates the in-memory book_files table — a pointer walk over
// ~308K rows — instead of a Pebble prefix scan with per-row JSON unmarshal.
// Mirrors the GetBookFilesForIDs fastpath from PR #1153.
//
// Pebble full-scan retained as fallback for cold-start (before memdb
// publishes) and tests with no memdb.
func (s *PebbleStore) GetAllBookFiles() ([]BookFile, error) {
	if s.UseMemDB && s.mem() != nil {
		return s.mem().GetAllBookFiles()
	}
	return s.getAllBookFilesPebbleScan()
}

func (s *PebbleStore) getAllBookFilesPebbleScan() ([]BookFile, error) {
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"), // ';' is one past ':' in ASCII
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var files []BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// Skip secondary index entries (book_file_pid:, book_file_path:, book_file_acoustid:).
		if !strings.HasPrefix(key, "book_file:") {
			continue
		}
		// Primary keys look like book_file:<bookID>:<fileID> — must have exactly 2 colons
		// after the prefix, meaning the full key has 3 colon-separated segments.
		parts := strings.SplitN(key, ":", 4)
		if len(parts) != 3 {
			continue
		}
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// GetBookFilesNeedingDelugeImport returns book_files that have a non-empty
// deluge_hash but have not yet been imported (imported_from_deluge_at IS NULL).
//
// When memdb is published, walks the sparse deluge_hash index — only rows
// where DelugeHash is non-empty are indexed — and post-filters on the
// ImportedFromDelugeAt nil check. Drops the deluge discovery handler +
// centralization plugin from a 308K full BookFile scan to walking just the
// (much smaller) deluge-touched subset (H2 + H8). Pebble full-scan retained
// as the cold-start fallback.
func (s *PebbleStore) GetBookFilesNeedingDelugeImport() ([]BookFile, error) {
	if s.UseMemDB && s.mem() != nil {
		return s.mem().GetBookFilesNeedingDelugeImport()
	}
	all, err := s.GetAllBookFiles()
	if err != nil {
		return nil, err
	}
	var out []BookFile
	for _, f := range all {
		if f.DelugeHash != "" && f.ImportedFromDelugeAt == nil {
			out = append(out, f)
		}
	}
	return out, nil
}

// GetBookFileByPID looks up a BookFile by iTunes persistent ID using the
// book_file_pid:<pid> secondary index.
func (s *PebbleStore) GetBookFileByPID(itunesPID string) (*BookFile, error) {
	if itunesPID == "" {
		return nil, nil
	}
	pidKey := []byte(fmt.Sprintf("book_file_pid:%s", itunesPID))
	value, closer, err := s.db.Get(pidKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := string(value)
	closer.Close()

	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt book_file_pid index value: %q", ref)
	}
	return s.getBookFileByID(parts[0], parts[1])
}

// ClearITunesPID clears itunes_persistent_id and itunes_path on the
// book_file with the given PID. Returns (true, nil) if a row was
// updated, (false, nil) if no row with that PID exists.
func (s *PebbleStore) ClearITunesPID(itunesPID string) (bool, error) {
	if itunesPID == "" {
		return false, nil
	}
	f, err := s.GetBookFileByPID(itunesPID)
	if err != nil {
		return false, err
	}
	if f == nil {
		return false, nil
	}
	f.ITunesPersistentID = ""
	f.ITunesPath = ""
	if err := s.UpdateBookFile(f.ID, f); err != nil {
		return false, fmt.Errorf("ClearITunesPID: %w", err)
	}
	return true, nil
}

// GetBookFileByPath looks up a BookFile by file path using the
// book_file_path:<crc32hex> secondary index.
func (s *PebbleStore) GetBookFileByPath(filePath string) (*BookFile, error) {
	if filePath == "" {
		return nil, nil
	}
	pathKey := []byte(fmt.Sprintf("book_file_path:%s", bookFilePathCRC(filePath)))
	value, closer, err := s.db.Get(pathKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := string(value)
	closer.Close()

	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt book_file_path index value: %q", ref)
	}
	f, err := s.getBookFileByID(parts[0], parts[1])
	if err != nil {
		return nil, err
	}
	// Verify the stored path matches (CRC collision guard).
	if f != nil && f.FilePath != filePath {
		return nil, nil
	}
	return f, nil
}

// GetBookFileByAcoustID looks up a BookFile by exact AcoustID fingerprint
// using the book_file_acoustid: secondary index.
func (s *PebbleStore) GetBookFileByAcoustID(fp string) (*BookFile, error) {
	if fp == "" {
		return nil, nil
	}
	key := []byte(fmt.Sprintf("book_file_acoustid:%s", fp))
	value, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref := string(value)
	closer.Close()

	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt book_file_acoustid index: %q", ref)
	}
	return s.getBookFileByID(parts[0], parts[1])
}

// GetBookFileByAcoustIDFuzzy scans all book_file records and returns the first
// whose AcoustID fingerprint similarity to fp is >= minSimilarity.
// O(n) over fingerprinted files — only called when exact match misses.
//
// Memdb fastpath: when memdb is warm, walk in-RAM book_files (seg0..6 are
// preserved post-MAYDEPLOY-J). Falls back to Pebble prefix scan below.
func (s *PebbleStore) GetBookFileByAcoustIDFuzzy(fp string, minSimilarity float64) (*BookFile, error) {
	if s.UseMemDB {
		if m := s.mem(); m != nil {
			return m.GetBookFileByAcoustIDFuzzy(fp, minSimilarity)
		}
	}

	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte{}, prefix...), 0xff),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var f BookFile
		if err := json.Unmarshal(iter.Value(), &f); err != nil {
			continue
		}
		// Check all 7 fingerprint segments for a fuzzy match.
		segs := [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
			f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6}
		matched := false
		for _, seg := range segs {
			if seg == "" {
				continue
			}
			sim, err := fingerprint.HammingSimilarity(fp, seg)
			if err != nil {
				continue
			}
			if sim >= minSimilarity {
				matched = true
				break
			}
		}
		if matched {
			return &f, nil
		}
	}
	return nil, iter.Error()
}

// DeleteBookFile removes the BookFile with the given ID (and its secondary
// indexes) from the store. It requires the bookID to be available on the
// struct; the caller must have obtained the record first, so we scan the
// secondary path index or retrieve by ID. Since we only have fileID here we
// perform a prefix scan to locate the record.
func (s *PebbleStore) DeleteBookFile(id string) error {
	// Scan all book_file: keys to find the one with this file ID.
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return err
	}

	var found *BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: book_file:<bookID>:<fileID>
		key := string(iter.Key())
		parts := strings.SplitN(key, ":", 3)
		if len(parts) == 3 && parts[2] == id {
			var f BookFile
			if jsonErr := json.Unmarshal(iter.Value(), &f); jsonErr == nil {
				found = &f
			}
			break
		}
	}
	iter.Close()

	if found == nil {
		return nil // already gone
	}

	batch := s.db.NewBatch()

	// Delete primary key.
	primaryKey := []byte(fmt.Sprintf("book_file:%s:%s", found.BookID, found.ID))
	if err := batch.Delete(primaryKey, nil); err != nil {
		batch.Close()
		return err
	}

	// Delete secondary indexes.
	if err := s.deleteBookFileSecondaryIndexes(batch, found); err != nil {
		batch.Close()
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "delete_book_file")
	s.DeleteBookFileFromMemDB(id)
	// Recompute book-level Duration/FileSize aggregates now that a file was removed.
	// Best-effort: the file delete already committed; don't fail on aggregate errors.
	s.notifyBookFileChange(found.BookID)
	return nil
}

// DeleteBookFilesForBook removes all BookFile records for a given bookID,
// including their secondary indexes.
func (s *PebbleStore) DeleteBookFilesForBook(bookID string) error {
	files, err := s.GetBookFiles(bookID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	batch := s.db.NewBatch()

	for i := range files {
		f := &files[i]
		primaryKey := []byte(fmt.Sprintf("book_file:%s:%s", f.BookID, f.ID))
		if err := batch.Delete(primaryKey, nil); err != nil {
			batch.Close()
			return err
		}
		if err := s.deleteBookFileSecondaryIndexes(batch, f); err != nil {
			batch.Close()
			return err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	// Recompute book-level aggregates after bulk-deleting all files for this book.
	// The book likely has Duration=0 after deletion, which is correct — nothing to sum.
	s.notifyBookFileChange(bookID)
	return nil
}

// UpsertBookFile creates or updates a BookFile. Lookup order:
//  1. If ITunesPersistentID is set, look up by PID.
//  2. Otherwise look up by FilePath.
//  3. If still not found, create a new record.
func (s *PebbleStore) UpsertBookFile(file *BookFile) error {
	var existing *BookFile
	var err error

	if file.ITunesPersistentID != "" {
		existing, err = s.GetBookFileByPID(file.ITunesPersistentID)
		if err != nil {
			return err
		}
	}

	if existing == nil && file.FilePath != "" {
		existing, err = s.GetBookFileByPath(file.FilePath)
		if err != nil {
			return err
		}
	}

	if existing == nil {
		return s.CreateBookFile(file)
	}

	// Preserve the existing ID and bookID; update in place.
	file.ID = existing.ID
	file.BookID = existing.BookID
	return s.UpdateBookFile(existing.ID, file)
}

// BatchUpsertBookFiles upserts a slice of BookFile records using a single
// PebbleDB batch for all writes. Each file is matched by iTunes persistent ID
// (if set) or by file path. This amortises the per-Commit overhead across
// all records in the slice.
func (s *PebbleStore) BatchUpsertBookFiles(files []*BookFile) error {
	if len(files) == 0 {
		return nil
	}

	batch := s.db.NewBatch()

	now := time.Now()
	for _, file := range files {
		if file == nil {
			continue
		}

		var existing *BookFile
		var lookupErr error

		if file.ITunesPersistentID != "" {
			existing, lookupErr = s.GetBookFileByPID(file.ITunesPersistentID)
		}
		if lookupErr != nil {
			batch.Close()
			return lookupErr
		}
		if existing == nil && file.FilePath != "" {
			existing, lookupErr = s.GetBookFileByPath(file.FilePath)
			if lookupErr != nil {
				batch.Close()
				return lookupErr
			}
		}

		if existing != nil {
			// Preserve identity fields; remove stale secondary indexes.
			file.ID = existing.ID
			file.BookID = existing.BookID
			file.CreatedAt = existing.CreatedAt
			file.UpdatedAt = now

			if err := s.deleteBookFileSecondaryIndexes(batch, existing); err != nil {
				batch.Close()
				return err
			}
		} else {
			if file.ID == "" {
				id, err := newULID()
				if err != nil {
					batch.Close()
					return err
				}
				file.ID = id
			}
			if file.CreatedAt.IsZero() {
				file.CreatedAt = now
			}
			file.UpdatedAt = now
		}

		// T020: drop AcoustIDSeg0..6 from the stored value via a copy.
		data, err := marshalBookFileDropSegs(file)
		if err != nil {
			batch.Close()
			return err
		}

		key := []byte(fmt.Sprintf("book_file:%s:%s", file.BookID, file.ID))
		if err := batch.Set(key, data, nil); err != nil {
			batch.Close()
			return err
		}

		if err := writeBookFileSecondaryIndexes(batch, file); err != nil {
			batch.Close()
			return err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "batch_upsert_book_files")
	return nil
}

// GetBookFileByID returns a single BookFile by bookID and fileID.
func (s *PebbleStore) GetBookFileByID(bookID, fileID string) (*BookFile, error) {
	return s.getBookFileByID(bookID, fileID)
}

// MoveBookFilesToBook reassigns BookFile records from sourceBookID to targetBookID.
func (s *PebbleStore) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	batch := s.db.NewBatch()

	for _, fid := range fileIDs {
		f, err := s.getBookFileByID(sourceBookID, fid)
		if err != nil {
			batch.Close()
			return fmt.Errorf("file not found: %s: %w", fid, err)
		}
		if f == nil {
			batch.Close()
			return fmt.Errorf("file not found: %s", fid)
		}

		// Delete old primary key
		oldKey := []byte(fmt.Sprintf("book_file:%s:%s", sourceBookID, fid))
		if err := batch.Delete(oldKey, nil); err != nil {
			batch.Close()
			return err
		}

		// Delete old secondary indexes
		if err := s.deleteBookFileSecondaryIndexes(batch, f); err != nil {
			batch.Close()
			return err
		}

		// Update book ID and write under new primary key
		f.BookID = targetBookID
		f.UpdatedAt = time.Now()

		data, err := json.Marshal(f)
		if err != nil {
			batch.Close()
			return err
		}
		newKey := []byte(fmt.Sprintf("book_file:%s:%s", targetBookID, fid))
		if err := batch.Set(newKey, data, nil); err != nil {
			batch.Close()
			return err
		}

		// Re-create secondary indexes with updated bookID
		if err := writeBookFileSecondaryIndexes(batch, f); err != nil {
			batch.Close()
			return err
		}
	}

	return batch.Commit(pebble.Sync)
}

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

// MergeChapterBooks moves all BookFiles from srcIDs onto primaryID, then
// updates the primary book's title and duration. Source books are marked
// with merged_into_book_id set to primaryID.
func (p *PebbleStore) MergeChapterBooks(primaryID string, srcIDs []string, newTitle string, duration float64) error {
	if len(srcIDs) == 0 {
		return nil
	}
	for _, srcID := range srcIDs {
		files, err := p.GetBookFiles(srcID)
		if err != nil {
			return fmt.Errorf("MergeChapterBooks: get files for %s: %w", srcID, err)
		}
		for i := range files {
			old := files[i]
			// Delete old primary + secondary indexes.
			batch := p.db.NewBatch()
			oldKey := []byte(fmt.Sprintf("book_file:%s:%s", old.BookID, old.ID))
			if err := batch.Delete(oldKey, nil); err != nil {
				batch.Close()
				return err
			}
			if err := p.deleteBookFileSecondaryIndexes(batch, &old); err != nil {
				batch.Close()
				return err
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				return fmt.Errorf("MergeChapterBooks: delete src file: %w", err)
			}
			// Re-parent the file to the primary book.
			old.BookID = primaryID
			if err := p.CreateBookFile(&old); err != nil {
				return fmt.Errorf("MergeChapterBooks: create merged file: %w", err)
			}
		}
		// Mark source book as merged.
		if err := p.FlagMetadataHashDuplicate(primaryID, srcID); err != nil {
			return fmt.Errorf("MergeChapterBooks: flag duplicate: %w", err)
		}
	}
	// Update primary book's title and duration.
	primary, err := p.GetBookByID(primaryID)
	if err != nil || primary == nil {
		return err
	}
	if newTitle != "" {
		primary.Title = newTitle
	}
	if duration > 0 {
		d := int(duration)
		primary.Duration = &d
	}
	_, err = p.UpdateBook(primaryID, primary)
	return err
}

// --- AIJobsStore (PebbleDB key-value implementation) ---
//
// Key scheme:
//   aijob:<id>           → JSON-encoded AIJob
//   aijob_payload:<id>   → raw payload bytes
//   aijob_batch:<bid>    → job ID (secondary index; batch_id → job_id)

// CreateAIJob stores a new AIJob row and its payload blob.
func (p *PebbleStore) CreateAIJob(job AIJob, payloadJSON []byte) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("CreateAIJob marshal: %w", err)
	}
	jobKey := []byte(fmt.Sprintf("aijob:%s", job.ID))
	if err := p.db.Set(jobKey, data, pebble.Sync); err != nil {
		return fmt.Errorf("CreateAIJob set job: %w", err)
	}
	payloadKey := []byte(fmt.Sprintf("aijob_payload:%s", job.ID))
	if err := p.db.Set(payloadKey, payloadJSON, pebble.Sync); err != nil {
		return fmt.Errorf("CreateAIJob set payload: %w", err)
	}
	return nil
}

// GetAIJob retrieves a job by its ID.
func (p *PebbleStore) GetAIJob(id string) (AIJob, error) {
	jobKey := []byte(fmt.Sprintf("aijob:%s", id))
	value, closer, err := p.db.Get(jobKey)
	if err == pebble.ErrNotFound {
		return AIJob{}, fmt.Errorf("ai job not found: %s", id)
	}
	if err != nil {
		return AIJob{}, err
	}
	defer closer.Close()
	var job AIJob
	if err := json.Unmarshal(value, &job); err != nil {
		return AIJob{}, err
	}
	return job, nil
}

// GetAIJobByBatchID retrieves a job using the OpenAI batch ID secondary index.
func (p *PebbleStore) GetAIJobByBatchID(batchID string) (AIJob, error) {
	idxKey := []byte(fmt.Sprintf("aijob_batch:%s", batchID))
	val, closer, err := p.db.Get(idxKey)
	if err == pebble.ErrNotFound {
		return AIJob{}, fmt.Errorf("ai job not found for batch: %s", batchID)
	}
	if err != nil {
		return AIJob{}, err
	}
	jobID := string(val)
	closer.Close()
	return p.GetAIJob(jobID)
}

// GetAIJobPayload returns the raw payload JSON stored alongside the job.
func (p *PebbleStore) GetAIJobPayload(id string) ([]byte, error) {
	payloadKey := []byte(fmt.Sprintf("aijob_payload:%s", id))
	value, closer, err := p.db.Get(payloadKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("ai job payload not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

// MarkAIJobSubmitted sets the job status to "submitted" and records the batch ID.
func (p *PebbleStore) MarkAIJobSubmitted(id, batchID string) error {
	job, err := p.GetAIJob(id)
	if err != nil {
		return err
	}
	job.Status = "submitted"
	job.BatchID = batchID
	job.SubmittedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if err := p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync); err != nil {
		return err
	}
	// Write secondary index: batch_id → job_id
	return p.db.Set([]byte(fmt.Sprintf("aijob_batch:%s", batchID)), []byte(id), pebble.Sync)
}

// MarkAIJobCompleted sets the job status to completed/completed_with_errors and
// records success/error counts and per-row error details.
func (p *PebbleStore) MarkAIJobCompleted(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error {
	job, err := p.GetAIJob(id)
	if err != nil {
		return err
	}
	job.Status = status
	job.SuccessCount = successCount
	job.ErrorCount = errorCount
	job.CompletedAt = time.Now().UTC()
	if len(rowErrors) > 0 {
		b, _ := json.Marshal(rowErrors)
		job.RowErrors = string(b)
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync)
}

// MarkAIJobFailed sets the job status to "failed" with an error message.
func (p *PebbleStore) MarkAIJobFailed(id, errMsg string) error {
	job, err := p.GetAIJob(id)
	if err != nil {
		return err
	}
	job.Status = "failed"
	job.ErrorMsg = errMsg
	job.CompletedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync)
}

// ListAIJobs returns jobs matching optional type/status filters, with
// limit/offset pagination. Results are ordered by CreatedAt descending.
func (p *PebbleStore) ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]AIJob, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("aijob:"),
		UpperBound: []byte("aijob:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []AIJob
	for iter.First(); iter.Valid(); iter.Next() {
		var job AIJob
		if err := json.Unmarshal(iter.Value(), &job); err != nil {
			continue
		}
		if typeFilter != "" && job.Type != typeFilter {
			continue
		}
		if statusFilter != "" && job.Status != statusFilter {
			continue
		}
		all = append(all, job)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if offset >= len(all) {
		return []AIJob{}, nil
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// KeyCount returns the total number of keys stored in the PebbleDB instance
// and the estimated on-disk byte size. Used by the DB health diagnostics endpoint.
func (p *PebbleStore) KeyCount() (count int64, sizeBytes uint64, err error) {
	iter, iterErr := p.db.NewIter(nil)
	if iterErr != nil {
		return 0, 0, fmt.Errorf("pebble key count iterator: %w", iterErr)
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	sizeBytes = p.db.Metrics().DiskSpaceUsage()
	return count, sizeBytes, nil
}

// UpdateBookFileHashes updates the original_file_hash and post_metadata_hash
// fields on a BookFile record stored in PebbleDB.
func (s *PebbleStore) UpdateBookFileHashes(id, originalHash, postMetadataHash string) error {
	// Find the file across all books via the secondary id index.
	val, closer, err := s.db.Get([]byte("book_file_id:" + id))
	if err != nil {
		return fmt.Errorf("UpdateBookFileHashes: lookup id index: %w", err)
	}
	bookFileKey := string(val)
	closer.Close()

	val2, closer2, err := s.db.Get([]byte(bookFileKey))
	if err != nil {
		return fmt.Errorf("UpdateBookFileHashes: get file: %w", err)
	}
	var bf BookFile
	if err := json.Unmarshal(val2, &bf); err != nil {
		closer2.Close()
		return fmt.Errorf("UpdateBookFileHashes: unmarshal: %w", err)
	}
	closer2.Close()

	if bf.OriginalFileHash == "" && originalHash != "" {
		bf.OriginalFileHash = originalHash
	}
	if postMetadataHash != "" {
		bf.PostMetadataHash = postMetadataHash
	}
	bf.UpdatedAt = time.Now()

	data, err := json.Marshal(&bf)
	if err != nil {
		return fmt.Errorf("UpdateBookFileHashes: marshal: %w", err)
	}
	return s.db.Set([]byte(bookFileKey), data, pebble.Sync)
}

// SetBookFileHash sets file_hash on a BookFile record in PebbleDB, and also
// sets original_file_hash if it is currently empty, matching scanner behaviour.
func (s *PebbleStore) SetBookFileHash(id, hash string) error {
	val, closer, err := s.db.Get([]byte("book_file_id:" + id))
	if err != nil {
		return fmt.Errorf("SetBookFileHash: lookup id index: %w", err)
	}
	bookFileKey := string(val)
	closer.Close()

	val2, closer2, err := s.db.Get([]byte(bookFileKey))
	if err != nil {
		return fmt.Errorf("SetBookFileHash: get file: %w", err)
	}
	var bf BookFile
	if err := json.Unmarshal(val2, &bf); err != nil {
		closer2.Close()
		return fmt.Errorf("SetBookFileHash: unmarshal: %w", err)
	}
	closer2.Close()

	bf.FileHash = hash
	if bf.OriginalFileHash == "" {
		bf.OriginalFileHash = hash
	}
	bf.UpdatedAt = time.Now()

	data, err := json.Marshal(&bf)
	if err != nil {
		return fmt.Errorf("SetBookFileHash: marshal: %w", err)
	}
	return s.db.Set([]byte(bookFileKey), data, pebble.Sync)
}

// AddMetadataRejection is not supported on PebbleStore.
// AddMetadataRejection is a no-op on PebbleStore (rejections not persisted).
// AddMetadataRejection persists a rejected metadata candidate under
// key metadata_rejection:<bookID>:<ULID>.
func (p *PebbleStore) AddMetadataRejection(r MetadataRejection) error {
	if r.ID == "" {
		id, err := newULID()
		if err != nil {
			return err
		}
		r.ID = id
	}
	if r.RejectedAt.IsZero() {
		r.RejectedAt = time.Now().UTC()
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("metadata_rejection:%s:%s", r.BookID, r.ID))
	return p.db.Set(key, data, pebble.Sync)
}

// GetMetadataRejections returns all rejection records for a book.
func (p *PebbleStore) GetMetadataRejections(bookID string) ([]MetadataRejection, error) {
	prefix := []byte(fmt.Sprintf("metadata_rejection:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []MetadataRejection
	for iter.First(); iter.Valid(); iter.Next() {
		var r MetadataRejection
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	if out == nil {
		out = []MetadataRejection{}
	}
	return out, nil
}

// DeleteMetadataRejections removes all rejection records for a book.
func (p *PebbleStore) DeleteMetadataRejections(bookID string) error {
	prefix := []byte(fmt.Sprintf("metadata_rejection:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(append([]byte(nil), prefix...), 0xFF),
	})
	if err != nil {
		return err
	}
	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	iter.Close()
	for _, k := range keys {
		if err := p.db.Delete(k, pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}

// GetDuplicateFilesByHash returns groups of BookFiles sharing the same
// OriginalFileHash. Groups are ordered by file count descending. limit=0
// defaults to 50.
func (p *PebbleStore) GetDuplicateFilesByHash(limit int) ([]DuplicateFileGroup, error) {
	if limit <= 0 {
		limit = 50
	}
	files, err := p.GetAllBookFiles()
	if err != nil {
		return nil, fmt.Errorf("GetDuplicateFilesByHash: %w", err)
	}

	// Build book title map for annotating results.
	books, _ := p.GetAllBooks(0, 0)
	bookTitles := make(map[string]string, len(books))
	bookPaths := make(map[string]string, len(books))
	for _, b := range books {
		bookTitles[b.ID] = b.Title
		bookPaths[b.ID] = b.FilePath
	}

	type group struct {
		files []BookFile
	}
	byHash := make(map[string]*group)
	for i := range files {
		h := files[i].OriginalFileHash
		if h == "" {
			continue
		}
		if byHash[h] == nil {
			byHash[h] = &group{}
		}
		byHash[h].files = append(byHash[h].files, files[i])
	}

	var result []DuplicateFileGroup
	for hash, g := range byHash {
		if len(g.files) < 2 {
			continue
		}
		bookIDs := make(map[string]struct{})
		var infos []DuplicateFileInfo
		var totalSize int64
		for _, f := range g.files {
			bookIDs[f.BookID] = struct{}{}
			totalSize += f.FileSize
			infos = append(infos, DuplicateFileInfo{
				BookFileID: f.ID,
				BookID:     f.BookID,
				BookTitle:  bookTitles[f.BookID],
				FilePath:   f.FilePath,
				BookPath:   bookPaths[f.BookID],
				FileSize:   f.FileSize,
			})
		}
		result = append(result, DuplicateFileGroup{
			Hash:      hash,
			FileCount: len(g.files),
			BookCount: len(bookIDs),
			TotalSize: totalSize,
			Files:     infos,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].FileCount > result[j].FileCount
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// GetBookFileHashStats scans all book_file primary records in memory and
// returns aggregate hash-coverage statistics, including a per-library breakdown
// derived from each file's source_import_path on its parent book.
func (p *PebbleStore) GetBookFileHashStats() (*BookFileHashStats, error) {
	files, err := p.GetAllBookFiles()
	if err != nil {
		return nil, fmt.Errorf("GetBookFileHashStats: %w", err)
	}

	stats := &BookFileHashStats{TotalBookFiles: len(files)}
	for _, f := range files {
		if f.FileHash != "" {
			stats.WithFileHash++
		}
		if f.OriginalFileHash != "" {
			stats.WithOriginalHash++
		}
	}
	stats.MissingFileHash = stats.TotalBookFiles - stats.WithFileHash

	// Gather per-library stats by grouping files under their parent book's source_import_path.
	// Build a bookID → source_import_path map first.
	allBooks, berr := p.GetAllBooks(0, 0)
	if berr == nil {
		bookPaths := make(map[string]string, len(allBooks))
		stats.TotalBooks = len(allBooks)
		for _, b := range allBooks {
			if b.SourceImportPath != nil && *b.SourceImportPath != "" {
				bookPaths[b.ID] = *b.SourceImportPath
			} else {
				bookPaths[b.ID] = ""
			}
		}
		// Check books without any files
		bookHasFile := make(map[string]bool, len(allBooks))
		for _, f := range files {
			bookHasFile[f.BookID] = true
		}
		for _, b := range allBooks {
			if !bookHasFile[b.ID] {
				stats.BooksWithNoFiles++
			}
		}

		libMap := make(map[string]*BookFileHashStatsByLib)
		for _, f := range files {
			lib := bookPaths[f.BookID]
			if lib == "" {
				continue
			}
			if _, ok := libMap[lib]; !ok {
				libMap[lib] = &BookFileHashStatsByLib{Path: lib}
			}
			libMap[lib].TotalFiles++
			if f.FileHash != "" {
				libMap[lib].WithHash++
			}
		}
		for _, row := range libMap {
			row.MissingHash = row.TotalFiles - row.WithHash
			stats.ByLibrary = append(stats.ByLibrary, *row)
		}
	}
	return stats, nil
}

// GetBookMetadataHashStats returns metadata_source_hash coverage across all books.
func (p *PebbleStore) GetBookMetadataHashStats() (*BookMetadataHashStats, error) {
	allBooks, err := p.GetAllBooks(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetBookMetadataHashStats: %w", err)
	}

	stats := &BookMetadataHashStats{TotalBooks: len(allBooks)}
	libMap := make(map[string]*BookMetadataHashStatsByLib)

	for _, b := range allBooks {
		hasHash := b.MetadataSourceHash != nil && *b.MetadataSourceHash != ""
		hasID := (b.ASIN != nil && *b.ASIN != "") ||
			(b.ISBN13 != nil && *b.ISBN13 != "") ||
			(b.ISBN10 != nil && *b.ISBN10 != "")

		if hasHash {
			stats.WithMetadataHash++
		} else {
			stats.MissingMetadataHash++
		}
		if hasID {
			stats.WithASINOrISBN++
			if !hasHash {
				stats.MissingHashHasID++
			}
		}

		lib := ""
		if b.SourceImportPath != nil {
			lib = *b.SourceImportPath
		}
		if lib == "" {
			continue
		}
		if _, ok := libMap[lib]; !ok {
			libMap[lib] = &BookMetadataHashStatsByLib{Path: lib}
		}
		libMap[lib].TotalBooks++
		if hasHash {
			libMap[lib].WithMetadataHash++
		} else {
			libMap[lib].MissingMetadataHash++
			if hasID {
				libMap[lib].MissingHashHasID++
			}
		}
	}

	for _, row := range libMap {
		stats.ByLibrary = append(stats.ByLibrary, *row)
	}
	return stats, nil
}

// GetAcoustIDStats returns fingerprint coverage across all book files, grouped by
// library root (the parent book's source_import_path).
//
// Uses getAllBookFilesPebbleScan (Pebble-direct) rather than GetAllBookFiles
// so that the hasFP check reads the full AcoustIDSeg0..6 fields from storage.
// After fable5 T019 those fields are stripped from memdb projections; reading
// them from memdb would return all-zeros and undercount fingerprinted files.
func (p *PebbleStore) GetAcoustIDStats() (*AcoustIDStats, error) {
	files, err := p.getAllBookFilesPebbleScan()
	if err != nil {
		return nil, fmt.Errorf("GetAcoustIDStats: %w", err)
	}

	// Build bookID → source_import_path for library grouping.
	allBooks, _ := p.GetAllBooks(0, 0)
	bookLib := make(map[string]string, len(allBooks))
	for _, b := range allBooks {
		if b.SourceImportPath != nil && *b.SourceImportPath != "" {
			bookLib[b.ID] = *b.SourceImportPath
		}
	}

	byLib := make(map[string]*AcoustIDStatsByLibrary)
	stats := &AcoustIDStats{}

	for _, f := range files {
		stats.TotalFiles++
		// T020: segs are no longer stored; check whole-file fp first (preferred),
		// then fall back to legacy seg fields for rows not yet swept by T020.
		hasFP := len(f.AcoustIDFingerprint) > 0 ||
			f.AcoustIDSeg0 != "" || f.AcoustIDSeg1 != "" || f.AcoustIDSeg2 != "" ||
			f.AcoustIDSeg3 != "" || f.AcoustIDSeg4 != "" || f.AcoustIDSeg5 != "" || f.AcoustIDSeg6 != ""
		if hasFP {
			stats.WithFingerprint++
		}

		root := bookLib[f.BookID]
		if root == "" {
			root = "(unknown)"
		}
		lib := byLib[root]
		if lib == nil {
			lib = &AcoustIDStatsByLibrary{LibraryRoot: root}
			byLib[root] = lib
		}
		lib.TotalFiles++
		if hasFP {
			lib.WithFingerprint++
		}
	}

	for _, v := range byLib {
		stats.ByLibrary = append(stats.ByLibrary, *v)
	}
	sort.Slice(stats.ByLibrary, func(i, j int) bool {
		return stats.ByLibrary[i].LibraryRoot < stats.ByLibrary[j].LibraryRoot
	})
	return stats, nil
}

// GetFilesWithFingerprintFailures scans all book_files and returns those where
// FingerprintFailedAt is set, optionally filtered to a specific reason string.
func (p *PebbleStore) GetFilesWithFingerprintFailures(reason string, limit, offset int) ([]BookFile, int64, error) {
	// Bypass memdb deliberately: memdb-resident BookFiles have their
	// fingerprint-diagnostic fields stripped (see stripBookFileForMemdb),
	// so the Failed/Reason/Detail/Diagnostic columns this endpoint
	// surfaces are only available from Pebble.
	allFiles, err := p.getAllBookFilesPebbleScan()
	if err != nil {
		return nil, 0, fmt.Errorf("GetFilesWithFingerprintFailures: %w", err)
	}
	var matched []BookFile
	for _, f := range allFiles {
		if f.FingerprintFailedAt == nil {
			continue
		}
		if reason != "" && (f.FingerprintFailureReason == nil || *f.FingerprintFailureReason != reason) {
			continue
		}
		matched = append(matched, f)
	}
	total := int64(len(matched))
	if offset >= len(matched) {
		return nil, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total, nil
}

// GetAuthorsByBookIDs returns a map from bookID → []Author for all given book IDs.
func (p *PebbleStore) GetAuthorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Author, error) {
	if len(bookIDs) == 0 {
		return map[string][]Author{}, nil
	}
	result := make(map[string][]Author, len(bookIDs))
	for _, bookID := range bookIDs {
		bookAuthors, err := p.GetBookAuthors(bookID)
		if err != nil {
			return nil, fmt.Errorf("GetAuthorsByBookIDs %s: %w", bookID, err)
		}
		var authors []Author
		for _, ba := range bookAuthors {
			author, err := p.GetAuthorByID(ba.AuthorID)
			if err != nil {
				return nil, fmt.Errorf("GetAuthorsByBookIDs author lookup %d: %w", ba.AuthorID, err)
			}
			if author != nil {
				authors = append(authors, *author)
			}
		}
		result[bookID] = authors
	}
	return result, nil
}

// GetNarratorsByBookIDs returns a map from bookID → []Narrator for all given book IDs.
func (p *PebbleStore) GetNarratorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Narrator, error) {
	if len(bookIDs) == 0 {
		return map[string][]Narrator{}, nil
	}
	result := make(map[string][]Narrator, len(bookIDs))
	for _, bookID := range bookIDs {
		bookNarrators, err := p.GetBookNarrators(bookID)
		if err != nil {
			return nil, fmt.Errorf("GetNarratorsByBookIDs %s: %w", bookID, err)
		}
		var narrators []Narrator
		for _, bn := range bookNarrators {
			narrator, err := p.GetNarratorByID(bn.NarratorID)
			if err != nil {
				return nil, fmt.Errorf("GetNarratorsByBookIDs narrator lookup %d: %w", bn.NarratorID, err)
			}
			if narrator != nil {
				narrators = append(narrators, *narrator)
			}
		}
		result[bookID] = narrators
	}
	return result, nil
}

// ClearAllAcoustIDFingerprints wipes AcoustIDSeg0..6 on every BookFile and
// drops the matching book_file_acoustid:<seg> secondary index entries in
// batched Pebble commits — one fsync per batchSize records instead of one
// per UpdateBookFile call. The progress callback is invoked roughly every
// 5000 records with (processed, cleared, total).
//
// Returns (cleared, total, err). cleared counts only files that actually had
// at least one non-empty segment.
func (s *PebbleStore) ClearAllAcoustIDFingerprints(ctx context.Context, batchSize int, progress func(processed, cleared, total int)) (int, int, error) {
	if batchSize <= 0 {
		batchSize = 2000
	}

	// First pass: collect primary keys so we don't hold an iterator open while
	// we mutate Pebble underneath it. 308K keys at ~50 bytes each ≈ 15MB — fine.
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return 0, 0, err
	}
	type ref struct {
		key  []byte
		data []byte
	}
	var refs []ref
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if !strings.HasPrefix(string(key), "book_file:") {
			continue
		}
		// Skip secondary indexes — only primary book_file:<bookID>:<fileID>.
		if c := strings.Count(string(key), ":"); c != 2 {
			continue
		}
		kc := make([]byte, len(key))
		copy(kc, key)
		vc := make([]byte, len(iter.Value()))
		copy(vc, iter.Value())
		refs = append(refs, ref{key: kc, data: vc})
	}
	if err := iter.Close(); err != nil {
		return 0, 0, err
	}

	total := len(refs)
	cleared := 0
	processed := 0
	batch := s.db.NewBatch()
	flush := func() error {
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		batch = s.db.NewBatch()
		return nil
	}

	// Pre-check needles: a row whose JSON contains none of these is
	// guaranteed to have no fingerprint data and can be skipped without
	// the (now-expensive) json.Unmarshal/Marshal round-trip. The
	// AcoustIDFingerprint []byte field can be 230 KB–1 MB of base64 per
	// row, so unmarshaling all 308 K rows just to check emptiness costs
	// ~10 minutes at ~33 rows/sec. With these needles the skip path is
	// ~5000 rows/sec and the slow path runs only for rows that actually
	// need clearing.
	//
	// Why two needles per seg: omitempty-tagged fields can either be
	// absent (no key) or present-but-empty (`"":""`). Either way the
	// fingerprint is empty. Same logic for the raw fp.
	var (
		fpNeedle   = []byte(`"acoustid_fingerprint":"`)
		seg0Needle = []byte(`"acoustid_seg0":"`)
		seg1Needle = []byte(`"acoustid_seg1":"`)
		seg2Needle = []byte(`"acoustid_seg2":"`)
		seg3Needle = []byte(`"acoustid_seg3":"`)
		seg4Needle = []byte(`"acoustid_seg4":"`)
		seg5Needle = []byte(`"acoustid_seg5":"`)
		seg6Needle = []byte(`"acoustid_seg6":"`)
		emptyStr   = []byte(`""`)
	)
	// hasNonEmpty returns true if the needle appears and is not
	// immediately followed by `""` (i.e. the field exists with a value).
	hasNonEmpty := func(data, needle []byte) bool {
		idx := bytes.Index(data, needle)
		if idx < 0 {
			return false
		}
		after := idx + len(needle)
		// needle ends with `:"` — the byte after that is the first char
		// of the value. If it's a closing quote, the value is empty.
		if after-1 >= 0 && after-1 < len(data) && data[after-1] == '"' {
			// Check immediately past the needle: if it's the closing
			// quote of an empty string we have `"":""` — empty value.
			if after < len(data) && data[after] == '"' {
				return false
			}
		}
		_ = emptyStr // referenced for documentation; bytes.Equal not needed
		return true
	}

	for _, r := range refs {
		select {
		case <-ctx.Done():
			_ = batch.Close()
			return cleared, total, ctx.Err()
		default:
		}

		processed++

		// Fast skip: raw bytes don't carry any of the fp/seg fields with
		// a non-empty value. Saves the json.Unmarshal cost — which has
		// to decode AcoustIDFingerprint's multi-KB base64 even when we
		// only care about it being empty.
		if !hasNonEmpty(r.data, fpNeedle) &&
			!hasNonEmpty(r.data, seg0Needle) &&
			!hasNonEmpty(r.data, seg1Needle) &&
			!hasNonEmpty(r.data, seg2Needle) &&
			!hasNonEmpty(r.data, seg3Needle) &&
			!hasNonEmpty(r.data, seg4Needle) &&
			!hasNonEmpty(r.data, seg5Needle) &&
			!hasNonEmpty(r.data, seg6Needle) {
			if progress != nil && processed%5000 == 0 {
				progress(processed, cleared, total)
			}
			continue
		}

		var f BookFile
		if err := json.Unmarshal(r.data, &f); err != nil {
			continue
		}

		if f.AcoustIDSeg0 == "" && f.AcoustIDSeg1 == "" && f.AcoustIDSeg2 == "" &&
			f.AcoustIDSeg3 == "" && f.AcoustIDSeg4 == "" && f.AcoustIDSeg5 == "" &&
			f.AcoustIDSeg6 == "" && len(f.AcoustIDFingerprint) == 0 {
			if progress != nil && processed%5000 == 0 {
				progress(processed, cleared, total)
			}
			continue
		}

		// Delete each acoustid secondary index entry.
		for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2,
			f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
			if seg == "" {
				continue
			}
			if err := batch.Delete([]byte("book_file_acoustid:"+seg), nil); err != nil {
				_ = batch.Close()
				return cleared, total, err
			}
		}

		f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2 = "", "", ""
		f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6 = "", "", "", ""
		// Whole-file fp + duration are part of "fingerprint state" — clearing
		// segs without clearing these would leave reset-all in a broken
		// half-cleared state and the LSH index orphaned.
		f.AcoustIDFingerprint = nil
		f.AcoustIDFingerprintDurationSec = 0
		f.UpdatedAt = time.Now()

		data, err := json.Marshal(&f)
		if err != nil {
			continue
		}
		if err := batch.Set(r.key, data, nil); err != nil {
			_ = batch.Close()
			return cleared, total, err
		}
		cleared++

		// Keep memdb in lock-step with Pebble so subsequent in-RAM lookups
		// don't return stale seg values.
		if s.UseMemDB {
			s.UpsertBookFileToMemDB(&f)
		}

		if batch.Len() >= batchSize {
			if err := flush(); err != nil {
				return cleared, total, err
			}
			if progress != nil {
				progress(processed, cleared, total)
			}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return cleared, total, err
	}

	// Wipe the LSH index in one prefix-delete pass — every per-row meta
	// entry is gone anyway since we just cleared every fingerprint, so we
	// don't need to look up individual subprints. RangeDelete is much
	// cheaper than 308K × 64 individual Deletes.
	wipeBatch := s.db.NewBatch()
	if err := wipeBatch.DeleteRange([]byte(lshKeyPrefix), []byte("fpidx;"), nil); err != nil {
		_ = wipeBatch.Close()
		return cleared, total, err
	}
	if err := wipeBatch.DeleteRange([]byte(lshMetaKeyPrefix), []byte("fpidx_meta;"), nil); err != nil {
		_ = wipeBatch.Close()
		return cleared, total, err
	}
	if err := wipeBatch.Commit(pebble.Sync); err != nil {
		return cleared, total, err
	}

	if progress != nil {
		progress(processed, cleared, total)
	}

	s.InvalidateLibraryStats()
	s.MarkQuickQueryDirty("no_fingerprints", "clear_all_acoustid_fingerprints")
	return cleared, total, nil
}

// SweepBookFileSegDropResult holds the outcome of a SweepBookFileSegDrop run.
type SweepBookFileSegDropResult struct {
	Total   int // total primary book_file: rows examined
	Rewrite int // rows rewritten (had at least one non-empty seg)
	Skipped int // rows skipped (already clean)
	Errors  int // rows skipped due to unmarshal/marshal errors (logged)
}

// SweepBookFileSegDrop iterates all primary book_file: rows in Pebble and
// rewrites any row that still carries AcoustIDSeg0..6 values, removing those
// fields from the stored JSON and deleting the corresponding
// book_file_acoustid: secondary index entries.
//
// T020 background sweep. Design notes:
//   - Reads raw Pebble bytes to avoid the memdb route (which has already
//     stripped segs in memory, T019).  This is the same pattern used by
//     ClearAllAcoustIDFingerprints (line ~10616 in this file).
//   - Uses byte-needle fast-skip to avoid json.Unmarshal on already-clean rows.
//   - If dryRun is true the method scans and counts but writes nothing.
//   - Resumable: re-running after a partial apply produces the same result —
//     rows already rewritten have no seg needles and are fast-skipped.
//   - batchSize controls how many rewrites are committed per Pebble sync
//     (0 → default 1000).
//   - progress is called roughly every batchSize rewrites with (rewrite, total).
func (s *PebbleStore) SweepBookFileSegDrop(
	ctx context.Context,
	dryRun bool,
	batchSize int,
	progress func(rewrite, total int),
) (SweepBookFileSegDropResult, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	var result SweepBookFileSegDropResult

	// Pass 1: collect primary keys + raw values so we don't hold an iterator
	// open while mutating Pebble.
	prefix := []byte("book_file:")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return result, err
	}
	type ref struct {
		key  []byte
		data []byte
	}
	var refs []ref
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if !strings.HasPrefix(string(key), "book_file:") {
			continue
		}
		// Primary keys are book_file:<bookID>:<fileID> — exactly 2 colons.
		if strings.Count(string(key), ":") != 2 {
			continue
		}
		kc := make([]byte, len(key))
		copy(kc, key)
		vc := make([]byte, len(iter.Value()))
		copy(vc, iter.Value())
		refs = append(refs, ref{key: kc, data: vc})
	}
	if err := iter.Close(); err != nil {
		return result, err
	}
	result.Total = len(refs)

	// seg needles (omitempty — field is absent when empty, so any occurrence
	// means the value is non-empty).
	seg0N := []byte(`"acoustid_seg0":"`)
	seg1N := []byte(`"acoustid_seg1":"`)
	seg2N := []byte(`"acoustid_seg2":"`)
	seg3N := []byte(`"acoustid_seg3":"`)
	seg4N := []byte(`"acoustid_seg4":"`)
	seg5N := []byte(`"acoustid_seg5":"`)
	seg6N := []byte(`"acoustid_seg6":"`)

	hasSegNeedle := func(data []byte) bool {
		return bytes.Contains(data, seg0N) ||
			bytes.Contains(data, seg1N) ||
			bytes.Contains(data, seg2N) ||
			bytes.Contains(data, seg3N) ||
			bytes.Contains(data, seg4N) ||
			bytes.Contains(data, seg5N) ||
			bytes.Contains(data, seg6N)
	}

	if dryRun {
		for _, r := range refs {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			default:
			}
			if !hasSegNeedle(r.data) {
				result.Skipped++
				continue
			}
			result.Rewrite++
		}
		return result, nil
	}

	// Pass 2: rewrite rows that carry seg needles.
	batch := s.db.NewBatch()
	rewrittenSinceFlush := 0
	flush := func() error {
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		batch = s.db.NewBatch()
		rewrittenSinceFlush = 0
		return nil
	}

	for _, r := range refs {
		select {
		case <-ctx.Done():
			_ = batch.Close()
			return result, ctx.Err()
		default:
		}

		if !hasSegNeedle(r.data) {
			result.Skipped++
			continue
		}

		var f BookFile
		if err := json.Unmarshal(r.data, &f); err != nil {
			slog.Warn("SweepBookFileSegDrop: unmarshal error; skipping row",
				"key", string(r.key), "error", err)
			result.Errors++
			continue
		}

		// Delete book_file_acoustid: secondary index entries for the segs
		// that are present on this row (they'll be absent after the rewrite).
		for _, seg := range [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2,
			f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6} {
			if seg == "" {
				continue
			}
			if delErr := batch.Delete([]byte("book_file_acoustid:"+seg), nil); delErr != nil {
				_ = batch.Close()
				return result, delErr
			}
		}

		// Rewrite the row without segs.
		newData, marshalErr := marshalBookFileDropSegs(&f)
		if marshalErr != nil {
			slog.Warn("SweepBookFileSegDrop: marshal error; skipping row",
				"key", string(r.key), "error", marshalErr)
			result.Errors++
			continue
		}
		if setErr := batch.Set(r.key, newData, nil); setErr != nil {
			_ = batch.Close()
			return result, setErr
		}
		result.Rewrite++
		rewrittenSinceFlush++

		if rewrittenSinceFlush >= batchSize {
			if err := flush(); err != nil {
				return result, err
			}
			if progress != nil {
				progress(result.Rewrite, result.Total)
			}
		}
	}

	// Commit any remaining work.
	if err := batch.Commit(pebble.Sync); err != nil {
		return result, err
	}

	if progress != nil {
		progress(result.Rewrite, result.Total)
	}
	return result, nil
}
