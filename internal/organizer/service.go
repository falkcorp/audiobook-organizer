// file: internal/organizer/service.go
// version: 1.33.0
// guid: c3d4e5f6-a7b8-c9d0-e1f2-a3b4c5d6e7f8
// last-edited: 2026-09-05

package organizer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
// OrganizerBookReader reads the book rows the rename/organize passes work from.
type OrganizerBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// OrganizerBookWriter persists book rows after a move, and flags a book for
// rescan when its files changed underneath it.
type OrganizerBookWriter interface {
	CreateBook(book *database.Book) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	MarkNeedsRescan(bookID string) error
	// DeleteBook is here only to roll back a half-built organized copy. It is
	// NOT a general delete path for this package -- CreateOrganizedVersion is
	// the sole caller, and only on a failure it has already logged.
	DeleteBook(id string) error
}

// OrganizerBookFileStore is the per-file half: the organizer repoints file rows
// at their new paths as it moves them.
type OrganizerBookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	CreateBookFile(file *database.BookFile) error
	UpdateBookFile(id string, file *database.BookFile) error
	// BatchCreateBookFiles writes a book's copied file rows atomically, so a
	// partial failure cannot leave the organized copy owning some of its audio.
	BatchCreateBookFiles(files []*database.BookFile) error
}

// OrganizerContributorStore reads the author/narrator/tag associations that the
// path templates interpolate, and re-links authors after a merge.
type OrganizerContributorStore interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetNarratorByID(id int) (*database.Narrator, error)
	GetBookTags(bookID string) ([]string, error)
}

// OrganizerAuditWriter records what an organize run did: the per-change rows,
// the path history a move can be undone from, and the operation's own params
// and state.
//
// SaveOperationParams and DeleteOperationState are declared here rather than
// embedding operations.OperationParamsWriter / operations.OperationStateDeleter
// so this package does not import internal/operations for two signatures. The
// methods satisfy those interfaces structurally, which is what the calls to
// operations.SaveParams and operations.ClearState need.
type OrganizerAuditWriter interface {
	CreateOperationChange(change *database.OperationChange) error
	RecordPathChange(change *database.BookPathChange) error
	SaveOperationParams(opID string, params []byte) error
	DeleteOperationState(opID string) error
}

// Store is what the organizer service needs from the database: the set of
// methods this package actually calls, established by emptying the interface and
// letting the compiler enumerate every unresolved call, so it is exhaustive
// rather than estimated.
//
// Until 2026-08-18 this embedded nine whole database.* interfaces (BookStore,
// BookFileStore, OperationStore, AuthorStore, SeriesStore, NarratorStore,
// MaintenanceStore, TagStore, PathHistoryStore) -- a transitive surface of 179
// distinct methods, of which the package called 16. Embedding a whole store
// interface is the cheapest thing to write and the most expensive thing to own:
// every method on it becomes reachable from here, and the next person to add a
// call finds it already compiles.
//
// database.OperationStore (30 methods) was in that list for one reason: the
// organizer passes its store to operations.SaveParams and operations.ClearState,
// which declared it. Narrowing those two parameters (see internal/operations/
// state.go) is what made this narrowing possible -- a wide parameter propagates
// width to every caller and to every interface those callers declare.
//
// The compile-time assertion below is what proves the concrete store still
// satisfies this. Narrowing an interface can only make it easier to satisfy, so
// no implementation moves.
type Store interface {
	// The four lookups organizer.go's own OrganizerStore requires: rs.db is
	// handed to org.SetStore, so Store must stay assignable to it.
	OrganizerStore

	OrganizerBookReader
	OrganizerBookWriter
	OrganizerBookFileStore
	OrganizerContributorStore
	OrganizerAuditWriter
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
	// ctx is threaded so a cancelled organize op aborts an in-flight external
	// metadata fetch promptly.
	FetchMetadataForBook func(ctx context.Context, bookID string) (any, error)
}

// SetWriteBackBatcher sets the iTunes write-back batcher.
func (orgSvc *Service) SetWriteBackBatcher(b WriteBackEnqueuer) {
	orgSvc.writeBackBatcher = b
}

// SetOrganizeHooks sets optional hooks that are propagated to every
// Organizer instance created by this service.
// ErrAuthorUnresolved is returned when a rename/organize is refused because
// the book's author would resolve to the "Unknown Author" placeholder. The
// caller should surface "resolve metadata first" rather than renaming.
var ErrAuthorUnresolved = errors.New("author unresolved — metadata fetch must resolve the author before organize renames this book")

func (orgSvc *Service) SetOrganizeHooks(hooks OrganizeHooks) {
	orgSvc.organizeHooks = hooks
}

