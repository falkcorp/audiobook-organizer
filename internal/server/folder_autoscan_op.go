// file: internal/server/folder_autoscan_op.go
// version: 1.7.0
// guid: 7b3e9f2a-4c1d-4e85-a6b8-2f0d5c8e1a93
// last-edited: 2026-09-02
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

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
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

// RegisterFolderAutoScanOp registers the "library.folder-auto-scan" OperationDef.
func (s *Server) RegisterFolderAutoScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.folder-auto-scan",
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

			folderPath := p.FolderPath
			progress := registryProgressAdapter{r: reporter}
			scanLog := operations.LoggerFromReporter(progress)

			_ = progress.Log("info", fmt.Sprintf("Auto-scanning newly added folder: %s", folderPath), nil)

			// Check if folder exists.
			if _, err := os.Stat(folderPath); os.IsNotExist(err) {
				return fmt.Errorf("folder does not exist: %s", folderPath)
			}

			// Scan directory for audiobook files (parallel).
			workers := config.AppConfig.ConcurrentScans
			if workers < 1 {
				workers = 4
			}
			books, err := scanner.ScanDirectoryParallel(ctx, folderPath, workers, scanLog)
			if err != nil {
				return fmt.Errorf("failed to scan folder: %w", err)
			}

			scanLog.Info("Found %d audiobook files", len(books))

			// Process the books to extract metadata (parallel).
			if len(books) > 0 {
				scanLog.Info("Processing metadata for %d books using %d workers", len(books), workers)
				if err := scanner.ProcessBooksParallel(ctx, books, workers, nil, scanLog); err != nil {
					return fmt.Errorf("failed to process books: %w", err)
				}

				// Auto-organize if enabled.
				if config.AppConfig.AutoOrganize && config.AppConfig.RootDir != "" {
					org := organizer.NewOrganizer(&config.AppConfig)
					organized := 0
					// Counted, not just skipped. "Auto-organize complete: 0
					// organized" tells an operator nothing about WHY zero, and
					// a lookup ERROR is not the same thing as a book that has
					// no DB row — collapsing both into one bare `continue` hid
					// both.
					var failed, lookupErrors, notInDB int
					for _, b := range books {
						dbBook, err := s.Ops().GetBookByFilePath(b.FilePath)
						if err != nil {
							lookupErrors++
							if lookupErrors <= 10 {
								_ = progress.Log("warn", fmt.Sprintf("Auto-organize: DB lookup failed for %s: %v", b.FilePath, err), nil)
							}
							continue
						}
						if dbBook == nil {
							notInDB++
							continue
						}
						// OrganizeOneBook, not Organizer.OrganizeBook: the
						// latter is the SINGLE-FILE path and errors on any
						// book whose file_path is a directory.
						//
						// This is the THIRD copy of this loop. #2303 fixed the
						// same defect in server.go's AutoOrganizeFn (588
						// production failures in one run) and hoisted the
						// three-way decision into OrganizeOneBook so it could
						// not be copied wrong again — but this copy already
						// existed and was missed, because that change grepped
						// for the symptom rather than for every caller of
						// Organizer.OrganizeBook.
						landing, err := s.organizeService.OrganizeOneBook(org, dbBook, scanLog)
						if err != nil {
							_ = progress.Log("warn", fmt.Sprintf("Organize failed for %s: %v", dbBook.Title, err), nil)
							failed++
							continue
						}
						newPath := landing.Path
						if newPath != dbBook.FilePath {
							dbBook.FilePath = newPath
							scanner.ApplyOrganizedFileMetadata(dbBook, newPath)
							if _, err := s.Ops().UpdateBook(dbBook.ID, dbBook); err != nil {
								_ = progress.Log("warn", fmt.Sprintf("Failed to update path for %s: %v", dbBook.Title, err), nil)
							} else {
								organized++
							}
						}
					}
					_ = progress.Log("info", fmt.Sprintf("Auto-organize complete: %d organized, %d failed, %d not in DB, %d lookup errors (of %d scanned)",
						organized, failed, notInDB, lookupErrors, len(books)), nil)
				} else if config.AppConfig.AutoOrganize && config.AppConfig.RootDir == "" {
					_ = progress.Log("warn", "Auto-organize enabled but root_dir not set", nil)
				}
			}

			// Trigger dedup check on newly scanned books (non-blocking goroutine).
			if s.dedupEngine != nil && len(books) > 0 {
				go func() {
					for _, b := range books {
						dbBook, err := s.Ops().GetBookByFilePath(b.FilePath)
						if err != nil || dbBook == nil {
							continue
						}
						if _, err := s.dedupEngine.CheckBook(ctx, dbBook.ID); err != nil {
							slog.Warn("dedup check failed for scanned book", "dbBook", dbBook.ID, "err", err)
						}
					}
				}()
			}

			// Update book count and last-scan timestamp for this import path.
			if p.FolderID != 0 {
				folder, err := s.Ops().GetImportPathByID(p.FolderID)
				if err != nil || folder == nil {
					_ = progress.Log("warn", fmt.Sprintf("Could not reload import path %d for update: %v", p.FolderID, err), nil)
				} else {
					folder.BookCount = len(books)
					now := time.Now()
					folder.LastScan = &now
					if err := s.Ops().UpdateImportPath(folder.ID, folder); err != nil {
						_ = progress.Log("warn", fmt.Sprintf("Failed to update book count: %v", err), nil)
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
			opID := opsregistry.ReporterOpID(reporter)
			summary := fmt.Sprintf("Auto-scan completed (%d books found)", len(books))
			_ = progress.Log("info", fmt.Sprintf("Auto-scan completed. Total books: %d", len(books)), nil)
			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "library.folder-auto-scan", "library", summary, activity.AlwaysShow)
			}
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterFolderAutoScanOp(reg) })
}
