// file: internal/scanner/service.go
// version: 1.10.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-29
package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// scanServiceStore is the narrow slice of scannerStore this service uses.
// scanServiceStore is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 5 methods. It was a
// pure pass-through of database.* embeds — 93 methods, none of them declared
// here and almost none of them used.
type scanServiceStore interface {
	GetAllImportPaths() ([]database.ImportPath, error)
	UpdateImportPath(id int, importPath *database.ImportPath) error
	GetScanCacheMap() (map[string]database.ScanCacheEntry, error)
	GetDirtyBookFolders() ([]string, error)
	CountBooksByPathPrefix(prefix string) (int, error)
}

// ScanService orchestrates multi-folder audiobook scanning.
type ScanService struct {
	db             scanServiceStore
	embedStore     *database.EmbeddingStore
	PostScanFn     func() // optional hook called after each full scan completes
	activityWriter *activity.Writer
	// AutoOrganizeFn is an optional hook called after books are processed in a
	// folder. The server layer wires in the auto-organize logic here to avoid
	// an import cycle (organizer → scanner → organizer).
	AutoOrganizeFn func(ctx context.Context, books []Book, log logger.Logger)
}

// NewScanService creates a new ScanService backed by the given store and embedding store.
func NewScanService(db scanServiceStore) *ScanService {
	return &ScanService{db: db}
}

// SetEmbeddingStore sets the EmbeddingStore for dedup candidate creation.
func (ss *ScanService) SetEmbeddingStore(es *database.EmbeddingStore) {
	ss.embedStore = es
}

// SetActivityWriter sets the activity writer used to batch per-book scan events.
func (ss *ScanService) SetActivityWriter(w *activity.Writer) {
	ss.activityWriter = w
}

// ScanRequest holds parameters for a scan operation.
type ScanRequest struct {
	FolderPath  *string
	Priority    *int
	ForceUpdate *bool

	// IncludeRootDir folds the organized library root into this scan WITHOUT
	// disabling the incremental skip cache.
	//
	// ForceUpdate did both at once, and the coupling was the problem: it is the
	// only way to reach RootDir, but it also sets scanCache = nil below, so the
	// only supported way to scan the whole library was also a full re-hash of
	// every file in it. Measured on the reference deployment that is 1.85 TB of
	// reads against storage that saturates at ~290 MiB/s (flat across 48/96/192
	// workers) -- about 5.5 hours once RootDir's files are included, versus
	// minutes when unchanged files are skipped.
	//
	// Separating them makes "scan everything, skip what has not changed" a
	// reachable combination for the first time.
	IncludeRootDir *bool

	// ResumeFolderIdx and ResumeItemOffset restore position from a previous
	// run's checkpoint. Folders before ResumeFolderIdx are skipped entirely;
	// within ResumeFolderIdx the first ResumeItemOffset books are skipped.
	//
	// Per-FOLDER granularity alone is not enough and that is the whole reason
	// the offset exists: a single production folder holds ~14,000 items, so both
	// the tag pass and the metadata pass run for hours INSIDE one folder. A
	// checkpoint that can only say "folder 3 of 5" throws away most of a day's
	// work on a restart.
	ResumeFolderIdx  int
	ResumeItemOffset int

	// Checkpoint, when non-nil, persists resume position. It is called after
	// each completed chunk of books and once per completed folder. Implementations
	// must be safe to call from the scan goroutine and should not block.
	Checkpoint func(folderIdx, itemOffset int)
}

// scanChunkSize bounds how many books are processed between checkpoints. It
// trades checkpoint write volume against how much work a restart repeats: at
// 500 a killed scan loses at most 500 books of metadata extraction, while a
// 14,000-item folder writes 28 checkpoints rather than 14,000.
const scanChunkSize = 500

// ScanStats accumulates per-scan book counts by source.
type ScanStats struct {
	TotalBooks   int
	LibraryBooks int
	ImportBooks  int
}

