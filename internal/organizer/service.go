// file: internal/organizer/service.go
// version: 1.7.1
// guid: c3d4e5f6-a7b8-c9d0-e1f2-a3b4c5d6e7f8
// last-edited: 2026-07-13

package organizer

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
	ulid "github.com/oklog/ulid/v2"
)

// Store is the narrow slice of database.Store required by this package.
// Using sub-interfaces reduces coupling and makes the service testable with a mock.
type Store interface {
	database.BookStore
	database.BookFileStore
	database.OperationStore
	database.AuthorStore
	database.NarratorStore
	database.MaintenanceStore
	database.TagStore
}

// Compile-time proof that PebbleStore satisfies organizer.Store.
var _ Store = (*database.PebbleStore)(nil)

// WriteBackEnqueuer is the interface for enqueuing iTunes write-back requests.
// Implemented by the server's WriteBackBatcher.
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
}

// Service orchestrates the library organization operation.
type Service struct {
	db               Store
	organizeHooks    OrganizeHooks
	writeBackBatcher WriteBackEnqueuer
	// ScanEnqueuer enqueues a background library scan. Wired by the server
	// package after construction to avoid a circular import.
	ScanEnqueuer func(ctx context.Context) error

	// DiscoverITunesLibraryPath discovers the iTunes library path.
	// Set by the server package after construction.
	DiscoverITunesLibraryPath func() string

	// ExecuteITunesSync executes an iTunes library sync.
	// Set by the server package after construction.
	ExecuteITunesSync func(ctx context.Context, log logger.Logger, libraryPath string) error

	// ApplyOrganizedFileMetadata applies metadata from an organized file to a Book.
	// Set by the server package after construction.
	ApplyOrganizedFileMetadata func(book *database.Book, newPath string)

	// ComputeITunesPath computes the iTunes-compatible path for a file.
	// Breaks the metafetch import cycle.
	ComputeITunesPath func(filePath string) string

	// FetchMetadataForBook fetches metadata for a book by ID.
	// Returns (result, error). Breaks the metafetch import cycle.
	FetchMetadataForBook func(bookID string) (interface{}, error)
}

// SetWriteBackBatcher sets the iTunes write-back batcher.
func (orgSvc *Service) SetWriteBackBatcher(b WriteBackEnqueuer) {
	orgSvc.writeBackBatcher = b
}

// SetOrganizeHooks sets optional hooks that are propagated to every
// Organizer instance created by this service.
func (orgSvc *Service) SetOrganizeHooks(hooks OrganizeHooks) {
	orgSvc.organizeHooks = hooks
}

// newOrganizer creates an Organizer with the service's hooks pre-wired.
func (orgSvc *Service) newOrganizer() *Organizer {
	org := NewOrganizer(&config.AppConfig)
	if orgSvc.organizeHooks != nil {
		org.SetHooks(orgSvc.organizeHooks)
	}
	return org
}

// NewService creates a new Service.
func NewService(db Store) *Service {
	return &Service{
		db: db,
		// Default no-ops for optional callbacks
		DiscoverITunesLibraryPath: func() string { return "" },
		ExecuteITunesSync: func(ctx context.Context, log logger.Logger, libraryPath string) error {
			return nil
		},
		ApplyOrganizedFileMetadata: func(book *database.Book, newPath string) {},
		ComputeITunesPath:          func(_ string) string { return "" },
		FetchMetadataForBook:       func(_ string) (interface{}, error) { return nil, nil },
	}
}

// Request describes an organize operation request.
type Request struct {
	FolderPath         *string
	Priority           *int
	FetchMetadataFirst bool
	SyncITunesFirst    bool
	OperationID        string
	BookIDs            []string // if set, only organize these books
}

// Stats holds organize operation statistics.
type Stats struct {
	Organized      int
	ReOrganized    int
	AlreadyCorrect int
	Skipped        int // soft-deleted / non-primary / missing file skips
	Failed         int
	Total          int
}

// PerformOrganizeWithID executes organization with checkpoint support.
func (orgSvc *Service) PerformOrganizeWithID(ctx context.Context, opID string, req *Request, log logger.Logger) error {
	_ = operations.SaveParams(orgSvc.db, opID, operations.OrganizeParams{})
	req.OperationID = opID
	err := orgSvc.PerformOrganize(ctx, req, log)
	_ = operations.ClearState(orgSvc.db, opID)
	return err
}

