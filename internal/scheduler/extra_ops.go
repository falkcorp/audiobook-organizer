// file: internal/scheduler/extra_ops.go
// version: 1.0.3
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210

// extra_ops registers OperationDefs for 13 scheduler tasks that previously
// used the legacy triggerOperation / triggerOperationWithID helpers.  Each def
// extracts the closure logic into a typed Run func so the UOS v2 dispatcher
// can manage lifecycle, cancellation, and concurrency without the old
// BridgeQueue.
//
// Extracted from internal/server/scheduler_extra_ops.go (SERVER-THIN-RESIDUAL).
// Receiver changed from *Server to *ExtraOpsRegistrar.

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ExtraOpsDeps holds the typed dependencies needed by ExtraOpsRegistrar.
// All fields are optional (nil-safe) to match the server's lazy-init pattern.
type ExtraOpsDeps struct {
	ActivityWriter       *activity.Writer
	AIScanStore          *database.AIScanStore
	DedupCache           *cache.Cache[gin.H]
	DedupEngine          *dedup.Engine
	MetadataFetchService *metafetch.Service
	OLService            *metafetch.OpenLibraryService
	AudiobookService     *audiobookspkg.AudiobookService
}

// ExtraOpsRegistrar holds a Store reference and typed Deps so the 13 scheduler
// OperationDefs can be registered without a *Server pointer.
type ExtraOpsRegistrar struct {
	Deps  ExtraOpsDeps
	Store database.Store
}

// NewExtraOpsRegistrar constructs an ExtraOpsRegistrar.
func NewExtraOpsRegistrar(store database.Store, deps ExtraOpsDeps) *ExtraOpsRegistrar {
	return &ExtraOpsRegistrar{Store: store, Deps: deps}
}

// extraOpsProgressAdapter bridges opsregistry.Reporter → operations.ProgressReporter
// so the run* helpers can be called from a v2 op Run function without changes.
type extraOpsProgressAdapter struct{ r opsregistry.Reporter }

func (a extraOpsProgressAdapter) UpdateProgress(current, total int, message string) error {
	return a.r.UpdateProgress(current, total, message)
}
func (a extraOpsProgressAdapter) Log(level, message string, details *string) error {
	l := slog.LevelInfo
	switch level {
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	case "debug":
		l = slog.LevelDebug
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	return a.r.Log(l, message, attrs...)
}
func (a extraOpsProgressAdapter) IsCanceled() bool { return a.r.IsCanceled() }

// --- dedup-llm-review ---

// RegisterDedupLLMReviewOp registers the scheduler.dedup-llm-review OperationDef.
func (r *ExtraOpsRegistrar) RegisterDedupLLMReviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.dedup-llm-review",
		Plugin:          "scheduler",
		DisplayName:     "Dedup LLM Review",
		Description:     "Run LLM review on ambiguous dedup candidates.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.dedup-llm-review",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			if r.Deps.DedupEngine == nil {
				_ = progress.Log("info", "Dedup engine not initialized, skipping LLM review", nil)
				return nil
			}
			_ = progress.Log("info", "Starting LLM review of ambiguous dedup candidates", nil)
			return r.Deps.DedupEngine.RunLLMReview(ctx)
		},
	})
}

// --- trash-cleanup ---

// RegisterTrashCleanupOp registers the scheduler.trash-cleanup OperationDef.
func (r *ExtraOpsRegistrar) RegisterTrashCleanupOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.trash-cleanup",
		Plugin:          "scheduler",
		DisplayName:     "Trash Cleanup",
		Description:     "Purge trashed book versions past their 14-day TTL.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.trash-cleanup",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			purged := versions.CleanupTrashedVersions(r.Store)
			_ = progress.Log("info", fmt.Sprintf("Trash cleanup: purged %d versions", purged), nil)
			return nil
		},
	})
}

// --- archive-sweep ---

