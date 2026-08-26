// file: internal/scanner/scanner.go
// version: 1.70.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhowden/tag"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/matcher"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/oklog/ulid/v2"
)

// saveBook is the per-book persistence hook used by ProcessBooksParallel.
// Its signature includes ctx so that callers can propagate cancellation into
// the DB write path and saveBookToDatabase can snapshot config at entry to
// avoid racing with test-teardown config restores (CI race: scanner.go:1700).
// Tests may replace this variable with a no-op; the signature must match.
var saveBook func(ctx context.Context, book *Book) error = saveBookToDatabase

// defaultLog is a package-level logger for functions that cannot accept a logger parameter.
var defaultLog = logger.New("scanner")

// scanProgressEvery is how many directories ScanDirectoryParallel processes
// between liveness checkpoints.
//
// The bound that matters is wall-clock, not count: the stuck-op watchdog kills
// an operation after ProgressTimeout (5m) without an UpdateProgress. A single
// directory is scanned in well under a second even when it holds hundreds of
// files, so 20 keeps the gap between checkpoints to seconds while costing one
// progress write per 20 directories rather than one per directory -- the
// reporter persists each update, so checkpointing every single directory would
// add ~13k writes to a full-library scan for no extra safety.
const scanProgressEvery = 20

// Counters for store failures that were previously swallowed silently
// (audit 2026-07-17 H5). Package-level atomics because saveBookToDatabase is
// reached through the saveBook variable, whose fixed signature cannot carry
// per-run state; each ProcessBooksParallel run snapshots the totals at entry
// and logs the delta in its completion summary. Concurrent runs may attribute
// a few of each other's failures to their own summary — acceptable for logs.
var (
	dupLookupErrCount       atomic.Int64 // duplicate-detection hash lookup store errors
	dupLookupSkipCount      atomic.Int64 // files skipped: duplicate status undeterminable
	scanCacheUpdateErrCount atomic.Int64 // UpdateScanCache failures (file re-hashed every scan until it succeeds)
	scanFailCountErrCount   atomic.Int64 // IncrScanFailCount failures

	// The three ways a scan-cache write-back can be abandoned BEFORE
	// UpdateScanCache is ever called. Until 2026-08-24 all three were dropped
	// on the floor by a `dbErr == nil && dbBook != nil` chain with no else, so
	// a store error, a missing row and a vanished file were indistinguishable
	// from a successful write.
	//
	// All three leave the path with no cache entry for that run, and
	// GetScanCacheMap skips rows whose LastScanMtime is nil, so all three cost
	// a re-read next scan. Only scanCacheNoRowCount is PERMANENT: a stat error
	// or a store error is transient and the next scan writes the entry, but a
	// path with no book row can never acquire one however often it is scanned.
	// See writeBackScanCache for why.
	scanCacheStatErrCount   atomic.Int64 // write-back abandoned: os.Stat failed
	scanCacheLookupErrCount atomic.Int64 // write-back abandoned: GetBookByFilePath returned an error
	scanCacheNoRowCount     atomic.Int64 // write-back abandoned: no book row exists at this path
	scanCachePanicCount     atomic.Int64 // write-back recovered from a panic (nil-wrapping store interface)

	// Rescan-age re-arm. UpdateScanCache CLEARS NeedsRescan, so a file that is
	// still inside the rescan-age window has to have the flag put back or the
	// age gate would defer it for a full period on the strength of whatever
	// half-written metadata this pass recorded. See writeBackScanCache.
	scanCacheRearmCount    atomic.Int64 // write-back re-armed NeedsRescan: file still inside the rescan-age window
	scanCacheRearmErrCount atomic.Int64 // MarkNeedsRescan failed after a write-back that needed re-arming

	// createBookFilesForBook's own by-path lookup. Split out because a store
	// ERROR and "no row here" are different facts with different consequences,
	// and the original code returned the same "" for both -- silently.
	bookFileLookupErrCount     atomic.Int64 // book lookup failed: chapters and scan-cache write-back skipped for this book
	bookFilePathRecoveredCount atomic.Int64 // no row at the segment path, but the row was found at the normalized directory
)

// Skip-decision counters. The skip decision used to return a bare bool with no
// counter, no log and no metric, so the single most load-bearing number in an
// incremental scan -- how many files it actually avoided re-reading -- was not
// observable at all. A scan that skipped everything and a scan that skipped
// nothing produced byte-identical logs.
//
// The four re-read reasons are counted SEPARATELY rather than as one
// "processed" total because they call for different fixes: "changed" is the
// scan working as designed, "dirty" is a user-requested rescan, "stat error"
// is a filesystem problem, and "cache miss" is the population that gets
// re-read every single tick forever (measured 12.8% of the library on
// 2026-08-24). Collapsing them into one number cannot tell those apart, which
// is the exact question any future skip-rate work has to answer.
var (
	skipUnchangedCount atomic.Int64 // skipped: mtime+size unchanged and no rescan flag
	skipTooFreshCount  atomic.Int64 // skipped: changed, but mtime is inside the rescan-age window
	readCacheMissCount atomic.Int64 // re-read: no scan-cache entry for this path
	readChangedCount   atomic.Int64 // re-read: mtime or size differs from the cached values
	readDirtyCount     atomic.Int64 // re-read: NeedsRescan set (forced per-book rescan)
	readStatErrCount   atomic.Int64 // re-read: os.Stat failed, so no comparison was possible
	readCacheOffCount  atomic.Int64 // re-read: cache disabled for this run (force_update)
)

// warnSampled increments c and logs at Warn on the first occurrence and every
// 1000th after — bounded noise on a mass store failure without ever being
// fully silent. Returns the new count.
func warnSampled(c *atomic.Int64, log logger.Logger, format string, args ...any) int64 {
	n := c.Add(1)
	if n == 1 || n%1000 == 0 {
		if log == nil {
			log = defaultLog
		}
		log.Warn(format, args...)
	}
	return n
}

// Scanner defines the interface for scanning and processing audiobook files.
// Tests can swap in a mock implementation via SetScanner.
type Scanner interface {
	ScanDirectory(rootDir string, scanLog logger.Logger) ([]Book, error)
	ScanDirectoryParallel(ctx context.Context, rootDir string, workers int, scanLog logger.Logger) ([]Book, error)
	ProcessBooks(books []Book, scanLog logger.Logger) error
	ProcessBooksParallel(ctx context.Context, books []Book, workers int, progressFn func(processed int, total int, bookPath string), scanLog logger.Logger) error
	ComputeFileHash(filePath string) (string, error)
}

// activeScanner overrides the default implementation. Set by tests via SetScanner.
var activeScanner Scanner

// activeEmbeddingStore is used for dedup candidate creation during scanning.
// Set by ScanService before starting a scan.
var activeEmbeddingStore *database.EmbeddingStore

// SetScanner overrides the default scanner implementation for testing.
func SetScanner(s Scanner) {
	activeScanner = s
}

// setActiveEmbeddingStore sets the embedding store for dedup detection.
func setActiveEmbeddingStore(es *database.EmbeddingStore) {
	activeEmbeddingStore = es
}

// detectMetadataHashDuplicate checks if a newly created/updated book matches an existing
// book by metadata_source_hash and creates a dedup candidate if found.
func detectMetadataHashDuplicate(book *database.Book, log logger.Logger) {
	if activeEmbeddingStore == nil {
		return
	}
	if getStore() == nil {
		return
	}

	// Compute hash based on current metadata
	hash := computeMetadataSourceHash(book)
	if hash == "" {
		return
	}

	// Check if another book has the same hash
	existing, err := getStore().GetBooksByMetadataSourceHash(hash)
	if err != nil {
		if log != nil {
			log.Warn("Failed to check for metadata hash duplicates for %s: %v", book.ID, err)
		}
		return
	}

	// Find a different book with the same hash
	for _, candidate := range existing {
		if candidate.ID != book.ID {
			if log != nil {
				log.Info("import: metadata hash duplicate detected book=%s existing=%s", book.ID, candidate.ID)
			}
			// Create dedup candidate with high confidence
			dedupCandidate := database.DedupCandidate{
				EntityType: "book",
				EntityAID:  candidate.ID,
				EntityBID:  book.ID,
				Layer:      "metadata_hash_match",
				Similarity: new(float64),
			}
			*dedupCandidate.Similarity = 1.0
			if err := activeEmbeddingStore.UpsertCandidate(dedupCandidate); err != nil && log != nil {
				log.Warn("Failed to create dedup candidate for %s and %s: %v", dedupCandidate.EntityAID, dedupCandidate.EntityBID, err)
			}
			break // Only create one candidate per scanned book
		}
	}
}

// pkgStore is the database the scanner's package-level helpers use.
// Wired by SetStore from NewServer at startup. Free helpers in this
// file (createBookFilesForBook, saveBookToDatabase, the inline DB
// lookups inside ProcessBooksParallel) read pkgStore rather than the
// host's database.GetGlobalStore (SERVER-GLOBAL-STORE-AUDIT phase 7).
// Nil-safe — all helpers check before use. Single-writer at startup,
// many readers later; the rwmutex protects against goroutines that
// touch it before NewServer runs (none today, but cheap insurance).
var (
	pkgStore   scannerStore
	pkgStoreMu sync.RWMutex
)

// SetStore wires the database the scanner package's free helpers use
// for book/file/work/hash lookups. Idempotent; pass nil to clear.
func SetStore(s scannerStore) {
	pkgStoreMu.Lock()
	pkgStore = s
	pkgStoreMu.Unlock()
}

// getStore returns the package-local store with read-lock protection.
// Used internally by the free helpers in scanner.go and the per-book
// helpers in book_files.go.
func getStore() scannerStore {
	pkgStoreMu.RLock()
	defer pkgStoreMu.RUnlock()
	return pkgStore
}

// aiBatchWorkers bounds how many AI filename-parsing batches are in flight at
// once. Network-bound work against a single backend, so this is a small fixed
// number rather than runtime.NumCPU(): the point is to stop waiting on one
// request at a time, not to saturate the model host. The per-batch delay is
// preserved per worker, so the aggregate request rate rises by this factor and
// no more.
const aiBatchWorkers = 4

// globalScanCache is set before a scan and used inside ProcessBooksParallel to
// skip files whose mtime+size are unchanged since the last successful scan.
// Protected by globalScanCacheMu because SetScanCache and ProcessBooksParallel
// may be called from different goroutines in tests.
var (
	globalScanCache   map[string]database.ScanCacheEntry
	globalScanCacheMu sync.RWMutex
)

// SetScanCache installs a pre-loaded scan-cache map before a scan run.
// Pass nil to disable incremental skip behaviour (full scan).
func SetScanCache(cache map[string]database.ScanCacheEntry) {
	globalScanCacheMu.Lock()
	defer globalScanCacheMu.Unlock()
	globalScanCache = cache
}

// ClearScanCache removes the cached map after a scan completes.
// Test/legacy direct-clear; production scan runs use AcquireScanCache's
// release func so concurrent runs don't clear each other's cache.
func ClearScanCache() {
	SetScanCache(nil)
}

// scanCacheRefs / scanCacheFullRuns reference-count concurrent scan runs that
// share globalScanCache (audit 2026-07-17 R-4): library.scan and
// library.import have distinct ConcurrencyKeys and can run this code path
// concurrently; without refcounting, the first finisher's deferred clear
// nils the cache under the still-running op, silently disabling incremental
// skip mid-run. Both guarded by globalScanCacheMu.
var (
	scanCacheRefs     int
	scanCacheFullRuns int
)

// AcquireScanCache registers a scan run with the shared incremental-skip
// cache and returns an idempotent release func the run must call (defer)
// when it finishes.
//
// Design choice (R-4): a refcount around the existing package-level cache was
// chosen over threading a per-run cache instance through the scan context
// because ProcessBooksParallel sits behind the Scanner interface (mocked by
// tests); changing its signature would ripple through every implementation
// and mock, while the refcount confines the fix to this file.
//
// Rules:
//   - The first acquirer installs its cache; later concurrent acquirers share
//     it (all production callers load the same map from GetScanCacheMap, so
//     sharing is safe).
//   - A run that passes nil requested a FULL scan (incremental skip disabled).
//     Any active full run disables the cache for every concurrent run:
//     skipping unchanged files under a force-rescan would be a correctness
//     bug, whereas re-processing unchanged files under an incremental run is
//     only slower. The cache stays disabled until the last run releases.
//   - The cache is cleared when the last active run releases.
func AcquireScanCache(cache map[string]database.ScanCacheEntry) func() {
	globalScanCacheMu.Lock()
	defer globalScanCacheMu.Unlock()
	scanCacheRefs++
	isFull := cache == nil
	if isFull {
		scanCacheFullRuns++
		globalScanCache = nil
	} else if scanCacheFullRuns == 0 && globalScanCache == nil {
		globalScanCache = cache
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			globalScanCacheMu.Lock()
			defer globalScanCacheMu.Unlock()
			scanCacheRefs--
			if isFull {
				scanCacheFullRuns--
			}
			if scanCacheRefs <= 0 {
				scanCacheRefs = 0
				scanCacheFullRuns = 0
				globalScanCache = nil
			}
		})
	}
}

// worksLookupCache caches a (normalizedTitle|authorID) → workID map for the
// duration of a single scan. Without this cache, saveBookToDatabase calls
// GetAllWorks() once per book, producing 50K × 50K = 2.5B lookups on a full
// scan of the production library (MAYDEPLOY-H6). With it, GetAllWorks is
// called at most once per scan and reused for every book.
//
// The cache is populated lazily on first access (so test paths that don't
// initialise it still work) and invalidated when the scanner creates a new
// Work mid-scan, so subsequent books in the same scan can find it.
//
// Set via InitWorksLookupCache / cleared via ClearWorksLookupCache from
// ScanService.performScanInternal. Protected by worksLookupMu.
var (
	worksLookupCache map[string]string // key = normalizedTitle + "|" + authorID(or "nil")
	worksLookupReady bool              // true when cache has been populated (or attempted) for this scan
	worksLookupMu    sync.RWMutex
)

// worksLookupKey builds the cache key used by both lookups and inserts.
func worksLookupKey(normalizedTitle string, authorID *int) string {
	if authorID == nil {
		return normalizedTitle + "|nil"
	}
	return normalizedTitle + "|" + strconv.Itoa(*authorID)
}

// InitWorksLookupCache builds the (normalizedTitle|authorID) → workID map by
// calling GetAllWorks once. Test/legacy direct-init; production scan runs use
// AcquireWorksLookupCache / ReleaseWorksLookupCache so concurrent runs don't
// clear each other's cache (audit 2026-07-17 R-4).
// If GetAllWorks fails, the cache is left empty but enabled — saveBookToDatabase
// will fall through to a direct CreateWork (which is the same fallback path as
// when nothing matched).
func InitWorksLookupCache() {
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	initWorksLookupCacheLocked()
}

// initWorksLookupCacheLocked is the body of InitWorksLookupCache; callers must
// hold worksLookupMu.
func initWorksLookupCacheLocked() {
	worksLookupCache = make(map[string]string)
	worksLookupReady = true
	store := getStore()
	if store == nil {
		return
	}
	works, err := store.GetAllWorks()
	if err != nil {
		defaultLog.Warn("InitWorksLookupCache: GetAllWorks failed: %v", err)
		return
	}
	for _, w := range works {
		worksLookupCache[worksLookupKey(util.NormalizeString(w.Title), w.AuthorID)] = w.ID
	}
	defaultLog.Info("InitWorksLookupCache: loaded %d works", len(works))
}

// ClearWorksLookupCache drops the per-scan works lookup map. Test/legacy
// direct-clear counterpart of InitWorksLookupCache; production scan runs use
// ReleaseWorksLookupCache.
func ClearWorksLookupCache() {
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	worksLookupCache = nil
	worksLookupReady = false
}

// worksLookupRefs reference-counts concurrent scan runs sharing the works
// lookup cache (audit 2026-07-17 R-4). Guarded by worksLookupMu. Without it,
// the first finishing run's deferred clear dropped the cache under a
// still-running concurrent run, degrading every remaining saveBookToDatabase
// call to an O(N) GetAllWorks scan per book.
var worksLookupRefs int

// AcquireWorksLookupCache registers a scan run with the shared works lookup
// cache, populating it on first use (one GetAllWorks call). Later concurrent
// acquirers share the same map — safe because all runs resolve works against
// the same store and rememberCreatedWork keeps the map coherent under
// worksLookupMu. Pair with a deferred ReleaseWorksLookupCache.
func AcquireWorksLookupCache() {
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	worksLookupRefs++
	if worksLookupReady && worksLookupCache != nil {
		return // already populated by a concurrent run
	}
	initWorksLookupCacheLocked()
}

