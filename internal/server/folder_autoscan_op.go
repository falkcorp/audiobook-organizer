// file: internal/server/folder_autoscan_op.go
// version: 1.11.0
// guid: 7b3e9f2a-4c1d-4e85-a6b8-2f0d5c8e1a93
// last-edited: 2026-11-19
//
// folder_autoscan_op registers the "library.folder-auto-scan" UOS v2 OperationDef.
// This op is enqueued when a new import path is added to the library; it replicates
// the richer logic (auto-organize, dedup check, import path update) that previously
// ran inline via the legacy queue.Enqueue call in filesystem_handlers.go.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// folderAutoScanOpParams holds the parameters for a library.folder-auto-scan run.
// It carried a LegacyOpID until 2026-08-22, on a comment claiming callers
// polled the twinned v1 row "via the v1 ops endpoint". They do not: the client
// polls getOperationStatus, which is a pure v2 lookup, and the v1 operation
// routes were retired as shims. So the id the caller was handed resolved at
// neither endpoint and the poll could never complete.
type folderAutoScanOpParams struct {
	FolderPath string `json:"folder_path"`
	FolderID   int    `json:"folder_id"`
}

// folderAutoScanOpID is HEAD's def ID for the folder auto-scan.
const folderAutoScanOpID = "library.folder-auto-scan"

// folderAutoScanStore is the reads of the auto-scan: the walk of the folder's
// audio files, the dedup engine's presence, and the rows the dedup check and the
// import-path update look up.
type folderAutoScanStore interface {
	// ScanFolder walks folderPath for audiobook files with workers.
	ScanFolder(ctx context.Context, folderPath string, workers int, scanLog logger.Logger) ([]scanner.Book, error)
	// DedupAvailable reports whether a dedup engine is wired.
	DedupAvailable() bool
	GetBookByFilePath(path string) (*database.Book, error)
	GetImportPathByID(id int) (*database.ImportPath, error)
}

// folderAutoScanWriter is the writes of the auto-scan: the metadata pass over the
// scanned books, the auto-organize hook, the dedup check, the import path's book
// count, and the activity rows.
type folderAutoScanWriter interface {
	// ProcessScanned extracts metadata for books and writes their rows.
	ProcessScanned(ctx context.Context, books []scanner.Book, workers int, scanLog logger.Logger, onWarn func(msg string)) error
	// AutoOrganizeScanned organizes the scanned books through the same hook the
	// library scan uses.
	AutoOrganizeScanned(ctx context.Context, books []scanner.Book, scanLog logger.Logger)
	// CheckBookDuplicates runs the dedup check on one book.
	CheckBookDuplicates(ctx context.Context, bookID string) (bool, error)
	UpdateImportPath(id int, path *database.ImportPath) error
	t7ActivityWriter
}

// folderAutoScanEnv is what the shared core takes from its shell (rules R2, R10).
type folderAutoScanEnv struct {
	t7Env
	Store  folderAutoScanStore
	Writer folderAutoScanWriter
}

// folderAutoScanCore is the body of the op, shared by HEAD's shell and the native
// kind.
func folderAutoScanCore(ctx context.Context, p folderAutoScanOpParams, env folderAutoScanEnv) error {
	folderPath := p.FolderPath
	scanLog := operations.LoggerFromReporter(env.reporter())

	env.Log(slog.LevelInfo, fmt.Sprintf("Auto-scanning newly added folder: %s", folderPath))

	// Check if folder exists.
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		return fmt.Errorf("folder does not exist: %s", folderPath)
	}

	// Scan directory for audiobook files (parallel).
	workers := config.AppConfig.ConcurrentScans
	if workers < 1 {
		workers = 4
	}
	books, err := env.Store.ScanFolder(ctx, folderPath, workers, scanLog)
	if err != nil {
		return fmt.Errorf("failed to scan folder: %w", err)
	}

	scanLog.Info("Found %d audiobook files", len(books))

	// Process the books to extract metadata (parallel).
	if len(books) > 0 {
		scanLog.Info("Processing metadata for %d books using %d workers", len(books), workers)
		// SF-02: surface a fully-aborted inline AI-parse phase into this
		// op's own record via reporter.Log, matching library.scan's
		// wiring in library_core_ops.go. Deliberately non-fatal --
		// ProcessBooksParallel still returns nil for an LLM outage, so
		// this does not turn a dead AI backend into a failed autoscan.
		if err := env.Writer.ProcessScanned(ctx, books, workers, scanLog, func(msg string) {
			env.Log(slog.LevelWarn, msg)
		}); err != nil {
			return fmt.Errorf("failed to process books: %w", err)
		}

		// Auto-organize through the SAME hook the library scan uses.
		// autoOrganizeScannedBooks resolves the scanned books to IDs,
		// backfills the single book_file row single-file books need to
		// pass FilterBooksNeedingOrganization, and hands them to
		// PerformOrganize, whose CreateOrganizedVersion writes the
		// version row, the book_file rows at their landed paths, the
		// version link and the OperationChange rows.
		//
		// Until 2026-09-02 this op ran a THIRD copy of the organize
		// loop instead: OrganizeOneBook (the file operation, no DB
		// work) followed by a hand-written FilePath repoint. It
		// discarded landing.Files, so every multi-file book it
		// organized kept book_file rows naming the pre-organize
		// paths -- audio in the library, rows pointing at the source.
		// #2303 rerouted server.go's AutoOrganizeFn through
		// PerformOrganize for exactly this defect and documented this
		// copy as the reason; the copy itself was left standing. It
		// is gone now rather than patched: one row-writing path, no
		// second implementation to drift.
		env.Writer.AutoOrganizeScanned(ctx, books, scanLog)
	}

	// Trigger dedup check on newly scanned books (non-blocking goroutine).
	if env.Store.DedupAvailable() && len(books) > 0 {
		go func() {
			for _, b := range books {
				dbBook, err := env.Store.GetBookByFilePath(b.FilePath)
				if err != nil || dbBook == nil {
					continue
				}
				if _, err := env.Writer.CheckBookDuplicates(ctx, dbBook.ID); err != nil {
					slog.Warn("dedup check failed for scanned book", "dbBook", dbBook.ID, "err", err)
				}
			}
		}()
	}

	// Update book count and last-scan timestamp for this import path.
	if p.FolderID != 0 {
		folder, err := env.Store.GetImportPathByID(p.FolderID)
		if err != nil || folder == nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("Could not reload import path %d for update: %v", p.FolderID, err))
		} else {
			folder.BookCount = len(books)
			now := time.Now()
			folder.LastScan = &now
			if err := env.Writer.UpdateImportPath(folder.ID, folder); err != nil {
				env.Log(slog.LevelWarn, fmt.Sprintf("Failed to update book count: %v", err))
			}
		}
	}

	// Status is NOT written here. The v2 worker derives it from this
	// function's return value, which is why the legacy row update this
	// replaced is gone rather than translated.
	//
	// The activity summary below tags on this op's OWN id; it used to tag on
	// the legacy one. That is the only entry involved — the scanner takes a
	// logger.Logger and no op id, so nothing else in this run is tagged. The
	// repoint still matters: GET /operations/:id/activity is reachable only
	// with an id the caller holds, and under the legacy id it never was.
	opID := env.OpID
	summary := fmt.Sprintf("Auto-scan completed (%d books found)", len(books))
	env.Log(slog.LevelInfo, fmt.Sprintf("Auto-scan completed. Total books: %d", len(books)))
	if opID != "" {
		env.Writer.FlushActivityOp(opID)
		env.Writer.EmitActivityInfo(opID, "library.folder-auto-scan", "library", summary, activity.AlwaysShow)
	}
	return nil
}

