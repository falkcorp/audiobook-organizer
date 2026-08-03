// file: internal/database/pebble_store.go
// version: 1.119.0
// guid: 0c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-07-30

package database

import (
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
	"github.com/falkcorp/audiobook-organizer/internal/matcher"
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

// serializeBookForIndex marshals a Book to JSON for index storage in the
// book:work:<wid>:<id> and book:versiongroup:<vg>:<id> rows.
//
// INDEX-CONSISTENCY: the embedded snapshot is a write-time convenience only. As
// of the point-verify-on-read fix, GetBooksByWorkID and GetBooksByVersionGroup
// no longer trust this value — they extract the book ID from the row KEY and
// point-look-up the authoritative book:<id> row — so a snapshot that goes stale
// (e.g. UpdateBook flipping MarkedForDeletion without changing WorkID) can no
// longer surface a phantom live book. The value is retained for backward
// compatibility with existing rows and any future value-consuming reader.
// GetBooksBySeriesIDCore / GetBooksByAuthorIDCore do NOT use this index at all
// (they full-scan the authoritative book:<id> rows post "Task 3.4 index
// removal").
func serializeBookForIndex(book *Book) ([]byte, error) {
	return json.Marshal(book)
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
//       GetBooksBySeriesIDCore and GetBooksByAuthorIDCore fall back to a full Pebble scan
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
	reviewMu                 sync.Mutex // serializes review-item upserts so concurrent same-DedupKey writes can't duplicate rows (review_store.go)
	opsLogSeq                int64      // monotonic counter for log key uniqueness; accessed via atomic
	rootDir                  string     // organized library root; set via SetRootDir after config load
	libraryCountsRecomputeMu sync.Mutex // gates recompute to prevent stampede when N callers see dirty cache
	UseMemDB                 bool       // feature flag: use in-memory query layer for aggregations / filtered reads

	// Primary-book-count cache. CountPrimaryBooks full-scans every book: key and
	// json.Unmarshal's each Book (~5.6s on a ~44K-book library). The 5s metrics
	// ticker (server_lifecycle.go) called it on every tick, saturating ~2 cores
	// on an otherwise-idle server. These fields cache the count for a short TTL so
	// the expensive scan runs at most once per primaryCountCacheTTL. primaryCountMu
	// guards the value fields; primaryCountRecomputeMu gates the recompute so a
	// burst of callers collapses to a single scan per window.
	primaryCountMu          sync.Mutex
	primaryCountRecomputeMu sync.Mutex
	primaryCount            int
	primaryCountComputedAt  time.Time
	primaryCountValid       bool

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

// WaitForWarmup blocks until the async memdb warmup goroutine has finished —
// memdb is published (success) or the store has fallen back to Pebble reads
// (failure). Either way, AFTER this returns the memdb-published state is stable,
// so subsequent write-throughs (UpsertAuthorToMemDB etc.) land in the published
// memdb and GetAll* reads are deterministic.
//
// Tests MUST call this right after NewPebbleStore. Without it there is a race: a
// write that lands while warmup is still snapshotting Pebble has its memdb
// write-through dropped (memSync no-ops while mem()==nil), then warmup publishes a
// memdb missing those rows — surfacing as the order-dependent "expected 3, got 2"
// GetAll* flakes under the full -race suite. Production never needs this (warmup
// is a one-time startup affair; reads fall back to Pebble until it publishes).
func (p *PebbleStore) WaitForWarmup() {
	if p.warmupDone != nil {
		<-p.warmupDone
	}
}

const statsLibraryKey = "stats:library"
const statsLibraryTTL = 10 * time.Minute
const defaultLibraryCountsMinIntervalSeconds = 600 // 10 minutes

// primaryCountCacheTTL bounds how stale the cached CountPrimaryBooks value may
// be. A metrics gauge / health probe tolerating tens of seconds of staleness is
// fine; this caps the 5.6s full-scan to at most once per window so the 5s status
// ticker stops re-scanning the whole library on every tick.
const primaryCountCacheTTL = 30 * time.Second

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

// Checkpoint writes a consistent point-in-time snapshot of the PebbleDB to
// destDir using Pebble's built-in checkpoint mechanism. The snapshot is safe
// to archive without the risk of torn writes that a live filepath.Walk incurs.
// destDir must not exist; Pebble creates it.
func (p *PebbleStore) Checkpoint(destDir string) error {
	return p.db.Checkpoint(destDir)
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

// Author Alias operations
//
// Key schema:
//   author_alias:<id>                              → AuthorAlias JSON
//   author_alias:author:<author_id>:<alias_id>     → alias_id (iterate by author)
//   author_alias:name:<lowercase_alias_name>       → alias_id (lookup by name)

// Series operations

// ---- Work operations (logical title-level grouping) ----

// Book operations

// GetAllBooksCore is Core-typed (STOREFID W5a/W5z): the return type is
// BookCore, not Book, so the nine heavy fields (Description, VersionNotes,
// BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt,
// BookSigCoveragePct, Author, Series) being absent is compiler-enforced
// rather than silently nil'd. A caller that needs any of the heavy fields
// MUST fetch via GetBookByID / GetAllBooksFullFrom (full Pebble). See
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (p *PebbleStore) GetAllBooksCore(limit, offset int) ([]BookCore, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllBooksCore(limit, offset, nil)
	}
	var books []BookCore
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
		books = append(books, book.Core())
		count++
	}

	return books, nil
}

// GetAllBooksFullFrom returns up to limit non-deleted books in PebbleDB key order
// after "book:<afterID>". Pass afterID="" to start from the beginning. This
// is O(1) seek vs GetAllBooks's O(offset) linear scan — use for cursor-based
// full-table iteration (e.g. search index backfill).
func (p *PebbleStore) GetAllBooksFullFrom(afterID string, limit int) ([]Book, error) {
	if p.UseMemDB && p.mem() != nil {
		// MemDB path. NOTE: this IS the production path — UseMemDB defaults to
		// true. The previous implementation loaded only limit*2+1 books from the
		// start and searched for afterID within that window, so cursor pagination
		// silently stopped at the 2*limit boundary (e.g. page 3 of a 200-page
		// scan returned nothing). Every full-table backfill that relied on this
		// (intro transcription, search-index backfill) only ever processed the
		// first ~2 pages of the library. See fix/transcribe-full-library.
		//
		// ListBookIDs walks the same memdb ID index as GetAllBooks and applies
		// the same MarkedForDeletion filter, so the ID ordering is authoritative.
		// Seek past afterID, then load the next `limit` books straight from
		// Pebble (GetBookByID bypasses memdb but returns identical data).
		ids, err := p.mem().ListBookIDs()
		if err != nil {
			return nil, err
		}
		start := 0
		if afterID != "" {
			// Default to len(ids): an unknown/stale cursor ends iteration
			// rather than restarting from the top (which would loop forever).
			start = len(ids)
			for i, id := range ids {
				if id == afterID {
					start = i + 1
					break
				}
			}
		}
		if start >= len(ids) {
			return nil, nil
		}
		end := len(ids)
		if limit > 0 && start+limit < end {
			end = start + limit
		}
		books := make([]Book, 0, end-start)
		for _, id := range ids[start:end] {
			b, err := p.GetBookByID(id)
			if err != nil || b == nil {
				continue
			}
			books = append(books, *b)
		}
		return books, nil
	}

	lower := []byte("book:0")
	if afterID != "" {
		// "\x01" is below any UUID character (0-9, a-f, '-') so this positions
		// the iterator at the first key strictly after "book:<afterID>".
		lower = append(append([]byte("book:"), afterID...), '\x01')
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: []byte("book:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var books []Book
	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		// The "book:0".."book:~" range holds record keys ("book:<id>") AND every
		// secondary index that shares the prefix: book:asin:, book:author:,
		// book:hash:, book:isbn13:, book:organizedhash:, book:originalhash:,
		// book:path:, book:series:, book:versiongroup:, book:work:. Only ":path:"
		// used to be skipped, so the others were unmarshalled as Books. Several
		// index families store an empty value (the data lives in the key), and
		// json.Unmarshal of zero bytes returns "unexpected end of JSON input",
		// which aborted the entire scan at the first book:asin: key.
		//
		// Record IDs (ULID/UUID) never contain ':', so a colon in the remainder
		// is what distinguishes an index key from a record. Match on that rather
		// than maintaining a list of index prefixes that new indexes can outgrow.
		rest, ok := strings.CutPrefix(key, "book:")
		if !ok || strings.Contains(rest, ":") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			return nil, err
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		books = append(books, book)
		count++
		if limit > 0 && count >= limit {
			break
		}
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
	return p.getAllBookSummariesFull(limit, offset)
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
	summaries, err := p.getAllBookSummariesFull(0, 0)
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
		if f.ExcludeQuarantined && s.QuarantinedAt != nil {
			continue
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

// getAllBookSummariesFull is the Pebble-backed implementation.
// It fetches all books via full iteration, then projects each Book into a BookSummary,
// skipping books marked for deletion.
func (p *PebbleStore) getAllBookSummariesFull(limit, offset int) ([]BookSummary, error) {
	if limit <= 0 {
		limit = 1_000_000
	}
	if offset < 0 {
		offset = 0
	}
	books, err := p.GetAllBooksCore(limit, offset)
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
			NarratorsJSON:        b.NarratorsJSON,
			TranscribedTitle:     b.TranscribedTitle,
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

// GetBooksByIDs returns the full Book rows for ids, preserving input order
// and silently skipping IDs that do not resolve (mirrors GetBookByID's
// nil-on-not-found). It reuses GetBookByID's exact read pattern per item —
// the same book:<id> point-get + json.Unmarshal — so it is full-fidelity,
// never a memdb-slim projection; heavy fields (AcoustIDFingerprint etc.)
// survive.
//
// Concurrency: this is a plain sequential loop, not a worker pool. Per
// CLAUDE.md's concurrency rule, that's correct here because callers bound
// ids to a single request page (searchWithBleve caps at
// searchPostFilterWindow = 10000 hits), not whole-library scale, and each
// item is a cheap local Pebble point-get.
//
// Error semantics (spec §C3, verbatim contract): a per-item not-found is
// skipped silently, matching GetBookByID. On the FIRST non-not-found
// read/unmarshal error, the loop stops and returns the rows read so far
// ALONGSIDE the error (not a bare nil, err) so the caller can still serve a
// partial page instead of failing the whole request.
func (p *PebbleStore) GetBooksByIDs(ids []string) ([]Book, error) {
	books := make([]Book, 0, len(ids))
	for _, id := range ids {
		key := []byte(fmt.Sprintf("book:%s", id))
		value, closer, err := p.db.Get(key)
		if err == pebble.ErrNotFound {
			continue
		}
		if err != nil {
			return books, fmt.Errorf("get book %q: %w", id, err)
		}

		var book Book
		unmarshalErr := json.Unmarshal(value, &book)
		closer.Close()
		if unmarshalErr != nil {
			return books, fmt.Errorf("unmarshal book %q: %w", id, unmarshalErr)
		}
		books = append(books, book)
	}
	return books, nil
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
		if util.NormalizeTitle(book.Title) != util.NormalizeTitle(normalizedTitle) {
			continue
		}
		if filepath.Dir(book.FilePath) != dirPath {
			continue
		}
		results = append(results, book)
	}
	return results, nil
}

// GetFolderDuplicatesCore is Core-typed (STOREFID W6) — see the interface
// doc comment.
//
// Groups books for dedup tier 2 ("same title in same folder, e.g. M4B +
// MP3"): buckets non-deleted, primary-version books by
// (util.NormalizeTitle(title), single-parent-dir) and emits every bucket
// with >=2 members as one []BookCore group. A book with no files, or whose
// files span more than one distinct parent directory, has an UNKNOWN parent
// dir — it is silently skipped (never grouped, never an error); an
// empty/whitespace title is skipped too (an empty-title bucket would glue
// unrelated books together). Delegates to the memdb twin when published
// (mirrors GetBooksBySeriesIDCore's delegation shape); otherwise pages
// through GetAllBooksCore (bounded pages) and resolves each book's parent
// dir via GetBookFiles — a single pass over books, never a per-book
// title-query fan-out (that O(N^2) shape is what GetBooksByTitleInDir would
// produce if called per book, so this method never calls it).
func (p *PebbleStore) GetFolderDuplicatesCore() ([][]BookCore, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetFolderDuplicatesCore()
	}

	var entries []folderDupEntry
	const folderDupPageSize = 500
	offset := 0
	for {
		page, err := p.GetAllBooksCore(folderDupPageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, book := range page {
			// GetAllBooksCore already excludes MarkedForDeletion rows; primary
			// version is not one of its filters, so it's checked here to
			// mirror GetBooksBySeriesIDCore's memdb-twin exclusions.
			if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
				continue
			}
			if strings.TrimSpace(book.Title) == "" {
				continue
			}

			files, ferr := p.GetBookFiles(book.ID)
			if ferr != nil {
				return nil, ferr
			}
			paths := make([]string, len(files))
			for i, f := range files {
				paths[i] = f.FilePath
			}
			dir, ok := singleParentDir(paths)
			if !ok {
				continue
			}

			entries = append(entries, folderDupEntry{
				book:      book,
				normTitle: util.NormalizeTitle(book.Title),
				dir:       dir,
			})
		}
		if len(page) < folderDupPageSize {
			break
		}
		offset += folderDupPageSize
	}

	return bucketFolderDuplicates(entries), nil
}

// folderDupEntry is one qualifying book (already filtered for
// deleted/non-primary/empty-title/unknown-dir) awaiting bucketing by
// GetFolderDuplicatesCore's bucketing pass. Shared between the PebbleStore
// scan-fallback (above) and the MemStore twin (memdb_reads.go) so the two
// backends can never drift in bucketing semantics.
type folderDupEntry struct {
	book      BookCore
	normTitle string
	dir       string
}

// bucketFolderDuplicates buckets entries by (normTitle, dir) and emits every
// bucket with >=2 members as one group, preserving first-seen bucket order.
func bucketFolderDuplicates(entries []folderDupEntry) [][]BookCore {
	type bucketKey struct {
		title string
		dir   string
	}
	buckets := make(map[bucketKey][]BookCore)
	order := make([]bucketKey, 0, len(entries))
	for _, e := range entries {
		key := bucketKey{title: e.normTitle, dir: e.dir}
		if _, exists := buckets[key]; !exists {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], e.book)
	}

	var groups [][]BookCore
	for _, key := range order {
		if len(buckets[key]) >= 2 {
			groups = append(groups, buckets[key])
		}
	}
	return groups
}

// singleParentDir returns the shared filepath.Dir of every path, or ("",
// false) when there are no paths, or the paths span more than one distinct
// directory — the "UNKNOWN parent dir" case documented on
// GetFolderDuplicatesCore, a non-disqualifying skip.
func singleParentDir(paths []string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	dir := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		if filepath.Dir(p) != dir {
			return "", false
		}
	}
	return dir, true
}

// GetDuplicateBooksByMetadataCore returns COARSE candidate duplicate groups
// for dedup tier 3 (metadata-fuzzy). It buckets non-deleted, primary-version,
// non-empty-title books by (author, first-significant-title-token), then runs
// pairwise title similarity ONLY within a bucket and transitively groups pairs
// scoring >= threshold. The result is deliberately coarse: fine-grained pair
// scoring (title + author) stays downstream in
// internal/dedup/book_dedup.go's metadataPairSimilarity — this getter only
// feeds it candidates, it is NOT a second scoring authority.
//
// Bucketing (spec Decision 3): the author component of the bucket key is
// AuthorID, not a re-normalized author name. This is EQUIVALENT to the locked
// "normalize author" decision, not a deviation: CreateAuthor
// (pebble_store_authors.go) deduplicates author rows on
// util.NormalizeAuthor(name), so two books whose author names are equal under
// that normalization already share one AuthorID. AuthorID is therefore a 1:1
// proxy for the normalized author name, and it avoids a per-book author-name
// join. BookCore.Authors is deliberately NOT used — it is empty on
// memdb-resident rows.
//
// O(N^2) guard: the pairwise comparison is provably confined to a single
// bucket (never the whole library), and a bucket larger than
// metadataFuzzyBucketCap is skipped with a slog.Warn (non-disqualifying: the
// run continues and returns every other group). This is the PR #1451
// index-then-narrow shape and forbids the per-book full-library scan that
// froze full-scan on 2026-07-07 (#19).
//
// Delegates to the memdb twin when published (mirrors
// GetFolderDuplicatesCore's delegation shape); otherwise pages through
// GetAllBooksCore in bounded pages. Fail-open contract: on error the consumer
// logs "metadata dedup failed" and continues, so a returned error just means
// tier 3 is empty for this run.
func (p *PebbleStore) GetDuplicateBooksByMetadataCore(threshold float64) ([][]BookCore, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetDuplicateBooksByMetadataCore(threshold)
	}

	var entries []metadataDupEntry
	const metadataDupPageSize = 500
	offset := 0
	for {
		page, err := p.GetAllBooksCore(metadataDupPageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, book := range page {
			// GetAllBooksCore already excludes MarkedForDeletion rows; primary
			// version is not one of its filters, so it's checked here to mirror
			// GetFolderDuplicatesCore's memdb-twin exclusions.
			if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
				continue
			}
			if e, ok := newMetadataDupEntry(book); ok {
				entries = append(entries, e)
			}
		}
		if len(page) < metadataDupPageSize {
			break
		}
		offset += metadataDupPageSize
	}

	return bucketMetadataDuplicates(entries, threshold), nil
}

// metadataFuzzyBucketCap bounds the pairwise (O(k^2)) work inside one bucket.
// A bucket with more than this many members is skipped entirely with a
// slog.Warn rather than compared pairwise — the anti-freeze guard for the
// #19 (2026-07-07) full-library-scan incident. Skipping is non-disqualifying.
const metadataFuzzyBucketCap = 200

// metadataDupEntry is one qualifying book (already filtered for
// deleted/non-primary/empty-title) awaiting bucketing by
// GetDuplicateBooksByMetadataCore. Shared between the PebbleStore
// scan-fallback and the MemStore twin (memdb_reads.go) so the two backends can
// never drift in bucketing/grouping semantics.
type metadataDupEntry struct {
	book       BookCore
	authorKey  string // "a:<id>" when AuthorID is set, "" when unknown
	titleToken string // first significant (non-article) normalized title token
	normTitle  string // full normalized title, used for pairwise scoring
}

// newMetadataDupEntry builds a metadataDupEntry from a BookCore, returning
// ok=false when the book must be skipped (empty title ⇒ never bucketed). An
// empty author (nil AuthorID) is NON-disqualifying: the book is still bucketed
// by its title token alone (authorKey ""), which just widens that bucket.
func newMetadataDupEntry(book BookCore) (metadataDupEntry, bool) {
	if strings.TrimSpace(book.Title) == "" {
		return metadataDupEntry{}, false
	}
	authorKey := ""
	if book.AuthorID != nil {
		authorKey = "a:" + strconv.Itoa(*book.AuthorID)
	}
	return metadataDupEntry{
		book:       book,
		authorKey:  authorKey,
		titleToken: firstSignificantTitleToken(book.Title),
		normTitle:  util.NormalizeTitle(book.Title),
	}, true
}

// metadataTitleArticles are leading articles stripped before picking the
// first significant title token. Without this, every "The ..." title collapses
// into one huge "the" bucket that trips metadataFuzzyBucketCap and is skipped,
// silently killing recall (and "The Hobbit" would never bucket with "Hobbit").
var metadataTitleArticles = map[string]bool{"the": true, "a": true, "an": true}

// firstSignificantTitleToken returns the first non-article whitespace token of
// the normalized title, edge-trimmed of punctuation. Titles consisting only of
// articles/punctuation fall back to their first non-empty token so the book
// still buckets (never returns "" for a non-empty title).
func firstSignificantTitleToken(title string) string {
	fields := strings.Fields(util.NormalizeTitle(title))
	fallback := ""
	for _, f := range fields {
		tok := strings.Trim(f, `.,:;!?"'()[]{}`)
		if tok == "" {
			continue
		}
		if fallback == "" {
			fallback = tok
		}
		if metadataTitleArticles[tok] {
			continue
		}
		return tok
	}
	return fallback
}

// bucketMetadataDuplicates buckets entries by (authorKey, titleToken), then
// within each bucket (>=2 members, at/under the cap) transitively groups books
// whose pairwise title similarity is >= threshold, emitting components of >=2
// books as groups. Bucket and group order is first-seen-stable. A
// threshold <= 0 means "no floor" (every in-bucket pair unions) but the bucket
// cap still bounds the work and guarantees termination.
func bucketMetadataDuplicates(entries []metadataDupEntry, threshold float64) [][]BookCore {
	buckets := make(map[string][]metadataDupEntry)
	order := make([]string, 0, len(entries))
	for _, e := range entries {
		key := e.authorKey + "\x00" + e.titleToken
		if _, exists := buckets[key]; !exists {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], e)
	}

	var groups [][]BookCore
	for _, key := range order {
		bucket := buckets[key]
		if len(bucket) < 2 {
			continue
		}
		if len(bucket) > metadataFuzzyBucketCap {
			slog.Warn("metadata dedup bucket exceeds cap; skipping",
				"bucketKey", key, "size", len(bucket), "cap", metadataFuzzyBucketCap)
			continue
		}
		groups = append(groups, groupMetadataBucket(bucket, threshold)...)
	}
	return groups
}