// ReleaseWorksLookupCache decrements the refcount and drops the cache when the
// last active scan run finishes.
func ReleaseWorksLookupCache() {
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	if worksLookupRefs > 0 {
		worksLookupRefs--
	}
	if worksLookupRefs == 0 {
		worksLookupCache = nil
		worksLookupReady = false
	}
}

// lookupWorkID returns the cached workID for (normalizedTitle, authorID), or
// "" if no match. Falls back to a one-shot GetAllWorks() scan when the cache
// hasn't been initialised (test paths and any code that calls saveBook outside
// a ScanService-driven scan).
func lookupWorkID(normalizedTitle string, authorID *int) string {
	key := worksLookupKey(normalizedTitle, authorID)
	worksLookupMu.RLock()
	ready := worksLookupReady
	if ready {
		id, ok := worksLookupCache[key]
		worksLookupMu.RUnlock()
		if ok {
			return id
		}
		return ""
	}
	worksLookupMu.RUnlock()

	// Cache not initialised (test/legacy path) — do the old per-call scan.
	store := getStore()
	if store == nil {
		return ""
	}
	works, err := store.GetAllWorks()
	if err != nil {
		return ""
	}
	for _, w := range works {
		if util.NormalizeString(w.Title) == normalizedTitle &&
			((authorID == nil && w.AuthorID == nil) ||
				(authorID != nil && w.AuthorID != nil && *authorID == *w.AuthorID)) {
			return w.ID
		}
	}
	return ""
}

// rememberCreatedWork records a freshly-created Work in the per-scan cache so
// subsequent books in the same scan can resolve it without re-querying.
func rememberCreatedWork(w *database.Work) {
	if w == nil {
		return
	}
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	if !worksLookupReady || worksLookupCache == nil {
		return
	}
	worksLookupCache[worksLookupKey(util.NormalizeString(w.Title), w.AuthorID)] = w.ID
}

// skipReason explains why a file was skipped or re-read. It exists so the scan
// summary can report WHICH reason dominated, not just a processed count.
type skipReason int

const (
	reasonUnchanged skipReason = iota // skipped
	reasonCacheOff                    // re-read: cache disabled for the run
	reasonCacheMiss                   // re-read: path absent from the cache
	reasonChanged                     // re-read: mtime or size differs
	reasonDirty                       // re-read: NeedsRescan set
	reasonTooFresh                    // skipped: changed, but inside the rescan-age window
	// re-read: os.Stat failed, which is no evidence the file is unchanged.
	// Added 2026-08-24 with classifySkipBook: the single-file path counted this
	// inline at the call site, which left the rollup with no way to report it.
	reasonStatErr
)

// rescanFreshCutoff returns the mtime at or below which a CHANGED file is
// considered settled enough to re-read. A file whose mtime is strictly greater
// than the cutoff changed too recently and is left alone until it goes quiet.
//
// A non-positive minAgeHours disables the gate, and it returns math.MaxInt64 to
// do it rather than 0: the comparison is `mtime > cutoff`, so the value that
// makes it never fire is the largest one, not the smallest. Returning 0 would
// invert the intent and gate every file with a post-epoch mtime -- i.e. all of
// them.
func rescanFreshCutoff(now time.Time, minAgeHours int) int64 {
	if minAgeHours <= 0 {
		return math.MaxInt64
	}
	return now.Add(-time.Duration(minAgeHours) * time.Hour).Unix()
}

// classifySkipFile decides whether a file can be skipped, and says why. The
// order of the checks matters for attribution: a dirty entry whose mtime also
// changed is reported as dirty, because the forced rescan is the reason a
// caller would care about.
//
// freshCutoff is the rescan-age gate (rescanFreshCutoff). It applies to exactly
// ONE branch -- a file that is in the cache, is not flagged for rescan, and has
// changed -- and that narrowness is the whole design:
//
//   - a path with NO cache entry is new and is read immediately, so discovery
//     is never delayed by the gate;
//   - NeedsRescan is checked FIRST, so an explicitly forced per-file rescan
//     bypasses the gate;
//   - a force_update run passes cache == nil, so the gate is never consulted at
//     all on a full sweep.
//
// Those are the two "unless" clauses the gate was specified with, and they are
// satisfied structurally rather than by a flag this function has to be told
// about.
func classifySkipFile(filePath string, mtime int64, size int64, cache map[string]database.ScanCacheEntry, freshCutoff int64) (bool, skipReason) {
	if cache == nil {
		return false, reasonCacheOff
	}
	entry, found := cache[filePath]
	if !found {
		return false, reasonCacheMiss
	}
	if entry.NeedsRescan {
		return false, reasonDirty
	}
	if entry.Mtime != mtime || entry.Size != size {
		// Strictly greater: a file whose mtime is EXACTLY the cutoff has aged
		// the full period and is re-read. Using >= here would hold it one more
		// scan for no reason.
		if mtime > freshCutoff {
			return true, reasonTooFresh
		}
		return false, reasonChanged
	}
	return true, reasonUnchanged
}

// classifySkipBook applies classifySkipFile across every file a book owns and
// skips the book only if EVERY one of them is skippable.
//
// Skipping is per FILE; processing is per BOOK. A book with six files where five
// are unchanged and one changed must still be reprocessed in full, because its
// duration, size and chapter timeline are all functions of the whole set.
// Deciding the other way -- reprocessing only the changed file -- silently
// corrupts exactly the aggregates RecomputeBookAggregates' partial-data rule
// exists to protect.
//
// The file set comes from the WALK (Book.SegmentFiles), never from Book.FilePath.
// That is deliberate and load-bearing: the per-file scan cache is a stepping
// stone to dropping Book.FilePath normalisation entirely, and a rollup that
// grouped by book path would re-introduce the dependency the schema change is
// paying to remove. TestClassifySkipBookIgnoresTheBookPath pins it -- the rollup
// must stay CORRECT when Book.FilePath is stale, empty or wrong, not merely
// avoid mentioning it.
//
// The reported reason is the FIRST file that refused to skip. A book re-read
// because one segment changed is attributed "changed", not to whatever the other
// five happened to be, so the summary names the reason a caller would act on.
//
// stat is injected so the decision is testable without touching a filesystem.
func classifySkipBook(files []string, cache map[string]database.ScanCacheEntry, freshCutoff int64,
	stat func(string) (os.FileInfo, error),
) (bool, skipReason) {
	if len(files) == 0 {
		return false, reasonCacheMiss
	}
	// A cache-disabled run is decided once for the whole book: consulting it per
	// file would report cacheOff N times for one book and inflate the summary.
	if cache == nil {
		return false, reasonCacheOff
	}
	deferred := false
	for _, f := range files {
		fi, err := stat(f)
		if err != nil {
			// A stat failure is no evidence the file is unchanged, so it forces
			// the re-read -- same rule the single-file path already applies.
			return false, reasonStatErr
		}
		skip, reason := classifySkipFile(f, fi.ModTime().Unix(), fi.Size(), cache, freshCutoff)
		if !skip {
			return false, reason
		}
		// tooFresh also skips, but it means "changed, come back later" rather
		// than "nothing to do". Collapsing it into unchanged here would hide
		// deferred work in the skip total -- and the run summary breaks the two
		// apart precisely so a library that is churning does not read as a
		// cache doing its job.
		if reason == reasonTooFresh {
			deferred = true
		}
	}
	if deferred {
		return true, reasonTooFresh
	}
	return true, reasonUnchanged
}

// scanFileSetFor returns every file the scan considers part of this book.
//
// SegmentFiles is the walk's own grouping and is the authority when present. It
// is used in preference to Book.FilePath on purpose: see classifySkipBook.
func scanFileSetFor(b *Book) []string {
	if len(b.SegmentFiles) > 0 {
		return b.SegmentFiles
	}
	if b.FilePath == "" {
		return nil
	}
	return []string{b.FilePath}
}

// recordSkipDecision counts one classifySkipFile verdict.
func recordSkipDecision(reason skipReason) {
	switch reason {
	case reasonUnchanged:
		skipUnchangedCount.Add(1)
	case reasonTooFresh:
		skipTooFreshCount.Add(1)
	case reasonCacheOff:
		readCacheOffCount.Add(1)
	case reasonCacheMiss:
		readCacheMissCount.Add(1)
	case reasonChanged:
		readChangedCount.Add(1)
	case reasonDirty:
		readDirtyCount.Add(1)
	case reasonStatErr:
		readStatErrCount.Add(1)
	}
}

// isExcludedPath checks whether a path matches any configured exclude pattern.
func isExcludedPath(path string) bool {
	for _, pattern := range config.AppConfig.ExcludePatterns {
		if pattern == "" {
			continue
		}
		if matched, err := filepath.Match(pattern, filepath.Base(path)); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(pattern, path); err == nil && matched {
			return true
		}
	}
	return false
}

// Book represents an audiobook file
type Book struct {
	FilePath         string
	Title            string
	Author           string
	Series           string
	Position         int
	Format           string
	Duration         int
	Narrator         string
	Language         string
	Publisher        string
	BookOrganizerID  string // Embedded AUDIOBOOK_ORGANIZER_ID for re-linking
	ASIN             string
	OpenLibraryID    string
	HardcoverID      string
	SegmentFiles     []string // For multi-file books grouped by album in mixed directories
	GoogleBooksID    string
	FileHash         string            // Pre-computed hash from ProcessFile (avoids double-read)
	SegmentHashes    map[string]string // filePath→hash written back by saveBookToDatabase dedup loop
	LibraryState     string            // If set, overrides the default "imported" state in saveBookToDatabase
	SourceImportPath string            // Top-level import path this file was discovered in; set by scan_service
}

// ScanDirectory scans the given directory for audiobook files.
// If scanLog is nil, a default logger is used.
func ScanDirectory(ctx context.Context, rootDir string, scanLog logger.Logger) ([]Book, error) {
	if activeScanner != nil {
		return activeScanner.ScanDirectory(rootDir, scanLog)
	}
	return ScanDirectoryParallel(ctx, rootDir, 1, scanLog)
}

// writeBackScanCache stamps the scan cache for filePath so the next incremental
// scan can skip it.
//
// This replaces two inlined copies, which were similar but NOT identical: the
// main path abandoned the write on three conditions (stat, lookup, no row)
// while the suspicious-file path had only two, because it reused an os.FileInfo
// its enclosing scope had already taken. They also recovered differently -- the
// suspicious-file copy used a bare `defer func() { recover() }()` that swallowed
// the panic, the main one logged it. Unifying keeps the logging recover.
//
// fi is the FileInfo the caller already holds, or nil to stat internally. The
// suspicious-file path MUST pass its own, and this is not an optimisation.
// That path stats a file, finds it under the minimum-size threshold, marks it
// LibraryState="suspicious", and then hashes the whole file -- a wide window in
// which a still-downloading file can grow past the threshold. Stamping the
// cache with a re-stat's POST-growth mtime/size makes the next scan's
// classifySkipFile (which compares only NeedsRescan/Mtime/Size, never
// LibraryState) skip the file, so it stays flagged suspicious permanently.
// Stamping the size the decision was actually made on leaves a mismatch, the
// next scan re-reads, and the flag clears itself. Partially-written files are
// not hypothetical here: 41.8% of book_file rows have no bytes.
//
// The three causes are counted individually because the fixes differ and the
// volumes differ by orders of magnitude: a stat error is a race with a moving
// file, a lookup error is a store problem, and "no row" is structural --
// saveBookToDatabase returns early without creating a row for a file that
// duplicates an already-version-linked book, so those paths can NEVER acquire a
// cache entry no matter how many times they are scanned. Only the last is
// self-perpetuating, and it selects for exactly the files that are most
// expensive to process.
func writeBackScanCache(filePath string, fi os.FileInfo, scanLog logger.Logger) {
	// Recover guard: getStore() may return a non-nil interface wrapping a nil
	// concrete pointer (happens in tests).
	defer func() {
		if r := recover(); r != nil {
			// Sampled: if getStore() ever returns a non-nil interface wrapping
			// a nil pointer this fires once per file, and an unsampled Warn
			// across a 44k-file library buries everything else in the log.
			warnSampled(&scanCachePanicCount, scanLog, "scan cache update recovered from panic: %v", r)
		}
	}()

	store := getStore()
	if store == nil {
		return
	}

	if fi == nil {
		var statErr error
		fi, statErr = os.Stat(filePath)
		if statErr != nil {
			warnSampled(&scanCacheStatErrCount, scanLog,
				"scan cache write-back skipped for %s: stat failed: %v (file will be re-read next scan)", filePath, statErr)
			return
		}
	}

	dbBook, dbErr := store.GetBookByFilePath(filePath)
	if dbErr != nil {
		warnSampled(&scanCacheLookupErrCount, scanLog,
			"scan cache write-back skipped for %s: book lookup failed: %v (file will be re-read next scan)", filePath, dbErr)
		return
	}
	if dbBook == nil {
		// Not an error, and deliberately not warnSampled: on a library with
		// many duplicate files this is the common case, not the exception. The
		// count lands in the run summary instead.
		scanCacheNoRowCount.Add(1)
		return
	}

	if uerr := store.UpdateScanCache(dbBook.ID, fi.ModTime().Unix(), fi.Size()); uerr != nil {
		// Silent failure meant the file was re-hashed every scan forever (H5).
		warnSampled(&scanCacheUpdateErrCount, scanLog,
			"UpdateScanCache failed for %s: %v (file will be re-hashed next scan)", filePath, uerr)
		return
	}

	// Re-arm NeedsRescan when the file is still inside the rescan-age window.
	//
	// UpdateScanCache CLEARS NeedsRescan by design. Without this, the
	// rescan-age gate would introduce a regression in exactly the population it
	// exists to protect: a file discovered part-way through being written is a
	// cache MISS, so it is read immediately and a row is created from whatever
	// bytes existed at that moment; when the write finishes, the mtime change
	// makes it reasonChanged, and the gate would then defer that half-written
	// row for a full period. Before the gate that case self-healed on the very
	// next scan.
	//
	// reasonDirty is checked before the gate, so NeedsRescan is the one
	// existing mechanism that already means "read this again regardless". This
	// covers both halves -- the row just created for a fresh file, and a
	// re-read of a file that is STILL moving -- because every processed file
	// reaches this same write-back.
	if fi.ModTime().Unix() > rescanFreshCutoff(time.Now(), config.AppConfig.MinRescanAgeHours) {
		if merr := store.MarkNeedsRescan(dbBook.ID); merr != nil {
			// Not silent: losing this leaves a still-changing file gated for a
			// full period on half-written metadata, which is the precise
			// failure the re-arm exists to prevent.
			warnSampled(&scanCacheRearmErrCount, scanLog,
				"MarkNeedsRescan failed for %s: %v (a still-changing file may be deferred by the rescan-age gate)", filePath, merr)
			return
		}
		scanCacheRearmCount.Add(1)
	}
}