// newOrganizer creates an Organizer with the service's hooks pre-wired.
func (orgSvc *Service) newOrganizer() *Organizer {
	org := NewOrganizer(&config.AppConfig)
	// Wire the store so AuthorID/SeriesID resolve in path templates. Without
	// this every service-path organize treated slim books as authorless —
	// see OrganizerStore's doc comment.
	org.SetStore(orgSvc.db)
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
		FetchMetadataForBook:       func(_ context.Context, _ string) (any, error) { return nil, nil },
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
	// Canceled records that the run stopped early because ctx was cancelled or
	// the operation was cancelled from the UI. Without it the summary said
	// "Organize complete" for a run that had been deliberately stopped, which
	// is indistinguishable from one that genuinely finished.
	Canceled bool
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
			// Stamp per page. This loop pulls the whole library in 1,000-book
			// pages and had no progress calls at all, so on a large library it
			// was a second silent window after the backup — same failure mode,
			// same watchdog. (0, 1) because the row count is not known until
			// the last short page arrives.
			log.UpdateProgress(0, 1, fmt.Sprintf("Loading library: %d books", len(allBooks)))
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
		// A sequential network call per book over the whole library, and it had
		// no progress calls either — the longest silent window of the three.
		// Stamped per book because the total IS known here, unlike the paging
		// loops above.
		for i := range allBooks {
			book := &allBooks[i]
			log.UpdateProgress(i+1, len(allBooks), fmt.Sprintf("Fetching metadata: %d/%d", i+1, len(allBooks)))
			if book.CoverURL != nil {
				continue // already enriched
			}
			if _, err := orgSvc.FetchMetadataForBook(ctx, book.ID); err == nil {
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
			log.UpdateProgress(0, 1, fmt.Sprintf("Reloading library after metadata: %d books", len(allBooks)))
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

	return organizeOutcomeError(stats)
}

// organizeOutcomeError converts a finished run's Stats into the error
// PerformOrganize returns, or nil when the run genuinely succeeded.
//
// This exists as its own function because PerformOrganize used to end in an
// unconditional `return nil`: every book could fail and the caller still saw
// success, so the operation was marked succeeded and nothing upstream had any
// way to learn otherwise. Making the rule a pure function means the policy can
// be stated and tested directly instead of being implied by the last line of a
// long method.
//
// The rule, deliberately:
//
//   - Cancelled runs report cancellation FIRST, and are never reported as
//     failures. A cancelled run has an unknown outcome rather than a bad one —
//     the books it never reached are neither organized nor failed — so calling
//     it a failure would be its own misreport.
//   - Only TOTAL failure is an error. A partial failure stays a success, so a
//     run where 1 book of 3000 fails is not reported as a failed operation;
//     the count is carried by the summary line and the organize_summary row.
//     "Total" means at least one book failed and NOT ONE was organized,
//     re-organized, or confirmed already-correct.
//   - A run that organized nothing because there was nothing to do (all zero)
//     is a success, not a failure.
//
// formatOrganizeSummary builds the one-line summary a person actually reads.
//
// stats.Failed is in this line deliberately. It used to appear ONLY in the
// organize_summary operation-change row and never in the logged message, so a
// run in which every single book failed printed
//
//	Organize complete: 0 organized, 0 re-organized, 0 already correct (stamped), 0 skipped
//
// which reads as a harmless no-op rather than a total failure. The verb is
// conditional for the same reason: a cancelled run also said "complete", which
// is indistinguishable from one that actually finished.
func formatOrganizeSummary(stats *Stats) string {
	verb := "complete"
	if stats.Canceled {
		verb = "CANCELED"
	}
	return fmt.Sprintf("Organize %s: %d organized, %d re-organized, %d already correct (stamped), %d skipped, %d FAILED, %d total",
		verb, stats.Organized, stats.ReOrganized, stats.AlreadyCorrect, stats.Skipped, stats.Failed, stats.Total)
}

// ErrOrganizeCanceled marks the error returned by a cancelled run, so callers
// can tell "the user stopped this" apart from "this went wrong".
//
// Without it, making cancellation return an error would simply have swapped one
// misreport for another: the caller in library_core_ops.go marks any non-nil
// error as "failed", so a deliberate cancel would have been recorded as a
// failure. (The v2 registry worker already gets this right — it checks
// ctxCanceled BEFORE runErr — but the operation's own logged status did not.)
var ErrOrganizeCanceled = errors.New("organize canceled")

func organizeOutcomeError(stats *Stats) error {
	if stats == nil {
		return nil
	}
	if stats.Canceled {
		return fmt.Errorf("%w after %d organized, %d re-organized, %d failed, of %d total",
			ErrOrganizeCanceled, stats.Organized, stats.ReOrganized, stats.Failed, stats.Total)
	}
	if stats.Failed > 0 && stats.Organized == 0 && stats.ReOrganized == 0 && stats.AlreadyCorrect == 0 {
		return fmt.Errorf("organize failed for all %d books attempted (of %d total); see the organize_summary operation change for per-book errors",
			stats.Failed, stats.Total)
	}
	return nil
}

// autoBackupMinInterval is how recent a successful backup must be for
// PerformOrganize to skip taking another one.
//
// WHY THIS EXISTS: the pre-organize backup archives the whole database, and on
// production that is 14 GB at gzip.BestCompression. Measured on prod
// 2026-08-11, two consecutive organize runs:
//
//	01:54:14 organize starting -> 02:14:42 backup failed   (20m28s)
//	06:31:29 organize starting -> 06:56:00 backup failed   (24m31s)
//
// Every organize paid 20-25 minutes before touching a single book. From the
// user's side the operation simply never started, and at 06:36:36 the ops
// registry logged `strike recorded ... kind=stuck  no progress for 5m8s`
// against an operation that was in fact working the whole time.
//
// Backing up before a destructive file-moving operation is right, so this does
// not remove the backup — it stops re-taking one that is still fresh.
const autoBackupMinInterval = 6 * time.Hour

// newestBackupAge returns the age and filename of the most recent archive in
// backupDir.
//
// This hand-rolled the directory walk until 2026-08-29, to avoid
// backup.ListBackups checksumming every archive it found -- a full read of every
// file, which at ~15 GB per archive cost enormously more than this freshness
// check is worth. ListBackups no longer hashes, so the duplicate has lost its
// reason to exist and is now a liability instead: two copies of the same
// "which files here are archives?" predicate that can drift apart silently.
//
// The predicates were identical (skip directories, require .tar.gz, skip
// entries that will not stat), and this needs only CreatedAt and Filename,
// which the listing still populates.
func newestBackupAge(backupDir string) (time.Duration, string, bool) {
	backups, err := backup.ListBackups(backupDir)
	if err != nil || len(backups) == 0 {
		return 0, "", false
	}
	newest := backups[0]
	for _, b := range backups[1:] {
		if b.CreatedAt.After(newest.CreatedAt) {
			newest = b
		}
	}
	return time.Since(newest.CreatedAt), newest.Filename, true
}

// backupMethod records which path autoBackup took. It exists to make the
// choice TESTABLE: the compaction race that motivates the checkpoint path does
// not reproduce on a small test database, so asserting "a backup was created"
// would pass whether or not the fix is present. Asserting on the method that
// was chosen is what actually goes red on a regression.
type backupMethod string

const (
	backupSkippedRecent backupMethod = "skipped-recent"
	backupNoPath        backupMethod = "skipped-no-path"
	backupCheckpoint    backupMethod = "checkpoint"
	backupLiveWalk      backupMethod = "live-walk"
	backupFailed        backupMethod = "failed"
)

// backupProgressInterval is how often the auto-backup forwards a progress
// stamp to the operation. Well under the registry watchdog's 5-minute
// ProgressTimeout, and far above the rate the archive walk fires at.
const backupProgressInterval = 15 * time.Second

// backupProgressReporter adapts backup.BackupProgress onto the operation
// logger, rate-limited to one stamp per interval.
//
// WHY THE THROTTLE: log.UpdateProgress writes through to Pebble, and the
// archive walk fires once per file — thousands of times on a 14 GB database.
// Unthrottled, the reporting would be a measurable share of the backup's cost.
//
// WHY IT IS NOT A TICKER: a goroutine stamping every 15s would satisfy the
// watchdog whether or not the backup was alive, converting a hang DETECTOR
// into a hang CONCEALER. This function only runs when the archiver has
// actually finished another file or another checksum chunk, so it cannot
// report motion that did not happen. The cost of that honesty is that a single
// file taking longer than ProgressTimeout to write would still be cancelled —
// acceptable, because Pebble SSTs are bounded well below that.
func backupProgressReporter(log logger.Logger, interval time.Duration) backup.BackupProgress {
	var last time.Time
	return func(phase string, filesDone int, bytesDone int64) {
		now := time.Now()
		if !last.IsZero() && now.Sub(last) < interval {
			return
		}
		last = now

		var msg string
		switch phase {
		case backup.PhaseCheckpoint:
			msg = "Backing up: snapshotting database"
		case backup.PhaseArchive:
			msg = fmt.Sprintf("Backing up: archived %d files (%s)", filesDone, humanBytes(bytesDone))
		case backup.PhaseChecksum:
			msg = fmt.Sprintf("Backing up: verifying archive (%s)", humanBytes(bytesDone))
		default:
			msg = "Backing up database"
		}
		// (0, 1) rather than a computed denominator: the total size of the
		// archive is not known until it is written, and inventing one produces
		// a percentage that jumps backwards.
		log.UpdateProgress(0, 1, msg)
	}
}

// humanBytes renders a byte count for an operator-facing progress line.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (orgSvc *Service) autoBackup(log logger.Logger) backupMethod {
	dbPath := config.AppConfig.DatabasePath
	dbType := config.AppConfig.DatabaseType
	if dbPath == "" {
		log.Warn("Skipping auto-backup: no database path configured")
		return backupNoPath
	}

	backupConfig := backup.DefaultBackupConfig()
	backupConfig.BackupDir = backup.ResolveDir(config.AppConfig.BackupDir, dbPath)
	backupConfig.MaxTotalBytes = backup.ResolveMaxTotalBytes(config.AppConfig.BackupMaxTotalBytes)
	backupConfig.Compression = config.AppConfig.BackupCompression
	backupConfig.CompressionLevel = config.AppConfig.BackupCompressionLevel

	if age, name, ok := newestBackupAge(backupConfig.BackupDir); ok && age < autoBackupMinInterval {
		log.Info("Skipping auto-backup: %s is %s old (under the %s threshold)",
			name, age.Truncate(time.Second), autoBackupMinInterval)
		return backupSkippedRecent
	}

	// Announce the phase and keep the progress channel alive. Without this the
	// operation looks hung for the entire archive and the registry marks it
	// stuck — see autoBackupMinInterval above.
	start := time.Now()
	log.UpdateProgress(0, 1, "Backing up database before organize (this can take several minutes)")
	// Log the RESOLVED destination, not just the source. backup_dir is
	// configurable and falls back to a directory beside the database when unset,
	// so an empty or lost setting silently relocates archives back onto the
	// database's own filesystem -- which is precisely the arrangement that filled
	// the disk and crash-looped the service on 2026-08-29. A one-line statement of
	// where the archive is actually going makes that reversion visible in the log
	// instead of discoverable only after the disk fills again.
	log.Info("Auto-backup starting: %s -> %s", dbPath, backupConfig.BackupDir)
	backupConfig.Progress = backupProgressReporter(log, backupProgressInterval)

	var (
		info   *backup.BackupInfo
		err    error
		method backupMethod
	)
	// Prefer the Pebble checkpoint path. CreateBackup walks the LIVE database
	// directory, so Pebble compaction deletes .sst/.log files between the walk
	// enumerating them and the archiver reading them — which is exactly how
	// both prod runs died:
	//
	//	Auto-backup failed: failed to add files to archive:
	//	  lstat /var/lib/audiobook-organizer/audiobooks.pebble/536537.sst: no such file or directory
	//
	// Checkpoint flushes and hard-links the SSTs into a private directory
	// first, so the archive is consistent by construction.
	//
	// Resolved with database.AsCapability rather than a bare type assertion:
	// the production store is wrapped by the search-index decorator, and a
	// bare `orgSvc.db.(backup.Checkpointable)` sees only the decorator's own
	// method set and silently falls back to the racy path. That exact shape
	// was a live bug in the version-group backfill (PR #2295).
	if cp, ok := database.AsCapability[backup.Checkpointable](orgSvc.db); ok && dbType == "pebble" {
		method = backupCheckpoint
		info, err = backup.CreateBackupWithCheckpoint(cp, dbPath, dbType, backupConfig)
	} else {
		method = backupLiveWalk
		if dbType == "pebble" {
			log.Warn("Auto-backup: store %T does not expose Checkpoint; falling back to a live-directory archive, which races Pebble compaction", orgSvc.db)
		}
		info, err = backup.CreateBackup(dbPath, dbType, backupConfig)
	}
	if err != nil {
		log.Warn("Auto-backup failed after %s: %s", time.Since(start).Truncate(time.Second), err.Error())
		return backupFailed
	}
	log.Info("Auto-backup created: %s (%d bytes) in %s via %s",
		info.Filename, info.Size, time.Since(start).Truncate(time.Second), method)
	return method
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
	skippedUnresolvedAuthor := 0
	for i, book := range allBooks {
		// Update progress during filtering so the UI doesn't show 0/0
		if i%500 == 0 || i == len(allBooks)-1 {
			log.UpdateProgress(i, len(allBooks), fmt.Sprintf("Scanning: %d/%d books", i, len(allBooks)))
		}

		// Skip soft-deleted books
		if book.IsSoftDeleted() {
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
		// Author gate: a rename/copy for a book with no resolvable author
		// would bake "Unknown Author" into the target path — the 2026-08-11
		// mass-reorganize mechanism. Defer these until metadata resolution
		// gives them a real author. (Books already sitting under the
		// placeholder stay put rather than being re-cemented.)
		if !orgSvc.newOrganizer().HasResolvedAuthor(&book) {
			skippedUnresolvedAuthor++
			continue
		}
		booksToOrganize = append(booksToOrganize, book)
	}
	if skippedUnresolvedAuthor > 0 {
		log.Info("Organize: Deferred %d book(s) with unresolved author — run metadata fetch, then organize renames them properly", skippedUnresolvedAuthor)
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
	// Follows supported_extensions. Getting this wrong is not cosmetic: a book
	// whose extension was missing from the private list took the directory
	// branch and was compared against GenerateTargetDirPath, so it was judged
	// to need a re-organize on every pass, forever.
	isFile := config.SupportedExtensionSet().MatchPath(book.FilePath)

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
	if !org.HasResolvedAuthor(book) {
		return "", ErrAuthorUnresolved
	}
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

	// Move the file or directory. moveExclusive cannot overwrite an existing
	// destination: for a file it link(2)s then unlinks, and link fails EEXIST
	// even against a worker that landed there a microsecond ago; for a
	// directory it relies on rename(2) refusing a non-empty occupant. The
	// safeRename this replaced checked-then-renamed, and two workers moving
	// different books into one target both passed the check — rename(2) then
	// silently REPLACED, destroying whichever book got there first.
	if err := moveExclusive(oldPath, targetPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("cannot move %s -> %s: destination already exists — refusing to overwrite; resolve the collision first", oldPath, targetPath)
		}
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

	// Keep the book_files rows in step with the move — for a single-file book
	// as well as a directory book.
	//
	// 🔴 Until 2026-09-05 this ran ONLY under `if info.IsDir()`, so a
	// single-file book's row kept pointing at the pre-move path after the file
	// moved. The download/stream endpoint (abs.serveItemFile) resolves the
	// bytes THROUGH the book_file row, not through book.FilePath, so every
	// single-file book that an apply renamed answered 404 ("bytes_missing")
	// for download and playback while its book record looked correct. The
	// stale row also made the next scan mint a DUPLICATE book at the new path,
	// because GetBookByFilePath(newPath) found nothing. Both symptoms trace to
	// this one row not being rewritten.
	//
	// A row-update failure is not fatal here: the physical move already
	// succeeded, so MarkNeedsRescan (once, after the loop) lets the next scan
	// self-heal the pointer rather than leaving the file silently unplayable.
	// Mirrors the MarkNeedsRescan idiom used elsewhere in this file (see
	// CreateOrganizedVersion).
	if bookFiles, bfErr := orgSvc.db.GetBookFiles(book.ID); bfErr == nil {
		var rescanNeeded bool
		var matchedAny bool
		for _, bf := range bookFiles {
			var newFilePath string
			switch {
			case !info.IsDir() && bf.FilePath == oldPath:
				// Single-file book: the row names the file that just moved.
				newFilePath = targetPath
			case info.IsDir() && strings.HasPrefix(bf.FilePath, oldPath+string(os.PathSeparator)):
				// Directory book: rebase each track under the new directory.
				// The trailing separator in the prefix keeps a sibling whose
				// path merely starts with oldPath ("…/Book 2" vs "…/Book 20")
				// from being rewritten.
				newFilePath = filepath.Join(targetPath, strings.TrimPrefix(bf.FilePath, oldPath+string(os.PathSeparator)))
			default:
				continue
			}
			matchedAny = true
			bf.FilePath = newFilePath
			bf.ITunesPath = orgSvc.ComputeITunesPath(newFilePath)
			if err := orgSvc.db.UpdateBookFile(bf.ID, &bf); err != nil {
				log.Warn("ReOrganizeInPlace: failed to update book_file %s path for book %s: %s", bf.ID, book.ID, err.Error())
				rescanNeeded = true
			}
		}
		// A single-file book whose row was ALREADY stale before this run (its
		// FilePath had drifted from book.FilePath, a known divergence class) has
		// no row matching oldPath, so the loop above rewrites nothing and the
		// file just moved out from under a row that still points elsewhere. That
		// is the very 404 this function exists to prevent, so flag it for the
		// next scan to self-heal rather than returning a silent success. (A
		// directory book with no matching row is left alone: its tracks may live
		// outside oldPath by design, and MarkNeedsRescan on every such book would
		// be noise.)
		if len(bookFiles) > 0 && !matchedAny && !info.IsDir() {
			log.Warn("ReOrganizeInPlace: single-file book %s moved but no book_file row matched the old path; marking for rescan", book.ID)
			rescanNeeded = true
		}
		if rescanNeeded {
			_ = orgSvc.db.MarkNeedsRescan(book.ID)
		}
	} else {
		// The move already succeeded, but we could not even read the rows to
		// rewrite them, so every one now points at the old (moved-away) path.
		// Mark for rescan so it self-heals.
		log.Warn("ReOrganizeInPlace: failed to load book_files for %s after move, marking for rescan: %s", book.ID, bfErr.Error())
		_ = orgSvc.db.MarkNeedsRescan(book.ID)
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
// the LibraryState/LastOrganizeOperationID/LastOrganizedAt stamp back onto that
// hydrated struct. See hydrateAndUpdateBook for the rationale.
//
// LibraryState MUST be set here, not just the two timestamps. All three callers
// mean "this book is now sitting at its correct organized path": the
// oldPath==newPath branch, the alreadyInRoot re-organize branch, and the bulk
// alreadyCorrect stamp. Until 2026-08-11 this helper wrote only the two stamp
// fields, so a book that was PERFECTLY organized kept library_state="imported"
// forever — and the dashboard's "Needs Organizing" card counts exactly
// library_state=="imported". Re-running organize could never clear it, because
// FilterBooksNeedingOrganization diverts already-correct books into
// alreadyCorrect BEFORE they can reach ReOrganizeInPlace, which does set the
// state correctly (see the oldPath==targetPath branch). That asymmetry — one
// path marking the state, its sibling silently not — is what produced a
// permanent, self-refilling organize backlog.
func (orgSvc *Service) stampOrganizeMetadata(bookID, operationID string, when time.Time) error {
	organizedState := "organized"
	return orgSvc.hydrateAndUpdateBook(bookID, func(b *database.Book) {
		b.LibraryState = &organizedState
		b.LastOrganizeOperationID = &operationID
		b.LastOrganizedAt = &when
	})
}

// LandingOutcome is what CommitLanding persisted for one organized book.
type LandingOutcome int

const (
	// LandingUnchanged: the landing is the book's current path. The book was
	// stamped as organized; no row changed its path.
	LandingUnchanged LandingOutcome = iota
	// LandingRenamed: an in-place landing. ReOrganizeInPlace already rewrote
	// the book's own rows; CommitLanding stamped it and recorded the rename.
	LandingRenamed
	// LandingVersioned: an out-of-root landing. CreateOrganizedVersion wrote
	// the version row and its book_file rows at the landed paths and demoted
	// the original.
	LandingVersioned
)

// CommitLanding persists what OrganizeOneBook did for book: it takes the
// Landing that call returned and writes whatever the database needs to agree
// with the disk -- the organize stamp for an unchanged or in-place landing, or
// the organized version row (with its book_file rows) for a landing outside
// the library root. It records the OperationChange rows for operationID (none
// when it is empty) on every branch, including the failure of the version
// write, so an operation's summary and its change log cannot disagree.
//
// It is THE row-writing step after OrganizeOneBook, for every caller.
// organizeBooks, the batch-save op and metafetch's library copy all go
// through it; until 2026-09-02 each had its own copy of this branch and three
// of the copies wrote no book_file rows at all -- files moved, rows still
// naming the source. A caller that needs to act on the outcome (a handler's
// response, an op's counters) switches on the returned LandingOutcome; the
// created version row is returned only for LandingVersioned.
//
// On a failed version write it returns the error WITHOUT having demoted the
// original: CreateOrganizedVersion has already removed the row and the files
// it wrote (Landing.Created), so the book is exactly as it was before the
// organize.
func (orgSvc *Service) CommitLanding(book *database.Book, landing *Landing, operationID string, log logger.Logger) (LandingOutcome, *database.Book, error) {
	if book == nil {
		return LandingUnchanged, nil, fmt.Errorf("organize: cannot commit a landing for a nil book")
	}
	if landing == nil || landing.Path == "" {
		return LandingUnchanged, nil, fmt.Errorf("organize: no landing for %s (%s) — nothing to commit", book.Title, book.ID)
	}
	oldPath := book.FilePath
	newPath := landing.Path
	now := time.Now()

	if oldPath == newPath {
		if updateErr := orgSvc.stampOrganizeMetadata(book.ID, operationID, now); updateErr != nil {
			log.Debug("Organize: failed to stamp book %s: %s", book.ID, updateErr.Error())
		}
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
		return LandingUnchanged, nil, nil
	}

	if landing.InPlace {
		if updateErr := orgSvc.stampOrganizeMetadata(book.ID, operationID, now); updateErr != nil {
			log.Debug("Organize: failed to stamp re-organized book %s: %s", book.ID, updateErr.Error())
		}
		log.Info("Re-organized %s: %s → %s", book.Title, oldPath, newPath)
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
		return LandingRenamed, nil, nil
	}

	// Version-aware organize: create a new book record for the organized copy.
	createdBook, createErr := orgSvc.CreateOrganizedVersion(book, landing, operationID, log)
	if createErr != nil {
		// Record the failure against the operation, the same way the
		// file-operation failure path in organizeBooks does. This used to be
		// skipped: the op's summary said N failed and the change log held
		// fewer than N organize_failed rows, and the gap looked like clean
		// books rather than unrecorded failures.
		if operationID != "" {
			_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
				ID:          ulid.Make().String(),
				OperationID: operationID,
				BookID:      book.ID,
				ChangeType:  "organize_failed",
				FieldName:   "file_path",
				OldValue:    oldPath,
				NewValue:    createErr.Error(),
			})
		}
		return LandingUnchanged, nil, createErr
	}

	// Stamp the new organized book record with this operation.
	createdBook.LastOrganizeOperationID = &operationID
	createdBook.LastOrganizedAt = &now
	if _, updateErr := orgSvc.db.UpdateBook(createdBook.ID, createdBook); updateErr != nil {
		log.Debug("Organize: failed to stamp new book %s: %s", createdBook.ID, updateErr.Error())
	}

	log.Info("Organized %s: created version %s → %s (original kept at %s)",
		book.Title, createdBook.ID, newPath, oldPath)
	return LandingVersioned, createdBook, nil
}

func (orgSvc *Service) organizeBooks(ctx context.Context, booksToOrganize []database.Book, alreadyCorrect []database.Book, log logger.Logger, operationID string) *Stats {
	stats := &Stats{Total: len(booksToOrganize) + len(alreadyCorrect)}

	// Thread-safe counters and collectors
	var statsMu sync.Mutex
	var progressCounter atomic.Int64

	const numWorkers = 8
	jobs := make(chan int, numWorkers*2)

	// Start worker goroutines
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Go(func() {
			workerOrg := orgSvc.newOrganizer()

			for i := range jobs {
				// Cancellation is checked HERE as well as in the feeder below.
				// Checking only the feeder stops new work being queued but lets
				// the eight workers drain everything already buffered, so a
				// cancelled organize kept moving files after the user asked it
				// to stop. ctx covers the cases log.IsCanceled() cannot see at
				// all — the HTTP client disconnecting, and server shutdown.
				if ctx.Err() != nil || log.IsCanceled() {
					statsMu.Lock()
					stats.Skipped++
					statsMu.Unlock()
					progressCounter.Add(1)
					continue
				}

				book := booksToOrganize[i]

				// Policy check: skip books tagged policy:no-organize.
				if tags, err := orgSvc.db.GetBookTags(book.ID); err == nil {
					if policy.EvaluatePolicy(tags).NoOrganize {
						log.Debug("organize: skipping book %s — policy:no-organize tag", book.ID)
						statsMu.Lock()
						stats.Skipped++
						statsMu.Unlock()
						progressCounter.Add(1)
						continue
					}
				}

				oldPath := book.FilePath

				// --- Step 1: File operations ---
				var newPath string
				var landing *Landing
				var err error

				// Same decision as the post-scan auto-organize hook, via one
				// shared method — see OrganizeOneBook for why that matters.
				// The DB-update step below branches on landing.InPlace, the
				// decision OrganizeOneBook actually took, not on a prefix
				// test of its own.
				landing, err = orgSvc.OrganizeOneBook(workerOrg, &book, log)
				if landing != nil {
					newPath = landing.Path
				}

				// --- Step 2: DB operations ---
				var commitErr error
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
				} else {
					// One row-writing path for every caller. The
					// already-correct / in-place / versioned branch lived
					// inline here until 2026-09-02, and every other caller
					// of OrganizeOneBook (the batch-save op, the folder
					// auto-scan, metafetch's library copy) had grown its own
					// copy of it -- three of them skipping the row writes.
					var outcome LandingOutcome
					outcome, _, commitErr = orgSvc.CommitLanding(&book, landing, operationID, log)
					statsMu.Lock()
					switch {
					case commitErr != nil:
						stats.Failed++
					case outcome == LandingUnchanged:
						stats.AlreadyCorrect++
					case outcome == LandingRenamed:
						stats.ReOrganized++
					default:
						stats.Organized++
					}
					statsMu.Unlock()
				}

				// --- Step 3: Enqueue iTunes writeback ---
				if err == nil && commitErr == nil && oldPath != newPath && orgSvc.writeBackBatcher != nil {
					orgSvc.writeBackBatcher.Enqueue(book.ID)
				}

				// --- Step 4: Progress reporting ---
				count := progressCounter.Add(1)
				if count%50 == 0 || count == int64(len(booksToOrganize)) {
					log.UpdateProgress(int(count), len(booksToOrganize),
						fmt.Sprintf("Organizing: %d/%d books", count, len(booksToOrganize)))
				}
			}
		})
	}

	// Feed jobs — cancellation checked here AND in the worker loop above.
	for i := range booksToOrganize {
		if ctx.Err() != nil {
			log.Info("Organize canceled: %s", ctx.Err().Error())
			stats.Canceled = true
			break
		}
		if log.IsCanceled() {
			log.Info("Organize canceled")
			stats.Canceled = true
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

	summary := formatOrganizeSummary(stats)
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

// OrganizeOneBook applies the correct organize strategy for one book and
// returns its new path.
//
// There are three, and picking the wrong one fails outright rather than
// degrading:
//
//	already under RootDir            -> ReOrganizeInPlace
//	file_path is a dir OR >1 rows    -> OrganizeDirectoryBook (multi-file book)
//	otherwise                        -> Organizer.OrganizeBook (single file)
//
// The ">1 rows" half of the middle test is what the HTTP handler and the
// batch-save op used; the worker loop used to look only at file_path. A
// multi-row book whose file_path names its first FILE therefore went down the
// single-file path here and every one of its rows was stamped with that one
// organized path. CreateOrganizedVersion now refuses that shape outright, and
// this decides it the same way as the other two callers so it never arises.
//
// WHY THIS IS A METHOD AND NOT AN INLINE `if`: this branch used to live only
// inside organizeBooks' worker loop. The post-scan auto-organize hook in
// package server called Organizer.OrganizeBook directly, so every multi-file
// book it touched failed with
//
//	cannot organize %q: file_path %s is a directory but single-file organize
//	was requested — use organizeDirectoryBook for multi-file books
//
// Measured on production 2026-08-11: 588 failures of exactly that shape in a
// single post-scan run. Both call sites now share one decision, so a third
// caller cannot reintroduce the same omission by copying the wrong half.
//
// The result is a Landing rather than a path so that the caller which turns it
// into DB rows (CreateOrganizedVersion) sees what actually landed, not a path
// it has to re-derive a plan from.
func (orgSvc *Service) OrganizeOneBook(org *Organizer, book *database.Book, log logger.Logger) (*Landing, error) {
	if book == nil {
		return nil, fmt.Errorf("cannot organize: book is nil")
	}
	oldPath := book.FilePath
	if config.AppConfig.RootDir != "" && strings.HasPrefix(oldPath, config.AppConfig.RootDir) {
		newPath, err := orgSvc.ReOrganizeInPlace(book, log)
		if err != nil {
			return nil, err
		}
		return &Landing{Path: newPath, InPlace: true}, nil
	}
	bookFiles, err := orgSvc.db.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot load segments for %s (%s): %w", book.Title, book.ID, err)
	}
	isDir := len(bookFiles) > 1
	if !isDir {
		if info, statErr := os.Stat(oldPath); statErr == nil && info.IsDir() {
			isDir = true
		}
	}
	if isDir {
		return orgSvc.organizeDirectoryBookRows(org, book, bookFiles, log)
	}
	return org.OrganizeSingleFile(book)
}

// OrganizeDirectoryBook handles organizing a multi-file book where file_path is a directory.
// It always uses book_files from the database — no directory scanning fallback.
// Returns the Landing: target directory, the source->organized map of every
// planned file (a directory landing is all-or-nothing, see
// organizeBookDirectory) and the files it created.
func (orgSvc *Service) OrganizeDirectoryBook(org *Organizer, book *database.Book, log logger.Logger) (*Landing, error) {
	bookFiles, err := orgSvc.db.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot load segments for %s (%s): %w", book.Title, book.ID, err)
	}
	return orgSvc.organizeDirectoryBookRows(org, book, bookFiles, log)
}

// organizeDirectoryBookRows is OrganizeDirectoryBook with the book_file rows
// already in hand, so OrganizeOneBook — which has to read them to decide the
// strategy — does not read them twice.
func (orgSvc *Service) organizeDirectoryBookRows(org *Organizer, book *database.Book, bookFiles []database.BookFile, log logger.Logger) (*Landing, error) {
	if len(bookFiles) == 0 {
		return nil, fmt.Errorf("no segments tracked for %q (id=%s) — run a library scan to detect files in %s", book.Title, book.ID, book.FilePath)
	}

	// Pass rows flagged Missing through as well: OrganizeBookDirectory skips
	// them for copying but counts them for track numbering, so a book whose
	// files are temporarily missing keeps the numbering it will have when they
	// come back. Filtering them here would renumber the book instead.
	//
	// The empty-FilePath skip below is only for the present/missing counters --
	// planTargetPaths drops those rows itself, deliberately, so that this and
	// the two row-writing paths cannot disagree about the row set. Do not treat
	// this loop as the thing that makes the plan correct.
	var segments []database.BookFile
	missingCount, presentCount := 0, 0
	for _, bf := range bookFiles {
		if bf.FilePath == "" {
			continue
		}
		if bf.Missing {
			missingCount++
		} else {
			presentCount++
		}
		segments = append(segments, bf)
	}

	if presentCount == 0 {
		return nil, fmt.Errorf("all %d segments for %q (id=%s) marked missing on disk — re-scan to verify, or restore from backup", missingCount, book.Title, book.ID)
	}

	log.Info("Organizing %d segment file(s) for %s (from book_files)", presentCount, book.Title)

	landing, err := org.organizeBookDirectory(book, segments)
	if err != nil {
		return nil, err
	}

	// The "pathMap is empty" case is now rejected inside OrganizeBookDirectory
	// itself, so it arrives here as a non-nil err above. It used to be checked
	// only here, which left the other two callers of OrganizeBookDirectory
	// (ensureLibraryCopy, organizeMultiFileBook) pointing books at directories
	// nothing had been copied into.
	//
	// The stat check below is NOT redundant with it: pathMap records what
	// organize believed it wrote, and this verifies the files are still there.
	copiedCount := 0
	for _, dstPath := range landing.Files {
		if _, statErr := os.Stat(dstPath); statErr == nil {
			copiedCount++
		}
	}
	if copiedCount == 0 {
		return nil, fmt.Errorf("organize produced 0 files for %s — all copies failed", book.Title)
	}

	return landing, nil
}

// resolveOrganizedFilePath decides what file_path an organized book_file row
// should carry: the path the file LANDED at, or its source path if it did not
// land. `landed` is Landing.Files from the organize that just ran.
//
// There is deliberately no disk tiebreaker here. Until 2026-09-02 this took a
// recomputed PLAN and adopted the planned target whenever a file existed there
// — which, when two books planned the same target, pointed the loser's row at
// the winner's audio. Whether a file at the planned path is this book's file
// was already decided (by hash) inside OrganizeBookDirectory; the answer is in
// `landed`, and a stat cannot improve on it.
//
// A directory landing is all-or-nothing, so a present row that was planned is
// always in `landed`. A row that is NOT there was never planned: it is flagged
// Missing (planTargetPaths drops those), or it was added by a scan between the
// two GetBookFiles reads. Neither is a failed copy, and the message must not
// say it was.
func resolveOrganizedFilePath(srcPath string, landed map[string]string, log logger.Logger) string {
	dstPath, ok := landed[srcPath]
	if !ok || dstPath == "" {
		log.Warn("organize: %q was not part of this landing (flagged missing, or added by a scan mid-organize); its row keeps the source path", srcPath)
		return srcPath
	}
	return dstPath
}

// rollbackOrganizedVersion undoes a partially-built organized copy after the
// per-file copy failed, so the caller can return an error instead of falling
// through to the version-group handover that demotes the original.
//
// Order matters: the author links and the book row go first, then the files on
// disk. If the process dies midway, a row with no file is recoverable (a rescan
// re-reads it); a file with no row is invisible to the library and is what the
// next organize would collide with.
//
// WHAT THIS DOES NOT CLEAN, deliberately:
//   - The book_file rows. There are none — BatchCreateBookFiles is atomic, so a
//     failed copy wrote zero rows. A CreateBookFile loop here would have needed
//     per-row cleanup AND an iTunes PID restore; that is why the loop was replaced.
//   - The BookPathChange row RecordPathChange wrote for this book ID. Path
//     changes are read per book, so a deleted book's changes are unreachable
//     rather than wrong, and no interface method exposes deleting one. It is an
//     orphan audit row, and it is the reason this is called a rollback and not a
//     transaction.
//
// DeleteBook does NOT delete book_file or book_authors rows (pebble_store.go:2811
// tears down seven index families and neither of those), which is why the author
// links are cleared explicitly rather than left to it.
//
// On disk it removes ONLY landing.Created — the files this organize wrote —
// and then the target directory if that left it empty (unlinkCreated, shared
// with the in-flight directory rollback in organizeBookDirectory). It used to
// os.RemoveAll the whole target directory, which is wrong twice over: a
// directory book whose target already held another book's files (the
// same-title collision that skips those targets) would have had the OTHER
// book deleted, and a single-file organize whose target was an earlier copy
// this book's row already owned (OrganizeBook returns it with mode "") would
// have had that earlier copy deleted. An adopted file was there before this
// run and is not ours to remove.
func (orgSvc *Service) rollbackOrganizedVersion(newBookID string, landing *Landing, log logger.Logger) {
	if newBookID != "" {
		// SetBookAuthors with no authors clears the links this function copied.
		if err := orgSvc.db.SetBookAuthors(newBookID, nil); err != nil {
			log.Warn("organize: rollback could not clear author links for %s: %v", newBookID, err)
		}
		if err := orgSvc.db.DeleteBook(newBookID); err != nil {
			log.Warn("organize: rollback could not delete the organized book row %s: %v — a book with no files is now in the library", newBookID, err)
		}
	}

	RemoveCreated(landing, newBookID, log)
}

// RemoveCreated is the on-disk half of an organize rollback: it removes the
// files a Landing wrote (Created) and then the landing directory if that left
// it empty. owner names the book the landing was for, in log lines only.
//
// Exported because every caller that commits a landing to its own rows -- the
// iTunes importer repoints the imported book's rows rather than versioning
// them -- needs the same rollback when its row write fails, and the rules are
// not obvious: only Created is removed (an adopted file, or a copy an earlier
// organize made, is not ours), containment is ensureUnderRoot rather than a
// prefix test, and with no root configured nothing is removed at all.
func RemoveCreated(landing *Landing, owner string, log logger.Logger) {
	if landing == nil || len(landing.Created) == 0 {
		return
	}
	root := config.AppConfig.RootDir
	if root == "" {
		// Without a root there is no containment guard, and a rollback that
		// cannot prove a path is ours must not remove it. That leaves files on
		// disk with no row — the next organize's collision — so this is an
		// Error naming every one of them, not a silent return.
		log.Error("organize: rollback cannot remove %d file(s) written for %s because root_dir is unset; left on disk with no row: %s",
			len(landing.Created), owner, strings.Join(landing.Created, ", "))
		return
	}
	dir := ""
	if landing.IsDir() {
		dir = landing.Path
		if err := ensureUnderRoot(dir, root); err != nil {
			// Same guard the files get. Only an EMPTY directory can be
			// removed, so this is symmetry rather than a live hole, but a
			// rollback that cannot prove a path is ours must not touch it.
			log.Error("organize: rollback for %s will not remove directory %s: %v", owner, dir, err)
			dir = ""
		}
	}
	// Containment is ensureUnderRoot (separator-aware), not a bare prefix
	// test: /library2/x is not under /library. Organize COPIES and the
	// original survives as the source row, so removing the copy destroys no
	// audio; the directory goes only if that left it empty.
	if leftover := unlinkCreated(landing.Created, root, dir); len(leftover) > 0 {
		log.Error("organize: rollback for %s could not remove %d file(s), left on disk with no row: %s",
			owner, len(leftover), strings.Join(leftover, ", "))
	}
}

// CreateOrganizedVersion creates a new book record for the organized copy and links it to the original.
func (orgSvc *Service) CreateOrganizedVersion(book *database.Book, landing *Landing, operationID string, log logger.Logger) (*database.Book, error) {
	if landing == nil || landing.Path == "" {
		return nil, fmt.Errorf("organize: no landing for %s (%s) — refusing to create an organized version from nothing", book.Title, book.ID)
	}
	// An in-place landing moved this book's own files; the row to update is
	// the book itself. Minting a second book row here would leave two rows on
	// one set of files — the #3046 M2 hole — and a failure past this point
	// would "roll back" by unlinking files the book still owns. Callers branch
	// on InPlace before calling; this refusal is for the caller that forgets.
	if landing.InPlace {
		return nil, fmt.Errorf("organize: landing for %s (%s) is in place at %s — the existing row must be updated, not versioned", book.Title, book.ID, landing.Path)
	}
	newPath := landing.Path
	isDir := landing.IsDir()
	newBookID := ulid.Make().String()
	isPrimary := true
	isNotPrimary := false
	organizedState := "organized"

	// Determine or create version group
	versionGroupID := ""
	joinedExistingGroup := false
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		versionGroupID = *book.VersionGroupID
		joinedExistingGroup = true
	} else {
		versionGroupID = ulid.Make().String()
	}

	// A group may already have elected a primary. This happens routinely: a
	// newly downloaded copy of a book the library already owns gets hash-matched
	// into the existing book's version group by the scanner, and organize then
	// runs against the new row. Below, only `book` (this call's own source row)
	// is demoted — the group's pre-existing organized primary is not, so
	// claiming primary here would leave the group with two.
	//
	// Deliberately: the NEW record yields, rather than the incumbent being
	// demoted. The incumbent is the copy that has already been through metadata
	// enrichment and sits under a real author directory; the new one is
	// frequently still `Unknown Author` because organize can run before
	// enrichment. Demoting the incumbent would make the worse-named record the
	// one the UI shows.
	//
	// This left 10,780 groups holding surplus primaries in production before it
	// was guarded — see
	// docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md
	if joinedExistingGroup {
		if members, err := orgSvc.db.GetBooksByVersionGroup(versionGroupID); err == nil {
			for i := range members {
				if members[i].ID == book.ID {
					continue // this row is demoted below
				}
				if members[i].IsPrimaryVersion != nil && *members[i].IsPrimaryVersion {
					isPrimary = false
					log.Info("Version group %s already has primary %s; organizing %q as a non-primary version",
						versionGroupID, members[i].ID, book.Title)
					break
				}
			}
		} else {
			// Fail open on the read: a lookup failure must not block the
			// organize itself. Worst case is the pre-guard behaviour.
			log.Warn("Could not check version group %s for an existing primary: %v", versionGroupID, err)
		}
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

	// The organized copy inherits the source row's SeriesID verbatim; if that
	// series has since been deleted, do not propagate the dangling ref onto a
	// brand-new row (C610).
	database.DropDanglingSeriesRef(orgSvc.db, &newBook, "organizer.copy")

	createdBook, err := orgSvc.db.CreateBook(&newBook)
	if err != nil {
		log.Error("Failed to create organized book record for %s: %v — removing the %d file(s) this organize wrote", book.Title, err, len(landing.Created))
		// No row exists yet, so only the on-disk half of the rollback applies.
		orgSvc.rollbackOrganizedVersion("", landing, log)
		return nil, err
	}
	// Import-provenance: CreateBook records an "import" path-change with an empty
	// OldPath for the new organized book. But organize RENAMED/MOVED the file from
	// book.FilePath → newPath, and that source path is known here, so record a
	// second "organize" path-change carrying the real old→new. Without this the
	// change log would only show "Imported — <newPath>" and drop where the file was
	// organized FROM. Mirrors the library_copy convention in
	// metafetch/service_apply.go (CreateBook import marker + explicit old→new).
	if book.FilePath != "" && book.FilePath != newPath {
		if err := orgSvc.db.RecordPathChange(&database.BookPathChange{
			BookID:     createdBook.ID,
			OldPath:    book.FilePath,
			NewPath:    newPath,
			ChangeType: "organize",
		}); err != nil {
			log.Warn("organize: failed to record organize path change for %s: %v", createdBook.ID, err)
		}
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

	// Copy book files to the new book with updated paths.
	//
	// The per-file path must come from the SAME planner OrganizeBookDirectory
	// copied with. Until 2026-08-15 this rebuilt it as
	// filepath.Join(newPath, filepath.Base(bf.FilePath)) -- a third, independent
	// derivation that silently kept the source filename. It happened to agree
	// while OrganizeBookDirectory also kept filepath.Base; now that the file
	// naming pattern decides the destination filename, guessing here would write
	// every organized book_file row a path with no file at it.
	// Both halves of this are load-bearing, and until 2026-08-24 neither was.
	//
	// The read error was discarded by an `err == nil` guard and the write error
	// by `_ =`, and BOTH fell through to the version-group handover below, which
	// demotes the ORIGINAL to organized_source and non-primary. So a failure here
	// produced a version group whose PRIMARY row owned no audio while the row that
	// still had the files was marked as the superseded source. Nothing logged, and
	// the function returned success.
	//
	// A zero-length result is NOT a failure: books with no book_file rows are
	// normal here (that is what ensureSingleFileBookFile backfills). Only a read
	// error and a write error abort.
	bookFiles, bfErr := orgSvc.db.GetBookFiles(book.ID)
	if bfErr != nil {
		log.Error("organize: cannot read book files for %s (%s): %v — rolling back the organized copy", book.Title, book.ID, bfErr)
		orgSvc.rollbackOrganizedVersion(newBookID, landing, log)
		return nil, fmt.Errorf("read book files for %s: %w", book.ID, bfErr)
	}
	if !isDir && len(bookFiles) > 1 {
		// A single-file landing for a multi-row book would stamp every row
		// with the one path. Nothing here can say which row that path belongs
		// to, so fail closed rather than write rows that are wrong by
		// construction.
		log.Error("organize: %s (%s) has %d book_file rows but landed as a single file at %s — rolling back", book.Title, book.ID, len(bookFiles), newPath)
		orgSvc.rollbackOrganizedVersion(newBookID, landing, log)
		return nil, fmt.Errorf("organize: %d book_file rows for %s but a single-file landing at %s", len(bookFiles), book.ID, newPath)
	}
	if len(bookFiles) > 0 {
		newFiles := make([]*database.BookFile, 0, len(bookFiles))
		for _, bf := range bookFiles {
			newBF := bf
			newBF.ID = ulid.Make().String()
			newBF.BookID = newBookID
			if isDir && bf.FilePath != "" {
				newBF.FilePath = resolveOrganizedFilePath(bf.FilePath, landing.Files, log)
			} else if !isDir {
				newBF.FilePath = newPath
			}
			if newBF.FilePath != "" {
				newBF.ITunesPath = orgSvc.ComputeITunesPath(newBF.FilePath)
			}
			newFiles = append(newFiles, &newBF)
		}
		// BatchCreateBookFiles rather than a CreateBookFile loop, for two reasons
		// beyond the single aggregate recompute it was written for. It returns an
		// error instead of offering one to discard; and its write is ATOMIC, which
		// is what makes this rollback simple. `newBF := bf` copies
		// ITunesPersistentID wholesale, so every row here stages a PID transfer off
		// the original's file row -- with a per-row loop, a failure at row K would
		// leave rows 1..K-1 having already moved their PID to a row this rollback
		// then deletes, stranding the PID exactly as #2872 fixed. Atomic means a
		// failure transfers no PID at all, so there is nothing to restore.
		if err := orgSvc.db.BatchCreateBookFiles(newFiles); err != nil {
			log.Error("organize: failed to copy %d book file row(s) to the organized copy of %s (%s): %v — rolling back", len(newFiles), book.Title, book.ID, err)
			orgSvc.rollbackOrganizedVersion(newBookID, landing, log)
			return nil, fmt.Errorf("copy book files for %s: %w", book.ID, err)
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