// PerformOrganize executes the library organization operation
func (orgSvc *Service) PerformOrganize(ctx context.Context, req *Request, log logger.Logger) error {
	log.Info("Starting file organization")

	// Optional: sync iTunes library first to ensure all books are up to date
	if req.SyncITunesFirst {
		orgSvc.syncITunesBeforeOrganize(ctx, log)
	}

	// Auto-backup database before organizing
	orgSvc.autoBackup(log)

	// Get books — either specific IDs or all books
	const fetchPageSize = 1000
	var allBooks []database.Book
	if len(req.BookIDs) > 0 {
		for _, id := range req.BookIDs {
			book, err := orgSvc.db.GetBookByID(id)
			if err != nil || book == nil {
				log.Warn("Book %s not found, skipping", id)
				continue
			}
			allBooks = append(allBooks, *book)
		}
	} else {
		for offset := 0; ; offset += fetchPageSize {
			// GetAllBooksCore: under prod's memdb-backed default, GetAllBooks
			// already returned heavy-field-nil'd (Description/BookSig*/
			// Author/Series) projections here, and expandPattern already
			// falls back to AuthorID/SeriesID lookups when Author/Series are
			// nil (see organizer.go) — so ToBook() is a lossless read-only
			// projection for this call site, not a behavior change.
			page, fetchErr := orgSvc.db.GetAllBooksCore(fetchPageSize, offset)
			if fetchErr != nil {
				log.Error("Failed to fetch books: %s", fetchErr.Error())
				return fmt.Errorf("failed to fetch books: %w", fetchErr)
			}
			for i := range page {
				allBooks = append(allBooks, page[i].ToBook())
			}
			if len(page) < fetchPageSize {
				break
			}
		}
	}

	logMsg := fmt.Sprintf("Fetched %d total books from database", len(allBooks))
	log.Info("%s", logMsg)
	log.Debug("Organize: %s", logMsg)

	// Optional: fetch metadata before organizing to normalize author names
	if req.FetchMetadataFirst {
		log.Info("Fetching metadata before organizing...")
		enriched := 0
		for i := range allBooks {
			book := &allBooks[i]
			if book.CoverURL != nil {
				continue // already enriched
			}
			if _, err := orgSvc.FetchMetadataForBook(book.ID); err == nil {
				enriched++
			}
		}
		log.Info("Metadata enriched for %d books", enriched)

		// Re-fetch all books since metadata may have changed
		allBooks = nil
		for offset := 0; ; offset += fetchPageSize {
			page, fetchErr := orgSvc.db.GetAllBooksCore(fetchPageSize, offset)
			if fetchErr != nil {
				return fmt.Errorf("failed to re-fetch books after metadata: %w", fetchErr)
			}
			for i := range page {
				allBooks = append(allBooks, page[i].ToBook())
			}
			if len(page) < fetchPageSize {
				break
			}
		}
	}

	// Filter books that need organizing
	booksToOrganize, alreadyCorrect := orgSvc.FilterBooksNeedingOrganization(allBooks, log)

	logMsg = fmt.Sprintf("Found %d books that need organizing, %d already correct (out of %d total)",
		len(booksToOrganize), len(alreadyCorrect), len(allBooks))
	log.Info("%s", logMsg)
	log.Debug("Organize: %s", logMsg)

	// Perform organization
	stats := orgSvc.organizeBooks(ctx, booksToOrganize, alreadyCorrect, log, req.OperationID)

	// Post-organize auto write-back now rides the batcher.
	if stats.Organized > 0 || stats.ReOrganized > 0 {
		// Note: auto-rescan disabled — organize already updates all paths and book_files.
	}

	return nil
}

func (orgSvc *Service) autoBackup(log logger.Logger) {
	dbPath := config.AppConfig.DatabasePath
	dbType := config.AppConfig.DatabaseType
	if dbPath == "" {
		log.Warn("Skipping auto-backup: no database path configured")
		return
	}

	backupConfig := backup.DefaultBackupConfig()
	if !filepath.IsAbs(backupConfig.BackupDir) {
		backupConfig.BackupDir = filepath.Join(filepath.Dir(dbPath), backupConfig.BackupDir)
	}

	info, err := backup.CreateBackup(dbPath, dbType, backupConfig)
	if err != nil {
		log.Warn("Auto-backup failed: %s", err.Error())
		return
	}
	log.Info("Auto-backup created: %s (%d bytes)", info.Filename, info.Size)
}

func (orgSvc *Service) syncITunesBeforeOrganize(ctx context.Context, log logger.Logger) {
	libraryPath := orgSvc.DiscoverITunesLibraryPath()
	if libraryPath == "" {
		log.Info("Skipping iTunes sync: no library found")
		return
	}

	log.Info("Running iTunes sync before organize: %s", libraryPath)

	if err := orgSvc.ExecuteITunesSync(ctx, log, libraryPath); err != nil {
		log.Warn("iTunes pre-sync failed (continuing with organize): %s", err.Error())
		return
	}

	log.Info("iTunes sync completed successfully")
}