// ScanDirectoryParallel scans directory with parallel workers for improved performance.
// If scanLog is nil, a default logger is used.
func ScanDirectoryParallel(ctx context.Context, rootDir string, workers int, scanLog logger.Logger) ([]Book, error) {
	if activeScanner != nil {
		return activeScanner.ScanDirectoryParallel(ctx, rootDir, workers, scanLog)
	}
	if scanLog == nil {
		scanLog = logger.New("scanner")
	}
	if workers < 1 {
		workers = 1
	}

	scanLog.Info("Scanning for audiobook files (using %d workers)...", workers)

	// Collect all directories first
	var dirs []string
	visitedInodes := make(map[uint64]struct{})
	var visitedMu sync.Mutex

	// walkErrCount tallies non-fatal walk/read/stat failures so dropped
	// subtrees are visible in the scan summary instead of silent
	// (audit 2026-07-17 R-5/M8). Atomic: ReadDir failures occur in workers.
	var walkErrCount atomic.Int64

	// dirsFound / dirsScanned / filesScanned drive the liveness checkpoints
	// below. Every phase of this function used to run silently; see the
	// comments at each UpdateProgress call for why that killed the 2026-08-16
	// rescan.
	//
	// Directory counters alone are not sufficient: a library kept as one flat
	// folder of tens of thousands of files has exactly one directory, so the
	// per-directory checkpoints fire once, at the end. filesScanned is what
	// covers that shape.
	var dirsFound atomic.Int64
	var dirsScanned atomic.Int64
	var filesScanned atomic.Int64

	// Operators with unusually slow storage can widen the interval; 0 or unset
	// means the default.
	every := config.AppConfig.ScanProgressEvery
	if every <= 0 {
		every = scanProgressEvery
	}

	registerDirectory := func(path string, info os.FileInfo) bool {
		if info == nil {
			return false
		}
		statInfo, err := os.Stat(path)
		if err != nil {
			walkErrCount.Add(1)
			scanLog.Warn("scan walk: cannot stat %s: %v (subtree skipped)", path, err)
			return false
		}
		if !statInfo.IsDir() {
			return false
		}
		inode, ok := getInode(statInfo)
		if !ok {
			dirs = append(dirs, path)
			return true
		}
		visitedMu.Lock()
		defer visitedMu.Unlock()
		if _, seen := visitedInodes[inode]; seen {
			scanLog.Warn("potential symlink loop detected, skipping already visited directory: %s", path)
			return false
		}
		visitedInodes[inode] = struct{}{}
		dirs = append(dirs, path)
		return true
	}

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == rootDir {
				return err
			}
			walkErrCount.Add(1)
			scanLog.Warn("scan walk: error at %s: %v (subtree skipped)", path, err)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			walkErrCount.Add(1)
			scanLog.Warn("scan walk: cannot read entry info for %s: %v (entry skipped)", path, err)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Symlinked directories are registered for scanning. Non-directory
			// symlinks return false and are intentionally ignored; stat
			// failures are warn-logged + counted inside registerDirectory (M8).
			_ = registerDirectory(path, info)
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".failed" {
				return filepath.SkipDir
			}
			if !registerDirectory(path, info) {
				return filepath.SkipDir
			}
			// Checkpoint during discovery. Until 2026-08-16 this walk ran
			// start-to-finish without a single UpdateProgress, so on a large
			// import root it looked identical to a hung process: the caller
			// had logged "Scanning folder N/M" and then went quiet for as long
			// as the walk took. The stuck-op watchdog kills an operation after
			// ProgressTimeout (5m) of silence, which is exactly how the
			// 2026-08-16 rescan died -- mid-walk of a folder holding 17,469
			// books, with the process demonstrably busy the whole time.
			//
			// The total is genuinely unknown while discovering, so current and
			// total move together; that is the same "growing denominator"
			// convention scanFolder already uses for its per-book counter.
			if n := int(dirsFound.Add(1)); n%every == 0 {
				scanLog.UpdateProgress(n, n, fmt.Sprintf("Discovering folders: %d found (%s)", n, filepath.Base(path)))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Parallel scan of directories
	var mu sync.Mutex
	var books []Book
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, workers)

	for _, dir := range dirs {
		wg.Add(1)
		go func(scanDir string) {
			defer wg.Done()
			semaphore <- struct{}{} // Acquire
			defer func() {
				<-semaphore // Release
				// Checkpoint per completed directory. This is the long phase
				// -- it stats and group-detects every audio file under each
				// directory -- and it reported nothing at all until
				// 2026-08-16. Unlike the discovery walk above, the denominator
				// is known here, so this is real progress rather than a
				// growing count.
				if n := int(dirsScanned.Add(1)); n%every == 0 || n == len(dirs) {
					scanLog.UpdateProgress(n, len(dirs),
						fmt.Sprintf("Scanning folders: %d/%d (%s)", n, len(dirs), filepath.Base(scanDir)))
				}
			}()

			// Read directory entries
			entries, err := os.ReadDir(scanDir)
			if err != nil {
				walkErrCount.Add(1)
				scanLog.Warn("scan walk: cannot read directory %s: %v (directory skipped)", scanDir, err)
				return
			}

			// Collect all supported audio files in this directory
			var audioFiles []string
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				path := filepath.Join(scanDir, entry.Name())
				if isExcludedPath(path) {
					continue
				}
				ext := strings.ToLower(filepath.Ext(path))
				if slices.Contains(config.AppConfig.SupportedExtensions, ext) {
					audioFiles = append(audioFiles, path)
				}
			}

			// Group files into logical books using album tags.
			//
			// The per-file checkpoint is what keeps a single-huge-directory
			// library alive: with one directory the per-directory counters
			// above fire once, at the very end, so this callback is the only
			// thing reporting during what may be hours of tag reading.
			localBooks := groupFilesIntoBooks(ctx, audioFiles, func() {
				if n := int(filesScanned.Add(1)); n%every == 0 {
					scanLog.UpdateProgress(n, n,
						fmt.Sprintf("Reading tags: %d files (%s)", n, filepath.Base(scanDir)))
				}
			})

			// Merge results
			if len(localBooks) > 0 {
				mu.Lock()
				books = append(books, localBooks...)
				mu.Unlock()
			}
		}(dir)
	}

	wg.Wait()

	// Surface dropped-subtree count in the scan summary (R-5): each failure was
	// warn-logged above, but a single aggregate makes a mass-failure obvious.
	if n := walkErrCount.Load(); n > 0 {
		scanLog.Warn("scan walk summary: walk_errors=%d (directories or entries skipped due to I/O errors; library may be undercounted)", n)
	}

	// Prevention post-pass: groupFilesIntoBooks runs per-leaf-dir, so a book laid
	// out as one chapter per "<prefix> - N" subdir shatters into one book per
	// chapter. Coalesce those siblings into one multi-file book before persisting.
	// Path-based + prefix⊆parent guard; gated OFF by default (see CoalesceShatteredSiblings).
	if config.AppConfig.CoalesceShatteredSiblings {
		before := len(books)
		books = coalesceShatteredSiblings(ctx, books)
		if len(books) != before {
			logging.Info(ctx, "scanner shattered-sibling coalesce", "books_before", before, "books_after", len(books))
		}
	}

	return books, nil
}

// ProcessBooks processes the discovered books to extract metadata and identify series.
// If scanLog is nil, a default logger is used.
func ProcessBooks(books []Book, scanLog logger.Logger) error {
	if activeScanner != nil {
		return activeScanner.ProcessBooks(books, scanLog)
	}
	return ProcessBooksParallel(context.Background(), books, config.AppConfig.ConcurrentScans, nil, scanLog)
}

