// file: internal/scheduler/extra_ops.go
// version: 1.27.0
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210
// last-edited: 2026-10-11

// extra_ops registers OperationDefs for 4 scheduler tasks that previously
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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/gin-gonic/gin"
)

// ExtraOpsDeps holds the typed dependencies needed by ExtraOpsRegistrar.
// All fields are optional (nil-safe) to match the server's lazy-init pattern.
type ExtraOpsDeps struct {
	ActivityWriter       *activity.Writer
	DedupCache           *cache.Cache[gin.H]
	DedupEngine          *dedup.Engine
	MetadataFetchService *metafetch.Service
	// ScanStandDown is the scan stand-down gate the metadata upgrade op
	// holds while it applies (metadata is never applied during a library scan).
	// Nil = ungated (tests).
	ScanStandDown opsregistry.ScanStandDownGate
}

// ExtraOpsStore is everything the 4 scheduler OperationDefs need from the
// store: metabatch.Store, which the metadata upgrade forwards into its
// service, plus the author, author-link and book methods the author ops call
// directly. Was database.Store (398 methods) until 2026-08-19;
// versions.TrashedVersionCleaner and the db-optimize/tombstone methods left
// with scheduler.trash-cleanup, scheduler.db-optimize and
// scheduler.tombstone-cleanup on 2026-10-10, and GetAllBooksCore left with
// scheduler.metadata-refresh on 2026-10-11 (04-P4c); GetAuthorByID and
// GetBooksByAuthorIDWithRoleCore left with scheduler.resolve-production-authors
// the same day (04-P4d).
type ExtraOpsStore interface {
	metabatch.Store // NewMetadataUpgradeService

	extraOpsAuthorStore
	extraOpsAuthorLinkStore

	// ModifyBook is the author split's primary-author write: a locked
	// read-modify-write of AuthorID/Author, never GetBookByID -> UpdateBook.
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// extraOpsAuthorStore is the author-consolidation op's author-row half. Split
// out because interfacebloat counts DECLARED ENTRIES (limit 8); the book-link
// half below was split off again on 2026-09-12 when the relink getter took the
// combined set to nine.
type extraOpsAuthorStore interface {
	CreateAuthor(name string) (*database.Author, error)
	DeleteAuthor(id int) error
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
}

// extraOpsAuthorLinkStore is the book<->author join the consolidation op
// reads and rewrites.
type extraOpsAuthorLinkStore interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	// GetBooksByAuthorIDForRelinkCore includes the trash; the split path's
	// relink list, because DeleteAuthor sweeps trashed books' credits too.
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// ExtraOpsRegistrar holds a Store reference and typed Deps so the 4 scheduler
// OperationDefs can be registered without a *Server pointer.
type ExtraOpsRegistrar struct {
	Deps  ExtraOpsDeps
	Store ExtraOpsStore
}

// NewExtraOpsRegistrar constructs an ExtraOpsRegistrar.
func NewExtraOpsRegistrar(store ExtraOpsStore, deps ExtraOpsDeps) *ExtraOpsRegistrar {
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
		Liveness:        opsregistry.LivenessNone,
		ProgressTimeout: 2 * time.Hour, // LivenessNone requires an explicit budget
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

// --- metadata-upgrade ---

// RegisterMetadataUpgradeOp registers the scheduler.metadata-upgrade OperationDef.
//
// It is the ONLY registration of the metadata upgrade (2026-09-27). Until then
// the same job was also registered as maintenance.metadata-upgrade by the
// maintenance plugin, a second def with its own ConcurrencyKey, so the two
// could run at once over the same books. Nothing in web/src or the handlers
// named the maintenance ID; it stays reachable as a FormerID, so an old
// operations_v2 row or an API enqueue by that ID resolves to this def.
func (r *ExtraOpsRegistrar) RegisterMetadataUpgradeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.metadata-upgrade",
		FormerIDs:       []string{"maintenance.metadata-upgrade"},
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "scheduler",
		DisplayName:     "Metadata Upgrade",
		Description:     "Replace metadata from lower-ranked sources (Open Library, Google Books, Wikipedia) with a higher-ranked source's (Hardcover, Audible) when its match passes the bulk-apply gate. User-locked fields are kept.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.metadata-upgrade",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		// Audible and generic network: the search queries every metadata
		// provider. OpenAI: the search may LLM-rerank its candidates.
		Capabilities: []opsregistry.Capability{
			opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite,
			opsregistry.CapNetworkAudible, opsregistry.CapNetworkGeneric, opsregistry.CapNetworkOpenAI,
		},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) (retErr error) {
			// Metadata is never applied during a library scan: hold the scan
			// stand-down before the first write. A running scan does not make
			// the op refuse: the acquire quiesces it and waits for it to park
			// (bounded by the lease), and the op fails only if it never parks.
			hold, sdErr := opsregistry.HoldScanStandDown(ctx, r.Deps.ScanStandDown, reporter, "scheduler.metadata-upgrade apply")
			if sdErr != nil {
				return sdErr
			}
			defer func() { retErr = hold.Finish(retErr) }()
			ctx, reporter = hold.Context(), hold.Reporter()

			progress := extraOpsProgressAdapter{r: reporter}
			p := sdk.NewProgress(reporter, 0)
			p.Start("Scanning for books with upgradeable metadata sources...")
			if r.Deps.MetadataFetchService == nil {
				return fmt.Errorf("metadata fetch service not initialized")
			}
			svc := metabatch.NewMetadataUpgradeService(r.Store, r.Deps.MetadataFetchService)
			_ = progress.Log("info", "Scanning for books with upgradeable metadata sources...", nil)
			// M7 (2026-07 error-correction sweep): thread progress through so
			// this 4-hour-timeout op reports books processed/total every 25
			// books instead of going silent between start and result.
			result, err := svc.RunUpgrade(ctx, 200, progress)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("Metadata upgrade complete: checked %d, upgraded %d, skipped %d, "+
				"owner-manual-only %d (Doctor Who / Big Finish / Torchwood), errors %d; sweep after %q -> %q (wrapped %v)",
				result.Checked, result.Upgraded, result.Skipped, result.OwnerManualOnly, result.Errors,
				result.CursorStart, result.CursorEnd, result.Wrapped)
			_ = progress.Log("info", msg, nil)
			p.Done(msg)
			return nil
		},
	})
}