// groupMetadataBucket runs union-find over the pairwise (O(k^2), k <= cap)
// title-similarity graph of a single bucket and returns its connected
// components of size >= 2, in first-seen order. Author is constant within a
// bucket (it is part of the bucket key), so scoring reduces to title
// similarity — the coarse, grouping-only metric; downstream re-scores
// title+author.
func groupMetadataBucket(bucket []metadataDupEntry, threshold float64) [][]BookCore {
	n := len(bucket)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if metadataTitleSimilarity(bucket[i].normTitle, bucket[j].normTitle) >= threshold {
				union(i, j)
			}
		}
	}

	comps := make(map[int][]BookCore)
	rootsOrder := make([]int, 0, n)
	for i := 0; i < n; i++ {
		r := find(i)
		if _, ok := comps[r]; !ok {
			rootsOrder = append(rootsOrder, r)
		}
		comps[r] = append(comps[r], bucket[i].book)
	}

	var groups [][]BookCore
	for _, r := range rootsOrder {
		if len(comps[r]) >= 2 {
			groups = append(groups, comps[r])
		}
	}
	return groups
}

// metadataTitleSimilarity is the coarse, grouping-only title metric: it reuses
// internal/matcher.ScoreMatch (spec §C2 "reuse the existing fuzzy similarity
// path", never a second metric) and symmetrizes its search-flavored,
// asymmetric score to a 0..1 float. It is INTENTIONALLY coarse — the real
// title+author scoring authority is book_dedup.go's metadataPairSimilarity.
func metadataTitleSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	s := matcher.ScoreMatch(a, b)
	if r := matcher.ScoreMatch(b, a); r > s {
		s = r
	}
	return float64(s) / 100.0
}