// ProcessBooksParallel processes books with parallel workers for improved performance.
// If scanLog is nil, a default logger is used.
func ProcessBooksParallel(ctx context.Context, books []Book, workers int, progressFn func(processed int, total int, bookPath string), scanLog logger.Logger) error {
	if activeScanner != nil {
		return activeScanner.ProcessBooksParallel(ctx, books, workers, progressFn, scanLog)
	}
	if scanLog == nil {
		scanLog = logger.New("scanner")
	}
	if workers < 1 {
		workers = 1
	}

	scanLog.Info("Processing audiobook metadata (using %d workers)...", workers)

	total := len(books)
	scanLog.Info("scan started: %d total files", total)

	// Snapshot swallowed-store-failure counters so the completion summary can
	// report this run's delta (audit 2026-07-17 H5).
	dupLookupErrStart := dupLookupErrCount.Load()
	dupLookupSkipStart := dupLookupSkipCount.Load()
	scanCacheErrStart := scanCacheUpdateErrCount.Load()
	scanFailCountErrStart := scanFailCountErrCount.Load()
	scanCacheStatErrStart := scanCacheStatErrCount.Load()
	scanCacheLookupErrStart := scanCacheLookupErrCount.Load()
	scanCacheNoRowStart := scanCacheNoRowCount.Load()
	scanCachePanicStart := scanCachePanicCount.Load()
	scanCacheRearmStart := scanCacheRearmCount.Load()
	scanCacheRearmErrStart := scanCacheRearmErrCount.Load()
	skipUnchangedStart := skipUnchangedCount.Load()
	skipTooFreshStart := skipTooFreshCount.Load()
	readCacheMissStart := readCacheMissCount.Load()
	readChangedStart := readChangedCount.Load()
	readDirtyStart := readDirtyCount.Load()
	readStatErrStart := readStatErrCount.Load()
	readCacheOffStart := readCacheOffCount.Load()

	// Computed ONCE for the run, not per file: a cutoff that drifts while the
	// scan walks 40k files would make the gate's verdict depend on where in the
	// run a file happened to land.
	freshCutoff := rescanFreshCutoff(time.Now(), config.AppConfig.MinRescanAgeHours)
	if config.AppConfig.MinRescanAgeHours > 0 {
		scanLog.Info("rescan-age gate active: a changed file is re-read only once its mtime is older than %dh",
			config.AppConfig.MinRescanAgeHours)
	} else {
		scanLog.Info("rescan-age gate disabled (min_rescan_age_hours=%d): every changed file is re-read",
			config.AppConfig.MinRescanAgeHours)
	}

	// progressCh serializes progress updates so callbacks and progress output
	// are handled in a single goroutine.
	progressCh := make(chan string, len(books))
	var progressWG sync.WaitGroup

	progressWG.Go(func() {
		processed := 0
		for path := range progressCh {
			processed++
			if progressFn != nil {
				progressFn(processed, total, path)
			}
			if processed%100 == 0 || processed == total {
				scanLog.Info("scan progress: %d/%d files processed", processed, total)
			}
		}
		scanLog.Info("scan complete: %d files processed", total)
	})

	// Build the AI fallback parser from the configured LLM backend. The routing
	// logic, and the incident that produced it, live on newAIParser in
	// ai_parse_async.go -- shared with the queued library.ai-parse operation so
	// the two paths cannot drift apart.
	aiParser, aiEnabled := newAIParser(scanLog)

	// Resolved once here rather than per book: the nomination gate runs inside
	// the worker loop and this would otherwise be an extra store read for every
	// book in the library. nil means the placeholder author has never been
	// created in this database, in which case no row can point at it and the
	// gate needs no exception.
	//
	// Guarded on aiEnabled because the gate is the only reader: a scan with AI
	// parsing switched off must not pay for a store read it will never consult.
	var placeholders *placeholderAuthors
	if aiEnabled {
		placeholders = newPlaceholderAuthors()
	}

	// Track books needing AI parsing for batch processing
	var aiCandidates []int
	var aiCandidatesMu sync.Mutex

	// Worker pool for parallel processing
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, workers)
	errChan := make(chan error, len(books))
	var ctxErr error

	for i := range books {
		// Check context cancellation before starting new work
		if ctx.Err() != nil {
			ctxErr = ctx.Err()
			break
		}

		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			semaphore <- struct{}{} // Acquire
			defer func() {
				<-semaphore // Release
				progressCh <- books[idx].FilePath
			}()

			// Check context cancellation at start of each worker
			if ctx.Err() != nil {
				return
			}

			// Incremental skip check: if mtime+size unchanged and no rescan flag, skip.
			// Every branch records why, so the completion summary can report the
			// skip rate and which re-read reason dominated.
			{
				globalScanCacheMu.RLock()
				cache := globalScanCache
				globalScanCacheMu.RUnlock()
				// Decide over EVERY file this book owns, not just its
				// representative one. Until 2026-08-24 this consulted
				// books[idx].FilePath alone, so a 6-file book whose segment 5
				// changed was skipped whole because segment 1 had not: the
				// changed audio never reached the row, and the duration and
				// chapter aggregates kept describing the previous contents.
				//
				// The file list comes from the walk, so it is correct even
				// before the per-file scan cache lands. Note what it does NOT
				// change on its own: a multi-file book's segments have no
				// cache entry today (the cache is keyed per BOOK), so they
				// report cacheMiss and the book is re-read exactly as it is
				// now. That is the grain mismatch, and closing it is the
				// per-file cache's job, not this rollup's.
				skip, reason := classifySkipBook(scanFileSetFor(&books[idx]), cache, freshCutoff, os.Stat)
				recordSkipDecision(reason)
				if skip {
					return // progress deferred func will still fire
				}
			}

			// Extract metadata from the file. For multi-file books where the filename
			// is a generic part number (e.g. "01 Part 1 of 67.mp3"), use folder path
			// hierarchy combined with first-file tags for richer metadata.
			fallbackUsed := false
			// Set when this book is handed to the AI filename-parsing phase.
			// It defers this book's scan-cache stamp; see the stamp site below.
			nominatedForAI := false
			filePath := books[idx].FilePath

			// Suspicious-file guard: single files below MinBookSizeBytes skip heavy processing.
			if threshold := config.AppConfig.MinBookSizeBytes; threshold > 0 {
				if fi, statErr := os.Stat(filePath); statErr == nil && !fi.IsDir() && fi.Size() < threshold {
					extractInfoFromPath(&books[idx])
					books[idx].LibraryState = "suspicious"
					if saveErr := saveBook(ctx, &books[idx]); saveErr != nil {
						scanLog.Warn("failed to save suspicious book %s: %v", filePath, saveErr)
					}
					scanLog.Warn("suspicious file (%d bytes, threshold %d): %s", fi.Size(), threshold, filePath)
					writeBackScanCache(filePath, fi, scanLog)
					return
				}
			}

			// Handle directory-based books (multi-file books grouped by album tag)
			if info, statErr := os.Stat(filePath); statErr == nil && info.IsDir() {
				dirPath := filePath
				firstFile := metadata.FindFirstAudioFile(dirPath, config.AppConfig.SupportedExtensions)
				if firstFile == "" {
					return // No audio files found in directory
				}
				fileCount := countAudioFilesInDir(dirPath, config.AppConfig.SupportedExtensions)
				bm, bmErr := metadata.AssembleBookMetadata(dirPath, firstFile, fileCount, 0)
				if bmErr == nil {
					if bm.Title != "" {
						books[idx].Title = bm.Title
					}
					if bm.PrimaryAuthor() != "" {
						books[idx].Author = bm.PrimaryAuthor()
					}
					if bm.Narrator != "" {
						books[idx].Narrator = bm.Narrator
					}
					if bm.Language != "" {
						books[idx].Language = bm.Language
					}
					if bm.Publisher != "" {
						books[idx].Publisher = bm.Publisher
					}
					if bm.SeriesName != "" {
						books[idx].Series = bm.SeriesName
					}
					if bm.SeriesPosition > 0 {
						books[idx].Position = bm.SeriesPosition
					}
				}
				// Compute hash from first file for dedup
				if h, herr := ComputeFileHash(firstFile); herr == nil {
					books[idx].FileHash = h
				}
				// Fallback to filepath extraction if title/author still unknown
				if books[idx].Title == "" || books[idx].Author == "" {
					extractInfoFromPath(&books[idx])
				}
				if books[idx].Position <= 0 {
					books[idx].Position = metadata.DetectVolumeNumber(books[idx].Title)
				}
				series, position := matcher.IdentifySeries(books[idx].Title, books[idx].FilePath)
				if books[idx].Series == "" && series != "" {
					books[idx].Series = series
				}
				if books[idx].Position == 0 && position > 0 {
					books[idx].Position = position
				}
				// Save the book and create segments
				if err := saveBook(ctx, &books[idx]); err != nil {
					errChan <- fmt.Errorf("failed to save book %s: %w", books[idx].FilePath, err)
				} else {
					// dirPath is a directory, so the normalization branch
					// (guarded by !info.IsDir()) does not fire and the row does
					// not move -- discarding the result is correct here, not an
					// oversight. Deliberately "does not" rather than "cannot":
					// the guard is evaluated against a fresh stat, so if this
					// call is ever changed to pass a FILE the discard becomes a
					// silent bug. TestProcessBooksParallelDirectoryBookKeepsItsPath
					// pins the current behaviour.
					_ = createBookFilesForBook(dirPath, nil, scanLog, normalizeToDirectory)
					// Chapters must be persisted AFTER the book files exist —
					// the multi-file synthesis path reads BookFile durations to
					// build the cumulative timeline. Never fatal to the scan.
					if err := PersistChaptersForBook(ctx, dirPath, scanLog); err != nil {
						scanLog.Warn("chapter persistence failed for %s: %v", dirPath, err)
					}
				}
				return // Done with this directory-based book
			}

			// CONS-17 (Path B): a sequential multi-file group (SegmentFiles>1,
			// detected at the grouping stage) carries FilePath=segs[0], a single
			// chapter file. Without this, it would fall to the per-file ProcessFile
			// path below and take its title from one chapter's tags — chapter
			// titles ("Chapter 1", "Part 1") then leak into and collide across
			// Book.Title. Route it through AssembleBookMetadata (folder preference)
			// exactly like generically-named part files; the segments are still
			// created from SegmentFiles at the saveBook step. This mirrors the
			// album-grouped multi-file path, which already routes via the folder.
			if metadata.IsGenericPartFilename(filePath) || len(books[idx].SegmentFiles) > 1 {
				dirPath := filepath.Dir(filePath)
				firstFile := metadata.FindFirstAudioFile(dirPath, config.AppConfig.SupportedExtensions)
				if firstFile == "" {
					firstFile = filePath
				}
				fileCount := countAudioFilesInDir(dirPath, config.AppConfig.SupportedExtensions)
				bm, bmErr := metadata.AssembleBookMetadata(dirPath, firstFile, fileCount, 0)
				if bmErr != nil {
					scanLog.Warn("AssembleBookMetadata failed for %s: %v", dirPath, bmErr)
					fallbackUsed = true
				} else {
					if bm.Title != "" {
						books[idx].Title = bm.Title
					}
					if bm.PrimaryAuthor() != "" {
						books[idx].Author = bm.PrimaryAuthor()
					}
					if bm.Narrator != "" {
						books[idx].Narrator = bm.Narrator
					}
					if bm.Language != "" {
						books[idx].Language = bm.Language
					}
					if bm.Publisher != "" {
						books[idx].Publisher = bm.Publisher
					}
					if bm.SeriesName != "" {
						books[idx].Series = bm.SeriesName
					}
					if bm.SeriesPosition > 0 {
						books[idx].Position = bm.SeriesPosition
					}
					fallbackUsed = bm.Title == "" || bm.PrimaryAuthor() == ""
				}
			} else {
				// Single-pass extraction: open file once for tags + mediainfo + hash.
				// Bounded: ProcessFile's chain is uncancellable syscalls, and a
				// malformed container stalled prod's scan on the same file for 3
				// days. A timeout arrives here as an ordinary pfErr and takes the
				// existing fallback + fail-count path below.
				meta, mi, fileHash, pfErr := ProcessFileWithTimeout(ctx, filePath)
				if pfErr != nil {
					scanLog.Warn("ProcessFile failed for %s: %v", filePath, pfErr)
					fallbackUsed = true
					if gs := getStore(); gs != nil {
						sum := sha256.Sum256([]byte(filePath))
						if _, ierr := gs.IncrScanFailCount(fmt.Sprintf("%x", sum[:8])); ierr != nil {
							// Silent failure disabled the auto-quarantine escalation path (H5).
							warnSampled(&scanFailCountErrCount, scanLog, "IncrScanFailCount failed for %s: %v", filePath, ierr)
						}
					}
				} else {
					// Reset fail counter on successful parse so transient failures
					// don't accumulate toward the auto-quarantine threshold.
					// Use recover guard: GetGlobalStore may return a non-nil interface
					// wrapping a nil concrete pointer in tests.
					func() {
						defer func() { recover() }() //nolint:errcheck
						if gs := getStore(); gs != nil {
							sum := sha256.Sum256([]byte(filePath))
							_ = gs.ResetScanFailCount(fmt.Sprintf("%x", sum[:8]))
						}
					}()
					if meta != nil {
						fallbackUsed = meta.UsedFilenameFallback
						if meta.Title != "" {
							books[idx].Title = meta.Title
						}
						if meta.Artist != "" {
							books[idx].Author = meta.Artist
						}
						if meta.Narrator != "" {
							books[idx].Narrator = meta.Narrator
						}
						if meta.Language != "" {
							books[idx].Language = meta.Language
						}
						if meta.Publisher != "" {
							books[idx].Publisher = meta.Publisher
						}
						if meta.Series != "" {
							books[idx].Series = meta.Series
						}
						if meta.SeriesIndex > 0 {
							books[idx].Position = meta.SeriesIndex
						}
						// Propagate custom organizer tags for re-linking
						if meta.BookOrganizerID != "" {
							books[idx].BookOrganizerID = meta.BookOrganizerID
						}
						if meta.ASIN != "" {
							books[idx].ASIN = meta.ASIN
						}
						if meta.OpenLibraryID != "" {
							books[idx].OpenLibraryID = meta.OpenLibraryID
						}
						if meta.HardcoverID != "" {
							books[idx].HardcoverID = meta.HardcoverID
						}
						if meta.GoogleBooksID != "" {
							books[idx].GoogleBooksID = meta.GoogleBooksID
						}
					}
					if mi != nil {
						if mi.Format != "" {
							books[idx].Format = "." + strings.TrimPrefix(strings.ToLower(mi.Format), ".")
						}
						if mi.Duration > 0 {
							books[idx].Duration = mi.Duration
						}
					}
					books[idx].FileHash = fileHash
				}
			}

			// Mark books needing AI parsing for batch processing later.
			// AI only fills EMPTY fields (title, author, series, narrator, publisher),
			// so if the DB already has title+author from a previous scan, re-running AI
			// would be a no-op. Skip to avoid thousands of redundant API calls on rescan.
			if aiEnabled && (fallbackUsed || books[idx].Title == "" || books[idx].Author == "" || books[idx].Series == "") {
				needsAI := true
				if getStore() != nil {
					if dbExisting, dbErr := getStore().GetBookByFilePath(books[idx].FilePath); dbErr == nil && dbExisting != nil {
						// The placeholder is a real author row with a real,
						// non-zero ID, so the bare non-nil check below used to
						// pass for a book whose author is precisely unknown --
						// which is the one case AI parsing exists to fix.
						if dbExisting.Title != "" && rowHasRealAuthor(dbExisting.AuthorID, placeholders) {
							needsAI = false
						}
					}
				}
				if needsAI {
					nominatedForAI = true
					aiCandidatesMu.Lock()
					aiCandidates = append(aiCandidates, idx)
					aiCandidatesMu.Unlock()
				}
			}

			// Fallback to filepath extraction if title/author still unknown
			if books[idx].Title == "" || books[idx].Author == "" {
				extractInfoFromPath(&books[idx])
			}

			if books[idx].Position <= 0 {
				books[idx].Position = metadata.DetectVolumeNumber(books[idx].Title)
			}

			// Identify series based on title and filepath
			series, position := matcher.IdentifySeries(books[idx].Title, books[idx].FilePath)
			if books[idx].Series == "" && series != "" {
				books[idx].Series = series
			}
			if books[idx].Position == 0 && position > 0 {
				books[idx].Position = position
			}

			// Check cancellation before saving
			if ctx.Err() != nil {
				return
			}

			// Save to database (database operations are thread-safe)
			if err := saveBook(ctx, &books[idx]); err != nil {
				errChan <- fmt.Errorf("failed to save book %s: %w", books[idx].FilePath, err)
			} else {
				// Create segments for multi-file books grouped by album.
				// Pass SegmentHashes (populated by saveBookToDatabase dedup loop)
				// to avoid re-hashing each segment file (PERF-2b).
				if len(books[idx].SegmentFiles) > 1 {
					// Keep the in-memory book in step with the row. This call
					// can move the stored FilePath to the containing directory,
					// and BOTH consumers below look the book up BY PATH:
					// PersistChaptersForBook returns silently on a miss (so
					// chapters were never persisted at all) and writeBackScanCache
					// counts a miss as "no book row" (so the book never acquired a
					// scan-cache entry). Measured on prod 2026-08-24. Note this
					// does NOT yet make the next scan SKIP the book -- see the
					// SCOPE paragraph on createBookFilesForBook for the two
					// grain mismatches that still defeat that.
					if moved := createBookFilesForBook(books[idx].FilePath, books[idx].SegmentFiles, scanLog, normalizeToDirectory, books[idx].SegmentHashes); moved != "" {
						books[idx].FilePath = moved
					}
				} else {
					// GENUINELY SINGLE-FILE BOOKS. This branch did not exist, so
					// createBookFilesForBook was never called for them and they
					// got no book_file row at all -- the gap
					// ensureSingleFileBookFile backfills from the organize hook.
					//
					// It became load-bearing when the scan cache moved to
					// book_file rows: GetScanCacheMap now iterates book_file:*
					// and UpdateBookFileScanCache resolves a row BY PATH, so a
					// book with no file row has no cache entry to read and no
					// row to stamp. Every single-file book would be re-read and
					// re-hashed on every scan, forever -- the same defect the
					// per-file cache was built to fix, arriving through the
					// other door.
					//
					// keepFilePath, NOT normalizeToDirectory: a single-file
					// book's row belongs at its file. Normalizing it to the
					// parent directory is wrong on its own terms (two books can
					// share a directory) and would also undo the thing that
					// makes these books work today.
					createSingleFileBookFile(&books[idx], scanLog)
				}
				// Persist chapters. Deliberately OUTSIDE the SegmentFiles>1 block
				// so it also runs for genuinely-single-file books, whose embedded
				// m4b chapter marks are the primary source. Never fatal to the scan.
				if err := PersistChaptersForBook(ctx, books[idx].FilePath, scanLog); err != nil {
					scanLog.Warn("chapter persistence failed for %s: %v", books[idx].FilePath, err)
				}
				// Update scan cache so next incremental scan skips this file.
				//
				// EXCEPT for books handed to the AI filename-parsing phase. The
				// stamp means "this file is fully processed, skip it next time",
				// and for an AI candidate that is a promise, not a fact: the
				// parse now happens in a queued operation that can be dropped on
				// restart, aborted by a permanent LLM failure, or stopped by the
				// batch failure threshold. Stamping here would make the next scan
				// skip the file at classifySkipFile -- which returns BEFORE the
				// nomination check above -- so the book would never be
				// re-nominated and would keep its empty fields permanently, with
				// a healthy skip rate in the log.
				//
				// So the AI phase owns the stamp for its own candidates and
				// writes it once a parse has been ATTEMPTED for the book (see
				// runAIBatchPhase), whatever the parse returned. Attempted, not
				// succeeded: a filename the LLM legitimately cannot parse must
				// still stop being re-read, or it churns every scan forever.
				// Anything that stops the phase short leaves the stamp unwritten,
				// the next scan re-reads the file, and the nomination gate --
				// which clears itself once the DB row has a title and an author
				// -- decides whether to queue it again.
				if !nominatedForAI {
					writeBackScanCache(books[idx].FilePath, nil, scanLog)
				}
			}
		}(i)
	}

	wg.Wait()
	close(progressCh)
	progressWG.Wait()
	close(errChan)

	// Collect any errors
	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		scanLog.Warn("%d books failed to save", len(errs))
	}

	// Per-run store-failure summary (H5): once per run, not per file.
	if d := dupLookupErrCount.Load() - dupLookupErrStart; d > 0 {
		scanLog.Warn("scan summary: %d duplicate-detection hash lookups failed (store errors)", d)
	}
	if d := dupLookupSkipCount.Load() - dupLookupSkipStart; d > 0 {
		scanLog.Warn("scan summary: %d files skipped because duplicate status was undeterminable (store errors during hash lookup)", d)
	}
	if d := scanCacheUpdateErrCount.Load() - scanCacheErrStart; d > 0 {
		scanLog.Warn("scan summary: %d scan-cache updates failed (affected files will be re-hashed next scan)", d)
	}
	if d := scanFailCountErrCount.Load() - scanFailCountErrStart; d > 0 {
		scanLog.Warn("scan summary: %d scan-fail-count increments failed", d)
	}
	if d := scanCacheStatErrCount.Load() - scanCacheStatErrStart; d > 0 {
		scanLog.Warn("scan summary: %d scan-cache write-backs skipped because os.Stat failed", d)
	}
	if d := scanCacheLookupErrCount.Load() - scanCacheLookupErrStart; d > 0 {
		scanLog.Warn("scan summary: %d scan-cache write-backs skipped because the book lookup failed (store errors)", d)
	}
	if d := scanCacheNoRowCount.Load() - scanCacheNoRowStart; d > 0 {
		// Not a store failure: these paths have no book row at all, so there is
		// nowhere to record that they were scanned. They will be re-read and
		// re-hashed on EVERY scan until either a row exists for them or the
		// scan cache learns to key on paths rather than book rows.
		scanLog.Warn("scan summary: %d scan-cache write-backs skipped because no book row exists at the path "+
			"(these files are re-read and re-hashed on every scan)", d)
	}
	if d := scanCachePanicCount.Load() - scanCachePanicStart; d > 0 {
		scanLog.Warn("scan summary: %d scan-cache write-backs recovered from a panic", d)
	}
	if d := scanCacheRearmErrCount.Load() - scanCacheRearmErrStart; d > 0 {
		scanLog.Warn("scan summary: %d NeedsRescan re-arms failed (those files may be deferred by the rescan-age gate "+
			"on half-written metadata)", d)
	}
	if d := scanCacheRearmCount.Load() - scanCacheRearmStart; d > 0 {
		scanLog.Info("scan summary: %d files re-armed for rescan because they are still inside the rescan-age window", d)
	}

	// Skip-rate summary. Logged UNCONDITIONALLY, unlike the error counters
	// above: those are silent when nothing went wrong, but the pathology this
	// instrument exists to catch is a scan that skipped NOTHING, and a `d > 0`
	// guard would print nothing in exactly that case -- the same silence the
	// counters were added to remove. A zero here is a finding, not a non-event.
	unchanged := skipUnchangedCount.Load() - skipUnchangedStart
	tooFresh := skipTooFreshCount.Load() - skipTooFreshStart
	skipped := unchanged + tooFresh
	reRead := struct{ cacheMiss, changed, dirty, statErr, cacheOff int64 }{
		cacheMiss: readCacheMissCount.Load() - readCacheMissStart,
		changed:   readChangedCount.Load() - readChangedStart,
		dirty:     readDirtyCount.Load() - readDirtyStart,
		statErr:   readStatErrCount.Load() - readStatErrStart,
		cacheOff:  readCacheOffCount.Load() - readCacheOffStart,
	}
	reReadTotal := reRead.cacheMiss + reRead.changed + reRead.dirty + reRead.statErr + reRead.cacheOff
	if decided := skipped + reReadTotal; decided > 0 {
		// tooFresh is broken out of the skip total rather than folded into it:
		// it is the only skip reason that represents deferred work rather than
		// work correctly avoided, so a run where it dominates means something
		// is churning the library, not that the cache is doing its job.
		scanLog.Info("scan summary: %d/%d files skipped (%.1f%%) = %d unchanged, %d too-fresh; "+
			"re-read %d = %d cache-miss, %d changed, %d forced-rescan, %d stat-error, %d cache-disabled",
			skipped, decided, float64(skipped)*100/float64(decided), unchanged, tooFresh, reReadTotal,
			reRead.cacheMiss, reRead.changed, reRead.dirty, reRead.statErr, reRead.cacheOff)
	}

	// Hand the AI candidates to the background queue if one is wired, and only
	// parse them inline when it is not.
	//
	// Deliberately ABOVE the ctxErr return below. Enqueuing is a short database
	// write on a detached context, so it is safe on a cancelled scan -- and a
	// cancelled scan is exactly when this matters: ctxErr is set whenever
	// cancellation lands while the dispatch loop is still running, which is the
	// common shape, and returning first would silently drop every candidate the
	// scan had nominated so far. It stays below wg.Wait(), because it reads
	// books and aiCandidates, which the workers write.
	//
	// Inline is the fallback, not the preference: this phase is a sequence of LLM
	// round trips with a 2s delay between batches, and it runs while the scan
	// still holds the "library.scan" ConcurrencyKey. On a library-sized scan that
	// is the difference between a scan bounded by disk speed and one bounded by
	// the LLM. Enqueuing lets the scan finish and the parsing drain behind it.
	//
	// An enqueue failure falls back to inline rather than dropping the work. That
	// restores the old blocking behaviour, which is bad but visible; silently
	// skipping the AI phase would leave books permanently unparsed with nothing
	// in the log tying it to the queue.
	if aiEnabled && len(aiCandidates) > 0 {
		queued, err := enqueueAIParse(ctx, books, aiCandidates, scanLog)
		switch {
		case err == nil:
			// Queued. The operation saves its own results and stamps the scan
			// cache for each book it attempts.
		case errors.Is(err, ErrAIParseEnqueueUnavailable):
			runAIBatchPhase(ctx, aiParser, books, aiCandidates, scanLog, saveBookAndReportPath)
		default:
			// Only the candidates that were NOT accepted. The chunks already
			// queued cannot be recalled, and parsing them inline as well would
			// pay for every one of those books twice at the LLM.
			remaining := aiCandidates[min(queued, len(aiCandidates)):]
			scanLog.Warn("failed to queue AI parsing after %d of %d candidate(s) (%v); parsing the remaining %d inline",
				queued, len(aiCandidates), err, len(remaining))
			if len(remaining) > 0 {
				runAIBatchPhase(ctx, aiParser, books, remaining, scanLog, saveBookAndReportPath)
			}
		}
	}

	if ctxErr != nil {
		return ctxErr
	}

	// After processing all books, try to match series using external APIs for uncertain cases
	if err := identifySeriesUsingExternalAPIs(books); err != nil {
		scanLog.Warn("Error identifying series using external APIs: %v", err)
	}

	return nil
}

