// file: internal/quarantine/service.go
// version: 1.0.2
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-07-03

package quarantine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
)

// Store is the narrow database interface required by QuarantineService.
type Store interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	RecordPathChange(change *database.BookPathChange) error
	GetBookPathHistory(bookID string) ([]database.BookPathChange, error)
	GetAllBooks(limit, offset int) ([]database.Book, error)
	GetScanFailCount(pathHash string) (int, error)
	GetITunesPurgePendingBooks() ([]database.Book, error)
}

// WriteBackEnqueuer is the narrow interface for queuing iTunes track removals.
type WriteBackEnqueuer interface {
	EnqueueRemove(pid string)
}

// QuarantineService handles quarantining and unquarantining audiobook files.
type QuarantineService struct {
	store   Store
	cfg     *config.Config
	events  plugin.EventPublisher
	batcher WriteBackEnqueuer
}

// NewQuarantineService creates a QuarantineService with the given dependencies.
func NewQuarantineService(store Store, cfg *config.Config, events plugin.EventPublisher) *QuarantineService {
	return &QuarantineService{store: store, cfg: cfg, events: events}
}

// SetWriteBackBatcher wires in the iTunes write-back batcher (optional; nil is safe).
func (qs *QuarantineService) SetWriteBackBatcher(batcher WriteBackEnqueuer) {
	qs.batcher = batcher
}

// scanFailKey returns the PebbleDB key suffix for a file's scan-fail counter.
func scanFailKey(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", h[:8])
}

const scanFailThreshold = 3

// QuarantineBook moves a book's file to .failed/{author}/{title}/{filename},
// updates the DB, records path history, sets iTunes purge_pending if linked,
// and publishes a book.quarantined event.
func (qs *QuarantineService) QuarantineBook(bookID, reason string) error {
	if qs.store == nil {
		return fmt.Errorf("store not initialized")
	}

	book, err := qs.store.GetBookByID(bookID)
	if err != nil || book == nil {
		return fmt.Errorf("book not found: %s", bookID)
	}
	if book.QuarantinedAt != nil {
		return nil // already quarantined
	}

	root := qs.cfg.RootDir
	if root == "" {
		return fmt.Errorf("RootDir not configured")
	}

	author := "Unknown Author"
	if book.Author != nil && book.Author.Name != "" {
		author = sanitizeDirName(book.Author.Name)
	}
	title := sanitizeDirName(book.Title)
	if title == "" {
		title = "Unknown"
	}
	filename := filepath.Base(book.FilePath)

	failedRoot := filepath.Clean(filepath.Join(root, ".failed"))
	dest := filepath.Clean(filepath.Join(failedRoot, author, title, filename))
	// Boundary check: dest must stay inside .failed/
	if !strings.HasPrefix(dest, failedRoot+string(filepath.Separator)) {
		return fmt.Errorf("quarantine path %q escapes .failed directory", dest)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("mkdir .failed: %w", err)
	}
	if err := os.Rename(book.FilePath, dest); err != nil {
		return fmt.Errorf("move to .failed: %w", err)
	}

	oldPath := book.FilePath
	now := time.Now()
	book.FilePath = dest
	book.QuarantineReason = &reason
	book.QuarantinedAt = &now

	if book.ITunesPersistentID != nil {
		purge := "purge_pending"
		book.ITunesSyncStatus = &purge
	}

	if _, err := qs.store.UpdateBook(bookID, book); err != nil {
		// Rollback: move file back before returning the error.
		if rollbackErr := os.Rename(dest, oldPath); rollbackErr != nil {
			slog.Error("QuarantineBook DB update failed and file rollback failed", "originalPath", oldPath, "rollbackErr", rollbackErr, "err", err)
		}
		return fmt.Errorf("update book: %w", err)
	}

	_ = qs.store.RecordPathChange(&database.BookPathChange{
		BookID:     bookID,
		OldPath:    oldPath,
		NewPath:    dest,
		ChangeType: "quarantine",
	})

	slog.Info("QuarantineBook moved", "oldPath", oldPath, "dest", dest, "reason", reason)

	qs.events.Publish(context.Background(), plugin.NewEvent(plugin.EventBookQuarantined, bookID, map[string]any{
		"title":          book.Title,
		"author":         author,
		"file_path":      dest,
		"original_path":  oldPath,
		"reason":         reason,
		"quarantined_at": now.Format(time.RFC3339),
	}))

	return nil
}