func (orgSvc *Service) FilterBooksNeedingOrganization(allBooks []database.Book, log logger.Logger) ([]database.Book, []database.Book) {
	booksToOrganize := make([]database.Book, 0)
	alreadyCorrect := make([]database.Book, 0)
	skippedMissingFiles := 0
	skippedDeleted := 0
	for i, book := range allBooks {
		// Update progress during filtering so the UI doesn't show 0/0
		if i%500 == 0 || i == len(allBooks)-1 {
			log.UpdateProgress(i, len(allBooks), fmt.Sprintf("Scanning: %d/%d books", i, len(allBooks)))
		}

		// Skip soft-deleted books
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			skippedDeleted++
			continue
		}

		// Skip non-primary versions — unless they're the only version in their VG
		// (i.e., no organized primary copy exists yet)
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			if book.VersionGroupID != nil && *book.VersionGroupID != "" {
				vgBooks, vgErr := orgSvc.db.GetBooksByVersionGroup(*book.VersionGroupID)
				if vgErr == nil {
					hasPrimary := false
					for _, vb := range vgBooks {
						if vb.IsPrimaryVersion != nil && *vb.IsPrimaryVersion {
							hasPrimary = true
							break
						}
					}
					if hasPrimary {
						continue // Has a primary version — skip this non-primary
					}
					// No primary exists yet — allow organize to create one
				}
			} else {
				continue
			}
		}
		// If already in root directory, check if path needs updating based on current metadata
		if config.AppConfig.RootDir != "" && strings.HasPrefix(book.FilePath, config.AppConfig.RootDir) {
			needsReOrganize, err := orgSvc.bookNeedsReOrganize(&book, log)
			if err != nil {
				log.Debug("Organize: Cannot compute target for %s: %s", book.Title, err.Error())
				continue
			}
			if !needsReOrganize {
				// Already in correct location — collect for stamping, don't log individually
				alreadyCorrect = append(alreadyCorrect, book)
				continue
			}
			log.Info("Organize: Book in RootDir needs re-organization: %s", book.Title)
			// Fall through to include in organize list
		}
		// Quick check: skip if file_path is empty
		if book.FilePath == "" {
			continue
		}
		// For books outside RootDir, rely on book_files to determine readiness.
		// Avoid os.Stat on 140K+ paths during filter — that was the main bottleneck.
		// organizeBook() will skip individual missing files when it runs.
		if config.AppConfig.RootDir == "" || !strings.HasPrefix(book.FilePath, config.AppConfig.RootDir) {
			bookFiles, bfErr := orgSvc.db.GetBookFiles(book.ID)
			if bfErr != nil || len(bookFiles) == 0 {
				// No book_files: can't organize without knowing which files to copy.
				log.Debug("Organize: Skipping %s — no book_files in DB", book.Title)
				skippedMissingFiles++
				continue
			}
			// Count how many active (non-missing) book files exist
			activeCount := 0
			for _, bf := range bookFiles {
				if bf.FilePath != "" && !bf.Missing {
					activeCount++
				}
			}
			if activeCount == 0 {
				log.Debug("Organize: Skipping %s — all book_files marked missing", book.Title)
				skippedMissingFiles++
				continue
			}
		}
		booksToOrganize = append(booksToOrganize, book)
	}
	if skippedDeleted > 0 {
		log.Info("Organize: Skipped %d soft-deleted book(s)", skippedDeleted)
	}
	if skippedMissingFiles > 0 {
		log.Info("Organize: Skipped %d book(s) with missing book files", skippedMissingFiles)
	}
	return booksToOrganize, alreadyCorrect
}

// bookNeedsReOrganize checks whether a book already in RootDir needs to be
// moved because its current path doesn't match the target path derived from
// current metadata.
func (orgSvc *Service) bookNeedsReOrganize(book *database.Book, log logger.Logger) (bool, error) {
	org := orgSvc.newOrganizer()

	// Determine dir vs file by extension — avoids os.Stat (the main scan bottleneck)
	ext := strings.ToLower(filepath.Ext(book.FilePath))
	audioExts := map[string]bool{".m4b": true, ".m4a": true, ".mp3": true, ".flac": true, ".ogg": true, ".opus": true, ".wma": true, ".aac": true}
	isFile := audioExts[ext]

	if !isFile {
		targetDir, err := org.GenerateTargetDirPath(book)
		if err != nil {
			return false, err
		}
		return book.FilePath != targetDir, nil
	}

	targetPath, err := org.GenerateTargetPath(book)
	if err != nil {
		return false, err
	}
	return book.FilePath != targetPath, nil
}