// headFolderAutoScan is HEAD's side of both interfaces: the scanner package's
// walk and metadata pass, the server's auto-organize hook, dedup engine, ops
// store and activity writer, each called as HEAD's body called it.
type headFolderAutoScan struct {
	headT7Activity
	s *Server
}

func (h headFolderAutoScan) ScanFolder(ctx context.Context, folderPath string, workers int, scanLog logger.Logger) ([]scanner.Book, error) {
	return scanner.ScanDirectoryParallel(ctx, folderPath, workers, scanLog)
}

func (h headFolderAutoScan) DedupAvailable() bool { return h.s.dedupEngine != nil }

func (h headFolderAutoScan) GetBookByFilePath(path string) (*database.Book, error) {
	return h.s.Ops().GetBookByFilePath(path)
}

func (h headFolderAutoScan) GetImportPathByID(id int) (*database.ImportPath, error) {
	return h.s.Ops().GetImportPathByID(id)
}

func (h headFolderAutoScan) ProcessScanned(ctx context.Context, books []scanner.Book, workers int, scanLog logger.Logger, onWarn func(msg string)) error {
	return scanner.ProcessBooksParallel(ctx, books, workers, nil, scanLog, onWarn)
}

func (h headFolderAutoScan) AutoOrganizeScanned(ctx context.Context, books []scanner.Book, scanLog logger.Logger) {
	h.s.autoOrganizeScannedBooks(ctx, books, scanLog)
}

func (h headFolderAutoScan) CheckBookDuplicates(ctx context.Context, bookID string) (bool, error) {
	return h.s.dedupEngine.CheckBook(ctx, bookID)
}

func (h headFolderAutoScan) UpdateImportPath(id int, path *database.ImportPath) error {
	return h.s.Ops().UpdateImportPath(id, path)
}

// folderAutoScanDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) folderAutoScanDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              folderAutoScanOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Folder Auto-Scan",
		Description:     "Auto-scan a newly added import path folder for audiobooks, then optionally organize and dedup.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "", // parallel per-folder scans are fine
		Permissions:     []auth.Permission{auth.PermScanTrigger},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p folderAutoScanOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("folder.autoscan: decode params: %w", err)
				}
			}
			h := headFolderAutoScan{headT7Activity: headT7Activity{w: s.activityWriter}, s: s}
			return folderAutoScanCore(ctx, p, folderAutoScanEnv{t7Env: t7HeadEnv(reporter, true), Store: h, Writer: h})
		},
	}
}

// RegisterFolderAutoScanOp registers the "library.folder-auto-scan" OperationDef.
func (s *Server) RegisterFolderAutoScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.folderAutoScanDef())
}

// folderAutoScanNative is the native books.library.folder-auto-scan kind. HEAD
// runs it only live (no preview, a library-write capability), so a Preview run
// is refused.
func (s *Server) folderAutoScanNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(folderAutoScanOpID)
	c := t7Common(s.folderAutoScanDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResBookFiles, ops.ResAuthors, ops.ResSeries, ops.ResFiles, ops.ResSettings, ops.ResActivity, ops.ResDedupCandidates)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return struct{}{}, err
			}
			p, err := t7DecodeNative[folderAutoScanOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, folderAutoScanCore(rc.Context(), p, folderAutoScanEnv{t7Env: t7NativeEnv(rc), Store: deps.Store, Writer: w})
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterFolderAutoScanOp(reg) })
}
