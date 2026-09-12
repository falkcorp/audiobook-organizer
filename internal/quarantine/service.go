// file: internal/quarantine/service.go
// version: 1.4.1
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-09-12

package quarantine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
)

// Path-history change types. changeQuarantine / changeUnquarantine record the
// BOOK's path and are what UnquarantineBook reads to find where the book came
// from, so the per-file journal rows must use a distinct type: a per-file row
// sharing "quarantine" would become the "most recent quarantine entry" and
// send the book back to one of its files' paths.
const (
	changeQuarantine       = "quarantine"
	changeUnquarantine     = "unquarantine"
	changeQuarantineFile   = "quarantine_file"
	changeUnquarantineFile = "unquarantine_file"
)

// BookRows reads and writes the book rows quarantine moves.
type BookRows interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetITunesPurgePendingBooks() ([]database.Book, error)
}

// FileRows reads and repoints a book's book_file rows as its files move.
type FileRows interface {
	// GetBookFiles returns the FULL book_file records. Quarantine repoints each
	// row with UpdateBookFile, which replaces the whole record, so a slim
	// projection here would blank every field it omits (fingerprints included).
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// PathHistory journals path changes so unquarantine can reverse them.
type PathHistory interface {
	RecordPathChange(change *database.BookPathChange) error
	GetBookPathHistory(bookID string) ([]database.BookPathChange, error)
}

// ScanFailCounts reads the per-file scan-fail counter behind auto-quarantine.
type ScanFailCounts interface {
	GetScanFailCount(pathHash string) (int, error)
}

// Store is the narrow database interface required by QuarantineService.
//
// Split into the 4 interfaces above on 2026-09-12. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type Store interface {
	BookRows
	FileRows
	PathHistory
	ScanFailCounts
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

// QuarantineBook moves a book out of the library into
// .failed/{author}/{title}/, repoints every one of the book's book_file rows
// at the file's new location in the same pass, updates the book row, records
// path history (the book's path, plus one quarantine_file row per moved file
// so UnquarantineBook can put each file back), sets iTunes purge_pending if
// linked, and publishes a book.quarantined event.
//
// Until 2026-09-12 only the book row moved: every book_file row kept pointing
// at the old path, so a quarantined book's files read as MISSING while the
// audio sat under .failed/, where the recover-missing-files walk (which prunes
// dot-dirs) can never find it again.
//
// A partial failure leaves rows matching the disk: only files that actually
// moved are repointed, everything else is named in the returned error, and the
// book is NOT marked quarantined, so calling QuarantineBook again finishes the
// job (files already under .failed/ are recognised and skipped).
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

	author := authorname.Placeholder
	if book.Author != nil && book.Author.Name != "" {
		author = sanitizeDirName(book.Author.Name)
	}
	title := sanitizeDirName(book.Title)
	if title == "" {
		title = "Unknown"
	}

	failedRoot := filepath.Clean(filepath.Join(root, ".failed"))
	destDir := filepath.Clean(filepath.Join(failedRoot, author, title))
	dest := filepath.Clean(filepath.Join(destDir, filepath.Base(book.FilePath)))
	// Boundary check: dest must stay inside .failed/
	if !isUnder(failedRoot, dest) || !isUnder(failedRoot, destDir) {
		return fmt.Errorf("quarantine path %q escapes .failed directory", dest)
	}

	// A file-shaped book's other files keep their layout relative to the
	// book file's directory, so disc1/01.mp3 and disc2/01.mp3 cannot collide.
	srcBase := filepath.Dir(book.FilePath)
	fileTo := func(p string) (string, bool) {
		to := filepath.Join(destDir, filepath.Base(p))
		if rel, ok := relUnder(srcBase, p); ok {
			to = filepath.Join(destDir, rel)
		}
		to = filepath.Clean(to)
		return to, isUnder(failedRoot, to)
	}

	oldPath := book.FilePath
	rl, err := qs.relocate(book, dest, fileTo, changeQuarantineFile, failedRoot)
	if err != nil {
		return err
	}
	complete := rl.bookAtDest && len(rl.failures) == 0

	if !rl.bookMoved && !complete {
		return rl.err(bookID, "quarantine")
	}

	now := time.Now()
	book.FilePath = dest
	if complete {
		book.QuarantineReason = &reason
		book.QuarantinedAt = &now
		if book.ITunesPersistentID != nil {
			purge := "purge_pending"
			book.ITunesSyncStatus = &purge
		}
	}

	if _, err := qs.store.UpdateBook(bookID, book); err != nil {
		qs.rollback(rl, "QuarantineBook")
		return fmt.Errorf("update book: %w", err)
	}

	if rl.bookMoved {
		_ = qs.store.RecordPathChange(&database.BookPathChange{
			BookID:     bookID,
			OldPath:    oldPath,
			NewPath:    dest,
			ChangeType: changeQuarantine,
		})
	}

	if !complete {
		return rl.err(bookID, "quarantine")
	}

	slog.Info("QuarantineBook moved",
		"bookID", logger.SanitizeLogValue(bookID),
		"oldPath", logger.SanitizeLogValue(oldPath),
		"dest", logger.SanitizeLogValue(dest),
		"filesRepointed", rl.repointed,
		"reason", logger.SanitizeLogValue(reason))

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
// (retrieved from path history), repoints its book_file rows back with it,
// and clears the quarantine fields.
//
// Each file goes back to the path its quarantine_file journal row recorded. A
// book quarantined before that journal existed had only its book path moved
// (its other rows were never repointed), so a row with no journal entry that
// is not under .failed/ is left alone, and one that IS under .failed/ is
// reported rather than guessed at.
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
	// Find the most-recent quarantine entry (history is ordered oldest-first),
	// and the most recent original path of every journaled file.
	var origPath string
	fileOrig := map[string]string{}
	for _, h := range history {
		switch h.ChangeType {
		case changeQuarantine:
			origPath = h.OldPath
		case changeQuarantineFile:
			fileOrig[h.NewPath] = h.OldPath
		}
	}
	if origPath == "" {
		return fmt.Errorf("no quarantine history entry found for book %s", bookID)
	}

	failedRoot := ""
	if qs.cfg != nil && qs.cfg.RootDir != "" {
		failedRoot = filepath.Clean(filepath.Join(qs.cfg.RootDir, ".failed"))
	}
	fileTo := func(p string) (string, bool) {
		to, ok := fileOrig[p]
		return to, ok
	}

	quarPath := book.FilePath
	rl, err := qs.relocate(book, origPath, fileTo, changeUnquarantineFile, failedRoot)
	if err != nil {
		return err
	}
	complete := rl.bookAtDest && len(rl.failures) == 0

	if !rl.bookMoved && !complete {
		return rl.err(bookID, "unquarantine")
	}

	book.FilePath = origPath
	if complete {
		book.QuarantineReason = nil
		book.QuarantinedAt = nil
		if book.ITunesSyncStatus != nil && *book.ITunesSyncStatus == "purge_pending" {
			dirty := "dirty"
			book.ITunesSyncStatus = &dirty
		}
	}

	if _, err := qs.store.UpdateBook(bookID, book); err != nil {
		qs.rollback(rl, "UnquarantineBook")
		return fmt.Errorf("update book: %w", err)
	}

	if rl.bookMoved {
		_ = qs.store.RecordPathChange(&database.BookPathChange{
			BookID:     bookID,
			OldPath:    quarPath,
			NewPath:    origPath,
			ChangeType: changeUnquarantine,
		})
	}

	if !complete {
		return rl.err(bookID, "unquarantine")
	}

	slog.Info("UnquarantineBook restored",
		"bookID", logger.SanitizeLogValue(bookID),
		"quarPath", logger.SanitizeLogValue(quarPath),
		"origPath", logger.SanitizeLogValue(origPath),
		"filesRepointed", rl.repointed)

	qs.events.Publish(context.Background(), plugin.NewEvent(plugin.EventBookUnquarantined, bookID, map[string]any{
		"file_path":       origPath,
		"quarantine_path": quarPath,
	}))

	return nil
}