// RegisterArchiveSweepOp registers the scheduler.archive-sweep OperationDef.
func (r *ExtraOpsRegistrar) RegisterArchiveSweepOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.archive-sweep",
		Plugin:          "scheduler",
		DisplayName:     "Archive Sweep",
		Description:     "Remove soft-deleted books past the 30-day retention window.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.archive-sweep",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			cleaned := sweep.SweepArchivedBooks(r.Store)
			_ = progress.Log("info", fmt.Sprintf("Archive sweep: cleaned %d books", cleaned), nil)
			return nil
		},
	})
}

// --- metadata-upgrade ---

// RegisterMetadataUpgradeOp registers the scheduler.metadata-upgrade OperationDef.
func (r *ExtraOpsRegistrar) RegisterMetadataUpgradeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.metadata-upgrade",
		Plugin:          "scheduler",
		DisplayName:     "Metadata Upgrade",
		Description:     "Upgrade metadata from lower-quality sources to richer ones when a high-confidence match is available.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.metadata-upgrade",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			p := sdk.NewProgress(reporter, 0)
			p.Start("Scanning for books with upgradeable metadata sources...")
			if r.Deps.MetadataFetchService == nil {
				return fmt.Errorf("metadata fetch service not initialized")
			}
			svc := metabatch.NewMetadataUpgradeService(r.Store, r.Deps.MetadataFetchService)
			_ = progress.Log("info", "Scanning for books with upgradeable metadata sources...", nil)
			result, err := svc.RunUpgrade(ctx, 200)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("Metadata upgrade complete: checked %d, upgraded %d, skipped %d, errors %d",
				result.Checked, result.Upgraded, result.Skipped, result.Errors)
			_ = progress.Log("info", msg, nil)
			p.Done(msg)
			return nil
		},
	})
}

// --- author-split-scan ---