// PerformScan executes the multi-folder scan operation.
// Accepts a logger.Logger for unified logging, progress, and change tracking.
//
// Checkpoint/resume: ScanRequest.Checkpoint, ResumeFolderIdx and
// ResumeItemOffset carry position across a restart. Folders already finished are
// skipped whole; the folder that was in flight resumes at the last completed
// chunk boundary.
//
// History, because this comment previously said the opposite. R-7 removed dead
// save/clear calls for a resume path that never existed (LoadParams[ScanParams]
// had zero callers) and recorded that the def used ResumePolicy=ResumeDrop, so a
// full re-walk was simply accepted. The policy became ResumeRestart in #2500,
// which stopped an interrupted scan being discarded but still re-ran it from
// zero — library_core_ops.go said so in capitals. On 2026-08-17 a production
// scan ran 3h50m+ against a 63,044-book library; losing that to a restart is the
// cost this plumbing removes.
//
// A full re-walk remains correct and idempotent — the scan cache skips unchanged
// files — so resuming is an optimisation, not a correctness requirement. Nothing
// breaks if a checkpoint is missing or stale; the worst case is the old
// behaviour of redoing work.
func (ss *ScanService) PerformScan(ctx context.Context, req *ScanRequest, log logger.Logger) error {
	return ss.performScanInternal(ctx, "", req, log)
}

// performScanInternal is the shared implementation behind PerformScan.
// opID may be empty when called without a tracked operation (activity batching is skipped).
func (ss *ScanService) performScanInternal(ctx context.Context, opID string, req *ScanRequest, log logger.Logger) error {
	// Set the active embedding store for dedup detection during this scan
	setActiveEmbeddingStore(ss.embedStore)

	if log == nil {
		log = logger.New("scan")
	}
	forceUpdate := req.ForceUpdate != nil && *req.ForceUpdate
	if forceUpdate {
		log.Debug("ScanService: Force update enabled - will update all book file paths in database")
	}

	// Determine which folders to scan
	includeRootDir := req.IncludeRootDir != nil && *req.IncludeRootDir
	foldersToScan, err := ss.determineFoldersToScan(req.FolderPath, forceUpdate, includeRootDir, log)
	if err != nil {
		return err
	}

	if len(foldersToScan) == 0 {
		log.Warn("No folders to scan")
		return nil
	}

	// Pre-load scan cache for incremental skip checks.
	var scanCache map[string]database.ScanCacheEntry
	if !forceUpdate {
		cache, err := ss.db.GetScanCacheMap()
		if err != nil {
			log.Warn("Failed to load scan cache, running full scan: %v", err)
		} else {
			scanCache = cache
			log.Info("Loaded scan cache with %d entries", len(cache))
		}
	}

	// Add any folders that have books flagged needs_rescan.
	if !forceUpdate && scanCache != nil {
		dirtyFolders, err := ss.db.GetDirtyBookFolders()
		if err == nil && len(dirtyFolders) > 0 {
			log.Info("Found %d folders with dirty books", len(dirtyFolders))
			folderSet := make(map[string]bool)
			for _, f := range foldersToScan {
				folderSet[f] = true
			}
			for _, df := range dirtyFolders {
				if !folderSet[df] {
					foldersToScan = append(foldersToScan, df)
				}
			}
		}
	}

	// First pass: count total files across all folders.
	// For incremental scans we use the cache size as an approximation to avoid
	// the expensive directory walk.
	var totalFilesAcrossFolders int
	if forceUpdate || scanCache == nil {
		totalFilesAcrossFolders = ss.countFilesAcrossFolders(foldersToScan, log)
		log.Info("Total audiobook files across all folders: %d", totalFilesAcrossFolders)
		if totalFilesAcrossFolders == 0 {
			log.Warn("No audiobook files detected during pre-scan; totals will update as files are processed")
		}
	} else {
		totalFilesAcrossFolders = len(scanCache)
		log.Info("Incremental scan: ~%d known files, checking for changes", totalFilesAcrossFolders)
	}

	// Install scan cache into the scanner package so workers can skip unchanged
	// files. Reference-counted (audit 2026-07-17 R-4): library.scan and
	// library.import have distinct ConcurrencyKeys and can run this path
	// concurrently — with a plain deferred clear, the first finisher nilled the
	// cache under the still-running run.
	releaseScanCache := AcquireScanCache(scanCache)
	defer releaseScanCache()

	// Build the per-scan works lookup cache so saveBookToDatabase does not
	// run GetAllWorks() once per book (MAYDEPLOY-H6: 50K books × 50K works
	// = 2.5B lookups → single load + map access). Reference-counted for the
	// same R-4 concurrent-run reason as the scan cache above.
	AcquireWorksLookupCache()
	defer ReleaseWorksLookupCache()

	// Scan each folder
	stats := &ScanStats{}
	var processedFiles atomic.Int32

	for folderIdx, folderPath := range foldersToScan {
		// Both cancellation channels have to be checked here. log.IsCanceled()
		// is the operation's own stop flag; ctx is the request/server one. Only
		// the former was checked, so a cancelled CONTEXT did not stop the walk —
		// the loop carried on into every remaining folder and each one failed
		// its metadata pass with "context canceled".
		//
		// Production, 2026-08-11: 2,406 folders were processed that way inside a
		// single scan, each logging the failure and continuing. The scan then
		// reported success.
		if err := ctx.Err(); err != nil {
			log.Info("Scan canceled (context): %v", err)
			return fmt.Errorf("scan canceled: %w", err)
		}
		if log.IsCanceled() {
			log.Info("Scan canceled")
			return fmt.Errorf("scan canceled")
		}

		// Resume: skip folders a previous run already finished. This is the
		// cheap half of resuming — it avoids re-walking and re-tagging an
		// entire completed folder.
		if folderIdx < req.ResumeFolderIdx {
			log.Info("Resuming: skipping folder %d/%d (already completed): %s",
				folderIdx+1, len(foldersToScan), folderPath)
			continue
		}
		// Within the folder the previous run died in, skip the books it already
		// processed. Only that one folder carries an offset; every later folder
		// starts at zero.
		itemOffset := 0
		if folderIdx == req.ResumeFolderIdx {
			itemOffset = req.ResumeItemOffset
		}

		err := ss.scanFolder(ctx, folderIdx, folderPath, foldersToScan, totalFilesAcrossFolders, &processedFiles, stats, opID, itemOffset, req.Checkpoint, log)
		if err != nil {
			log.Error("Error scanning folder %s: %v", folderPath, err)
			continue
		}
		// The folder finished. Record that, so a restart begins at the next
		// folder with a zero offset rather than re-entering this one.
		if req.Checkpoint != nil {
			req.Checkpoint(folderIdx+1, 0)
		}
	}

	// Flush any pending per-file batches before writing the completion entry,
	// so batch rows land in the activity log before the scan-finished marker.
	activity.FlushOperation(ss.activityWriter, opID)

	// Report completion with change counters
	counters := log.ChangeCounters()
	if counters != nil && (counters["book_create"] > 0 || counters["book_update"] > 0) {
		log.Info("scan changes: %d created, %d updated, %d skipped",
			counters["book_create"], counters["book_update"], counters["book_skip"])
	}
	ss.reportCompletion(totalFilesAcrossFolders, int(processedFiles.Load()), stats, log)
	if ss.PostScanFn != nil {
		ss.PostScanFn()
	}
	return nil
}