// ReOrganizeInPlace renames/moves a book that is already in RootDir to its
// correct location based on current metadata. Returns the new path.
func (orgSvc *Service) ReOrganizeInPlace(book *database.Book, log logger.Logger) (string, error) {
	org := orgSvc.newOrganizer()
	oldPath := book.FilePath

	info, err := os.Stat(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("source path no longer exists: %s — re-scan the library to update tracking", oldPath)
		}
		if os.IsPermission(err) {
			return "", fmt.Errorf("permission denied reading source: %s — check filesystem permissions and ACLs", oldPath)
		}
		return "", fmt.Errorf("cannot access source %s: %w", oldPath, err)
	}

	var targetPath string
	if info.IsDir() {
		targetPath, err = org.GenerateTargetDirPath(book)
	} else {
		targetPath, err = org.GenerateTargetPath(book)
	}
	if err != nil {
		return "", err
	}

	if oldPath == targetPath {
		// Already in correct location — still stamp as organized. Written via
		// hydrateAndUpdateBook (hydrate-before-write), not the in-memory
		// `book` pointer directly — see that helper's doc comment.
		organizedState := "organized"
		book.LibraryState = &organizedState
		now := time.Now()
		book.LastOrganizedAt = &now
		if err := orgSvc.hydrateAndUpdateBook(book.ID, func(b *database.Book) {
			b.LibraryState = &organizedState
			b.LastOrganizedAt = &now
		}); err != nil {
			log.Debug("Organize: failed to stamp already-in-place book %s: %s", book.ID, err.Error())
		}
		return targetPath, nil
	}

	// Create parent directory for target
	parentDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(parentDir, 0775); err != nil {
		return "", fmt.Errorf("cannot create target directory %s: %w (check parent permissions and disk space)", parentDir, err)
	}

	// Rename (move) the file or directory
	if err := os.Rename(oldPath, targetPath); err != nil {
		return "", fmt.Errorf("cannot move %s -> %s: %w (verify both paths exist, target not in use, same filesystem, write permission)", oldPath, targetPath, err)
	}

	// Update the book record — set path and mark as organized. Written via
	// hydrateAndUpdateBook (hydrate-before-write), not the in-memory `book`
	// pointer directly — see that helper's doc comment.
	book.FilePath = targetPath
	organizedState := "organized"
	book.LibraryState = &organizedState
	now := time.Now()
	book.LastOrganizedAt = &now
	if err := orgSvc.hydrateAndUpdateBook(book.ID, func(b *database.Book) {
		b.FilePath = targetPath
		b.LibraryState = &organizedState
		b.LastOrganizedAt = &now
	}); err != nil {
		log.Warn("Failed to update book path for %s: %s", book.ID, err.Error())
	}

	// Update book_files paths if this is a directory book
	if info.IsDir() {
		if bookFiles, bfErr := orgSvc.db.GetBookFiles(book.ID); bfErr == nil {
			// rescanNeeded is set if any book_files row fails to update below.
			// The physical move already succeeded at this point, so a failed
			// row update leaves the DB pointing at the old (now-nonexistent)
			// path; MarkNeedsRescan (called once, after the loop) self-heals
			// it on the next library scan instead of leaving the file
			// silently missing/unplayable. Mirrors the MarkNeedsRescan idiom
			// used elsewhere in this file (see CreateOrganizedVersion).
			var rescanNeeded bool
			for _, bf := range bookFiles {
				if strings.HasPrefix(bf.FilePath, oldPath) {
					bf.FilePath = filepath.Join(targetPath, strings.TrimPrefix(bf.FilePath, oldPath+"/"))
					if bf.FilePath != "" {
						bf.ITunesPath = orgSvc.ComputeITunesPath(bf.FilePath)
					}
					if err := orgSvc.db.UpdateBookFile(bf.ID, &bf); err != nil {
						log.Warn("ReOrganizeInPlace: failed to update book_file %s path for book %s: %s", bf.ID, book.ID, err.Error())
						rescanNeeded = true
					}
				}
			}
			if rescanNeeded {
				_ = orgSvc.db.MarkNeedsRescan(book.ID)
			}
		} else {
			// Same failure class as above, one level up: the directory move
			// already succeeded, but we couldn't even read the book_files
			// rows to rewrite them, so every one of them now points at the
			// old (moved-away) path. Mark for rescan so it self-heals.
			log.Warn("ReOrganizeInPlace: failed to load book_files for %s after move, marking for rescan: %s", book.ID, bfErr.Error())
			_ = orgSvc.db.MarkNeedsRescan(book.ID)
		}
	}

	// Try to remove the now-empty parent directory tree
	orgSvc.cleanupEmptyParents(filepath.Dir(oldPath), config.AppConfig.RootDir, log)

	log.Info("Re-organized: %s → %s", oldPath, targetPath)
	return targetPath, nil
}