// RegisterAuthorSplitScanOp registers the scheduler.author-split-scan OperationDef.
func (r *ExtraOpsRegistrar) RegisterAuthorSplitScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.author-split-scan",
		Plugin:          "scheduler",
		DisplayName:     "Author Split Scan",
		Description:     "Find and split composite author names into individual author records.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.author-split-scan",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			store := r.Store
			if store == nil {
				return fmt.Errorf("database not initialized")
			}
			_ = progress.Log("info", "Starting author split scan", nil)
			authors, err := store.GetAllAuthors()
			if err != nil {
				return fmt.Errorf("failed to get authors: %w", err)
			}
			_ = progress.Log("info", fmt.Sprintf("Scanning %d authors for composite names...", len(authors)), nil)

			splitCount := 0
			booksUpdated := 0
			errCount := 0
			total := len(authors)
			p := sdk.NewProgress(reporter, total)
			p.Start(fmt.Sprintf("Scanning %d authors for composite names", total))

			for i, author := range authors {
				if ctx.Err() != nil {
					return ctx.Err()
				}

				parts := dedup.SplitCompositeAuthorName(author.Name)
				if len(parts) <= 1 {
					if (i+1)%200 == 0 {
						p.StepN(i+1, fmt.Sprintf("Checked %d/%d authors", i+1, total))
					}
					continue
				}

				// Actually split: create/find individual authors
				var newAuthors []database.Author
				for _, name := range parts {
					name = strings.TrimSpace(name)
					if name == "" {
						continue
					}
					existing, err := store.GetAuthorByName(name)
					if err == nil && existing != nil {
						newAuthors = append(newAuthors, *existing)
						continue
					}
					created, err := store.CreateAuthor(name)
					if err != nil {
						errCount++
						_ = progress.Log("warning", fmt.Sprintf("Failed to create author %q: %v", name, err), nil)
						continue
					}
					newAuthors = append(newAuthors, *created)
				}
				if len(newAuthors) == 0 {
					continue
				}

				// Re-link all books from composite author to individual authors
				books, err := store.GetBooksByAuthorIDWithRoleCore(author.ID)
				if err != nil {
					errCount++
					_ = progress.Log("warning", fmt.Sprintf("Failed to get books for author %q: %v", author.Name, err), nil)
					continue
				}

				for _, book := range books {
					bookAuthors, err := store.GetBookAuthors(book.ID)
					if err != nil {
						continue
					}
					role := "author"
					for _, ba := range bookAuthors {
						if ba.AuthorID == author.ID {
							role = ba.Role
							break
						}
					}
					var updated []database.BookAuthor
					for _, ba := range bookAuthors {
						if ba.AuthorID != author.ID {
							updated = append(updated, ba)
						}
					}
					for _, na := range newAuthors {
						alreadyLinked := false
						for _, ba := range updated {
							if ba.AuthorID == na.ID {
								alreadyLinked = true
								break
							}
						}
						if !alreadyLinked {
							updated = append(updated, database.BookAuthor{
								BookID:   book.ID,
								AuthorID: na.ID,
								Role:     role,
								Position: len(updated),
							})
						}
					}
					if err := store.SetBookAuthors(book.ID, updated); err != nil {
						errCount++
						continue
					}
					// Update primary AuthorID to first individual author
					if book.AuthorID != nil && *book.AuthorID == author.ID && len(newAuthors) > 0 {
						firstID := newAuthors[0].ID
						book.AuthorID = &firstID
						// book is BookCore (heavy fields nil) — bridge via
						// .ToBook() so PebbleStore.UpdateBook sees the
						// expected all-nil heavy fields and restores them
						// from the stored row (STOR-1 guard in UpdateBook),
						// rather than a type mismatch or an accidental
						// heavy-field wipe.
						full := book.ToBook()
						_, _ = store.UpdateBook(book.ID, &full)
					}
					booksUpdated++
				}

				// Delete the composite author record
				if err := store.DeleteAuthor(author.ID); err != nil {
					_ = progress.Log("warning", fmt.Sprintf("Failed to delete composite author %q: %v", author.Name, err), nil)
					errCount++
				} else {
					splitCount++
					partNames := fmt.Sprintf("%v", parts)
					_ = progress.Log("info", fmt.Sprintf("Split %q → %s (%d books updated)", author.Name, partNames, len(books)), nil)
				}

				if (i+1)%200 == 0 || splitCount%50 == 0 {
					p.StepN(i+1, fmt.Sprintf("Checked %d/%d authors, split %d so far", i+1, total, splitCount))
				}
			}

			// Invalidate dedup cache since authors changed
			if r.Deps.DedupCache != nil {
				r.Deps.DedupCache.Invalidate("author-duplicates")
			}

			resultMsg := fmt.Sprintf("Split %d composite authors, updated %d books (%d errors)", splitCount, booksUpdated, errCount)
			_ = progress.Log("info", resultMsg, nil)
			p.Done(resultMsg)
			return nil
		},
	})
}

// --- db-optimize ---

