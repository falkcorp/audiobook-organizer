// file: internal/scanner/service.go
// version: 1.10.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-07-18
package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// scanServiceStore is the narrow slice of database.Store this service uses.
type scanServiceStore interface {
	database.OperationStore
	database.BookReader
	database.BookWriter
	database.ImportPathStore
	database.MaintenanceStore
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
}

// ScanStats accumulates per-scan book counts by source.
type ScanStats struct {
	TotalBooks   int
	LibraryBooks int
	ImportBooks  int
}

// PerformScan executes the multi-folder scan operation.
// Accepts a logger.Logger for unified logging, progress, and change tracking.
//
// R-7: there is intentionally no checkpoint/resume plumbing here. The old
// PerformScanWithID saved operations.ScanParams via operations.SaveParams for a
// resume path that never existed — LoadParams[ScanParams] had zero callers and
// the method itself had zero callers. The v2 "library.scan" OperationDef uses
// ResumePolicy=ResumeDrop (see internal/server/library_core_ops.go), so an
// interrupted scan is dropped and simply restarts from scratch on the next run;
// a full re-walk is idempotent (the scan cache skips unchanged files). The dead
// save/clear calls were removed to stop implying a resume contract that isn't
// honored.
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
	foldersToScan, err := ss.determineFoldersToScan(req.FolderPath, forceUpdate, log)
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
		if log.IsCanceled() {
			log.Info("Scan canceled")
			return fmt.Errorf("scan canceled")
		}

		err := ss.scanFolder(ctx, folderIdx, folderPath, foldersToScan, totalFilesAcrossFolders, &processedFiles, stats, opID, log)
		if err != nil {
			log.Error("Error scanning folder %s: %v", folderPath, err)
			continue
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

func (ss *ScanService) determineFoldersToScan(folderPath *string, forceUpdate bool, log logger.Logger) ([]string, error) {
	var foldersToScan []string

	if folderPath != nil && *folderPath != "" {
		// Scan specific folder
		foldersToScan = []string{*folderPath}
		log.Info("Starting scan of folder: %s", *folderPath)
	} else {
		// Full scan: include RootDir if force_update enabled, then all import paths
		if forceUpdate && config.AppConfig.RootDir != "" {
			foldersToScan = append(foldersToScan, config.AppConfig.RootDir)
			log.Info("Full rescan: including library path %s", config.AppConfig.RootDir)
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

func (ss *ScanService) scanFolder(ctx context.Context, folderIdx int, folderPath string, foldersToScan []string, totalFilesAcrossFolders int, processedFiles *atomic.Int32, stats *ScanStats, opID string, log logger.Logger) error {
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

		log.Info("Processing metadata for %d books using %d workers", len(books), workers)
		if err := ProcessBooksParallel(ctx, books, workers, progressCallback, log.With("scanner")); err != nil {
			log.Error("Failed to process books: %v", err)
		} else {
			log.Info("Successfully processed %d books", len(books))
		}

		// Auto-organize if enabled (via server-layer hook to avoid import cycle)
		if ss.AutoOrganizeFn != nil {
			ss.AutoOrganizeFn(ctx, books, log)
		}
	}

	// Update book count for this import path
	ss.updateImportPathBookCount(folderPath, len(books), log)

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