// extractInfoFromPath tries to extract author and title information from the file path
func extractInfoFromPath(book *Book) {
	// The organizer files a book with no resolvable author under a placeholder
	// directory, and its naming scheme puts the author in the filename too --
	// so an organized authorless book is literally named
	// "<title> - Unknown Author.mp3". Parsing that back out launders the
	// system's own "I don't know" into an author indistinguishable from one a
	// human supplied, and the resulting non-nil AuthorID then closes the AI
	// nomination gate below, so the book can never be re-parsed. Measured on
	// production 2026-08-25: 3,407 books sat behind that closed gate.
	//
	// Cleared on a defer rather than at each assignment because this function
	// returns from several branches; a guard per branch is one a future branch
	// can miss. The title half of the parse is still wanted, so only the author
	// is dropped.
	defer func() {
		if authorname.IsPlaceholder(book.Author) {
			book.Author = ""
		}
	}()

	path := book.FilePath

	// Remove the extension
	baseName := filepath.Base(path)
	baseName = strings.TrimSuffix(baseName, filepath.Ext(baseName))

	// Remove leading track/chapter numbers
	parts := strings.Split(baseName, " ")
	if len(parts) > 0 {
		if _, err := strconv.Atoi(parts[0]); err == nil {
			baseName = strings.Join(parts[1:], " ")
		}
	}
	baseName = strings.TrimSpace(baseName)

	// Remove chapter info from end
	re := regexp.MustCompile(`(?i)[-_]\d+\s+Chapter\s+\d+$`)
	baseName = re.ReplaceAllString(baseName, "")

	// Try underscore separator first
	if strings.Contains(baseName, "_") && !strings.Contains(baseName, " - ") {
		parts := strings.SplitN(baseName, "_", 2)
		if len(parts) == 2 {
			left := strings.TrimSpace(parts[0])
			right := strings.TrimSpace(parts[1])
			leftIsName := looksLikePersonName(left)
			rightIsName := looksLikePersonName(right)
			if rightIsName && !leftIsName && book.Author == "" {
				book.Author = right
				book.Title = left
				return
			} else if leftIsName && !rightIsName && book.Author == "" {
				book.Author = left
				book.Title = right
				return
			} else if leftIsName && rightIsName && book.Author == "" {
				leftIsTitle := looksLikeTitleCandidate(left)
				rightIsTitle := looksLikeTitleCandidate(right)
				if leftIsTitle && !rightIsTitle {
					book.Author = right
					book.Title = left
					return
				}
				if rightIsTitle && !leftIsTitle {
					book.Author = left
					book.Title = right
					return
				}
			}
		}
	}

	// Try to parse "Title - Author" or "Author - Title" patterns from filename
	if strings.Contains(baseName, " - ") {
		title, author := parseFilenameForAuthor(baseName)
		if author != "" && book.Author == "" {
			book.Author = author
			book.Title = title
		} else {
			// Fallback to old behavior: treat as "Series - Title"
			parts := strings.Split(baseName, " - ")
			if len(parts) > 1 {
				book.Title = strings.TrimSpace(parts[len(parts)-1])
				if book.Series == "" {
					book.Series = strings.TrimSpace(parts[0])
				}
			} else {
				book.Title = baseName
			}
		}
	} else {
		book.Title = baseName
	}

	// NOTE: the placeholder is deliberately NOT cleared here, before this
	// fallback -- only on the defer at the top, after this has run.
	//
	// Clearing it first looks like an improvement (a book at
	// ".../Terry Pratchett/Mort - Unknown Author.mp3" would recover its author
	// from the directory) and is actively harmful under the organizer's own
	// layout, which is <root>/<author>/<title>/<file>. The IMMEDIATE parent is
	// then the TITLE, so opening this fallback yields
	// Author = "Pratchett 036" -- the book's own title as its author. That is
	// strictly worse than the placeholder: it still closes the AI nomination
	// gate, and unlike the placeholder nothing downstream can recognise it as
	// junk. Measured 2026-08-25; the guard test below pins it.
	//
	// The directory fallback becomes safe once
	// todo.d/20260825-directory-fallback-reads-title-as-author.md is fixed.
	if book.Author == "" {
		book.Author = extractAuthorFromDirectory(path)
	}

	if book.Position <= 0 {
		book.Position = metadata.DetectVolumeNumber(book.Title)
	}
}

// extractAuthorFromDirectory extracts author from directory with validation
func extractAuthorFromDirectory(filePath string) string {
	dirs := strings.Split(filepath.Dir(filePath), string(os.PathSeparator))
	if len(dirs) == 0 {
		return ""
	}

	dirName := dirs[len(dirs)-1]

	// Skip common non-author directory names
	skipDirs := map[string]bool{
		// The organizer's own placeholder directory. Reading it back as an author
		// is what made an authorless book look authored and locked it out of AI
		// re-parsing; see internal/authorname.
		strings.ToLower(authorname.Placeholder): true,

		"books": true, "audiobooks": true, "newbooks": true, "downloads": true,
		"media": true, "audio": true, "library": true, "collection": true,
		"import": true, "imports": true, "organized": true,
		"bt": true, "incomplete": true, "data": true,
	}

	if skipDirs[strings.ToLower(dirName)] {
		return ""
	}

	// Handle "Author, Co-Author - translator - Title" patterns
	if strings.Contains(dirName, " - translator - ") || strings.Contains(dirName, " - narrated by - ") {
		re := regexp.MustCompile(`^([^-]+)\s*-\s*(?:translator|narrated by)\s*-`)
		matches := re.FindStringSubmatch(dirName)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}

	// Extract from "Author - Title" directory pattern
	if strings.Contains(dirName, " - ") {
		parts := strings.SplitN(dirName, " - ", 2)
		if len(parts) > 0 {
			author := strings.TrimSpace(parts[0])
			if isValidAuthor(author) {
				return author
			}
		}
	}

	// Use directory name if valid
	if isValidAuthor(dirName) {
		return dirName
	}

	return ""
}

// isValidAuthor checks if extracted author string is valid
func isValidAuthor(author string) bool {
	if author == "" {
		return false
	}

	lower := strings.ToLower(author)

	// Skip invalid patterns
	if strings.HasPrefix(lower, "book") || strings.HasPrefix(lower, "chapter") ||
		strings.HasPrefix(lower, "part") || strings.HasPrefix(lower, "vol") ||
		strings.HasPrefix(lower, "volume") || strings.HasPrefix(lower, "disc") {
		return false
	}

	// Skip purely numeric
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}

	// Skip chapter patterns
	if strings.HasPrefix(lower, "chapter ") {
		return false
	}

	return true
} // parseFilenameForAuthor attempts to intelligently parse title and author from filename
// Handles patterns like "Title - Author" or "Author - Title"
// Returns (title, author) where author is empty string if pattern not detected
func parseFilenameForAuthor(filename string) (string, string) {
	parts := strings.Split(filename, " - ")
	if len(parts) != 2 {
		return "", "" // Not a simple two-part pattern
	}

	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])

	// Heuristic: check if right side looks like an author name
	rightIsName := looksLikePersonName(right)
	leftIsName := looksLikePersonName(left)

	if rightIsName && !leftIsName {
		// Pattern: "Title - Author"
		return left, right
	} else if leftIsName && !rightIsName {
		// Pattern: "Author - Title"
		return right, left
	} else if rightIsName {
		// Both could be names, prefer "Title - Author" pattern
		return left, right
	}

	// Couldn't determine, return empty author
	return "", ""
}

// looksLikePersonName checks if a string looks like a person's name
// Looks for patterns like "John Smith", "J. Smith", "J. K. Rowling"
func looksLikePersonName(s string) bool {
	if !isValidAuthor(s) {
		return false
	}

	// Check for initials like "J. K. Rowling" or "J.K. Rowling"
	if strings.Contains(s, ".") {
		words := strings.Fields(s)
		if len(words) > 1 {
			initials := 0
			nonInitials := 0
			for _, word := range words {
				if isInitialToken(word) {
					initials++
					continue
				}
				nonInitials++
			}
			if nonInitials > 0 || initials >= 2 {
				return true
			}
		}
	}

	// Check for multi-word names with proper capitalization
	words := strings.Fields(s)
	if len(words) >= 2 && len(words) <= 4 {
		// Check if all words start with uppercase
		allProperCase := true
		for _, word := range words {
			if len(word) == 0 || (word[0] < 'A' || word[0] > 'Z') {
				allProperCase = false
				break
			}
		}
		if allProperCase {
			return true
		}
	}

	// Check for "FirstName LastName" pattern (at least one space, proper case)
	if len(words) >= 2 {
		// First word starts with capital
		if len(words[0]) > 0 && words[0][0] >= 'A' && words[0][0] <= 'Z' {
			// Second word starts with capital
			if len(words[1]) > 0 && words[1][0] >= 'A' && words[1][0] <= 'Z' {
				return true
			}
		}
	}

	return false
}

// looksLikeTitleCandidate flags titles that commonly begin with articles.
func looksLikeTitleCandidate(s string) bool {
	lower := util.NormalizeString(s)
	return strings.HasPrefix(lower, "the ") || strings.HasPrefix(lower, "a ") || strings.HasPrefix(lower, "an ")
}

// isInitialToken reports whether a word is a single-letter initial with a period.
func isInitialToken(word string) bool {
	return len(word) == 2 && word[1] == '.' && word[0] >= 'A' && word[0] <= 'Z'
}

// recoverNormalizedBookPath answers "is this segment file's book already
// filed under its parent directory?" and returns that directory if so.
//
// It exists because FilePath normalization is one-way and invisible: the scan
// walk always re-emits a multi-file book as FilePath=segs[0] (it makes no store
// calls, so it cannot know the row moved), while the row itself stays at the
// directory. Every scan after the first therefore looks the book up at a path
// that no longer has a row.
//
// The ownership check is the load-bearing part. Finding *a* book at the parent
// directory is not enough -- a directory can hold a different book than the one
// this segment belongs to, and writing chapters or a scan-cache entry to the
// wrong book is worse than writing none. The row must demonstrably own THIS
// file, i.e. carry a BookFile whose path is exactly the one we were asked about.
func recoverNormalizedBookPath(bookFilePath string) string {
	info, statErr := os.Stat(bookFilePath)
	if statErr != nil || info.IsDir() {
		// Only a FILE path can have been normalized away from.
		return ""
	}

	dirPath := filepath.Dir(bookFilePath)
	dirBook, derr := getStore().GetBookByFilePath(dirPath)
	if derr != nil || dirBook == nil {
		return ""
	}

	files, ferr := getStore().GetBookFiles(dirBook.ID)
	if ferr != nil {
		return ""
	}
	for _, bf := range files {
		if bf.FilePath == bookFilePath {
			return dirPath
		}
	}
	return ""
}

// createBookFilesForBook creates BookFile records for a book.
// If segmentFiles is nil, it scans dirPath for all audio files.
// If segmentFiles is provided, only those specific files become BookFile records.
// After creating book files, if book.FilePath points to a file (not a directory),
// it normalizes it to the parent directory.
// The optional knownHashes parameter (filePath→hash) lets callers pass hashes
// already computed during the dedup check (PERF-2b) to avoid a second os.Open.
// The returned string is the path the book row LIVES AT once this function is
// done, or "" if it did not move it.
//
// Returning it is not a convenience. The normalization block below can rewrite
// the stored FilePath to the containing directory, and until 2026-08-24 nothing
// told the caller. The caller went on using the path it passed IN, which no
// longer matched any row, and TWO consumers then failed silently on the miss:
//
//   - writeBackScanCache found no row, so the book never acquired a scan-cache
//     entry at all and every scan counted it in scanCacheNoRowCount;
//   - PersistChaptersForBook found no row and returned quietly (its comment
//     classifies "a stale path" as a benign data condition), so the book's
//     chapters were never persisted at all.
//
// Measured on prod 2026-08-24: books stored under a normalized directory path
// had last_scan_mtime = nil, while a single-file book -- which never reaches
// this function, so is never normalized -- had a real timestamp. Handing the
// caller the new path lets it keep its in-memory Book in step with the row,
// which fixes both consumers with one change.
//
// SCOPE -- do not read more into this than it does. Populating the scan-cache
// entry does NOT yet make the next scan SKIP the book, and an earlier version
// of this comment wrongly said it did. Two independent things still defeat the
// skip, both measured 2026-08-24:
//
//  1. KEY GRAIN. GetScanCacheMap keys the cache by book.FilePath -- now the
//     directory -- but the walk emits, and classifySkipFile looks up, the
//     SEGMENT file path. The lookup misses on every scan.
//  2. VALUE GRAIN. This path hands writeBackScanCache a DIRECTORY to stat, so
//     the stored size is the directory inode's (128 bytes in the measurement),
//     never the segment's. Aligning the keys alone would still fail the size
//     comparison.
//
// The scan cache is keyed per-BOOK while the skip decision is made per-FILE;
// closing that needs per-file cache keying, which is tracked separately. What
// this function's return value fixes is chapter persistence and the no-row
// counter -- both real, neither a skip.
// createSingleFileBookFile gives a genuinely single-file book its one
// book_file row, without moving the book row to its parent directory.
//
// Two things it deliberately does, both of which look like they could be
// dropped and cannot:
//
//  1. It passes the file EXPLICITLY rather than letting createBookFilesForBook
//     scan the directory. With a nil segment list that function reads the whole
//     containing folder and turns every audio file in it into a row for THIS
//     book -- fine when the folder is the book, catastrophic when two
//     single-file books share one folder, which is the ordinary case for an
//     unorganized library.
//
//  2. It stats first and does nothing for a directory. A book whose FilePath is
//     a directory but which has no book_file rows is a different defect, and
//     papering over it with a single row pointing at the folder would make the
//     scan cache stamp a directory inode's mtime and size -- the exact VALUE
//     grain mismatch the per-file cache exists to remove.
//
// Failure is never fatal to the scan: the book is already saved, and a missing
// book_file row costs an incremental skip, not data.
func createSingleFileBookFile(book *Book, scanLog logger.Logger) {
	if book == nil || book.FilePath == "" {
		return
	}
	info, err := os.Stat(book.FilePath)
	if err != nil || info.IsDir() {
		return
	}
	// SegmentFiles is empty for a genuinely single-file book, so fall back to
	// the book's own path. A len==1 SegmentFiles whose element differs from
	// FilePath would file the row somewhere the book-grain stamp mirror cannot
	// match (it requires files[0].FilePath == book.FilePath) -- but the scanner
	// cannot construct that shape: all four places that set SegmentFiles set
	// FilePath = segs[0] alongside it. Checked 2026-08-25; if a fifth site
	// appears that breaks the invariant, this needs to pass book.FilePath.
	segs := book.SegmentFiles
	if len(segs) == 0 {
		segs = []string{book.FilePath}
	}
	createBookFilesForBook(book.FilePath, segs, scanLog, keepFilePath, book.SegmentHashes)
}

// normalizeToDirectory / keepFilePath name createBookFilesForBook's
// normalizeBookPath argument at the call sites, because a bare true/false there
// says nothing about which of the two very different behaviours is wanted.
const (
	normalizeToDirectory = true
	keepFilePath         = false
)