// GetBooksBySeriesIDCore is Core-typed (STOREFID W4): the return type is
// BookCore, not Book, so the nine heavy fields (Description, VersionNotes,
// BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt,
// BookSigCoveragePct, Author, Series) being absent is compiler-enforced
// rather than silently nil'd. Both the memdb-fast-path rows (already
// stripped at the source) and the getBooksBySeriesIDFull scan fallback
// (full Book, projected via .Core() here) are covered — mapping via .Core()
// just makes that guarantee visible in the type system. A caller that needs
// any of the heavy fields MUST fetch via GetBookByID / GetAllBooksFullFrom
// (full Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (p *PebbleStore) GetBooksBySeriesIDCore(seriesID int) ([]BookCore, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetBooksBySeriesIDCore(seriesID, 0, 0)
	}
	books, err := p.getBooksBySeriesIDFull(seriesID)
	if err != nil {
		return nil, err
	}
	cores := make([]BookCore, len(books))
	for i := range books {
		cores[i] = books[i].Core()
	}
	return cores, nil
}

// getBooksBySeriesIDFull performs a full Pebble book scan filtered by series ID.
// Fallback path after Task 3.4 index removal.
func (p *PebbleStore) getBooksBySeriesIDFull(seriesID int) ([]Book, error) {
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

// GetBooksByAuthorIDCore is Core-typed (STOREFID P3-W2): the return type is
// BookCore, not Book, so the nine heavy fields (Description, VersionNotes,
// BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt,
// BookSigCoveragePct, Author, Series) being absent is compiler-enforced
// rather than silently nil'd. Both the memdb-fast-path rows and the
// getBooksByAuthorIDFull scan fallback are already stripped of those fields
// at the source (memdb never carries them; the Pebble scan below returns
// full Book only as an intermediate before projecting to Core) — mapping
// via .Core() here just makes that guarantee visible in the type system. A
// caller that needs any of the heavy fields MUST fetch via GetBookByID /
// GetAllBooksFullFrom (full Pebble). See
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (p *PebbleStore) GetBooksByAuthorIDCore(authorID int) ([]BookCore, error) {
	var books []Book
	var err error
	if p.UseMemDB && p.mem() != nil {
		books, err = p.mem().GetBooksByAuthorID(authorID, 0, 0)
	} else {
		books, err = p.getBooksByAuthorIDFull(authorID)
	}
	if err != nil {
		return nil, err
	}
	cores := make([]BookCore, len(books))
	for i := range books {
		cores[i] = books[i].Core()
	}
	return cores, nil
}

// getBooksByAuthorIDFull performs a full Pebble book scan filtered by author ID.
// Fallback path after Task 3.4 index removal.
func (p *PebbleStore) getBooksByAuthorIDFull(authorID int) ([]Book, error) {
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

// GetBooksByAuthorIDWithRoleCore returns all books where the author appears
// in the book_authors junction table (any role). It also falls back to the
// legacy AuthorID field for books not yet migrated to the junction table.
//
// Core-typed (STOREFID P3-W2b): the return type is BookCore, not Book, so
// the nine heavy fields (Description, VersionNotes, BookSigV1, BookSigV1Mask,
// BookSigSegments, BookSigBuiltAt, BookSigCoveragePct, Author, Series) being
// absent is compiler-enforced rather than silently nil'd. Both the memdb
// fast-path rows and the junction/legacy-scan fallback below are already
// stripped of those fields at the source (memdb never carries them; the
// Pebble scan returns full Book only as an intermediate before projecting to
// Core) — mapping via .Core() here just makes that guarantee visible in the
// type system. A caller that needs any of the heavy fields MUST fetch via
// GetBookByID / GetAllBooksFullFrom (full Pebble). See
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (p *PebbleStore) GetBooksByAuthorIDWithRoleCore(authorID int) ([]BookCore, error) {
	if p.UseMemDB && p.mem() != nil {
		books, err := p.mem().GetBooksByAuthorID(authorID, 0, 0)
		if err != nil {
			return nil, err
		}
		cores := make([]BookCore, len(books))
		for i := range books {
			cores[i] = books[i].Core()
		}
		return cores, nil
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
	cores := make([]BookCore, len(books))
	for i := range books {
		cores[i] = books[i].Core()
	}
	return cores, nil
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

	// Preserve fields stripped by stripBookForMemdb (STOR-1). Callers that
	// sourced `book` from the memdb projection (GetAllBooks on the production
	// UseMemDB path) or from a BookCore->ToBook projection carry nil
	// Description/VersionNotes/BookSig*/Author/Series even though the stored
	// row has real values. Restoring from oldBook — already fetched above via
	// the Pebble-direct GetBookByID — costs zero extra reads. Mirrors the
	// UpsertBookFile/BatchUpsertBookFiles fingerprint-preserve guard (PERF-7)
	// — keep both in sync. stripBookForMemdb nils exactly these nine fields,
	// so the guard restores exactly them.
	if book.Description == nil {
		book.Description = oldBook.Description
	}
	if book.VersionNotes == nil {
		book.VersionNotes = oldBook.VersionNotes
	}
	if book.BookSigV1 == nil {
		book.BookSigV1 = oldBook.BookSigV1
	}
	if book.BookSigV1Mask == nil {
		book.BookSigV1Mask = oldBook.BookSigV1Mask
	}
	if book.BookSigSegments == nil {
		book.BookSigSegments = oldBook.BookSigSegments
	}
	if book.BookSigBuiltAt == nil {
		book.BookSigBuiltAt = oldBook.BookSigBuiltAt
	}
	if book.BookSigCoveragePct == nil {
		book.BookSigCoveragePct = oldBook.BookSigCoveragePct
	}
	// Author/Series are denormalized display objects derived from
	// AuthorID/SeriesID; they are recomputed on read, never user-cleared to
	// nil (no empty-string-style sentinel exists for these pointer structs),
	// so preserve-on-nil is correct — the same class of fix as the seven
	// fields above (STOREFID W5d-1 / #1887; the CreateOrganizedVersion write
	// wiped these before the call-site hydrate landed). A write that
	// legitimately changes the author must set BOTH AuthorID and a fresh
	// Author object (see the author-split ops); this guard only stops the
	// nil-projection wipe, it does not re-derive.
	if book.Author == nil {
		book.Author = oldBook.Author
	}
	if book.Series == nil {
		book.Series = oldBook.Series
	}

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

	// Delete work-ID index (INDEX-CONSISTENCY). CreateBook writes
	// book:work:<WorkID>:<id> but the original DeleteBook never tore it down,
	// leaving a dangling index row that surfaced the hard-deleted book as a
	// live phantom in GetBooksByWorkID. Mirror UpdateBook's teardown.
	if book.WorkID != nil && *book.WorkID != "" {
		workKey := []byte(fmt.Sprintf("book:work:%s:%s", *book.WorkID, id))
		if err := batch.Delete(workKey, nil); err != nil {
			batch.Close()
			return err
		}
	}

	// Delete the three file-hash index rows CreateBook writes. Same dangling-row
	// class as the work index above: without these, book:hash / book:originalhash
	// / book:organizedhash lookups resolve to a hard-deleted book ID. Guards
	// mirror CreateBook's (nil / empty-string skipped).
	if book.FileHash != nil && *book.FileHash != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("book:hash:%s", *book.FileHash)), nil); err != nil {
			batch.Close()
			return err
		}
	}
	if book.OriginalFileHash != nil && *book.OriginalFileHash != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("book:originalhash:%s", *book.OriginalFileHash)), nil); err != nil {
			batch.Close()
			return err
		}
	}
	if book.OrganizedFileHash != nil && *book.OrganizedFileHash != "" {
		if err := batch.Delete([]byte(fmt.Sprintf("book:organizedhash:%s", *book.OrganizedFileHash)), nil); err != nil {
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

	// Delete the book's embedding row (emb:v:book:<id>), if any. Without
	// this, a deleted book's embedding is orphaned forever: dedup.embed-scan
	// iterates GetAllBooks, which by construction never returns a deleted
	// book, so the orphaned row is never revisited or re-embedded to a
	// current model/dimension. This targets the same keyspace as
	// EmbeddingStore.Delete("book", id) (see embVecKey in
	// embedding_store.go); deleted directly in this batch for atomicity
	// with the rest of the book removal rather than wiring in a separate
	// EmbeddingStore dependency on PebbleStore.
	embKey := []byte(embVecPfx + "book:" + id)
	if err := batch.Delete(embKey, nil); err != nil {
		batch.Close()
		return err
	}

	// Delete this book's persisted chapter list (abs-sync TASK-06).
	if err := batch.Delete([]byte("chapters:"+id), nil); err != nil {
		batch.Close()
		return err
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
				authorNames[a.ID] = util.NormalizeAuthor(a.Name)
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
			// Apply pagination: only collect results in the requested range.
			// limit == 0 means "no limit" (return all matches).
			if count >= offset && (limit == 0 || len(filtered) < limit) {
				filtered = append(filtered, book)
			}
			count++
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}

	return filtered, nil
}

// CountPrimaryBooks returns the number of primary, non-deleted books.
//
// The underlying scan (countPrimaryBooksScan) iterates every book: key and
// json.Unmarshal's each Book — ~5.6s on a ~44K-book library. The 5s metrics/status
// ticker (server_lifecycle.go) and GET /health both call this, so an uncached scan
// pinned ~2 cores continuously on an idle server. The result is cached for
// primaryCountCacheTTL; the count may lag a write by up to that window, which is
// fine for a metrics gauge / health probe.
func (p *PebbleStore) CountPrimaryBooks() (int, error) {
	// Fast path: fresh cached value.
	p.primaryCountMu.Lock()
	if p.primaryCountValid && time.Since(p.primaryCountComputedAt) < primaryCountCacheTTL {
		c := p.primaryCount
		p.primaryCountMu.Unlock()
		return c, nil
	}
	p.primaryCountMu.Unlock()

	// Gate the recompute so a burst of callers (e.g. concurrent /health probes)
	// collapses to a single scan per window instead of each running its own 5.6s
	// scan. Double-check the cache after acquiring the gate — a peer may have just
	// refreshed it.
	p.primaryCountRecomputeMu.Lock()
	defer p.primaryCountRecomputeMu.Unlock()

	p.primaryCountMu.Lock()
	if p.primaryCountValid && time.Since(p.primaryCountComputedAt) < primaryCountCacheTTL {
		c := p.primaryCount
		p.primaryCountMu.Unlock()
		return c, nil
	}
	p.primaryCountMu.Unlock()

	count, err := p.countPrimaryBooksScan()
	if err != nil {
		return 0, err
	}

	p.primaryCountMu.Lock()
	p.primaryCount = count
	p.primaryCountComputedAt = time.Now()
	p.primaryCountValid = true
	p.primaryCountMu.Unlock()

	return count, nil
}

// countPrimaryBooksScan is the uncached full scan behind CountPrimaryBooks.
func (p *PebbleStore) countPrimaryBooksScan() (int, error) {
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

// CountAllBooks returns the count of all non-deleted books regardless of
// IsPrimaryVersion. Matches what GetAllBooksCore/PageBooks iterates — use this
// for progress denominators in ops that process every book.
func (p *PebbleStore) CountAllBooks() (int, error) {
	if p.UseMemDB && p.mem() != nil {
		all, err := p.mem().GetAllBooksCore(0, 0, nil)
		if err != nil {
			return 0, err
		}
		return len(all), nil
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
	//
	// INDEX-CONSISTENCY: like GetBooksByWorkID, the index value embeds a
	// serialized Book snapshot that UpdateBook only refreshes when the
	// VersionGroupID changes, so a same-group edit (SoftDeleteBook sets
	// MarkedForDeletion via UpdateBook without touching the group) leaves it
	// stale. Treat the index as a POINTER: the trailing key segment is the book
	// ID (a ULID, no nested colons); point-look-up the authoritative book:<id>
	// row and skip anything absent (hard-deleted) or MarkedForDeletion
	// (soft-deleted). This can never desync from the source of truth.
	prefix := []byte(fmt.Sprintf("book:versiongroup:%s:", groupID))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';' // ':' + 1
	idxIter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	var books []Book
	for idxIter.First(); idxIter.Valid(); idxIter.Next() {
		bookID := string(idxIter.Key()[len(prefix):])
		if bookID == "" {
			continue
		}
		b, err := p.GetBookByID(bookID)
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

// Operation operations

// Operation Log operations

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

// Metadata provenance operations

// Metadata change history operations

// User Preference operations

// Playlist operations

// ---- Extended keyspace implementation ----

// Roles

// User playlists (spec 3.4)
//
// Key schema:
//   upl:{id}                    → UserPlaylist JSON
//   idx:upl:name:{lcase-name}   → playlist ID
//   idx:upl:itunes:{pid}        → playlist ID
//   idx:upl:dirty:{id}          → "1" (pending-push set)

// User positions + book state (spec 3.6)
//
// Key schema:
//   upos:{userID}:{bookID}:{segmentID}  → UserPosition JSON
//   ubs:{userID}:{bookID}               → UserBookState JSON
//   idx:ubs:status:{userID}:{status}:{bookID} → "1"

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

// Invites

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

// ---- Operation State Persistence (resumable operations) ----

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

// ---------- Author tag wrappers (PebbleStore) ----------

// ---------- Series tag wrappers (PebbleStore) ----------

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
//
//lint:ignore U1000 kept: no-op placeholder for the secondary-index-delete hook signature (see doc above, 2026-07-12)
func deleteFingerprintLSHIndexesByID(_ *pebble.Batch, _ string) error {
	return nil
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

// SweepBookFileSegDropResult holds the outcome of a SweepBookFileSegDrop run.
type SweepBookFileSegDropResult struct {
	Total   int // total primary book_file: rows examined
	Rewrite int // rows rewritten (had at least one non-empty seg)
	Skipped int // rows skipped (already clean)
	Errors  int // rows skipped due to unmarshal/marshal errors (logged)
}