func (ss *ScanService) determineFoldersToScan(folderPath *string, forceUpdate, includeRootDir bool, log logger.Logger) ([]string, error) {
	var foldersToScan []string

	if folderPath != nil && *folderPath != "" {
		// Scan specific folder
		foldersToScan = []string{*folderPath}
		log.Info("Starting scan of folder: %s", *folderPath)
	} else {
		// DECISION (2026-08-11): a DEFAULT scan deliberately does NOT walk
		// RootDir; only force_update does. Now that library.scan runs on a
		// timer (scheduled.library_scan, default every 6h) this is a standing
		// choice rather than an accident, so the reasoning is recorded here:
		//
		//  1. RootDir is organize's DESTINATION, not a source. scanFolder
		//     invokes AutoOrganizeFn on the books it processes, so folding the
		//     destination into every timed scan would feed already-organized
		//     books back into the organize path on a loop.
		//  2. RootDir is operator-set and unconstrained, so its blast radius is
		//     unknown at this layer. On the reference deployment the books
		//     tree holds a hands-off iTunes subtree alongside PDFs, archives
		//     and unrelated media; a RootDir pointed at or above that level
		//     would be re-walked every 6 hours. (NOT asserting that is where
		//     RootDir currently points — it is a DB setting this code cannot
		//     see, which is exactly why the timed path stays conservative.)
		//  2b. Point 1 is conditional, not absolute: AutoOrganizeFn returns
		//     immediately unless config.AppConfig.AutoOrganize is set
		//     (server.go:925). Where auto-organize is OFF, scanning RootDir
		//     cannot feed the organize loop at all, and the only remaining cost
		//     is the walk itself -- which the incremental cache already absorbs.
		//     That is why include_root_dir is safe to offer as an explicit
		//     opt-in while the DEFAULT stays exactly as conservative as before.
		//  3. The consequence is bounded and known: a folder dropped straight
		//     into the organized library root is NOT auto-discovered. The
		//     remedy is to add it as an import path (which the watcher
		//     supervisor now picks up without a restart) or to run a scan with
		//     force_update=true.
		//
		// The log line below states the exclusion out loud so this is a
		// visible behaviour, not a silent one.
		if (forceUpdate || includeRootDir) && config.AppConfig.RootDir != "" {
			foldersToScan = append(foldersToScan, config.AppConfig.RootDir)
			if forceUpdate {
				log.Info("Full rescan: including library path %s", config.AppConfig.RootDir)
			} else {
				log.Info("Including library path %s (incremental: unchanged files are still skipped)",
					config.AppConfig.RootDir)
			}
		}

		// Add all import paths
		folders, err := ss.db.GetAllImportPaths()
		if err != nil {
			return nil, fmt.Errorf("failed to get import paths: %w", err)
		}
		for _, folder := range folders {
			if folder.Enabled {
				foldersToScan = append(foldersToScan, folder.Path)
			}
		}
		log.Info("Scanning %d total folders (%d import paths)", len(foldersToScan), len(folders))
		if !forceUpdate && !includeRootDir && config.AppConfig.RootDir != "" {
			log.Info("Library root %s excluded from this incremental scan (organize destination); "+
				"use include_root_dir=true to add it (still incremental), or force_update=true "+
				"to add it AND re-hash everything", config.AppConfig.RootDir)
		}
	}

	return foldersToScan, nil
}

