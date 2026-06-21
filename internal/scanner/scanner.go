// file: internal/scanner/scanner.go
// version: 1.44.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-19

package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dhowden/tag"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/matcher"
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

// Scanner defines the interface for scanning and processing audiobook files.
// Tests can swap in a mock implementation via SetScanner.
type Scanner interface {
	ScanDirectory(rootDir string, scanLog logger.Logger) ([]Book, error)
	ScanDirectoryParallel(rootDir string, workers int, scanLog logger.Logger) ([]Book, error)
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
	pkgStore   database.Store
	pkgStoreMu sync.RWMutex
)

// SetStore wires the database the scanner package's free helpers use
// for book/file/work/hash lookups. Idempotent; pass nil to clear.
func SetStore(s database.Store) {
	pkgStoreMu.Lock()
	pkgStore = s
	pkgStoreMu.Unlock()
}

// getStore returns the package-local store with read-lock protection.
// Used internally by the free helpers in scanner.go and the per-book
// helpers in book_files.go.
func getStore() database.Store {
	pkgStoreMu.RLock()
	defer pkgStoreMu.RUnlock()
	return pkgStore
}

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
func ClearScanCache() {
	SetScanCache(nil)
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
	worksLookupCache    map[string]string // key = normalizedTitle + "|" + authorID(or "nil")
	worksLookupReady    bool              // true when cache has been populated (or attempted) for this scan
	worksLookupDisabled bool              // true outside a scan window (lazy fallback to GetAllWorks)
	worksLookupMu       sync.RWMutex
)

// worksLookupKey builds the cache key used by both lookups and inserts.
func worksLookupKey(normalizedTitle string, authorID *int) string {
	if authorID == nil {
		return normalizedTitle + "|nil"
	}
	return normalizedTitle + "|" + strconv.Itoa(*authorID)
}