// RegisterDBOptimizeOp registers the scheduler.db-optimize OperationDef.
func (r *ExtraOpsRegistrar) RegisterDBOptimizeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.db-optimize",
		Plugin:          "scheduler",
		DisplayName:     "Database Optimize",
		Description:     "Optimize database (VACUUM/compact) for the main, AI scan, and OpenLibrary stores.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.db-optimize",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			store := r.Store
			if store == nil {
				return fmt.Errorf("database not initialized")
			}

			storesOptimized := 0
			storesTotal := 3
			startTotal := time.Now()
			p := sdk.NewProgress(reporter, storesTotal)
			p.Start("Starting database optimization")

			// 1. Main store
			_ = progress.Log("info", "Optimizing main database (VACUUM, ANALYZE, WAL checkpoint)...", nil)
			p.StepN(0, "Optimizing main database (0/3)")
			t1 := time.Now()
			if err := store.Optimize(); err != nil {
				_ = progress.Log("error", fmt.Sprintf("Main DB optimization failed: %v", err), nil)
			} else {
				storesOptimized++
				_ = progress.Log("info", fmt.Sprintf("Main database optimized in %s", time.Since(t1).Round(time.Millisecond)), nil)
			}
			p.StepN(1, fmt.Sprintf("Main database done (1/%d)", storesTotal))

			// 2. AI scan store
			if r.Deps.AIScanStore != nil {
				t2 := time.Now()
				if err := r.Deps.AIScanStore.Optimize(); err != nil {
					_ = progress.Log("error", fmt.Sprintf("AI scan DB optimization failed: %v", err), nil)
				} else {
					storesOptimized++
					_ = progress.Log("info", fmt.Sprintf("AI scan database optimized in %s", time.Since(t2).Round(time.Millisecond)), nil)
				}
			} else {
				_ = progress.Log("info", "AI scan store not initialized, skipping", nil)
			}
			p.StepN(2, fmt.Sprintf("AI scan database done (2/%d)", storesTotal))

			// 3. OpenLibrary store (accessed via OLService)
			if r.Deps.OLService != nil && r.Deps.OLService.Store() != nil {
				t3 := time.Now()
				if err := r.Deps.OLService.Store().Optimize(); err != nil {
					_ = progress.Log("error", fmt.Sprintf("OL cache optimization failed: %v", err), nil)
				} else {
					storesOptimized++
					_ = progress.Log("info", fmt.Sprintf("OpenLibrary cache optimized in %s", time.Since(t3).Round(time.Millisecond)), nil)
				}
			} else {
				_ = progress.Log("info", "OpenLibrary store not initialized, skipping", nil)
			}
			p.StepN(3, fmt.Sprintf("OpenLibrary cache done (3/%d)", storesTotal))

			p.Done(fmt.Sprintf("Database optimization complete: %d/%d stores in %s",
				storesOptimized, storesTotal, time.Since(startTotal).Round(time.Millisecond)))
			return nil
		},
	})
}

// --- cleanup-old-backups ---

// RegisterCleanupOldBackupsOp registers the scheduler.cleanup-old-backups OperationDef.
func (r *ExtraOpsRegistrar) RegisterCleanupOldBackupsOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.cleanup-old-backups",
		Plugin:          "scheduler",
		DisplayName:     "Cleanup Old Backups",
		Description:     "Remove old .bak-* backup files past the configured retention period.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.cleanup-old-backups",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			rootDir := config.AppConfig.RootDir
			if rootDir == "" {
				_ = progress.Log("info", "No root directory configured, skipping backup cleanup", nil)
				return nil
			}
			retentionDays := config.AppConfig.PurgeSoftDeletedAfterDays
			if retentionDays <= 0 {
				retentionDays = 30
			}
			maxAge := time.Duration(retentionDays) * 24 * time.Hour
			removed := 0
			_ = progress.Log("info", fmt.Sprintf("Scanning %s for .bak-* files older than %d days", rootDir, retentionDays), nil)

			err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil || info.IsDir() {
					return nil
				}
				if strings.Contains(info.Name(), ".bak-") {
					age := time.Since(info.ModTime())
					if age > maxAge {
						if rmErr := os.Remove(path); rmErr != nil {
							slog.Warn("failed to remove old backup:", "path", path, "rmErr", rmErr)
						} else {
							removed++
							slog.Info("cleaned up old backup (age )", "path", path, "age", age.Round(time.Hour))
						}
					}
				}
				return nil
			})
			_ = progress.Log("info", fmt.Sprintf("Backup cleanup complete: removed %d file(s)", removed), nil)
			return err
		},
	})
}

// --- isbn-enrichment ---

// RegisterISBNEnrichmentOp registers the scheduler.isbn-enrichment OperationDef.
func (r *ExtraOpsRegistrar) RegisterISBNEnrichmentOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.isbn-enrichment",
		Plugin:          "scheduler",
		DisplayName:     "ISBN Enrichment",
		Description:     "Enrich missing ISBN identifiers from external metadata sources.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.isbn-enrichment",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p schedulerExtraOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("isbn-enrichment: decode params: %w", err)
			}
			progress := extraOpsProgressAdapter{r: reporter}
			return r.runIsbnEnrichment(ctx, progress, p.LegacyOpID)
		},
	})
}