func (ss *ScanService) countFilesAcrossFolders(foldersToScan []string, log logger.Logger) int {
	totalFilesAcrossFolders := 0
	for _, folderPath := range foldersToScan {
		if _, err := os.Stat(folderPath); os.IsNotExist(err) {
			log.Warn("Folder does not exist: %s", folderPath)
			continue
		}
		fileCount := 0
		walkErrLogged := false
		walkErr := filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// Permission/I-O errors here undercount the progress
				// denominator; log the first per folder so the undercount is
				// not silent (audit 2026-07-17 H6), then keep counting.
				if !walkErrLogged {
					walkErrLogged = true
					log.Warn("count phase: walk error under %s at %s: %v (progress total may undercount)", folderPath, path, err)
				}
				return nil
			}
			if d.IsDir() {
				// Must match the discovery walk exactly. If the counter walks
				// a subtree the scanner skips, the progress denominator counts
				// files that will never be scanned and the bar can never reach
				// 100%. A 15 GB .backups directory inside the library would do
				// precisely that.
				if pathutil.ShouldSkipDir(folderPath, path) {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			for _, supported := range config.AppConfig.SupportedExtensions {
				if ext == supported {
					fileCount++
					break
				}
			}
			return nil
		})
		if walkErr != nil && !walkErrLogged {
			// Only reachable when the root itself fails to stat.
			log.Warn("count phase: walk failed for %s: %v (progress total may undercount)", folderPath, walkErr)
		}
		log.Info("Folder %s: Found %d audiobook files", folderPath, fileCount)
		totalFilesAcrossFolders += fileCount
	}
	return totalFilesAcrossFolders
}