// UnquarantineBook moves a quarantined book back to its original path
// (retrieved from path history) and clears the quarantine fields.
func (qs *QuarantineService) UnquarantineBook(bookID string) error {
	if qs.store == nil {
		return fmt.Errorf("store not initialized")
	}

	book, err := qs.store.GetBookByID(bookID)
	if err != nil || book == nil {
		return fmt.Errorf("book not found: %s", bookID)
	}
	if book.QuarantinedAt == nil {
		return nil // not quarantined
	}

	history, err := qs.store.GetBookPathHistory(bookID)
	if err != nil {
		return fmt.Errorf("get path history: %w", err)
	}
	// Find the most-recent quarantine entry (history is ordered oldest-first).
	var origPath string
	for _, h := range history {
		if h.ChangeType == "quarantine" {
			origPath = h.OldPath
		}
	}
	if origPath == "" {
		return fmt.Errorf("no quarantine history entry found for book %s", bookID)
	}

	if err := os.MkdirAll(filepath.Dir(origPath), 0755); err != nil {
		return fmt.Errorf("mkdir original path: %w", err)
	}
	if err := os.Rename(book.FilePath, origPath); err != nil {
		return fmt.Errorf("restore from .failed: %w", err)
	}

	quarPath := book.FilePath
	book.FilePath = origPath
	book.QuarantineReason = nil
	book.QuarantinedAt = nil
	if book.ITunesSyncStatus != nil && *book.ITunesSyncStatus == "purge_pending" {
		dirty := "dirty"
		book.ITunesSyncStatus = &dirty
	}

	if _, err := qs.store.UpdateBook(bookID, book); err != nil {
		// Rollback: move file back to quarantine location.
		if rollbackErr := os.Rename(origPath, quarPath); rollbackErr != nil {
			slog.Error("UnquarantineBook DB update failed and file rollback failed", "originalPath", origPath, "rollbackErr", rollbackErr, "err", err)
		}
		return fmt.Errorf("update book: %w", err)
	}

	_ = qs.store.RecordPathChange(&database.BookPathChange{
		BookID:     bookID,
		OldPath:    quarPath,
		NewPath:    origPath,
		ChangeType: "unquarantine",
	})

	slog.Info("UnquarantineBook →", "quarPath", quarPath, "origPath", origPath)

	qs.events.Publish(context.Background(), plugin.NewEvent(plugin.EventBookUnquarantined, bookID, map[string]any{
		"file_path":       origPath,
		"quarantine_path": quarPath,
	}))

	return nil
}

// AutoQuarantineFailedScans checks for books whose scan-fail counter has reached
// the threshold and quarantines them automatically.
func (qs *QuarantineService) AutoQuarantineFailedScans() {
	if qs.store == nil {
		return
	}
	// Paginate to avoid missing books when library exceeds page size.
	const pageSize = 1000
	var offset int
	for {
		page, err := qs.store.GetAllBooks(pageSize, offset)
		if err != nil || len(page) == 0 {
			break
		}
		for _, b := range page {
			if b.QuarantinedAt != nil {
				continue
			}
			n, _ := qs.store.GetScanFailCount(scanFailKey(b.FilePath))
			if n >= scanFailThreshold {
				slog.Info("auto-quarantine (fail count)", "filePath", b.FilePath, "failCount", n)
				_ = qs.QuarantineBook(b.ID, fmt.Sprintf("taglib failed to read file after %d consecutive scan attempts", n))
			}
		}
		if len(page) < pageSize {
			break
		}
		offset += pageSize
	}
}

// ProcessITunesPurgePending finds books with itunes_sync_status = "purge_pending",
// enqueues their PIDs for ITL removal, and clears their iTunes linkage.
// Called at the start of each iTunes sync cycle.
func (qs *QuarantineService) ProcessITunesPurgePending() {
	if qs.store == nil || qs.batcher == nil {
		return
	}
	books, err := qs.store.GetITunesPurgePendingBooks()
	if err != nil || len(books) == 0 {
		return
	}
	for _, b := range books {
		if b.ITunesPersistentID == nil {
			continue
		}
		qs.batcher.EnqueueRemove(*b.ITunesPersistentID)
		slog.Info("ProcessITunesPurgePending queued ITL removal for", "persistentID", *b.ITunesPersistentID, "bookID", b.ID)

		// Clear iTunes linkage so the book is no longer tied to iTunes.
		cleared := "unlinked"
		b.ITunesSyncStatus = &cleared
		b.ITunesPersistentID = nil
		if _, err := qs.store.UpdateBook(b.ID, &b); err != nil {
			slog.Warn("ProcessITunesPurgePending UpdateBook", "bookID", b.ID, "err", err)
		}
	}
}

// sanitizeDirName strips characters unsafe for directory names, including path
// traversal sequences and control characters.
func sanitizeDirName(name string) string {
	// Replace path-separator and shell-special characters.
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "-", "\"", "-", "<", "-", ">", "-", "|", "-",
	)
	name = replacer.Replace(name)

	// Strip control characters (including null bytes).
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)

	// Replace ".." traversal component with "-".
	name = strings.ReplaceAll(name, "..", "-")

	return strings.TrimSpace(name)
}