// diskMove is one rename relocate performed, kept so rollback can reverse it.
type diskMove struct{ from, to string }

// relocation is the outcome of one relocate pass.
type relocation struct {
	// bookMoved: book.FilePath itself was renamed to its destination in THIS
	// pass. bookAtDest: it is at its destination now (moved now, or already
	// there from an earlier partial run).
	bookMoved  bool
	bookAtDest bool
	moves      []diskMove
	// priors are full-record snapshots of every row repointed in this pass.
	priors    []database.BookFile
	repointed int
	failures  []string
}

func (r *relocation) err(bookID, verb string) error {
	return fmt.Errorf("%s book %s incomplete: %d file(s) not moved: %s",
		verb, bookID, len(r.failures), strings.Join(r.failures, "; "))
}

// relocate moves a book's files to their destinations and repoints each
// book_file row in the same pass. It never deletes a row and never repoints a
// row whose file did not move: after it returns, every row it touched names a
// file that exists, and every row it could not move is still named in
// failures with its original path intact.
//
// bookTo is where book.FilePath goes. When book.FilePath is a directory it is
// renamed in one step (so cover art and sidecars travel with it) and each row
// under it is translated by prefix. When it is a file, each row's file is
// moved on its own: the row equal to book.FilePath goes to bookTo, every other
// row goes where fileTo says. fileTo returning ok=false means "no destination
// known": the row is reported if it sits under failedRoot, else left alone.
//
// An error return means nothing was moved.
func (qs *QuarantineService) relocate(book *database.Book, bookTo string, fileTo func(string) (string, bool), fileChangeType, failedRoot string) (*relocation, error) {
	rows, err := qs.store.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("load book files: %w", err)
	}
	rl := &relocation{}
	from := book.FilePath

	if from == bookTo {
		rl.bookAtDest = true
	}
	info, statErr := os.Lstat(from)

	if statErr == nil && info.IsDir() {
		if !rl.bookAtDest {
			if err := moveNoClobber(from, bookTo); err != nil {
				return nil, err
			}
			rl.moves = append(rl.moves, diskMove{from: from, to: bookTo})
			rl.bookMoved, rl.bookAtDest = true, true
		}
		for i := range rows {
			row := rows[i]
			rel, ok := relUnder(from, row.FilePath)
			if !ok {
				rl.fail(row.FilePath, "outside the book directory, not moved")
				continue
			}
			newPath := filepath.Join(bookTo, rel)
			if newPath == row.FilePath {
				continue // already there (the directory did not need to move)
			}
			if _, err := os.Lstat(newPath); err != nil {
				// The row was already missing before the move; repointing it
				// would only move where it is missing from.
				rl.fail(row.FilePath, "not on disk")
				continue
			}
			if err := qs.repointRow(rl, row, newPath, ""); err != nil {
				rl.fail(row.FilePath, err.Error())
			}
		}
		return rl, nil
	}

	bookRowSeen := false
	for i := range rows {
		row := rows[i]
		var to string
		if row.FilePath == from {
			bookRowSeen = true
			to = bookTo
		} else {
			var ok bool
			to, ok = fileTo(row.FilePath)
			if !ok {
				if failedRoot != "" && isUnder(failedRoot, row.FilePath) {
					rl.fail(row.FilePath, "no recorded destination")
				}
				continue
			}
		}
		if row.FilePath == to {
			continue // already there from an earlier partial run
		}
		if err := moveNoClobber(row.FilePath, to); err != nil {
			rl.fail(row.FilePath, err.Error())
			continue
		}
		rl.moves = append(rl.moves, diskMove{from: row.FilePath, to: to})
		if row.FilePath == from {
			rl.bookMoved, rl.bookAtDest = true, true
		}
		if err := qs.repointRow(rl, row, to, fileChangeType); err != nil {
			// The file moved but its row could not follow. Put the file back so
			// the row is right again; if even that fails, say so loudly.
			if backErr := os.Rename(to, row.FilePath); backErr != nil {
				slog.Error("quarantine: row repoint failed and file could not be moved back",
					"fileID", logger.SanitizeLogValue(row.ID),
					"path", logger.SanitizeLogValue(row.FilePath),
					"movedTo", logger.SanitizeLogValue(to),
					"err", err, "moveBackErr", backErr)
			} else {
				rl.moves = rl.moves[:len(rl.moves)-1]
				if row.FilePath == from {
					rl.bookMoved, rl.bookAtDest = false, false
				}
			}
			rl.fail(row.FilePath, "repoint row: "+err.Error())
		}
	}

	// A book whose own path has no book_file row (legacy single-file books)
	// still has to move.
	if !bookRowSeen && !rl.bookAtDest {
		if err := moveNoClobber(from, bookTo); err != nil {
			rl.fail(from, err.Error())
		} else {
			rl.moves = append(rl.moves, diskMove{from: from, to: bookTo})
			rl.bookMoved, rl.bookAtDest = true, true
		}
	}
	return rl, nil
}