func createBookFilesForBook(bookFilePath string, segmentFiles []string, scanLog logger.Logger, normalizeBookPath bool, knownHashes ...map[string]string) string {
	if getStore() == nil {
		return ""
	}

	dbBook, err := getStore().GetBookByFilePath(bookFilePath)
	if err != nil {
		// A store error is NOT "the row is still where you left it" -- we do not
		// know where it is. Returning "" is the only safe answer (the caller
		// must not be sent to a path we cannot confirm), but it was previously
		// returned with no log and no counter, so the two consumers below went
		// silently unserviced. Say so.
		warnSampled(&bookFileLookupErrCount, scanLog,
			"createBookFilesForBook: book lookup failed for %s: %v "+
				"(chapters and scan-cache write-back skipped for this book)", bookFilePath, err)
		return ""
	}
	if dbBook == nil {
		// No row at this path. Before giving up, check whether THIS book was
		// normalized by an earlier scan and now lives at its parent directory.
		//
		// Without this, a book normalized on its first scan can never recover:
		// every later scan re-emits FilePath=segs[0], finds no row there, and
		// returns "" -- so the caller keeps the dead path and BOTH consumers
		// miss again, forever. The 2026-08-24 fix closed the path that CREATES
		// the desync; this is the path that REPAIRS the rows it already made.
		if recovered := recoverNormalizedBookPath(bookFilePath); recovered != "" {
			bookFilePathRecoveredCount.Add(1)
			return recovered
		}
		return ""
	}

	// normalizedPath stays "" unless the normalization block below actually
	// moves the row. "" means "the row is still where you left it".
	var normalizedPath string

	// Check if book files already exist
	existing, _ := getStore().GetBookFiles(dbBook.ID)
	if len(existing) > 0 {
		return "" // BookFiles already created (rescan)
	}

	// If no specific files provided, scan the directory
	scanDir := bookFilePath
	info, statErr := os.Stat(bookFilePath)
	if statErr == nil && !info.IsDir() {
		scanDir = filepath.Dir(bookFilePath)
	}

	if len(segmentFiles) == 0 {
		entries, rerr := os.ReadDir(scanDir)
		if rerr != nil {
			return ""
		}
		audioExts := make(map[string]bool)
		for _, ext := range config.AppConfig.SupportedExtensions {
			audioExts[ext] = true
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if audioExts[ext] {
				segmentFiles = append(segmentFiles, filepath.Join(scanDir, entry.Name()))
			}
		}
	}

	bfs := make([]*database.BookFile, 0, len(segmentFiles))
	for i, filePath := range segmentFiles {
		trackNum := i + 1
		ext := strings.ToLower(filepath.Ext(filePath))
		var sizeBytes int64
		if fi, serr := os.Stat(filePath); serr == nil {
			sizeBytes = fi.Size()
		}

		bf := &database.BookFile{
			ID:               ulid.Make().String(),
			BookID:           dbBook.ID,
			FilePath:         filePath,
			OriginalFilename: filepath.Base(filePath),
			Format:           strings.TrimPrefix(ext, "."),
			FileSize:         sizeBytes,
			TrackNumber:      trackNum,
		}

		// Read the file's tags so we capture EVERY tag losslessly (RawTags) and
		// use the real track/disc position from the tag instead of the file's
		// positional index (the index-based TrackNumber above is only a fallback,
		// and grouping-by-folder over positional tracks is what shattered books).
		if meta, merr := metadata.ExtractMetadata(filePath, nil); merr == nil {
			bf.RawTags = meta.AllTags
			if meta.TrackNumber > 0 {
				bf.TrackNumber = meta.TrackNumber
			}
			bf.TrackCount = meta.TrackTotal
			bf.DiscNumber = meta.DiscNumber
			bf.DiscCount = meta.DiscTotal
			if meta.Title != "" {
				bf.Title = meta.Title
			}
		} else {
			scanLog.Debug("tag read failed for %s (keeping positional track): %v", filePath, merr)
		}

		var preHash string
		if len(knownHashes) > 0 && knownHashes[0] != nil {
			preHash = knownHashes[0][filePath]
		}
		if preHash != "" {
			bf.FileHash = preHash
			bf.OriginalFileHash = preHash
		} else if h, herr := ComputeFileHash(filePath); herr == nil {
			bf.FileHash = h
			bf.OriginalFileHash = h
		}

		bfs = append(bfs, bf)
	}

	if len(bfs) > 0 {
		if serr := getStore().BatchUpsertBookFiles(bfs); serr != nil {
			scanLog.Warn("failed to batch upsert book files for book %s: %v", dbBook.ID, serr)
		}
	}

	// Normalize book.FilePath to directory if it currently points to a file.
	//
	// Re-read the book instead of writing back dbBook, which was loaded at the top
	// of this function and is now stale. BatchUpsertBookFiles above recomputes the
	// book's Duration and FileSize and writes them via UpdateBook; dbBook still
	// carries the pre-batch values (nil on a first import). UpdateBook's
	// preserve-on-nil guard covers Description, VersionNotes, the five BookSig*
	// fields, Author and Series — it does NOT cover Duration or FileSize, so a nil
	// on either is written through as nil and silently destroys what the recompute
	// just stored.
	//
	// This branch fires when Book.FilePath points at a file rather than a
	// directory, i.e. single-file audiobooks — so without the re-read every
	// single-file book the scanner imports would have its totals computed and then
	// erased inside this one function.
	if normalizeBookPath && statErr == nil && !info.IsDir() {
		dirPath := filepath.Dir(bookFilePath)
		toUpdate := dbBook
		if fresh, gerr := getStore().GetBookByID(dbBook.ID); gerr == nil && fresh != nil {
			toUpdate = fresh
		} else if gerr != nil {
			// Fall back to the stale snapshot: a book whose FilePath still points
			// at a file is misfiled for every later path-based lookup, which is
			// worse than losing a derived total. Nothing re-derives that total on
			// its own — maintenance.recompute-book-aggregates is one-shot and
			// refuses to run once its sentinel is set (see todo.d) — so the values
			// stay wrong until some other write to this book's files recomputes
			// them. Log it rather than lose it silently.
			scanLog.Warn("could not re-read book %s before FilePath normalization; "+
				"writing the pre-batch snapshot, which may reset Duration/FileSize: %v",
				dbBook.ID, gerr)
		}
		toUpdate.FilePath = dirPath
		if _, updateErr := getStore().UpdateBook(toUpdate.ID, toUpdate); updateErr != nil {
			scanLog.Warn("failed to normalize FilePath for book %s: %v", dbBook.ID, updateErr)
		} else {
			// ONLY on a successful write. If UpdateBook failed the row still
			// lives at the path the caller passed in, and reporting a move that
			// did not happen would send the caller's scan-cache write-back and
			// chapter persistence to a path with no row -- reintroducing the
			// exact bug this return value exists to close, in the error path.
			normalizedPath = dirPath
		}
	}

	if len(segmentFiles) > 0 {
		scanLog.Debug("Created %d book files for book %s", len(segmentFiles), dbBook.Title)
	}
	return normalizedPath
}

// createSegmentsForBook is deprecated and removed — use createBookFilesForBook instead.

// parseCueFile reads a CUE sheet and returns the audio files it references.
// CUE files use FILE "name.mp3" BINARY/WAVE/MP3 to list tracks.
func parseCueFile(cuePath string) (title string, files []string) {
	data, err := os.ReadFile(cuePath)
	if err != nil {
		return "", nil
	}
	dir := filepath.Dir(cuePath)
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		// Extract TITLE from top-level TITLE "..."
		if strings.HasPrefix(strings.ToUpper(line), "TITLE ") && title == "" {
			title = extractQuotedValue(line)
		}
		// Extract FILE references
		if strings.HasPrefix(strings.ToUpper(line), "FILE ") {
			fname := extractQuotedValue(line)
			if fname != "" {
				full := filepath.Join(dir, fname)
				if _, err := os.Stat(full); err == nil {
					files = append(files, full)
				}
			}
		}
	}
	return title, files
}

// parseM3UFile reads an M3U/M3U8 playlist and returns the audio files it references.
func parseM3UFile(m3uPath string) []string {
	data, err := os.ReadFile(m3uPath)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(m3uPath)
	var files []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Resolve relative paths
		full := line
		if !filepath.IsAbs(line) {
			full = filepath.Join(dir, line)
		}
		if _, err := os.Stat(full); err == nil {
			files = append(files, full)
		}
	}
	return files
}

// extractQuotedValue extracts the value between the first pair of double quotes.
func extractQuotedValue(line string) string {
	start := strings.Index(line, "\"")
	if start < 0 {
		return ""
	}
	end := strings.Index(line[start+1:], "\"")
	if end < 0 {
		return ""
	}
	return line[start+1 : start+1+end]
}

// findPlaylistGroupings scans a directory for CUE/M3U files and returns
// groups of audio files they reference. Each group maps to a single book.
// Returns: map of group name -> list of audio file paths
func findPlaylistGroupings(dirPath string, audioFiles []string) map[string][]string {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	groups := make(map[string][]string)
	// Track which audio files are claimed by a playlist
	claimed := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		full := filepath.Join(dirPath, entry.Name())

		switch ext {
		case ".cue":
			title, files := parseCueFile(full)
			if len(files) == 0 {
				continue
			}
			if title == "" {
				title = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			}
			// Only include files that are in our audioFiles set
			var matched []string
			audioSet := make(map[string]bool)
			for _, af := range audioFiles {
				audioSet[af] = true
			}
			for _, f := range files {
				if audioSet[f] && !claimed[f] {
					matched = append(matched, f)
					claimed[f] = true
				}
			}
			if len(matched) > 0 {
				groups[title] = matched
			}

		case ".m3u", ".m3u8":
			files := parseM3UFile(full)
			if len(files) == 0 {
				continue
			}
			title := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			audioSet := make(map[string]bool)
			for _, af := range audioFiles {
				audioSet[af] = true
			}
			var matched []string
			for _, f := range files {
				if audioSet[f] && !claimed[f] {
					matched = append(matched, f)
					claimed[f] = true
				}
			}
			if len(matched) > 0 {
				groups[title] = matched
			}
		}
	}

	return groups
}

// quickReadAlbum reads just the album tag from an audio file without full processing.
func quickReadAlbum(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(m.Album())
}

// quickReadMultiFileInfo reads album, album_artist, and track tags from an
// audio file in one open/parse pass, returning a MultiFileInfo for the
// multi-file detector. Best-effort: unreadable files yield an info with
// only Path populated.
func quickReadMultiFileInfo(filePath string) MultiFileInfo {
	info := MultiFileInfo{Path: filePath}
	f, err := os.Open(filePath)
	if err != nil {
		return info
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return info
	}
	info.Album = strings.TrimSpace(m.Album())
	info.AlbumArtist = strings.TrimSpace(m.AlbumArtist())
	tn, tt := m.Track()
	info.TrackNum = tn
	info.TotalTracks = tt
	return info
}

// groupFilesIntoBooks groups audio files from a single directory into logical books.
// When all files in a directory share the same non-empty album tag, they become a
// single directory-based Book (with segments created later). Otherwise, each file
// is treated as an individual Book (existing hash-based dedup handles linking).
// groupFilesIntoBooks groups a directory's audio files into logical books.
//
// onFileScanned, if supplied, is invoked once per file as the tag-reading pass
// walks them. It exists because this loop is the longest uninterrupted stretch
// of work in a scan: it opens and reads tags from every file in one directory.
// A library kept as a single flat folder of tens of thousands of files spends
// its entire scan inside this one call, so per-directory progress reporting
// cannot see it -- there is only ever one directory -- and the stuck-op
// watchdog kills the scan while it is working. Variadic so the existing
// callers and tests need no change.
func groupFilesIntoBooks(ctx context.Context, files []string, onFileScanned ...func()) []Book {
	noteFile := func() {
		for _, fn := range onFileScanned {
			if fn != nil {
				fn()
			}
		}
	}
	if len(files) <= 1 {
		var books []Book
		for _, f := range files {
			books = append(books, Book{
				FilePath: f,
				Format:   strings.ToLower(filepath.Ext(f)),
			})
		}
		return books
	}

	// MAYDEPLOY-G1: multi-file audiobook detection — if the folder contains
	// N≥3 audio files matching a sequential naming pattern (Chapter NN,
	// Part N of M, (NN of MM), bare NN, etc.) AND ≥75% of files share an
	// album or album_artist tag, treat the whole folder as ONE Book with
	// N BookFiles rather than letting the existing per-file paths kick in.
	if len(files) >= 3 {
		infos := make([]MultiFileInfo, len(files))
		for i, f := range files {
			infos[i] = quickReadMultiFileInfo(f)
			noteFile()
		}
		if ok, sorted := DetectMultiFileGroup(infos, DefaultMultiFileConfig()); ok {
			segs := make([]string, len(sorted))
			for i, s := range sorted {
				segs[i] = s.Path
			}
			logging.Info(ctx, "scanner multi-file group detected",
				"dir", filepath.Dir(segs[0]),
				"count", len(segs),
				"first", filepath.Base(segs[0]),
				"last", filepath.Base(segs[len(segs)-1]),
			)
			return []Book{{
				FilePath:     segs[0],
				Format:       strings.ToLower(filepath.Ext(segs[0])),
				SegmentFiles: segs,
			}}
		}
	}

	// Sample up to 3 files to quickly check if directory is a single-album book
	sampleSize := min(3, len(files))

	var firstAlbum string
	allSame := true
	for i := 0; i < sampleSize; i++ {
		album := quickReadAlbum(files[i])
		if album == "" {
			allSame = false
			break
		}
		if firstAlbum == "" {
			firstAlbum = util.NormalizeString(album)
		} else if util.NormalizeString(album) != firstAlbum {
			allSame = false
			break
		}
	}

	// If all sampled files share the same album and there are multiple files,
	// treat the entire directory as a single multi-file book
	if allSame && firstAlbum != "" && len(files) > 1 {
		dirPath := filepath.Dir(files[0])
		return []Book{{
			FilePath: dirPath,
			Format:   strings.ToLower(filepath.Ext(files[0])),
		}}
	}

	// Mixed directory — group by album, create one book per album group
	albumGroups := make(map[string][]string) // normalized album -> file paths
	var noAlbum []string
	for _, f := range files {
		album := quickReadAlbum(f)
		// This is the other unbounded tag-reading pass, and the one a flat
		// library actually hits: a folder of many unrelated books fails the
		// multi-file-group check above and lands here, reading tags from every
		// file with no checkpoint of its own.
		noteFile()
		if album == "" {
			noAlbum = append(noAlbum, f)
		} else {
			key := util.NormalizeString(album)
			albumGroups[key] = append(albumGroups[key], f)
		}
	}

	// For files with no album tag, try CUE/M3U playlist grouping as fallback
	if len(noAlbum) > 1 {
		dirPath := filepath.Dir(noAlbum[0])
		plGroups := findPlaylistGroupings(dirPath, noAlbum)
		if len(plGroups) > 0 {
			claimed := make(map[string]bool)
			for _, groupFiles := range plGroups {
				for _, f := range groupFiles {
					claimed[f] = true
				}
			}
			// Merge playlist groups into albumGroups
			for title, groupFiles := range plGroups {
				key := "pl:" + util.NormalizeString(title)
				albumGroups[key] = append(albumGroups[key], groupFiles...)
			}
			// Reduce noAlbum to only unclaimed files
			var remaining []string
			for _, f := range noAlbum {
				if !claimed[f] {
					remaining = append(remaining, f)
				}
			}
			noAlbum = remaining
		}
	}

	var books []Book
	for _, albumFiles := range albumGroups {
		if len(albumFiles) > 1 {
			// Multi-file book: use first file as FilePath, store all files for segment creation
			books = append(books, Book{
				FilePath:     albumFiles[0],
				Format:       strings.ToLower(filepath.Ext(albumFiles[0])),
				SegmentFiles: albumFiles,
			})
		} else {
			books = append(books, Book{
				FilePath: albumFiles[0],
				Format:   strings.ToLower(filepath.Ext(albumFiles[0])),
			})
		}
	}
	// Apply chapter consolidation to files with no album tag and no playlist
	// claim. Groups of ≥ 3 files sharing a numbered base title that are
	// individually short are merged into one multi-file book.
	books = append(books, consolidateChapterGroups(ctx, noAlbum)...)
	return books
}