// --- temp-file-cleanup ---

// RegisterTempFileCleanupOp registers the scheduler.temp-file-cleanup OperationDef.
func (r *ExtraOpsRegistrar) RegisterTempFileCleanupOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.temp-file-cleanup",
		Plugin:          "scheduler",
		DisplayName:     "Temp File Cleanup",
		Description:     "Remove orphaned *.tmp.m4b / *.tmp.m4a files left by crashed ffmpeg operations.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.temp-file-cleanup",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p schedulerExtraOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("temp-file-cleanup: decode params: %w", err)
			}
			progress := extraOpsProgressAdapter{r: reporter}
			removed := sweep.CleanupOrphanedTempFiles(config.AppConfig.RootDir, r.Deps.ActivityWriter, p.LegacyOpID)
			activity.FlushOperation(r.Deps.ActivityWriter, p.LegacyOpID)
			msg := fmt.Sprintf("Removed %d orphaned temp files", removed)
			_ = progress.Log("info", msg, nil)
			activity.EmitInfo(r.Deps.ActivityWriter, p.LegacyOpID, "temp-file-cleanup", "temp-file-cleanup", msg,
				activity.TagsIf(removed == 0, activity.NoOpTag)...)
			return nil
		},
	})
}

// --- purge-deleted ---

// RegisterPurgeDeletedOp registers the scheduler.purge-deleted OperationDef.
func (r *ExtraOpsRegistrar) RegisterPurgeDeletedOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.purge-deleted",
		Plugin:          "scheduler",
		DisplayName:     "Purge Soft-Deleted",
		Description:     "Purge soft-deleted books past their retention period.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.purge-deleted",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p schedulerExtraOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("purge-deleted: decode params: %w", err)
			}
			progress := extraOpsProgressAdapter{r: reporter}
			prog := sdk.NewProgress(reporter, 0)
			prog.Start("Starting purge of soft-deleted books")
			_ = progress.Log("info", "Starting purge of soft-deleted books", nil)
			r.runAutoPurgeSoftDeleted(ctx, p.LegacyOpID)
			activity.FlushOperation(r.Deps.ActivityWriter, p.LegacyOpID)
			_ = progress.Log("info", "Purge complete", nil)
			prog.Done("Purge complete")
			return nil
		},
	})
}

// --- tombstone-cleanup ---

// RegisterTombstoneCleanupOp registers the scheduler.tombstone-cleanup OperationDef.
func (r *ExtraOpsRegistrar) RegisterTombstoneCleanupOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.tombstone-cleanup",
		Plugin:          "scheduler",
		DisplayName:     "Tombstone Cleanup",
		Description:     "Resolve author tombstone chains (A→B→C becomes A→C).",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.tombstone-cleanup",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			store := r.Store
			if store == nil {
				return fmt.Errorf("database not initialized")
			}
			p := sdk.NewProgress(reporter, 0)
			p.Start("Resolving tombstone chains")
			_ = progress.Log("info", "Starting author tombstone chain resolution", nil)
			updated, err := store.ResolveTombstoneChains()
			if err != nil {
				return fmt.Errorf("tombstone chain resolution failed: %w", err)
			}
			resultMsg := fmt.Sprintf("Resolved %d tombstone chains", updated)
			_ = progress.Log("info", resultMsg, nil)
			p.Done(resultMsg)
			return nil
		},
	})
}

// --- resolve-production-authors ---