// --- author-split-scan ---

// scheduler.author-split-scan takes only a mode. It ignored its params until
// 2026-09-25 and wrote on every run. It resolves the mode through
// opmode.ParseDryRun: dry_run defaults to TRUE when absent; dryRun is accepted
// as an alias; disagreeing values are an error. The scheduled trigger
// (tasks.go) sends schedulerExtraOpParams{}, which carries neither key, so a
// scheduled run is a dry run -- deliberately, by owner decision; do not add
// dry_run=false to that enqueue. (scheduler.resolve-production-authors had the
// same contract until 04-P4d moved it to maintenance.resolve-production-authors.)

// RegisterAuthorSplitScanOp registers the scheduler.author-split-scan OperationDef.
func (r *ExtraOpsRegistrar) RegisterAuthorSplitScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.author-split-scan",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "scheduler",
		DisplayName:     "Author Split Scan",
		Description:     "Find and split composite author names into individual author records. Dry run by default (scheduled runs too); pass dry_run=false to write.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.author-split-scan",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			dryRun, err := opmode.ParseDryRun("scheduler.author-split-scan", rawParams)
			if err != nil {
				return err
			}
			progress := extraOpsProgressAdapter{r: reporter}
			store := r.Store
			if store == nil {
				return fmt.Errorf("database not initialized")
			}
			_ = progress.Log("info", fmt.Sprintf("Starting author split scan (dry_run=%v)", dryRun), nil)
			authors, err := store.GetAllAuthors()
			if err != nil {
				return fmt.Errorf("failed to get authors: %w", err)
			}
			_ = progress.Log("info", fmt.Sprintf("Scanning %d authors for composite names...", len(authors)), nil)

			splitCount := 0
			booksUpdated := 0
			errCount := 0
			wouldSplit, wouldCreate, wouldRelink := 0, 0, 0
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

				if dryRun {
					// Reads only; see the maintenance sibling's comment.
					missing := 0
					for _, name := range parts {
						name = strings.TrimSpace(name)
						if name == "" {
							continue
						}
						if existing, lerr := store.GetAuthorByName(name); lerr != nil || existing == nil {
							missing++
						}
					}
					books, berr := store.GetBooksByAuthorIDForRelinkCore(author.ID)
					if berr != nil {
						errCount++
						_ = progress.Log("warning", fmt.Sprintf("Failed to get books for author %q: %v", author.Name, berr), nil)
						continue
					}
					wouldSplit++
					wouldCreate += missing
					wouldRelink += len(books)
					_ = progress.Log("info", fmt.Sprintf("Would split %q (id %d) → %q (%d new authors, %d books)",
						author.Name, author.ID, parts, missing, len(books)), nil)
					continue
				}

				// Actually split: create/find individual authors
				var newAuthors []database.Author
				partFailed := false
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
					if errors.Is(err, database.ErrImplausibleAuthorName) {
						// A junk part ("read by narrator" split out of
						// "Stephen King, read by narrator") is dropped on
						// purpose: it was never a credit worth keeping.
						continue
					}
					if err != nil {
						errCount++
						partFailed = true
						_ = progress.Log("warning", fmt.Sprintf("Failed to create author %q: %v", name, err), nil)
						continue
					}
					newAuthors = append(newAuthors, *created)
				}
				// Every real part must resolve, or the composite is left alone. The
				// relink below REPLACES the composite credit with newAuthors and the
				// composite row is then deleted, so a store error on one part would
				// silently drop that person's credit from every book. Only a part
				// the creation gate refuses (database.ErrImplausibleAuthorName) is
				// dropped, because it was junk rather than a person.
				if partFailed || len(newAuthors) == 0 {
					continue
				}

				// Re-link all books from composite author to individual
				// authors. ForRelink includes the trash; see
				// extraOpsAuthorLinkStore.
				books, err := store.GetBooksByAuthorIDForRelinkCore(author.ID)
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
					// Update primary AuthorID to first individual author.
					if book.AuthorID != nil && *book.AuthorID == author.ID && len(newAuthors) > 0 {
						firstID := newAuthors[0].ID
						// `book` is a BookCore (heavy fields nil). Do NOT write
						// its ToBook() projection: it carries nil Author/Series
						// AND, because this op changes AuthorID, the
						// guard-preserved Author would be STALE (still the
						// composite). ModifyBook re-reads the full stored row
						// under its write lock and sets BOTH the new AuthorID
						// and a fresh denormalized Author, so the row stays
						// consistent (Author.ID == AuthorID), not
						// preserved-stale (STOREFID W5d-1 / #1887), and a column
						// another writer commits meanwhile is not reverted
						// (audit A1#15). Sibling of
						// internal/plugins/maintenance/author.go — keep in sync.
						newPrimary := newAuthors[0]
						written, err := store.ModifyBook(book.ID, func(full *database.Book) error {
							full.AuthorID = &firstID
							full.Author = &newPrimary
							return nil
						})
						switch {
						case err != nil:
							errCount++
							schedLog.Warn("author split: primary author write failed: book=%s author=%q err=%v", book.ID, author.Name, err)
							_ = progress.Log("warning", fmt.Sprintf("author split: failed to update primary author on book %s: %v", book.ID, err), nil)
						case written == nil:
							errCount++
							schedLog.Warn("author split: book vanished before primary author write: book=%s author=%q", book.ID, author.Name)
							_ = progress.Log("warning", fmt.Sprintf("author split: book %s no longer exists, primary author not updated", book.ID), nil)
						}
					}
					booksUpdated++
				}

				// Delete the composite author record -- only once nothing still
				// credits it. The per-book loop above skips failures, and a
				// delete after a skipped book erases that book's credit.
				if err := database.VerifyAuthorUnlinked(store, author.ID); err != nil {
					_ = progress.Log("warning", fmt.Sprintf("Composite author %q NOT deleted: %v", author.Name, err), nil)
					errCount++
				} else if err := store.DeleteAuthor(author.ID); err != nil {
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

			if dryRun {
				resultMsg := fmt.Sprintf("DRY RUN: would split %d composite authors, create %d authors, relink %d books (%d errors); "+
					"pass dry_run=false to write", wouldSplit, wouldCreate, wouldRelink, errCount)
				_ = progress.Log("info", resultMsg, nil)
				p.Done(resultMsg)
				return nil
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

// --- cleanup-old-backups ---

// RegisterCleanupOldBackupsOp registers the scheduler.cleanup-old-backups OperationDef.
func (r *ExtraOpsRegistrar) RegisterCleanupOldBackupsOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.cleanup-old-backups",
		Liveness:        opsregistry.LivenessNone,
		ProgressTimeout: 1 * time.Hour, // LivenessNone requires an explicit budget
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
		Run: func(ctx context.Context, _ json.RawMessage, reporter opsregistry.Reporter) error {
			return runCleanupOldBackups(ctx, extraOpsProgressAdapter{r: reporter})
		},
	})
}