func (ss *ScanService) scanFolder(ctx context.Context, folderIdx int, folderPath string, foldersToScan []string, totalFilesAcrossFolders int, processedFiles *atomic.Int32, stats *ScanStats, opID string, itemOffset int, checkpoint func(folderIdx, itemOffset int), log logger.Logger) error {
	currentProcessed := int(processedFiles.Load())
	displayTotal := totalFilesAcrossFolders
	if currentProcessed > displayTotal {
		displayTotal = currentProcessed
	}
	log.UpdateProgress(currentProcessed, displayTotal, fmt.Sprintf("Scanning folder %d/%d: %s", folderIdx+1, len(foldersToScan), folderPath))
	log.Info("Scanning folder: %s", folderPath)

	// Check if folder exists
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		log.Warn("Folder does not exist: %s", folderPath)
		return nil
	}

	// Scan directory for audiobook files (parallel)
	workers := config.AppConfig.ConcurrentScans
	if workers < 1 {
		workers = 4
	}
	books, err := ScanDirectoryParallel(ctx, folderPath, workers, log.With("scanner"))
	if err != nil {
		return fmt.Errorf("failed to scan folder: %w", err)
	}

	log.Info("Found %d audiobook files in %s", len(books), folderPath)
	stats.TotalBooks += len(books)
	if folderPath == config.AppConfig.RootDir {
		stats.LibraryBooks += len(books)
	} else {
		stats.ImportBooks += len(books)
	}

	// Prepare per-book progress reporting
	targetTotal := totalFilesAcrossFolders
	if targetTotal == 0 {
		targetTotal = len(books)
	}
	progressCallback := func(_ int, _ int, bookPath string) {
		current := processedFiles.Add(1)
		displayTotal := targetTotal
		if int(current) > displayTotal {
			displayTotal = int(current)
		}
		message := fmt.Sprintf("Processed: %d/%d books", current, displayTotal)
		if bookPath != "" {
			message = fmt.Sprintf("Processed: %d/%d books (%s)", current, displayTotal, filepath.Base(bookPath))
		}
		log.UpdateProgress(int(current), displayTotal, message)
		if ss.activityWriter != nil && opID != "" {
			activity.LogBatch(ss.activityWriter, opID, "tag-scan", "scan-service",
				activity.BatchItem{Name: filepath.Base(bookPath)})
		}
	}

	// Process the books to extract metadata (parallel)
	if len(books) > 0 {
		// Tag every book with its source import path before saving to DB.
		// This must happen before ProcessBooksParallel (which calls CreateBook)
		// so that source_import_path is set on first insert and survives organize.
		// Only apply when the folder being scanned is NOT the organized library root,
		// otherwise we'd overwrite the original import path on re-scans.
		if folderPath != config.AppConfig.RootDir {
			for i := range books {
				if books[i].SourceImportPath == "" {
					books[i].SourceImportPath = folderPath
				}
			}
		}

		// Deterministic order is what makes an offset mean the same thing across
		// runs. ScanDirectoryParallel returns books in worker-completion order,
		// which varies run to run — resuming at "offset 4200" against a
		// differently-ordered slice would skip an arbitrary 4200 books and
		// re-run others, so the sort is load-bearing, not cosmetic.
		sort.Slice(books, func(i, j int) bool { return books[i].FilePath < books[j].FilePath })

		start := itemOffset
		if start < 0 {
			start = 0
		}
		if start > len(books) {
			start = len(books)
		}
		if start > 0 {
			log.Info("Resuming folder %s at book %d/%d", folderPath, start, len(books))
			processedFiles.Add(int32(start))
		}

		log.Info("Processing metadata for %d books using %d workers (from offset %d)", len(books), workers, start)
		processChunk := func(ctx context.Context, chunk []Book) error {
			return ProcessBooksParallel(ctx, chunk, workers, progressCallback, log.With("scanner"))
		}
		if err := ss.processBookChunks(ctx, books, start, folderIdx, checkpoint, processChunk); err != nil {
			// Do NOT fall through to auto-organize. These books did not get
			// their metadata extracted, so their title/author are whatever the
			// scan started with — frequently empty. Organizing them anyway
			// expands the naming pattern over blank fields and sends every one
			// of them to the same degenerate path, where all but the first fail
			// as "target already occupied".
			//
			// That is not a theoretical risk. In the 4h15m production scan of
			// 2026-08-11 this branch fired 2,406 times with "context canceled",
			// and the same run logged 7,561 "safeRename refusing to overwrite
			// existing destination" and 3,481 organize-collision candidates.
			// 848 books collided on one path,
			// "Unknown Author/Unknown Title/Unknown Title - Unknown Author.mp3".
			//
			// Returning the error also stops this folder being counted as a
			// success: scanFolder used to log here and then `return nil`, so a
			// folder whose metadata pass failed outright was indistinguishable
			// from one that worked.
			log.Error("Failed to process books: %v", err)
			return fmt.Errorf("metadata processing failed for %s (%d books), "+
				"skipping auto-organize so unprocessed books are not filed under "+
				"placeholder names: %w", folderPath, len(books), err)
		}
		log.Info("Successfully processed %d books", len(books))

		// Auto-organize if enabled (via server-layer hook to avoid import cycle)
		if ss.AutoOrganizeFn != nil {
			ss.AutoOrganizeFn(ctx, books, log)
		}
	}

	// Update book count for this import path
	ss.updateImportPathBookCount(folderPath, len(books), log)

	return nil
}