// cleanupEmptyParents removes empty directories from dir up to (but not
// including) stopAt.
func (orgSvc *Service) cleanupEmptyParents(dir, stopAt string, log logger.Logger) {
	for dir != stopAt && strings.HasPrefix(dir, stopAt) && dir != "/" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			log.Debug("Could not remove empty dir %s: %s", dir, err.Error())
			break
		}
		log.Debug("Removed empty directory: %s", dir)
		dir = filepath.Dir(dir)
	}
}

// hydrateAndUpdateBook fetches the full book row via GetBookByID, applies
// mutate to that hydrated struct, and writes it back via UpdateBook — never
// writing a BookCore-derived (.ToBook()) copy directly, so a full-fidelity
// store backend never has its heavy fields (Description, BookSigV1, etc.)
// wiped by an organizer writeback. This does not rely on
// PebbleStore.UpdateBook's own STOR-1 self-heal (which already restores 7/9
// heavy fields from the old row); doing it explicitly here matches the
// STOREFID sweep's pattern so every organizer writeback call site is correct
// independent of that store-internal guard.
//
// If hydration fails (book deleted mid-organize, store error) the write is
// skipped entirely (fail-closed) rather than falling back to writing the
// possibly-Core-derived in-memory copy — the one shape of write this helper
// exists to prevent.
func (orgSvc *Service) hydrateAndUpdateBook(bookID string, mutate func(*database.Book)) error {
	hydrated, err := orgSvc.db.GetBookByID(bookID)
	if err != nil {
		return err
	}
	if hydrated == nil {
		return fmt.Errorf("book %s not found for hydrate-before-write", bookID)
	}
	mutate(hydrated)
	_, err = orgSvc.db.UpdateBook(bookID, hydrated)
	return err
}

// stampOrganizeMetadata hydrates the full book row via GetBookByID and writes
// the LastOrganizeOperationID/LastOrganizedAt stamp back onto that hydrated
// struct. See hydrateAndUpdateBook for the rationale.
func (orgSvc *Service) stampOrganizeMetadata(bookID, operationID string, when time.Time) error {
	return orgSvc.hydrateAndUpdateBook(bookID, func(b *database.Book) {
		b.LastOrganizeOperationID = &operationID
		b.LastOrganizedAt = &when
	})
}