// runCleanupOldBackups is the body of scheduler.cleanup-old-backups, lifted out
// of the OperationDef literal so the app-directory guard below can be driven by
// a test without standing up a Registry, a store and a bus. Extracting it
// changes no behaviour; the closure above now only adapts the reporter.
// cleanupProgressLogger is what runCleanupOldBackups actually calls: one
// method. extraOpsProgressAdapter satisfies it, and so does a two-line test
// stub -- which is the point, since the alternative is standing up a Registry,
// a store and a bus to exercise a filesystem guard.
type cleanupProgressLogger interface {
	Log(level, message string, details *string) error
}

func runCleanupOldBackups(ctx context.Context, progress cleanupProgressLogger) error {
	rootDir := config.AppConfig.RootDir
	if rootDir == "" {
		_ = progress.Log("info", "No root directory configured, skipping backup cleanup", nil)
		return nil
	}
	retentionDays := config.EffectiveBackupRetentionDays()
	maxAge := time.Duration(retentionDays) * 24 * time.Hour
	removed := 0
	_ = progress.Log("info", fmt.Sprintf("Scanning %s for .bak-* files older than %d days", rootDir, retentionDays), nil)

	// The library root holds application state as well as books: a backup
	// directory of multi-GB archives and an OpenLibrary dump directory with an
	// embedded database. Both are operator-settable to names with no leading
	// dot, so the dot rule alone never covered them. This op DELETES by
	// filename, so descending into them is not merely wasted I/O over ~90 GB --
	// an operator file named "*.bak-*" parked in the backup directory would be
	// removed.
	//
	// NOTE: this is one of THREE implementations of "delete old backup files"
	// in the tree (see internal/plugins/maintenance/cleanup.go with the same
	// predicate, and internal/maintenance/jobs/cleanup_backups.go with a
	// different regex). All three are guarded, and the first two now share one
	// retention source, config.EffectiveBackupRetentionDays(); consolidating them
	// is a separate change.
	app := appdirs.Current()
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if pathutil.ShouldSkipDir(rootDir, path, app) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(info.Name(), ".bak-") {
			// Age is the time in the backup's NAME (fileops.BackupTime: <unix>[-N]
			// or the legacy YYYYMMDD-HHMMSS), falling back to mtime for a name
			// without one. A create_backups backup is a hardlink and keeps the
			// original audio's mtime, often years old, so mtime alone would delete
			// a fresh backup on the next run (fileops KeepBackup, AGE CONTRACT).
			age := time.Since(fileops.BackupTime(info.Name(), info.ModTime()))
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
}