// saveBookToDatabase saves the book information to the database.
// ctx is used to abort early if the enclosing operation has been canceled —
// in particular, we snapshot config.AppConfig.RootDir at entry so goroutine
// workers never read the global config after it has been restored by test
// teardown (the CI race detected at scanner.go:1700).
func saveBookToDatabase(ctx context.Context, book *Book) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Snapshot the fields we need from the global config so that even if
	// config.AppConfig is written by another goroutine (e.g. the next test's
	// setupTestServer) after this point, we use a consistent local copy.
	rootDir := config.AppConfig.RootDir

	// Prefer using the unified Store API when available
	if getStore() != nil {
		// Resolve author/series with conflict-aware get-or-create semantics.
		authorID, err := resolveAuthorID(book.Author)
		if err != nil {
			return err
		}
		seriesID, err := resolveSeriesID(book.Series, authorID)
		if err != nil {
			return err
		}

		// Attempt Work association (normalize title + author).
		// Uses the per-scan worksLookupCache (MAYDEPLOY-H6) to avoid an
		// O(N) GetAllWorks scan per book.
		var workID *string
		if book.Title != "" {
			canonical := util.NormalizeString(book.Title)
			if id := lookupWorkID(canonical, authorID); id != "" {
				wid := id
				workID = &wid
			}
			if workID == nil {
				newWork := &database.Work{Title: book.Title, AuthorID: authorID}
				created, err := getStore().CreateWork(newWork)
				if err == nil {
					wid := created.ID
					workID = &wid
					// Make the new work visible to subsequent books in this scan.
					rememberCreatedWork(created)
				} else if isUniqueConstraintError(err) {
					// A parallel worker likely created the same work — bypass
					// the cache (it may be stale) and re-query.
					works, lookupErr := getStore().GetAllWorks()
					if lookupErr == nil {
						for _, w := range works {
							if util.NormalizeString(w.Title) == canonical &&
								((authorID == nil && w.AuthorID == nil) ||
									(authorID != nil && w.AuthorID != nil && *authorID == *w.AuthorID)) {
								wid := w.ID
								workID = &wid
								// Refresh the cache entry with the resolved ID.
								rememberCreatedWork(&w)
								break
							}
						}
					}
				}
			}
		}

		// Compute file hash variants for deduplication/state mapping.
		// If ProcessFile pre-computed the hash, reuse it to avoid a second read.
		var fileHash *string
		var fileSize *int64
		var originalFileHash *string
		var organizedFileHash *string
		precomputedHash := book.FileHash
		var hash string
		var hashErr error
		if precomputedHash != "" {
			hash = precomputedHash
		} else {
			hash, hashErr = ComputeFileHash(book.FilePath)
		}
		if hashErr == nil && hash != "" {
			// Check if this hash is blocked
			blocked, err := getStore().IsHashBlocked(hash)
			if err != nil {
				defaultLog.Warn("failed to check hash blocklist: %v", err)
			} else if blocked {
				defaultLog.Info("Skipping file %s: hash %s is blocked", book.FilePath, hash)
				return nil // Skip this file
			}

			fileHash = stringPtrValue(hash)
			originalFileHash = stringPtrValue(hash)
			if size, err := getFileSize(book.FilePath); err == nil {
				fileSize = &size
			}
			if rootDir != "" && strings.HasPrefix(book.FilePath, rootDir) {
				organizedFileHash = stringPtrValue(hash)
			}
		}

		var seriesSequence *int
		if book.Position > 0 {
			seriesSequence = &book.Position
		}
		var duration *int
		if book.Duration > 0 {
			duration = &book.Duration
		}

		ls := "imported"
		if book.LibraryState != "" {
			ls = book.LibraryState
		}
		dbBook := &database.Book{
			Title:             book.Title,
			AuthorID:          authorID,
			SeriesID:          seriesID,
			SeriesSequence:    seriesSequence,
			FilePath:          book.FilePath,
			Format:            strings.TrimPrefix(book.Format, "."),
			Duration:          duration,
			WorkID:            workID,
			Narrator:          nullablePtr(book.Narrator),
			Language:          nullablePtr(book.Language),
			Publisher:         nullablePtr(book.Publisher),
			ASIN:              nullablePtr(book.ASIN),
			OpenLibraryID:     nullablePtr(book.OpenLibraryID),
			HardcoverID:       nullablePtr(book.HardcoverID),
			GoogleBooksID:     nullablePtr(book.GoogleBooksID),
			FileHash:          fileHash,
			FileSize:          fileSize,
			OriginalFileHash:  originalFileHash,
			OrganizedFileHash: organizedFileHash,
			LibraryState:      new(ls),
			Quantity:          new(1),
			SourceImportPath:  nullablePtr(book.SourceImportPath),
		}

		// Re-link by embedded AUDIOBOOK_ORGANIZER_ID: if the file contains our ID tag,
		// find the existing record and update its path (handles file moves/renames).
		if book.BookOrganizerID != "" {
			existingByOrgID, orgErr := getStore().GetBookByID(book.BookOrganizerID)
			if orgErr == nil && existingByOrgID != nil && existingByOrgID.FilePath != book.FilePath {
				defaultLog.Info("re-linking book %s (moved from %s to %s)",
					book.BookOrganizerID, existingByOrgID.FilePath, book.FilePath)
				existingByOrgID.FilePath = book.FilePath
				preserveExistingFields(dbBook, existingByOrgID)
				_, err = getStore().UpdateBook(existingByOrgID.ID, existingByOrgID)
				return err
			}
		}

		// supersededBook* records the predecessor row when a hash-duplicate is
		// version-linked below and a BRAND NEW Book ULID is minted for this
		// path. It is the untagged-move case: the file has no
		// AUDIOBOOK_ORGANIZER_ID tag to re-link by, so the moved file cannot
		// reuse its old row and the ABS sync identity (plus the listening
		// position keyed to the old ULID) has to be carried forward explicitly
		// once the new row exists. See followSyncIdentityOnVersionLink.
		var supersededBookID, supersededBookPath string

		// Upsert semantics with duplicate detection:
		// 1. Try lookup by file path first (exact match)
		existing, err := getStore().GetBookByFilePath(book.FilePath)
		if err != nil {
			return fmt.Errorf("book lookup failed: %w", err)
		}

		// 2. If not found by path but we have a file hash, check for duplicates via indexes
		if existing == nil && fileHash != nil && *fileHash != "" {
			hashLookups := []func(string) (*database.Book, error){
				getStore().GetBookByFileHash,
				getStore().GetBookByOriginalHash,
				getStore().GetBookByOrganizedHash,
			}
			lookupErrs := 0
			var firstLookupErr error
			for _, lookup := range hashLookups {
				candidate, lerr := lookup(*fileHash)
				if lerr != nil {
					// Not-found is (nil, nil) on every store; a non-nil error
					// is a real store failure (audit 2026-07-17 H5).
					lookupErrs++
					if firstLookupErr == nil {
						firstLookupErr = lerr
					}
					warnSampled(&dupLookupErrCount, defaultLog, "duplicate-detection hash lookup failed for %s: %v", book.FilePath, lerr)
					continue
				}
				if candidate != nil {
					existing = candidate
					break
				}
			}

			// A failing store must not silently re-import an existing book: if
			// any lookup errored and none found a match, this file cannot be
			// proven NOT to be a duplicate — skip importing it (conservative;
			// H5). The file is untouched on disk and will import on the next
			// scan once the store recovers.
			if existing == nil && lookupErrs > 0 {
				dupLookupSkipCount.Add(1)
				return fmt.Errorf("skipping import of %s: duplicate status undeterminable (%d/%d hash lookups failed, first error: %w)",
					book.FilePath, lookupErrs, len(hashLookups), firstLookupErr)
			}

			if existing != nil {
				defaultLog.Debug("Found duplicate book by hash: %s (existing: %s, new: %s)",
					existing.Title, existing.FilePath, book.FilePath)

				// Check if these are already version-linked
				alreadyLinked := existing.VersionGroupID != nil && *existing.VersionGroupID != ""

				if rootDir != "" &&
					strings.HasPrefix(book.FilePath, rootDir) &&
					!strings.HasPrefix(existing.FilePath, rootDir) {
					defaultLog.Debug("Promoting organized path for %s", existing.Title)
				} else if alreadyLinked {
					defaultLog.Debug("Already version-linked (group %s), skipping: %s", *existing.VersionGroupID, existing.FilePath)
					return nil
				} else {
					// Link both records via version_group_id. Primary = the one in RootDir.
					h := sha256.Sum256([]byte(existing.ID + "|" + book.FilePath))
					groupID := fmt.Sprintf("vg-%x", h[:8])

					existingInRoot := rootDir != "" && strings.HasPrefix(existing.FilePath, rootDir)
					newInRoot := rootDir != "" && strings.HasPrefix(book.FilePath, rootDir)

					// Mark the one in RootDir as primary; if neither or both are in root, existing wins.
					existingPrimary := existingInRoot || !newInRoot
					newPrimary := newInRoot && !existingInRoot

					existing.VersionGroupID = &groupID
					existing.IsPrimaryVersion = &existingPrimary
					if _, uerr := getStore().UpdateBook(existing.ID, existing); uerr != nil {
						defaultLog.Warn("Failed to set version group on existing book %s: %v", existing.ID, uerr)
					}

					dbBook.VersionGroupID = &groupID
					dbBook.IsPrimaryVersion = &newPrimary
					defaultLog.Info("Auto-linked hash-duplicate as version group %s (primary=%s): %s <-> %s",
						groupID, existing.FilePath, existing.FilePath, book.FilePath)
					// A new ULID is about to be minted for this path; remember
					// the row it supersedes so the sync identity can follow if
					// this turns out to be a move rather than a second copy.
					supersededBookID, supersededBookPath = existing.ID, existing.FilePath
					// Fall through to create the new (non-primary) record below
					existing = nil
				}
			}
		}

		// 2b. Multi-file book dedup: hash every segment file individually and tally
		// votes per parent book. This correctly handles cases where the first file
		// was damaged/replaced (single-hash check above would miss it) and also
		// detects partial damage (bit rot) when only some files changed.
		if existing == nil && len(book.SegmentFiles) > 1 {
			bookVotes := make(map[string]int)
			bookCandidates := make(map[string]*database.Book)

			for _, segFile := range book.SegmentFiles {
				h, herr := ComputeFileHash(segFile)
				if herr != nil || h == "" {
					continue
				}
				// Write back so createBookFilesForBook can reuse without re-hashing.
				if book.SegmentHashes == nil {
					book.SegmentHashes = make(map[string]string)
				}
				book.SegmentHashes[segFile] = h
				candidate, lerr := getStore().GetBookBySegmentFileHash(h)
				if lerr != nil || candidate == nil {
					continue
				}
				bookVotes[candidate.ID]++
				bookCandidates[candidate.ID] = candidate
			}

			// Find the parent book with the most matching segment files.
			bestID, bestCount := "", 0
			for id, count := range bookVotes {
				if count > bestCount {
					bestCount, bestID = count, id
				}
			}

			if bestID != "" {
				threshold := int(math.Ceil(float64(len(book.SegmentFiles)) * 0.8))
				if bestCount >= threshold {
					matchedBook := bookCandidates[bestID]
					if bestCount < len(book.SegmentFiles) {
						defaultLog.Warn(
							"Multi-file dedup: %d/%d files matched existing book %q — possible corruption or bit rot in %s",
							bestCount, len(book.SegmentFiles), matchedBook.Title, book.FilePath)
					} else {
						defaultLog.Debug("Multi-file dedup: all %d files matched existing book %q", bestCount, matchedBook.Title)
					}

					// Version-link the new path to the matched book; same logic as step 2 above.
					alreadyLinked := matchedBook.VersionGroupID != nil && *matchedBook.VersionGroupID != ""
					if alreadyLinked {
						defaultLog.Debug("Multi-file dedup: already version-linked (group %s), skipping: %s",
							*matchedBook.VersionGroupID, book.FilePath)
						return nil
					}
					h2 := sha256.Sum256([]byte(matchedBook.ID + "|" + book.FilePath))
					groupID := fmt.Sprintf("vg-%x", h2[:8])
					existingInRoot := rootDir != "" && strings.HasPrefix(matchedBook.FilePath, rootDir)
					newInRoot := rootDir != "" && strings.HasPrefix(book.FilePath, rootDir)
					matchedPrimary := existingInRoot || !newInRoot
					newPrimary := newInRoot && !existingInRoot
					matchedBook.VersionGroupID = &groupID
					matchedBook.IsPrimaryVersion = &matchedPrimary
					if _, uerr := getStore().UpdateBook(matchedBook.ID, matchedBook); uerr != nil {
						defaultLog.Warn("Multi-file dedup: failed to set version group on %s: %v", matchedBook.ID, uerr)
					}
					dbBook.VersionGroupID = &groupID
					dbBook.IsPrimaryVersion = &newPrimary
					defaultLog.Info("Multi-file dedup: linked %q as version group %s (%d/%d files matched)",
						matchedBook.Title, groupID, bestCount, len(book.SegmentFiles))
					// Same untagged-move follow as the single-hash branch above.
					supersededBookID, supersededBookPath = matchedBook.ID, matchedBook.FilePath
					// Leave existing == nil so CreateBook runs below with the version fields set.
				} else {
					defaultLog.Debug("Multi-file dedup: best match %d/%d files (threshold %d) — treating as new book: %s",
						bestCount, len(book.SegmentFiles), threshold, book.FilePath)
				}
			}
		}

		if existing == nil {
			// Smart dedup: check for same-title books in same directory (format-aware version linking)
			if dbBook.Title != "" {
				parentDir := filepath.Dir(book.FilePath)
				siblings, lookupErr := getStore().GetBooksByTitleInDir(strings.ToLower(dbBook.Title), parentDir)
				if lookupErr == nil && len(siblings) > 0 {
					// Determine or reuse version_group_id
					var groupID string
					for _, sib := range siblings {
						if sib.VersionGroupID != nil && *sib.VersionGroupID != "" {
							groupID = *sib.VersionGroupID
							break
						}
					}
					if groupID == "" {
						h := sha256.Sum256([]byte(parentDir + "/" + strings.ToLower(dbBook.Title)))
						groupID = fmt.Sprintf("vg-%x", h[:8])
					}
					dbBook.VersionGroupID = &groupID
					isM4B := strings.EqualFold(dbBook.Format, "m4b")
					isPrimary := isM4B
					dbBook.IsPrimaryVersion = &isPrimary

					// Update siblings to share the version group
					for _, sib := range siblings {
						if sib.VersionGroupID == nil || *sib.VersionGroupID == "" {
							sibIsM4B := strings.EqualFold(sib.Format, "m4b")
							sib.VersionGroupID = &groupID
							sib.IsPrimaryVersion = &sibIsM4B
							if _, uerr := getStore().UpdateBook(sib.ID, &sib); uerr != nil {
								defaultLog.Warn("Failed to update sibling version group for %s: %v", sib.FilePath, uerr)
							}
						}
					}
					defaultLog.Info("Auto-linked version group %s for %q in %s", groupID, dbBook.Title, parentDir)
				}
			}

			_, err = getStore().CreateBook(dbBook)
			if err == nil {
				followSyncIdentityOnVersionLink(supersededBookID, supersededBookPath, dbBook.ID)
				// Check for metadata hash duplicates
				detectMetadataHashDuplicate(dbBook, defaultLog)
				if hooks := currentScanHooks(); hooks != nil {
					hooks.OnBookScanned(dbBook.ID, dbBook.Title)
					hooks.OnImportDedup(dbBook.ID)
				}
			}
			return err
		}

		// Rescan of an already-imported file (matched by path, or an existing
		// hash-duplicate whose organized path we are promoting above). Writing
		// the partial dbBook literal via a full-replace UpdateBook wiped every
		// Book field the scanner does not populate — fetched metadata, ratings,
		// Whisper transcriptions, media-info, review status, quarantine state,
		// lifecycle timestamps, etc. — because UpdateBook replaces the whole row
		// (data-loss bug).
		//
		// Fix (INVERT): start from the COMPLETE existing row and overlay ONLY
		// the fields the scanner is authoritative for. Everything else survives
		// by construction, so a newly-added Book field can never silently
		// regress. `existing` is already loaded (GetBookByFilePath ->
		// GetBookByID is a full-fidelity point-get), so this adds no extra DB
		// read. Copy it first so the getter's pointer is never mutated in place.
		merged := *existing
		// Consult per-field provenance before overlaying: a field the user
		// locked or explicitly set is not ours to rewrite from a file tag.
		locked, ok := lockedFieldsForBook(getStore(), existing.ID)
		if !ok {
			defaultLog.Warn("metadata field state unreadable for book %s (%s); "+
				"treating every guarded field as locked so a scan cannot clobber a user edit",
				existing.ID, existing.FilePath)
		}
		applyScannerFields(&merged, dbBook, locked)

		_, err = getStore().UpdateBook(existing.ID, &merged)
		if err == nil {
			// Check for metadata hash duplicates after update
			detectMetadataHashDuplicate(&merged, defaultLog)
		}
		return err
	}

	return fmt.Errorf("database store not initialized")
}

// followSyncIdentityOnVersionLink carries a superseded book's ABS sync identity
// (its durable libraryItemId) and each user's listening position onto the
// freshly created row when a hash-duplicate version-link mints a NEW Book ULID
// for a path.
//
// Why the on-disk check decides it: the two situations reaching this point are
// indistinguishable from the DB alone.
//
//   - The predecessor's file is GONE -> the file was moved and, being untagged,
//     could not be re-linked to its own row. The new row is the live book, so
//     the identity must follow it or the device's place in the book is orphaned
//     on a row that points at a path that no longer exists.
//   - The predecessor's file still EXISTS -> a genuine second copy/version.
//     Both books are real; the identity stays on the original.
//
// Note the IsPrimaryVersion flag cannot be used as that signal: in this branch
// newPrimary is `newInRoot && !existingInRoot`, which the enclosing else-if
// has already excluded, so it is always false regardless of what happened on
// disk.
//
// Best-effort and never fails the import; merge.FollowBookIDChange logs at
// ERROR (with both book IDs) if the carry-forward fails, and is a no-op when
// the predecessor never had a sync identity minted.
func followSyncIdentityOnVersionLink(supersededID, supersededPath, newBookID string) {
	if supersededID == "" || supersededPath == "" || newBookID == "" {
		return
	}
	if _, err := os.Stat(supersededPath); err == nil {
		return // predecessor file still present: a second copy, not a move
	} else if !os.IsNotExist(err) {
		// Cannot prove the predecessor is gone (permissions, unmounted share).
		// Stay conservative and leave the identity where it is.
		defaultLog.Warn("sync-identity: cannot stat superseded book path %s, leaving identity on %s: %v",
			supersededPath, supersededID, err)
		return
	}
	merge.FollowBookIDChange(getStore(), supersededID, newBookID)
}