func (orgSvc *Service) organizeBooks(ctx context.Context, booksToOrganize []database.Book, alreadyCorrect []database.Book, log logger.Logger, operationID string) *Stats {
	stats := &Stats{Total: len(booksToOrganize) + len(alreadyCorrect)}

	// Thread-safe counters and collectors
	var statsMu sync.Mutex
	var progressCounter int64

	const numWorkers = 8
	jobs := make(chan int, numWorkers*2)

	// Start worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerOrg := orgSvc.newOrganizer()

			for i := range jobs {
				book := booksToOrganize[i]

				// Policy check: skip books tagged policy:no-organize.
				if tags, err := orgSvc.db.GetBookTags(book.ID); err == nil {
					if policy.EvaluatePolicy(tags).NoOrganize {
						log.Debug("organize: skipping book %s — policy:no-organize tag", book.ID)
						statsMu.Lock()
						stats.Skipped++
						statsMu.Unlock()
						atomic.AddInt64(&progressCounter, 1)
						continue
					}
				}

				oldPath := book.FilePath
				isDir := false
				if info, err := os.Stat(oldPath); err == nil && info.IsDir() {
					isDir = true
				}
				alreadyInRoot := config.AppConfig.RootDir != "" && strings.HasPrefix(oldPath, config.AppConfig.RootDir)

				// --- Step 1: File operations ---
				var newPath string
				var err error

				if alreadyInRoot {
					newPath, err = orgSvc.ReOrganizeInPlace(&book, log)
				} else if isDir {
					newPath, err = orgSvc.OrganizeDirectoryBook(workerOrg, &book, log)
				} else {
					newPath, _, err = workerOrg.OrganizeBook(&book)
				}

				// --- Step 2: DB operations ---
				if err != nil {
					log.Warn("Failed to organize %s: %s", book.Title, err.Error())
					statsMu.Lock()
					stats.Failed++
					statsMu.Unlock()

					if operationID != "" {
						_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: operationID,
							BookID:      book.ID,
							ChangeType:  "organize_failed",
							FieldName:   "file_path",
							OldValue:    oldPath,
							NewValue:    err.Error(),
						})
					}
				} else if oldPath == newPath {
					now := time.Now()
					if updateErr := orgSvc.stampOrganizeMetadata(book.ID, operationID, now); updateErr != nil {
						log.Debug("Organize: failed to stamp book %s: %s", book.ID, updateErr.Error())
					}
					statsMu.Lock()
					stats.AlreadyCorrect++
					statsMu.Unlock()

					if operationID != "" {
						_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: operationID,
							BookID:      book.ID,
							ChangeType:  "organize_skipped",
							FieldName:   "file_path",
							OldValue:    oldPath,
							NewValue:    oldPath,
						})
					}
				} else if alreadyInRoot {
					now := time.Now()
					if updateErr := orgSvc.stampOrganizeMetadata(book.ID, operationID, now); updateErr != nil {
						log.Debug("Organize: failed to stamp re-organized book %s: %s", book.ID, updateErr.Error())
					}
					log.Info("Re-organized %s: %s → %s", book.Title, oldPath, newPath)
					statsMu.Lock()
					stats.ReOrganized++
					statsMu.Unlock()

					if operationID != "" {
						_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: operationID,
							BookID:      book.ID,
							ChangeType:  "organize_rename",
							FieldName:   "file_path",
							OldValue:    oldPath,
							NewValue:    newPath,
						})
						oldState := ""
						if book.LibraryState != nil {
							oldState = *book.LibraryState
						}
						_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: operationID,
							BookID:      book.ID,
							ChangeType:  "metadata_update",
							FieldName:   "library_state",
							OldValue:    oldState,
							NewValue:    "organized",
						})
					}
				} else {
					// Version-aware organize: create a new book record for the organized copy
					createdBook, createErr := orgSvc.CreateOrganizedVersion(workerOrg, &book, newPath, isDir, operationID, log)
					if createErr != nil {
						statsMu.Lock()
						stats.Failed++
						statsMu.Unlock()
						goto progress
					}

					// Stamp the new organized book record with this operation
					now := time.Now()
					createdBook.LastOrganizeOperationID = &operationID
					createdBook.LastOrganizedAt = &now
					if _, updateErr := orgSvc.db.UpdateBook(createdBook.ID, createdBook); updateErr != nil {
						log.Debug("Organize: failed to stamp new book %s: %s", createdBook.ID, updateErr.Error())
					}

					log.Info("Organized %s: created version %s → %s (original kept at %s)",
						book.Title, createdBook.ID, newPath, oldPath)

					statsMu.Lock()
					stats.Organized++
					statsMu.Unlock()
				}

				// --- Step 3: Enqueue iTunes writeback ---
				if err == nil && oldPath != newPath && orgSvc.writeBackBatcher != nil {
					orgSvc.writeBackBatcher.Enqueue(book.ID)
				}

			progress:
				// --- Step 4: Progress reporting ---
				count := atomic.AddInt64(&progressCounter, 1)
				if count%50 == 0 || count == int64(len(booksToOrganize)) {
					log.UpdateProgress(int(count), len(booksToOrganize),
						fmt.Sprintf("Organizing: %d/%d books", count, len(booksToOrganize)))
				}
			}
		}()
	}

	// Feed jobs — cancellation checked here.
	for i := range booksToOrganize {
		if log.IsCanceled() {
			log.Info("Organize canceled")
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Stamp already-correct books with this operation ID (sequential — bulk stamp).
	// Same hydrate-before-write treatment as the two per-book writebacks above:
	// alreadyCorrect is sourced from the same Core-fetched allBooks slice, so
	// stamping goes through stampOrganizeMetadata rather than writing back the
	// in-memory (ToBook()-derived) copy directly.
	if operationID != "" && len(alreadyCorrect) > 0 {
		stampNow := time.Now()
		for i := range alreadyCorrect {
			b := &alreadyCorrect[i]
			if updateErr := orgSvc.stampOrganizeMetadata(b.ID, operationID, stampNow); updateErr != nil {
				log.Debug("Organize: failed to stamp already-correct book %s: %s", b.ID, updateErr.Error())
			}
		}
		stats.AlreadyCorrect += len(alreadyCorrect)
	}

	summary := fmt.Sprintf("Organize complete: %d organized, %d re-organized, %d already correct (stamped), %d skipped",
		stats.Organized, stats.ReOrganized, stats.AlreadyCorrect, stats.Skipped)
	log.Info("%s", summary)

	// Record summary as operation change
	if operationID != "" {
		_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      "",
			ChangeType:  "organize_summary",
			FieldName:   "stats",
			OldValue:    "",
			NewValue: fmt.Sprintf("organized:%d re_organized:%d already_correct:%d skipped:%d failed:%d total:%d",
				stats.Organized, stats.ReOrganized, stats.AlreadyCorrect, stats.Skipped, stats.Failed, stats.Total),
		})
	}

	return stats
}