// RegisterResolveProductionAuthorsOp registers the scheduler.resolve-production-authors OperationDef.
func (r *ExtraOpsRegistrar) RegisterResolveProductionAuthorsOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.resolve-production-authors",
		Plugin:          "scheduler",
		DisplayName:     "Resolve Production Authors",
		Description:     "Resolve real authors for production company entries.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.resolve-production-authors",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			store := r.Store
			if store == nil {
				return fmt.Errorf("database not initialized")
			}
			_ = progress.Log("info", "Starting production author resolution", nil)
			authors, err := store.GetAllAuthors()
			if err != nil {
				return fmt.Errorf("failed to get authors: %w", err)
			}

			var prodAuthors []database.Author
			for _, a := range authors {
				if dedup.IsProductionCompany(a.Name) {
					prodAuthors = append(prodAuthors, a)
				}
			}

			_ = progress.Log("info", fmt.Sprintf("Found %d production company authors", len(prodAuthors)), nil)
			total := len(prodAuthors)
			resolved := 0
			p := sdk.NewProgress(reporter, total)
			p.Start(fmt.Sprintf("Processing %d production companies", total))
			for i, author := range prodAuthors {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				books, err := store.GetBooksByAuthorIDWithRoleCore(author.ID)
				if err != nil {
					p.StepN(i+1, fmt.Sprintf("Processed %d/%d production companies (%d books resolved)", i+1, total, resolved))
					continue
				}
				for _, book := range books {
					if r.Deps.MetadataFetchService == nil {
						continue
					}
					resp, fetchErr := r.Deps.MetadataFetchService.FetchMetadataForBookByTitle(book.ID)
					if fetchErr == nil && resp != nil && resp.Book != nil && resp.Book.AuthorID != nil {
						newAuthor, _ := store.GetAuthorByID(*resp.Book.AuthorID)
						if newAuthor != nil && !dedup.IsProductionCompany(newAuthor.Name) {
							resolved++
						}
					}
				}
				p.StepN(i+1, fmt.Sprintf("Processed %d/%d production companies (%d books resolved)", i+1, total, resolved))
			}

			if r.Deps.DedupCache != nil {
				r.Deps.DedupCache.Invalidate("author-duplicates")
			}

			resultMsg := fmt.Sprintf("Resolved %d books across %d production companies", resolved, total)
			_ = progress.Log("info", resultMsg, nil)
			p.Done(resultMsg)
			return nil
		},
	})
}

// --- metadata-refresh ---

// RegisterMetadataRefreshOp registers the scheduler.metadata-refresh OperationDef.
func (r *ExtraOpsRegistrar) RegisterMetadataRefreshOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.metadata-refresh",
		Plugin:          "scheduler",
		DisplayName:     "Metadata Refresh",
		Description:     "Re-fetch metadata for incomplete books.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.metadata-refresh",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			return r.runMetadataRefreshScan(ctx, progress)
		},
	})
}

// --- helper methods (extracted from server/metadata_handlers.go and server/audiobooks_handlers.go) ---

// runIsbnEnrichment enriches missing ISBN identifiers from external sources.
// Idempotent — books that already have an ISBN are skipped, so a restart
// safely re-runs from scratch (no checkpoint needed).
func (r *ExtraOpsRegistrar) runIsbnEnrichment(ctx context.Context, progress operations.ProgressReporter, opID string) error {
	if r.Deps.MetadataFetchService == nil || r.Deps.MetadataFetchService.ISBNEnrichment() == nil {
		_ = progress.Log("info", "ISBN enrichment service is not configured, skipping", nil)
		return nil
	}
	startMsg := "Scanning for books missing ISBN identifiers"
	_ = progress.Log("info", startMsg, nil)
	if operations.IsManual(ctx) {
		activity.EmitInfo(r.Deps.ActivityWriter, opID, "isbn-enrich", "isbn-enrichment", startMsg, activity.AlwaysShow)
	}
	_ = progress.UpdateProgress(0, 2, "Starting ISBN enrichment (0/2) (0.00%)")
	checked, updated, err := r.Deps.MetadataFetchService.ISBNEnrichment().EnrichMissingISBNs(ctx, 100, r.Deps.ActivityWriter, opID)
	if err != nil {
		return err
	}
	activity.FlushOperation(r.Deps.ActivityWriter, opID)
	msg := fmt.Sprintf("ISBN enrichment complete: checked %d, updated %d", checked, updated)
	_ = progress.Log("info", msg, nil)
	_ = progress.UpdateProgress(2, 2, fmt.Sprintf("%s (2/2) (100.00%%)", msg))
	tags := activity.TagsIf(updated == 0, activity.NoOpTag)
	if operations.IsManual(ctx) {
		tags = append(tags, activity.AlwaysShow)
	}
	activity.EmitInfo(r.Deps.ActivityWriter, opID, "isbn-enrich", "isbn-enrichment", msg, tags...)
	return nil
}