// repointRow rewrites one row's FilePath. row is the FULL record from
// GetBookFiles, because UpdateBookFile replaces the whole record. When
// changeType is set the move is journaled as a path-history row.
func (qs *QuarantineService) repointRow(rl *relocation, row database.BookFile, newPath, changeType string) error {
	updated := row
	updated.FilePath = newPath
	if err := qs.store.UpdateBookFile(row.ID, &updated); err != nil {
		return err
	}
	rl.priors = append(rl.priors, row)
	rl.repointed++
	if changeType != "" {
		if err := qs.store.RecordPathChange(&database.BookPathChange{
			BookID:     row.BookID,
			OldPath:    row.FilePath,
			NewPath:    newPath,
			ChangeType: changeType,
		}); err != nil {
			slog.Warn("quarantine: could not journal file move; unquarantine will not find this file",
				"fileID", logger.SanitizeLogValue(row.ID),
				"path", logger.SanitizeLogValue(newPath), "err", err)
		}
	}
	return nil
}

// rollback reverses a relocate pass after the book-row update failed. Each
// rename is undone newest-first; afterwards a row snapshot is restored only
// if its original file is back on disk, so rows keep matching the disk even
// when a rename back fails.
func (qs *QuarantineService) rollback(rl *relocation, op string) {
	for i := len(rl.moves) - 1; i >= 0; i-- {
		m := rl.moves[i]
		if err := os.Rename(m.to, m.from); err != nil {
			slog.Error(op+" DB update failed and file rollback failed",
				"from", logger.SanitizeLogValue(m.to),
				"to", logger.SanitizeLogValue(m.from), "err", err)
		}
	}
	for i := range rl.priors {
		prior := rl.priors[i]
		if _, err := os.Lstat(prior.FilePath); err != nil {
			continue // its file did not come back; the repointed row is the truth
		}
		if err := qs.store.UpdateBookFile(prior.ID, &prior); err != nil {
			slog.Error(op+" rollback could not restore book_file row",
				"fileID", logger.SanitizeLogValue(prior.ID),
				"path", logger.SanitizeLogValue(prior.FilePath), "err", err)
		}
	}
}