// OrganizeDirectoryBook handles organizing a multi-file book where file_path is a directory.
// It always uses book_files from the database — no directory scanning fallback.
// Returns the target directory path.
func (orgSvc *Service) OrganizeDirectoryBook(org *Organizer, book *database.Book, log logger.Logger) (string, error) {
	bookFiles, err := orgSvc.db.GetBookFiles(book.ID)
	if err != nil {
		return "", fmt.Errorf("cannot load segments for %s (%s): %w", book.Title, book.ID, err)
	}
	if len(bookFiles) == 0 {
		return "", fmt.Errorf("no segments tracked for %q (id=%s) — run a library scan to detect files in %s", book.Title, book.ID, book.FilePath)
	}

	var segmentPaths []string
	missingCount := 0
	for _, bf := range bookFiles {
		if bf.FilePath == "" {
			continue
		}
		if bf.Missing {
			missingCount++
			continue
		}
		segmentPaths = append(segmentPaths, bf.FilePath)
	}

	if len(segmentPaths) == 0 {
		return "", fmt.Errorf("all %d segments for %q (id=%s) marked missing on disk — re-scan to verify, or restore from backup", missingCount, book.Title, book.ID)
	}

	log.Info("Organizing %d segment file(s) for %s (from book_files)", len(segmentPaths), book.Title)

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segmentPaths)
	if err != nil {
		return "", err
	}

	// Verify at least some files were actually copied to the target
	if len(pathMap) == 0 {
		return "", fmt.Errorf("no files were copied for %s — all source files missing", book.Title)
	}

	// Check how many files actually exist in the target directory
	copiedCount := 0
	for _, dstPath := range pathMap {
		if _, statErr := os.Stat(dstPath); statErr == nil {
			copiedCount++
		}
	}
	if copiedCount == 0 {
		return "", fmt.Errorf("organize produced 0 files for %s — all copies failed", book.Title)
	}

	return targetDir, nil
}