// runAutoPurgeSoftDeleted purges soft-deleted books past the retention period.
func (r *ExtraOpsRegistrar) runAutoPurgeSoftDeleted(ctx context.Context, opID string) {
	if config.AppConfig.PurgeSoftDeletedAfterDays <= 0 {
		return
	}
	if r.Store == nil {
		slog.Debug("Auto-purge skipped database not initialized")
		return
	}
	if r.Deps.AudiobookService == nil {
		slog.Debug("Auto-purge skipped audiobook service not initialized")
		return
	}

	days := config.AppConfig.PurgeSoftDeletedAfterDays
	result, err := r.Deps.AudiobookService.PurgeSoftDeletedBooks(ctx, config.AppConfig.PurgeSoftDeletedDeleteFiles, &days)
	if err != nil {
		slog.Warn("Auto-purge failed", "error", err)
		return
	}

	msg := fmt.Sprintf("Purged %d/%d soft-deleted books (%d files deleted, %d errors)",
		result.Purged, result.Attempted, result.FilesDeleted, len(result.Errors))
	slog.Info("Auto-purge", "msg", msg)
	activity.EmitInfo(r.Deps.ActivityWriter, opID, "purge-deleted", "purge-deleted", msg,
		activity.TagsIf(result.Purged == 0, activity.NoOpTag)...)
	for _, e := range result.Errors {
		activity.LogBatch(r.Deps.ActivityWriter, opID, "purge-deleted", "purge-deleted",
			activity.BatchItem{Name: e, Detail: "error"})
	}
}

// runMetadataRefreshScan reports books with incomplete metadata. Read-only,
// safe to re-run on restart with no state.
func (r *ExtraOpsRegistrar) runMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error {
	store := r.Store
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = progress.Log("info", "Starting metadata refresh scan", nil)
	books, err := store.GetAllBooks(10000, 0)
	if err != nil {
		return fmt.Errorf("failed to get books: %w", err)
	}
	total := len(books)
	denom := total + 2
	fmtPct := func(cur int) float64 {
		if denom <= 0 {
			return 0
		}
		return float64(cur) / float64(denom) * 100
	}
	_ = progress.UpdateProgress(0, denom, fmt.Sprintf("Scanning %d books for incomplete metadata (0/%d) (%.2f%%)", total, denom, fmtPct(0)))
	_ = progress.Log("info", fmt.Sprintf("Checking %d books for incomplete metadata", total), nil)
	incomplete := 0
	for i, book := range books {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if book.AuthorID == nil || book.Title == "" {
			incomplete++
			_ = progress.Log("debug", fmt.Sprintf("Incomplete: %q (id=%s)", book.Title, book.ID), nil)
		}
		if (i+1)%200 == 0 {
			_ = progress.UpdateProgress(i+1, denom, fmt.Sprintf("Checked %d/%d books (%.2f%%)", i+1, total, fmtPct(i+1)))
		}
	}
	resultMsg := fmt.Sprintf("Found %d books with incomplete metadata out of %d total", incomplete, total)
	_ = progress.Log("info", resultMsg, nil)
	_ = progress.UpdateProgress(denom, denom, fmt.Sprintf("%s (%d/%d) (100.00%%)", resultMsg, denom, denom))
	return nil
}