// ComputeSegmentFileHash computes SHA256 of the first 1MB of a file for use as
// a segment-level fingerprint. This lighter hash enables auto-relinking when
// files are moved on disk.
func ComputeSegmentFileHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	const maxBytes = 1024 * 1024 // 1 MB
	h := sha256.New()
	if _, err := io.CopyN(h, f, maxBytes); err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeFileHash computes a SHA256 hash of the file, using a chunked strategy
// for large audiobook files to balance accuracy and performance.
func ComputeFileHash(filePath string) (string, error) {
	if activeScanner != nil {
		return activeScanner.ComputeFileHash(filePath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// For large files (> 100MB), hash first 10MB + last 10MB + size for speed
	info, err := file.Stat()
	if err != nil {
		return "", err
	}

	const threshold = 100 * 1024 * 1024 // 100MB
	const chunkSize = 10 * 1024 * 1024  // 10MB

	if info.Size() > threshold {
		// Quick hash for large files: first chunk + last chunk + size
		h := sha256.New()

		// First chunk
		first := make([]byte, chunkSize)
		n, err := file.Read(first)
		if err != nil && err != io.EOF {
			return "", err
		}
		h.Write(first[:n])

		// Last chunk
		if info.Size() > chunkSize {
			// A discarded Seek error would hash the wrong window and poison
			// dedup (audit 2026-07-17 DL-4); matches the check in
			// process_file.go computeHashFromReader.
			if _, err := file.Seek(-chunkSize, io.SeekEnd); err != nil {
				return "", err
			}
			last := make([]byte, chunkSize)
			n, err := file.Read(last)
			if err != nil && err != io.EOF {
				return "", err
			}
			h.Write(last[:n])
		}

		// Include size in hash
		h.Write(fmt.Appendf(nil, "%d", info.Size()))

		return hex.EncodeToString(h.Sum(nil)), nil
	}

	// Full hash for smaller files
	return computeFullFileHash(filePath)
}

// computeFullFileHash computes the SHA256 hash of the entire file
func computeFullFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// getFileSize returns the size of a file in bytes
func getFileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func stringPtrValue(s string) *string {
	copy := s
	return &copy
}

//go:fix inline
func stringPtr(s string) *string {
	return new(s)
}

//go:fix inline
func intPtr(i int) *int {
	return new(i)
}

func nullablePtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// placeholderAuthors answers "is this author row the placeholder?" for the
// duration of one scan.
//
// It resolves by NAME, from the row's own id, rather than by comparing against a
// single id looked up once from the author:name: index. That distinction is load
// bearing and was measured, not assumed: production carries TWO author rows both
// named "Unknown Author" (54845 with no books, 54846 with 2,128), and the name
// index can only point at one of them. A check keyed on whichever id
// GetAuthorByName returns guards exactly one row and silently leaves every book
// under the other one gated -- which, had the index named the empty row, would
// have made this whole fix inert in production while every test still passed.
//
// Answers are cached because the gate runs once per book and author rows repeat
// heavily; the mutex is required because the gate runs inside the scan's worker
// pool.
type placeholderAuthors struct {
	mu    sync.Mutex
	known map[int]bool
}

func newPlaceholderAuthors() *placeholderAuthors {
	return &placeholderAuthors{known: make(map[int]bool)}
}

// is reports whether authorID is an author row carrying the placeholder name.
//
// An unreadable or missing row answers false: the gate's job is to decide
// whether AI parsing is pointless, and "we could not tell" is not grounds for
// skipping a book. It never CREATES the placeholder row -- doing so would mint,
// on a database that had so far avoided it, the exact row this treats as absent.
func (p *placeholderAuthors) is(authorID int) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	cached, ok := p.known[authorID]
	p.mu.Unlock()
	if ok {
		return cached
	}

	result := false
	if store := getStore(); store != nil {
		if author, err := store.GetAuthorByID(authorID); err == nil && author != nil {
			result = authorname.IsPlaceholder(author.Name)
		}
	}

	p.mu.Lock()
	p.known[authorID] = result
	p.mu.Unlock()
	return result
}

// rowHasRealAuthor reports whether a book row's scalar author is one that makes
// AI parsing pointless. The placeholder does not count: a book filed under it is
// exactly a book whose author still needs resolving.
func rowHasRealAuthor(authorID *int, placeholders *placeholderAuthors) bool {
	if authorID == nil || *authorID == 0 {
		return false
	}
	return !placeholders.is(*authorID)
}

func resolveAuthorID(authorName string) (*int, error) {
	trimmed := strings.TrimSpace(authorName)
	if trimmed == "" {
		return nil, nil
	}

	// Normalize collapsed initials: "J.B." → "J. B."
	initialsRe := regexp.MustCompile(`([A-Z]\.)([A-Z])`)
	for initialsRe.MatchString(trimmed) {
		trimmed = initialsRe.ReplaceAllString(trimmed, "$1 $2")
	}
	trimmed = strings.TrimSpace(trimmed)

	author, err := getStore().GetAuthorByName(trimmed)
	if err != nil {
		return nil, fmt.Errorf("author lookup failed: %w", err)
	}
	if author != nil {
		return &author.ID, nil
	}

	author, err = getStore().CreateAuthor(trimmed)
	if err != nil {
		if !isUniqueConstraintError(err) {
			return nil, fmt.Errorf("author create failed: %w", err)
		}
		// Concurrent create: re-fetch existing record.
		author, err = getStore().GetAuthorByName(trimmed)
		if err != nil {
			return nil, fmt.Errorf("author lookup after conflict failed: %w", err)
		}
		if author == nil {
			return nil, fmt.Errorf("author conflict detected but author not found: %s", trimmed)
		}
	}
	return &author.ID, nil
}

func resolveSeriesID(seriesName string, authorID *int) (*int, error) {
	trimmed := strings.TrimSpace(seriesName)
	if trimmed == "" {
		return nil, nil
	}

	// Strip any embedded title/position contamination from the series name.
	// Position info is discarded here; the scanner does not set SeriesSequence.
	if cleaned, _, flagged := metadata.StripSeriesContamination(trimmed, ""); !flagged && cleaned != "" {
		trimmed = cleaned
	}

	series, err := getStore().GetSeriesByName(trimmed, authorID)
	if err != nil {
		return nil, fmt.Errorf("series lookup failed: %w", err)
	}
	if series != nil {
		return &series.ID, nil
	}

	series, err = getStore().CreateSeries(trimmed, authorID)
	if err != nil {
		if !isUniqueConstraintError(err) {
			return nil, fmt.Errorf("series create failed: %w", err)
		}
		// Concurrent create: re-fetch existing record.
		series, err = getStore().GetSeriesByName(trimmed, authorID)
		if err != nil {
			return nil, fmt.Errorf("series lookup after conflict failed: %w", err)
		}
		if series == nil {
			return nil, fmt.Errorf("series conflict detected but series not found: %s", trimmed)
		}
	}
	return &series.ID, nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unique constraint") ||
		strings.Contains(lower, "duplicate key")
}

// preserveExistingFields keeps enriched metadata fields from the existing database record
// when the scanner doesn't extract them (i.e. produces nil/zero). This prevents rescan
// from wiping out data added by metadata fetch, AI parse, or manual edits.
func preserveExistingFields(scanned *database.Book, existing *database.Book) {
	if scanned.Narrator == nil && existing.Narrator != nil {
		scanned.Narrator = existing.Narrator
	}
	if scanned.NarratorsJSON == nil && existing.NarratorsJSON != nil {
		scanned.NarratorsJSON = existing.NarratorsJSON
	}
	if scanned.Publisher == nil && existing.Publisher != nil {
		scanned.Publisher = existing.Publisher
	}
	if scanned.Language == nil && existing.Language != nil {
		scanned.Language = existing.Language
	}
	if scanned.PrintYear == nil && existing.PrintYear != nil {
		scanned.PrintYear = existing.PrintYear
	}
	if scanned.AudiobookReleaseYear == nil && existing.AudiobookReleaseYear != nil {
		scanned.AudiobookReleaseYear = existing.AudiobookReleaseYear
	}
	if scanned.CoverURL == nil && existing.CoverURL != nil {
		scanned.CoverURL = existing.CoverURL
	}
	if scanned.WorkID == nil && existing.WorkID != nil {
		scanned.WorkID = existing.WorkID
	}
	if scanned.ISBN10 == nil && existing.ISBN10 != nil {
		scanned.ISBN10 = existing.ISBN10
	}
	if scanned.ISBN13 == nil && existing.ISBN13 != nil {
		scanned.ISBN13 = existing.ISBN13
	}
	if scanned.ASIN == nil && existing.ASIN != nil {
		scanned.ASIN = existing.ASIN
	}
	if scanned.Edition == nil && existing.Edition != nil {
		scanned.Edition = existing.Edition
	}
	if scanned.Description == nil && existing.Description != nil {
		scanned.Description = existing.Description
	}
	// Preserve external provider IDs
	if scanned.OpenLibraryID == nil && existing.OpenLibraryID != nil {
		scanned.OpenLibraryID = existing.OpenLibraryID
	}
	if scanned.HardcoverID == nil && existing.HardcoverID != nil {
		scanned.HardcoverID = existing.HardcoverID
	}
	if scanned.GoogleBooksID == nil && existing.GoogleBooksID != nil {
		scanned.GoogleBooksID = existing.GoogleBooksID
	}
	// Preserve iTunes fields
	if scanned.ITunesPersistentID == nil && existing.ITunesPersistentID != nil {
		scanned.ITunesPersistentID = existing.ITunesPersistentID
	}
	if scanned.ITunesDateAdded == nil && existing.ITunesDateAdded != nil {
		scanned.ITunesDateAdded = existing.ITunesDateAdded
	}
	if scanned.ITunesPlayCount == nil && existing.ITunesPlayCount != nil {
		scanned.ITunesPlayCount = existing.ITunesPlayCount
	}
	if scanned.ITunesLastPlayed == nil && existing.ITunesLastPlayed != nil {
		scanned.ITunesLastPlayed = existing.ITunesLastPlayed
	}
	if scanned.ITunesRating == nil && existing.ITunesRating != nil {
		scanned.ITunesRating = existing.ITunesRating
	}
	if scanned.ITunesBookmark == nil && existing.ITunesBookmark != nil {
		scanned.ITunesBookmark = existing.ITunesBookmark
	}
	if scanned.ITunesImportSource == nil && existing.ITunesImportSource != nil {
		scanned.ITunesImportSource = existing.ITunesImportSource
	}
	// Preserve version management fields
	if scanned.IsPrimaryVersion == nil && existing.IsPrimaryVersion != nil {
		scanned.IsPrimaryVersion = existing.IsPrimaryVersion
	}
	if scanned.VersionGroupID == nil && existing.VersionGroupID != nil {
		scanned.VersionGroupID = existing.VersionGroupID
	}
	if scanned.VersionNotes == nil && existing.VersionNotes != nil {
		scanned.VersionNotes = existing.VersionNotes
	}
	// Preserve deletion state
	if scanned.MarkedForDeletion == nil && existing.MarkedForDeletion != nil {
		scanned.MarkedForDeletion = existing.MarkedForDeletion
	}
	if scanned.MarkedForDeletionAt == nil && existing.MarkedForDeletionAt != nil {
		scanned.MarkedForDeletionAt = existing.MarkedForDeletionAt
	}
	// Preserve series sequence if scan has nil/zero and existing has a value
	if (scanned.SeriesSequence == nil || *scanned.SeriesSequence == 0) && existing.SeriesSequence != nil && *existing.SeriesSequence != 0 {
		scanned.SeriesSequence = existing.SeriesSequence
	}
	// Preserve SourceImportPath — once set it must never be overwritten
	if scanned.SourceImportPath == nil && existing.SourceImportPath != nil {
		scanned.SourceImportPath = existing.SourceImportPath
	}
}

// applyScannerFields overlays the fields the scanner freshly derives from the
// file and its tags onto dst (a copy of the COMPLETE existing row), leaving
// every other field untouched. It is the write-side of the rescan data-loss
// fix: previously a rescan wrote a partial Book literal via a full-replace
// UpdateBook, wiping fetched metadata, ratings, Whisper transcriptions,
// media-info (Bitrate/Codec/…), MetadataReviewStatus/Source/Hash,
// ITunesSyncStatus, quarantine state, version links, and lifecycle timestamps.
// Starting from the full row and overlaying only scanner-owned fields makes
// data loss impossible by construction — a future Book field is preserved
// automatically rather than silently dropped.
//
// The rule for every overlaid field is "scanned value wins if present (non-nil
// / non-zero), else keep existing." This reproduces the prior behavior for the
// fields preserveExistingFields already guarded and for the always-set
// LibraryState/Quantity, and adds the same guard to the fields the old code
// overwrote unconditionally (Title/AuthorID/SeriesID/Format/hashes/Duration) —
// which is precisely the wipe this fixes (an untagged file yields nil
// AuthorID/SeriesID), applied consistently.
func applyScannerFields(dst *database.Book, scanned *database.Book, locked map[string]bool) {
	// Identity / file-derived fields (freshly read from the file this scan).
	if scanned.FilePath != "" {
		dst.FilePath = scanned.FilePath
	}
	if scanned.Format != "" {
		dst.Format = scanned.Format
	}
	if scanned.FileHash != nil {
		dst.FileHash = scanned.FileHash
	}
	if scanned.FileSize != nil {
		dst.FileSize = scanned.FileSize
	}
	// OriginalFileHash records the pre-organize hash; once stored it must be
	// preserved, so only adopt the scanned value when none is stored yet.
	if dst.OriginalFileHash == nil && scanned.OriginalFileHash != nil {
		dst.OriginalFileHash = scanned.OriginalFileHash
	}
	// OrganizedFileHash: scanned value wins when the file is under RootDir.
	if scanned.OrganizedFileHash != nil {
		dst.OrganizedFileHash = scanned.OrganizedFileHash
	}
	if scanned.Duration != nil {
		dst.Duration = scanned.Duration
	}
	if scanned.LibraryState != nil {
		dst.LibraryState = scanned.LibraryState
	}
	if scanned.Quantity != nil {
		dst.Quantity = scanned.Quantity
	}

	// Tag-derived identity fields.
	if scanned.Title != "" && !locked["title"] {
		dst.Title = scanned.Title
	}
	if scanned.AuthorID != nil && !locked["author"] {
		dst.AuthorID = scanned.AuthorID
	}
	if scanned.SeriesID != nil && !locked["series"] {
		dst.SeriesID = scanned.SeriesID
	}
	if scanned.SeriesSequence != nil && *scanned.SeriesSequence != 0 && !locked["series_sequence"] {
		dst.SeriesSequence = scanned.SeriesSequence
	}
	if scanned.WorkID != nil {
		dst.WorkID = scanned.WorkID
	}

	// Tag-derived enrichment fields.
	if scanned.Narrator != nil && !locked["narrator"] {
		dst.Narrator = scanned.Narrator
	}
	if scanned.Language != nil && !locked["language"] {
		dst.Language = scanned.Language
	}
	if scanned.Publisher != nil && !locked["publisher"] {
		dst.Publisher = scanned.Publisher
	}
	if scanned.ASIN != nil {
		dst.ASIN = scanned.ASIN
	}
	if scanned.OpenLibraryID != nil {
		dst.OpenLibraryID = scanned.OpenLibraryID
	}
	if scanned.HardcoverID != nil {
		dst.HardcoverID = scanned.HardcoverID
	}
	if scanned.GoogleBooksID != nil {
		dst.GoogleBooksID = scanned.GoogleBooksID
	}

	// SourceImportPath is set on CreateBook only and must never be mutated on
	// UpdateBook, so it is deliberately NOT overlaid here (dst keeps existing's).
}

// identifySeriesUsingExternalAPIs tries to match books to series using external APIs
func identifySeriesUsingExternalAPIs(books []Book) error {
	// Implement API calls to GoodReads or similar services
	// This is a placeholder - actual implementation would depend on available APIs
	return nil
}

// countAudioFilesInDir counts the number of audio files (by extension) in a directory.
// Non-recursive.
func countAudioFilesInDir(dirPath string, supportedExts []string) int {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0
	}
	extSet := make(map[string]bool, len(supportedExts))
	for _, e := range supportedExts {
		extSet[strings.ToLower(e)] = true
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && extSet[strings.ToLower(filepath.Ext(e.Name()))] {
			count++
		}
	}
	return count
}