// CreateOrganizedVersion creates a new book record for the organized copy and links it to the original.
func (orgSvc *Service) CreateOrganizedVersion(org *Organizer, book *database.Book, newPath string, isDir bool, operationID string, log logger.Logger) (*database.Book, error) {
	newBookID := ulid.Make().String()
	isPrimary := true
	isNotPrimary := false
	organizedState := "organized"

	// Determine or create version group
	versionGroupID := ""
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		versionGroupID = *book.VersionGroupID
	} else {
		versionGroupID = ulid.Make().String()
	}

	// Create the new organized book record (copy of metadata)
	newBook := database.Book{
		ID:                   newBookID,
		Title:                book.Title,
		AuthorID:             book.AuthorID,
		Narrator:             book.Narrator,
		SeriesID:             book.SeriesID,
		SeriesSequence:       book.SeriesSequence,
		FilePath:             newPath,
		Format:               book.Format,
		FileSize:             book.FileSize,
		FileHash:             book.FileHash,
		OriginalFileHash:     book.OriginalFileHash,
		Duration:             book.Duration,
		Bitrate:              book.Bitrate,
		SampleRate:           book.SampleRate,
		Channels:             book.Channels,
		BitDepth:             book.BitDepth,
		Codec:                book.Codec,
		Edition:              book.Edition,
		Language:             book.Language,
		Publisher:            book.Publisher,
		PrintYear:            book.PrintYear,
		AudiobookReleaseYear: book.AudiobookReleaseYear,
		ISBN10:               book.ISBN10,
		ISBN13:               book.ISBN13,
		ASIN:                 book.ASIN,
		CoverURL:             book.CoverURL,
		OpenLibraryID:        book.OpenLibraryID,
		HardcoverID:          book.HardcoverID,
		GoogleBooksID:        book.GoogleBooksID,
		OriginalFilename:     book.OriginalFilename,
		LibraryState:         &organizedState,
		VersionGroupID:       &versionGroupID,
		IsPrimaryVersion:     &isPrimary,
		Quality:              book.Quality,
	}

	if !isDir {
		orgSvc.ApplyOrganizedFileMetadata(&newBook, newPath)
	}

	createdBook, err := orgSvc.db.CreateBook(&newBook)
	if err != nil {
		log.Error("Failed to create organized book record for %s: %v", book.Title, err)
		if newPath != "" && config.AppConfig.RootDir != "" && strings.HasPrefix(newPath, config.AppConfig.RootDir) {
			if isDir {
				if rmErr := os.RemoveAll(newPath); rmErr != nil {
					log.Warn("organize: cleanup of partial directory organize failed (%s): %v", newPath, rmErr)
				} else {
					log.Warn("organize: cleaned up partial directory organize at %s after CreateBook failure", newPath)
				}
			} else {
				if rmErr := os.Remove(newPath); rmErr != nil {
					log.Warn("organize: cleanup of partial single-file organize failed (%s): %v", newPath, rmErr)
				}
			}
		}
		return nil, err
	}
	// Mark both the organized copy and the original for rescan
	_ = orgSvc.db.MarkNeedsRescan(createdBook.ID)
	_ = orgSvc.db.MarkNeedsRescan(book.ID)

	// Copy book_authors relationships to the new book
	if authors, err := orgSvc.db.GetBookAuthors(book.ID); err == nil && len(authors) > 0 {
		var newAuthors []database.BookAuthor
		for _, ba := range authors {
			newAuthors = append(newAuthors, database.BookAuthor{
				BookID:   newBookID,
				AuthorID: ba.AuthorID,
				Role:     ba.Role,
			})
		}
		_ = orgSvc.db.SetBookAuthors(newBookID, newAuthors)
	}

	// Copy book files to the new book with updated paths
	if bookFiles, err := orgSvc.db.GetBookFiles(book.ID); err == nil && len(bookFiles) > 0 {
		for _, bf := range bookFiles {
			newBF := bf
			newBF.ID = ulid.Make().String()
			newBF.BookID = newBookID
			if isDir && bf.FilePath != "" {
				fileName := filepath.Base(bf.FilePath)
				newBF.FilePath = filepath.Join(newPath, fileName)
			} else if !isDir {
				newBF.FilePath = newPath
			}
			if newBF.FilePath != "" {
				newBF.ITunesPath = orgSvc.ComputeITunesPath(newBF.FilePath)
			}
			_ = orgSvc.db.CreateBookFile(&newBF)
		}
	}

	// Update original book: set version group, mark as non-primary, update state.
	// `book` here is a page-derived (GetAllBooksCore→ToBook, heavy-field-nil)
	// projection, so writing it directly would wipe the original's denormalized
	// Author/Series under a full-fidelity backend (STOREFID W5d-1, #1887).
	// Fixed via hydrate-before-write, matching hydrateAndUpdateBook's pattern —
	// but NOT that helper itself: its fail-closed skip-on-hydrate-error would
	// leave the version group with two primaries, which is worse than the rare
	// Author/Series wipe this fallback accepts. So: hydrate and write the full
	// row on success; if hydration fails, fall back to the direct state-only
	// write (today's pre-fix behavior) so the state transition always lands —
	// fail-OPEN for the state transition, preserve-heavy-when-possible.
	organizedSourceState := "organized_source"
	book.VersionGroupID = &versionGroupID
	book.IsPrimaryVersion = &isNotPrimary
	book.LibraryState = &organizedSourceState
	if hydrated, hydrateErr := orgSvc.db.GetBookByID(book.ID); hydrateErr == nil && hydrated != nil {
		hydrated.VersionGroupID = &versionGroupID
		hydrated.IsPrimaryVersion = &isNotPrimary
		hydrated.LibraryState = &organizedSourceState
		if _, err := orgSvc.db.UpdateBook(book.ID, hydrated); err != nil {
			log.Warn("Failed to update original book %s version group: %v", book.ID, err)
		}
	} else {
		if hydrateErr != nil {
			log.Warn("organize: hydrate-before-write failed for original book %s, falling back to state-only write (Author/Series may be wiped): %v", book.ID, hydrateErr)
		}
		if _, err := orgSvc.db.UpdateBook(book.ID, book); err != nil {
			log.Warn("Failed to update original book %s version group: %v", book.ID, err)
		}
	}

	// Record operation changes for undo
	if operationID != "" {
		_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      createdBook.ID,
			ChangeType:  "book_create",
			FieldName:   "organized_version",
			OldValue:    "",
			NewValue:    fmt.Sprintf("version_of:%s path:%s", book.ID, newPath),
		})
		_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      book.ID,
			ChangeType:  "metadata_update",
			FieldName:   "version_group_id",
			OldValue:    "",
			NewValue:    versionGroupID,
		})
	}

	return createdBook, nil
}

// TriggerAutomaticRescan triggers a background rescan of the library via the v2 registry.
func (orgSvc *Service) TriggerAutomaticRescan(ctx context.Context, log logger.Logger) {
	if config.AppConfig.RootDir == "" {
		return
	}
	if orgSvc.ScanEnqueuer == nil {
		log.Warn("ScanEnqueuer not wired; skipping automatic rescan")
		return
	}
	log.Info("Triggering automatic rescan of library path...")
	if err := orgSvc.ScanEnqueuer(ctx); err != nil {
		log.Warn("Failed to enqueue rescan: %s", err.Error())
	} else {
		log.Info("Rescan operation queued successfully")
	}
}
