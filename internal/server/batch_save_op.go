// file: internal/server/batch_save_op.go
// version: 1.5.0
// guid: 3f2a1b4c-5d6e-7f8a-9b0c-1d2e3f4a5b6c
// last-edited: 2026-08-27
//
// batch_save_op registers the "metadata.batch-save" v2 OperationDef.
// The HTTP handler batchWriteBackAudiobooks creates a v1 op record for
// backward-compatible polling, then enqueues the run here.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// batchSaveOpParams is the JSON params for the metadata.batch-save op.
// LegacyOpID is gone as of 2026-08-22.
type batchSaveOpParams struct {
	BookIDs  []string `json:"book_ids"`
	Organize bool     `json:"organize"`
	Force    bool     `json:"force"`
}

// RegisterBatchSaveToFilesOp registers the "metadata.batch-save" v2 OperationDef.
// BatchWriteBackAudiobooks enqueues here and returns this run's v2 id; it no
// longer pre-creates a v1 op record.
//
// Per-book failures are reported with progress.Log, NOT store.AddOperationLog.
// The two are different keyspaces: AddOperationLog writes "operationlog:<id>:",
// which only sysinfo.CollectSystemLogs reads, and only for ids it finds via
// GetRecentOperations — a v1-row scan. With no v1 row minted, logs written
// there are unreachable by every API, while the v2 readers (GetOperationV2,
// GetOperationLogs, /system/logs) all read "opv2:log:<id>:" via GetOpLogsV2.
// dbReporter.Log holds logMu, so this is safe from the worker pool.
func (s *Server) RegisterBatchSaveToFilesOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "metadata.batch-save",
		Liveness:        opsregistry.LivenessRunItems,
		Plugin:          "metadata",
		DisplayName:     "Batch Save to Files",
		Description:     "Write metadata from database back to audio file tags for a set of books, with optional re-organize.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "metadata.batch-save",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p batchSaveOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("batch-save: decode params: %w", err)
				}
			}

			store := s.storeForWiring()
			progress := registryProgressAdapter{r: reporter}
			bookIDs := p.BookIDs
			totalBooks := len(bookIDs)

			_ = progress.UpdateProgress(0, totalBooks, "starting save to files")

			var written, organized, failed, skipped atomic.Int64

			// Per-item work. Everything mutated here is either an atomic counter or
			// store-local, so N of these can run at once. The `organizer` and the
			// activity logger are constructed PER ITEM rather than shared across the
			// pool: neither documents itself as goroutine-safe, and both are cheap
			// value-ish constructors, so per-item construction is the safe default.
			runOne := func(ctx context.Context, id string) error {
				book, err := store.GetBookByID(id)
				if err != nil || book == nil {
					failed.Add(1)
					_ = progress.Log("warn", fmt.Sprintf("book %s not found", id), nil)
					return nil
				}

				// Skip if already written and metadata hasn't changed since last write
				if !p.Force && book.LastWrittenAt != nil && !book.UpdatedAt.After(*book.LastWrittenAt) {
					skipped.Add(1)
					return nil
				}

				org := organizer.NewOrganizer(&config.AppConfig)
				log2 := logger.NewWithActivityLog("batch-write-back", store)

				releaseFileWrite, gateErr := writeBackFileGate.acquire(ctx)
				if gateErr != nil {
					return gateErr
				}
				defer releaseFileWrite()

				// Serialize on the destination path so two workers can never write
				// or move the same file at once — see internal/server/path_locks.go
				// for the three ways two book IDs collapse onto one path.
				//
				// RESIDUAL GAP, stated rather than hidden: for a book in a protected
				// path, WriteBackMetadataForBook redirects internally to a library
				// copy (internal/metafetch/service_writeback.go:681-691) whose path
				// is not visible from this package. Two protected books sharing one
				// library copy therefore remain unserialized here. Unlike
				// runBulkWriteBack, this op has no protected-path skip, so the gap is
				// real; closing it needs an exported resolver in internal/metafetch.
				release := writeBackPathLocks.lock(book.FilePath)
				_, wbErr := s.metadataFetchService.WriteBackMetadataForBook(id)
				if wbErr != nil {
					release()
					failed.Add(1)
					detail := wbErr.Error()
					_ = progress.Log("warn", fmt.Sprintf("write-back failed for %s", book.Title), &detail)
					return nil
				}
				written.Add(1)
				// Stamp last_written_at on the book the user sees (may differ from library copy)
				_ = store.SetLastWrittenAt(id, time.Now())

				// Organize. Still under the same path lock: organizing MOVES the file,
				// so releasing between the write and the move would reopen the race.
				if p.Organize {
					book, _ = store.GetBookByID(id)
					if book != nil {
						oldPath := book.FilePath
						alreadyInRoot := config.AppConfig.RootDir != "" && strings.HasPrefix(oldPath, config.AppConfig.RootDir)
						var newPath string
						var orgErr error
						if alreadyInRoot {
							newPath, orgErr = s.organizeService.ReOrganizeInPlace(book, log2)
						} else {
							bookFiles, _ := store.GetBookFiles(id)
							isDir := len(bookFiles) > 1
							if !isDir {
								if info, statErr := os.Stat(oldPath); statErr == nil && info.IsDir() {
									isDir = true
								}
							}
							if isDir {
								newPath, orgErr = s.organizeService.OrganizeDirectoryBook(org, book, log2)
							} else {
								newPath, _, orgErr = org.OrganizeBook(book)
							}
						}
						if orgErr != nil {
							detail := orgErr.Error()
							_ = progress.Log("warn", fmt.Sprintf("organize failed for %s", book.Title), &detail)
						} else if newPath != "" && newPath != oldPath {
							organized.Add(1)
						}
					}
				}
				release()

				// Enqueue ITL write-back
				if s.writeBackBatcher != nil {
					s.writeBackBatcher.Enqueue(id)
				}
				return nil
			}

			// RunItems reports progress after EVERY item (run_items.go:131), which is
			// what resets the registry stuck-op watchdog. This def sets Timeout: 4h
			// but no explicit ProgressTimeout, so it inherits the 5-minute default;
			// PerItemTimeout is set to 3 minutes, comfortably below it, so a single
			// wedged book fails its own item instead of killing the whole op.
			//
			// ErrModeCollect, NOT the default ErrModeFail: the loop this replaces
			// `continue`d past every per-book failure, and ErrModeFail would cancel
			// the entire batch on the first bad book. (runOne returns nil on per-book
			// failure anyway and records it in the counters, so this is belt-and-braces.)
			//
			// The Label is deliberately COARSE and constant. reporter_db.go:345 writes
			// one op_logs_v2 row per DISTINCT progress message, so a label carrying the
			// book id or the running tallies would write one DB row per book.
			runErr := opsregistry.RunItems(ctx, reporter, bookIDs, runOne, opsregistry.RunItemsOptions{
				Concurrency:    writeBackWorkers(),
				PerItemTimeout: 3 * time.Minute,
				ErrMode:        opsregistry.ErrModeCollect,
				Label:          func(int, int) string { return "saving metadata to files" },
			})
			// Return BEFORE the "complete" progress row. runOne never returns a
			// non-nil error today (per-book failures are recorded in the counters
			// and swallowed), so runErr is exactly ctx.Err() on cancellation and nil
			// otherwise — see run_items.go:203-207, where runItemsPar returns
			// ctx.Err() only when the collected error slice is empty.
			//
			// Checking it here rather than after preserves the behaviour of the loop
			// this replaces, which returned ctx.Err() from the top of every
			// iteration: a canceled batch-save must NOT write a "complete: ..."
			// progress row on its way out. It also means a future edit that makes
			// runOne return a real error surfaces it instead of reporting success.
			if runErr != nil {
				return runErr
			}

			_ = progress.UpdateProgress(totalBooks, totalBooks,
				fmt.Sprintf("complete: written %d, organized %d, skipped %d, failed %d",
					written.Load(), organized.Load(), skipped.Load(), failed.Load()))
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBatchSaveToFilesOp(reg) })
}