// InitWorksLookupCache builds the (normalizedTitle|authorID) → workID map by
// calling GetAllWorks once. Called by ScanService at the start of each scan.
// If GetAllWorks fails, the cache is left empty but enabled — saveBookToDatabase
// will fall through to a direct CreateWork (which is the same fallback path as
// when nothing matched).
func InitWorksLookupCache() {
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	worksLookupCache = make(map[string]string)
	worksLookupReady = true
	worksLookupDisabled = false
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

// ClearWorksLookupCache drops the per-scan works lookup map. Called by
// ScanService after the scan completes (deferred).
func ClearWorksLookupCache() {
	worksLookupMu.Lock()
	defer worksLookupMu.Unlock()
	worksLookupCache = nil
	worksLookupReady = false
	worksLookupDisabled = true
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

// shouldSkipFile returns true when a file is unchanged since the last scan and
// does not have a pending rescan request.
func shouldSkipFile(filePath string, mtime int64, size int64, cache map[string]database.ScanCacheEntry) bool {
	if cache == nil {
		return false
	}
	entry, found := cache[filePath]
	if !found {
		return false
	}
	return entry.Mtime == mtime && entry.Size == size && !entry.NeedsRescan
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
	FileHash         string // Pre-computed hash from ProcessFile (avoids double-read)
	LibraryState     string // If set, overrides the default "imported" state in saveBookToDatabase
	SourceImportPath string // Top-level import path this file was discovered in; set by scan_service
}

// ScanDirectory scans the given directory for audiobook files.
// If scanLog is nil, a default logger is used.
func ScanDirectory(rootDir string, scanLog logger.Logger) ([]Book, error) {
	if activeScanner != nil {
		return activeScanner.ScanDirectory(rootDir, scanLog)
	}
	return ScanDirectoryParallel(rootDir, 1, scanLog)
}

// ScanDirectoryParallel scans directory with parallel workers for improved performance.
// If scanLog is nil, a default logger is used.
func ScanDirectoryParallel(rootDir string, workers int, scanLog logger.Logger) ([]Book, error) {
	if activeScanner != nil {
		return activeScanner.ScanDirectoryParallel(rootDir, workers, scanLog)
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

	registerDirectory := func(path string, info os.FileInfo) bool {
		if info == nil {
			return false
		}
		statInfo, err := os.Stat(path)
		if err != nil || !statInfo.IsDir() {
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
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
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
			semaphore <- struct{}{}        // Acquire
			defer func() { <-semaphore }() // Release

			// Read directory entries
			entries, err := os.ReadDir(scanDir)
			if err != nil {
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
				for _, supportedExt := range config.AppConfig.SupportedExtensions {
					if ext == supportedExt {
						audioFiles = append(audioFiles, path)
						break
					}
				}
			}

			// Group files into logical books using album tags
			localBooks := groupFilesIntoBooks(audioFiles)

			// Merge results
			if len(localBooks) > 0 {
				mu.Lock()
				books = append(books, localBooks...)
				mu.Unlock()
			}
		}(dir)
	}

	wg.Wait()

	// Prevention post-pass: groupFilesIntoBooks runs per-leaf-dir, so a book laid
	// out as one chapter per "<prefix> - N" subdir shatters into one book per
	// chapter. Coalesce those siblings into one multi-file book before persisting.
	// Path-based + prefix⊆parent guard; gated OFF by default (see CoalesceShatteredSiblings).
	if config.AppConfig.CoalesceShatteredSiblings {
		before := len(books)
		books = coalesceShatteredSiblings(books)
		if len(books) != before {
			slog.Info("scanner shattered-sibling coalesce", "books_before", before, "books_after", len(books))
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

	// progressCh serializes progress updates so callbacks and progress output
	// are handled in a single goroutine.
	progressCh := make(chan string, len(books))
	var progressWG sync.WaitGroup
	progressWG.Add(1)

	go func() {
		defer progressWG.Done()
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
	}()

	var aiParser *ai.OpenAIParser
	aiEnabled := false
	if config.AppConfig.EnableAIParsing {
		if config.AppConfig.OpenAIAPIKey == "" {
			scanLog.Warn("AI parsing enabled but OpenAI API key is not configured")
		} else {
			aiParser = ai.NewOpenAIParser(&config.AppConfig, config.AppConfig.OpenAIAPIKey, true)
			if aiParser != nil && aiParser.IsEnabled() {
				aiEnabled = true
				scanLog.Debug("OpenAI parser initialized for filename metadata fallback")
			} else {
				scanLog.Warn("failed to initialize OpenAI parser, AI fallback disabled")
			}
		}
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
			{
				globalScanCacheMu.RLock()
				cache := globalScanCache
				globalScanCacheMu.RUnlock()
				if cache != nil {
					if fi, statErr := os.Stat(books[idx].FilePath); statErr == nil {
						if shouldSkipFile(books[idx].FilePath, fi.ModTime().Unix(), fi.Size(), cache) {
							return // progress deferred func will still fire
						}
					}
				}
			}

			// Extract metadata from the file. For multi-file books where the filename
			// is a generic part number (e.g. "01 Part 1 of 67.mp3"), use folder path
			// hierarchy combined with first-file tags for richer metadata.
			fallbackUsed := false
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
					func() {
						defer func() { recover() }()
						if store := getStore(); store != nil {
							if dbBook, dbErr := store.GetBookByFilePath(filePath); dbErr == nil && dbBook != nil {
								_ = store.UpdateScanCache(dbBook.ID, fi.ModTime().Unix(), fi.Size())
							}
						}
					}()
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
					createBookFilesForBook(dirPath, nil, scanLog)
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
				meta, mi, fileHash, pfErr := ProcessFile(filePath)
				if pfErr != nil {
					scanLog.Warn("ProcessFile failed for %s: %v", filePath, pfErr)
					fallbackUsed = true
					if gs := getStore(); gs != nil {
						sum := sha256.Sum256([]byte(filePath))
						_, _ = gs.IncrScanFailCount(fmt.Sprintf("%x", sum[:8]))
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
						if dbExisting.Title != "" && dbExisting.AuthorID != nil && *dbExisting.AuthorID != 0 {
							needsAI = false
						}
					}
				}
				if needsAI {
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
				// Create segments for multi-file books grouped by album
				if len(books[idx].SegmentFiles) > 1 {
					createBookFilesForBook(books[idx].FilePath, books[idx].SegmentFiles, scanLog)
				}
				// Update scan cache so next incremental scan skips this file.
				// Use a deferred recover guard in case GlobalStore is a non-nil interface
				// wrapping a nil concrete pointer (can happen in tests).
				func() {
					defer func() {
						if r := recover(); r != nil {
							scanLog.Warn("scan cache update recovered from panic: %v", r)
						}
					}()
					store := getStore()
					if store == nil {
						return
					}
					if fi, statErr := os.Stat(books[idx].FilePath); statErr == nil {
						if dbBook, dbErr := store.GetBookByFilePath(books[idx].FilePath); dbErr == nil && dbBook != nil {
							_ = store.UpdateScanCache(dbBook.ID, fi.ModTime().Unix(), fi.Size())
						}
					}
				}()
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

	if ctxErr != nil {
		return ctxErr
	}

	// Batch AI parsing phase: process candidates in batches of 20 with rate limiting
	if aiEnabled && len(aiCandidates) > 0 {
		scanLog.Info("AI batch parsing %d books in batches of 20", len(aiCandidates))
		const batchSize = 20
		const delayBetweenBatches = 2 * time.Second

		for start := 0; start < len(aiCandidates); start += batchSize {
			if ctx.Err() != nil {
				break
			}

			end := start + batchSize
			if end > len(aiCandidates) {
				end = len(aiCandidates)
			}
			batch := aiCandidates[start:end]

			// Collect filenames for this batch
			filenames := make([]string, len(batch))
			for i, idx := range batch {
				filenames[i] = filepath.Base(books[idx].FilePath)
			}

			aiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			results, aiErr := aiParser.ParseBatch(aiCtx, filenames)
			cancel()

			if aiErr != nil {
				scanLog.Warn("AI batch parsing failed (batch %d-%d): %v", start, end, aiErr)
				// Rate limit error — wait longer before retry/next batch
				if start+batchSize < len(aiCandidates) {
					time.Sleep(5 * time.Second)
				}
				continue
			}

			// Apply results
			for i, idx := range batch {
				if i >= len(results) || results[i] == nil {
					continue
				}
				aiMeta := results[i]
				if books[idx].Title == "" && aiMeta.Title != "" {
					books[idx].Title = aiMeta.Title
				}
				if books[idx].Author == "" && aiMeta.Author != "" {
					books[idx].Author = aiMeta.Author
				}
				if books[idx].Series == "" && aiMeta.Series != "" {
					books[idx].Series = aiMeta.Series
				}
				if books[idx].Position == 0 && aiMeta.SeriesNum > 0 {
					books[idx].Position = aiMeta.SeriesNum
				}
				if books[idx].Narrator == "" && aiMeta.Narrator != "" {
					books[idx].Narrator = aiMeta.Narrator
				}
				if books[idx].Publisher == "" && aiMeta.Publisher != "" {
					books[idx].Publisher = aiMeta.Publisher
				}

				// Re-save with updated metadata
				if saveErr := saveBook(ctx, &books[idx]); saveErr != nil {
					scanLog.Warn("failed to re-save AI-enriched book %s: %v", books[idx].FilePath, saveErr)
				}
			}

			scanLog.Info("AI batch %d-%d complete (%d results)", start, end, len(results))

			// Rate limit: wait between batches to avoid OpenAI throttling
			if end < len(aiCandidates) {
				time.Sleep(delayBetweenBatches)
			}
		}
	}

	// After processing all books, try to match series using external APIs for uncertain cases
	if err := identifySeriesUsingExternalAPIs(books); err != nil {
		scanLog.Warn("Error identifying series using external APIs: %v", err)
	}

	return nil
}

// extractInfoFromPath tries to extract author and title information from the file path
func extractInfoFromPath(book *Book) {
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

	// Extract author from directory name
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

// createBookFilesForBook creates BookFile records for a book.
// If segmentFiles is nil, it scans dirPath for all audio files.
// If segmentFiles is provided, only those specific files become BookFile records.
// After creating book files, if book.FilePath points to a file (not a directory),
// it normalizes it to the parent directory.
func createBookFilesForBook(bookFilePath string, segmentFiles []string, scanLog logger.Logger) {
	if getStore() == nil {
		return
	}

	dbBook, err := getStore().GetBookByFilePath(bookFilePath)
	if err != nil || dbBook == nil {
		return
	}

	// Check if book files already exist
	existing, _ := getStore().GetBookFiles(dbBook.ID)
	if len(existing) > 0 {
		return // BookFiles already created (rescan)
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
			return
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

		if h, herr := ComputeFileHash(filePath); herr == nil {
			bf.FileHash = h
			bf.OriginalFileHash = h
		}

		if serr := getStore().UpsertBookFile(bf); serr != nil {
			scanLog.Warn("failed to upsert book file for %s: %v", filePath, serr)
		}
	}

	// Normalize book.FilePath to directory if it currently points to a file
	if statErr == nil && !info.IsDir() {
		dirPath := filepath.Dir(bookFilePath)
		dbBook.FilePath = dirPath
		if _, updateErr := getStore().UpdateBook(dbBook.ID, dbBook); updateErr != nil {
			scanLog.Warn("failed to normalize FilePath for book %s: %v", dbBook.ID, updateErr)
		}
	}

	if len(segmentFiles) > 0 {
		scanLog.Debug("Created %d book files for book %s", len(segmentFiles), dbBook.Title)
	}
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
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
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
	for _, line := range strings.Split(string(data), "\n") {
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
func groupFilesIntoBooks(files []string) []Book {
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
		}
		if ok, sorted := DetectMultiFileGroup(infos, DefaultMultiFileConfig()); ok {
			segs := make([]string, len(sorted))
			for i, s := range sorted {
				segs[i] = s.Path
			}
			slog.Info("scanner multi-file group detected",
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
	sampleSize := 3
	if sampleSize > len(files) {
		sampleSize = len(files)
	}

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
	books = append(books, consolidateChapterGroups(noAlbum)...)
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
			LibraryState:      stringPtr(ls),
			Quantity:          intPtr(1),
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
			for _, lookup := range hashLookups {
				candidate, err := lookup(*fileHash)
				if err != nil {
					continue
				}
				if candidate != nil {
					existing = candidate
					break
				}
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
				// Check for metadata hash duplicates
				detectMetadataHashDuplicate(dbBook, defaultLog)
				if scanHooks != nil {
					scanHooks.OnBookScanned(dbBook.ID, dbBook.Title)
					scanHooks.OnImportDedup(dbBook.ID)
				}
			}
			return err
		}

		// Preserve original hash if already stored and we are rescanning a library file
		if existing.OriginalFileHash != nil {
			dbBook.OriginalFileHash = existing.OriginalFileHash
		}
		if dbBook.OrganizedFileHash == nil && existing.OrganizedFileHash != nil {
			dbBook.OrganizedFileHash = existing.OrganizedFileHash
		}

		// Preserve enriched fields that scanner doesn't extract (e.g. from metadata fetch or AI parse)
		preserveExistingFields(dbBook, existing)

		_, err = getStore().UpdateBook(existing.ID, dbBook)
		if err == nil {
			// Check for metadata hash duplicates after update
			detectMetadataHashDuplicate(dbBook, defaultLog)
		}
		return err
	}

	return fmt.Errorf("database store not initialized")
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
			file.Seek(-chunkSize, io.SeekEnd)
			last := make([]byte, chunkSize)
			n, err := file.Read(last)
			if err != nil && err != io.EOF {
				return "", err
			}
			h.Write(last[:n])
		}

		// Include size in hash
		h.Write([]byte(fmt.Sprintf("%d", info.Size())))

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

func stringPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}

func nullablePtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
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