func (r *relocation) fail(path, why string) {
	r.failures = append(r.failures, fmt.Sprintf("%s: %s", logger.SanitizeLogValue(path), why))
}

// moveNoClobber renames from to to, creating to's parent, and refuses to
// overwrite: os.Rename silently replaces an existing file, and two books (or
// two files of one book) landing on the same name must not destroy audio.
func moveNoClobber(from, to string) error {
	if _, err := os.Lstat(from); err != nil {
		return fmt.Errorf("source not on disk: %w", err)
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("destination %s already exists", to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect destination %s: %w", to, err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(to), err)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("move: %w", err)
	}
	return nil
}

// isUnder reports whether p is strictly inside dir.
func isUnder(dir, p string) bool {
	rel, ok := relUnder(dir, p)
	return ok && rel != ""
}

// relUnder returns p relative to dir when p is dir or inside it.
func relUnder(dir, p string) (string, bool) {
	dir, p = filepath.Clean(dir), filepath.Clean(p)
	if p == dir {
		return "", true
	}
	if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
		return "", false
	}
	return p[len(dir)+1:], true
}

// AutoQuarantineFailedScans checks for books whose scan-fail counter has reached
// the threshold and quarantines them automatically.
func (qs *QuarantineService) AutoQuarantineFailedScans() {
	if qs.store == nil {
		return
	}
	// One limit-0 call = one consistent snapshot. Offset pages over the async
	// memdb can skip or repeat rows whenever the snapshot swaps between calls
	// (see reconcile #2443) — and this loop MUTATES via QuarantineBook as it
	// walks, so paging was swapping the snapshot under its own feet.
	books, err := qs.store.GetAllBooksCore(0, 0)
	if err != nil {
		return
	}
	for _, b := range books {
		if b.QuarantinedAt != nil {
			continue
		}
		n, _ := qs.store.GetScanFailCount(scanFailKey(b.FilePath))
		if n >= scanFailThreshold {
			slog.Info("auto-quarantine (fail count)", "filePath", b.FilePath, "failCount", n)
			_ = qs.QuarantineBook(b.ID, fmt.Sprintf("taglib failed to read file after %d consecutive scan attempts", n))
		}
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
