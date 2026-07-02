// file: internal/server/folder_autoscan_op.go
// version: 1.2.0
// guid: 7b3e9f2a-4c1d-4e85-a6b8-2f0d5c8e1a93
// last-edited: 2026-07-01
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
type folderAutoScanOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
	FolderPath string `json:"folder_path"`
	FolderID   int    `json:"folder_id"`
}

// RegisterFolderAutoScanOp registers the "library.folder-auto-scan" OperationDef.
func (s *Server) RegisterFolderAutoScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.folder-auto-scan",
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
				_ = json.Unmarshal(rawParams, &p)
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
					for _, b := range books {
						dbBook, err := s.Store().GetBookByFilePath(b.FilePath)
						if err != nil || dbBook == nil {
							continue
						}
						newPath, _, err := org.OrganizeBook(dbBook)
						if err != nil {
							_ = progress.Log("warn", fmt.Sprintf("Organize failed for %s: %v", dbBook.Title, err), nil)
							continue
						}
						if newPath != dbBook.FilePath {
							dbBook.FilePath = newPath
							scanner.ApplyOrganizedFileMetadata(dbBook, newPath)
							if _, err := s.Store().UpdateBook(dbBook.ID, dbBook); err != nil {
								_ = progress.Log("warn", fmt.Sprintf("Failed to update path for %s: %v", dbBook.Title, err), nil)
							} else {
								organized++
							}
						}
					}
					_ = progress.Log("info", fmt.Sprintf("Auto-organize complete: %d organized", organized), nil)
				} else if config.AppConfig.AutoOrganize && config.AppConfig.RootDir == "" {
					_ = progress.Log("warn", "Auto-organize enabled but root_dir not set", nil)
				}
			}

			// Trigger dedup check on newly scanned books (non-blocking goroutine).
			if s.dedupEngine != nil && len(books) > 0 {
				go func() {
					for _, b := range books {
						dbBook, err := s.Store().GetBookByFilePath(b.FilePath)
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
				folder, err := s.Store().GetImportPathByID(p.FolderID)
				if err != nil || folder == nil {
					_ = progress.Log("warn", fmt.Sprintf("Could not reload import path %d for update: %v", p.FolderID, err), nil)
				} else {
					folder.BookCount = len(books)
					now := time.Now()
					folder.LastScan = &now
					if err := s.Store().UpdateImportPath(folder.ID, folder); err != nil {
						_ = progress.Log("warn", fmt.Sprintf("Failed to update book count: %v", err), nil)
					}
				}
			}

			// Bridge the v2 run completion back to the legacy v1
			// Operation row so callers (e.g. POST /import-paths) that
			// returned scan_operation_id = LegacyOpID can poll
			// completion via the v1 ops endpoint. Without this the
			// v1 row sticks in "queued" forever even though the work
			// is done.
			summary := fmt.Sprintf("Auto-scan completed (%d books found)", len(books))
			if p.LegacyOpID != "" && s.Store() != nil {
				_ = s.Store().UpdateOperationStatus(p.LegacyOpID, "completed", len(books), len(books), summary)
			}
			_ = progress.Log("info", fmt.Sprintf("Auto-scan completed. Total books: %d", len(books)), nil)
			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "library.folder-auto-scan", "library", summary, activity.AlwaysShow)
			}
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterFolderAutoScanOp(reg) })
}