// processBookChunks runs ProcessBooksParallel over books[start:] in fixed-size
// chunks, checkpointing the absolute offset after each chunk completes.
//
// Chunking is what gives resume a granularity finer than "a whole folder"
// without giving up parallelism: each chunk is still processed by the full
// worker pool, and only the boundary between chunks is a synchronisation point.
// A chunk is checkpointed only after ProcessBooksParallel returns success for
// it, so a chunk that fails is re-run on resume rather than skipped.
// process is injected rather than calling ProcessBooksParallel directly so the
// chunk boundaries and checkpoint sequence can be tested without a database.
func (ss *ScanService) processBookChunks(
	ctx context.Context,
	books []Book,
	start int,
	folderIdx int,
	checkpoint func(folderIdx, itemOffset int),
	process func(ctx context.Context, chunk []Book) error,
) error {
	for off := start; off < len(books); off += scanChunkSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := off + scanChunkSize
		if end > len(books) {
			end = len(books)
		}
		if err := process(ctx, books[off:end]); err != nil {
			// No checkpoint on failure: recording `end` here would let a resume
			// step over a chunk that never actually processed.
			return err
		}
		if checkpoint != nil {
			checkpoint(folderIdx, end)
		}
	}
	return nil
}

// updateImportPathBookCount stores the accurate total book count for an import
// path after a scan. It queries the DB for the real total (not just what was
// found in this incremental batch) so the stored count stays correct across
// both full and incremental scans.
func (ss *ScanService) updateImportPathBookCount(folderPath string, _ int, log logger.Logger) {
	total, err := ss.db.CountBooksByPathPrefix(folderPath)
	if err != nil {
		log.Warn("Failed to count books for folder %s: %v", folderPath, err)
		return
	}
	folders, _ := ss.db.GetAllImportPaths()
	for _, folder := range folders {
		if folder.Path == folderPath {
			folder.BookCount = total
			if err := ss.db.UpdateImportPath(folder.ID, &folder); err != nil {
				log.Warn("Failed to update book count for folder %s: %v", folderPath, err)
			}
			break
		}
	}
}

func (ss *ScanService) reportCompletion(totalFilesAcrossFolders int, finalProcessed int, stats *ScanStats, log logger.Logger) {
	var completionMsg string
	if stats.LibraryBooks > 0 && stats.ImportBooks > 0 {
		completionMsg = fmt.Sprintf("Scan completed. Library: %d books, Import: %d books (Total: %d)", stats.LibraryBooks, stats.ImportBooks, stats.TotalBooks)
	} else if stats.LibraryBooks > 0 {
		completionMsg = fmt.Sprintf("Scan completed. Library: %d books", stats.LibraryBooks)
	} else if stats.ImportBooks > 0 {
		completionMsg = fmt.Sprintf("Scan completed. Import: %d books", stats.ImportBooks)
	} else {
		completionMsg = "Scan completed. No books found"
	}

	finalTotal := totalFilesAcrossFolders
	if finalProcessed > finalTotal {
		finalTotal = finalProcessed
	}
	log.UpdateProgress(finalProcessed, finalTotal, completionMsg)
	log.Info("%s", completionMsg)
}

// ApplyOrganizedFileMetadata updates a book's hash and size fields to reflect
// a newly-organized file path. It is exported so server-layer code can reuse it.
func ApplyOrganizedFileMetadata(book *database.Book, newPath string) {
	hash, err := ComputeFileHash(newPath)
	if err != nil {
		defaultLog.Warn("failed to compute organized hash for %s: %v", newPath, err)
	} else if hash != "" {
		book.FileHash = stringPtr(hash)
		book.OrganizedFileHash = stringPtr(hash)
		if book.OriginalFileHash == nil {
			book.OriginalFileHash = stringPtr(hash)
		}
	}
	if info, err := os.Stat(newPath); err == nil {
		size := info.Size()
		book.FileSize = &size
	}
}
