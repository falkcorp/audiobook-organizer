// file: internal/quarantine/service.go
// version: 1.6.0
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
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
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
// .failed/{author}/{title} [{bookID}]/, repoints every one of the book's
// book_file rows at the file's new location in the same pass, updates the
// book row, sets iTunes purge_pending if linked, and publishes a
// book.quarantined event.
//
// Until 2026-09-12 only the book row moved: every book_file row kept pointing
// at the old path, so a quarantined book's files read as MISSING while the
// audio sat under .failed/, where the recover-missing-files walk (which prunes
// dot-dirs) can never find it again.
//
// History is written BEFORE the move it describes (journal-first): the book's
// "quarantine" entry when a pass starts, and one "quarantine_file" entry
// immediately before each file's rename. A history write that fails stops
// that move before it happens, so no file can sit under .failed/ without a
// record of where it came from. The folder carries the book ID so two books
// with the same author and title never share one.
//
// A file whose destination is taken keeps its row, is named in the returned
// error, and leaves the book NOT marked quarantined. Calling QuarantineBook
// again resumes the same cycle: same source layout, same destination (even if
// the title changed), and a file an interrupted pass already moved is accepted
// only when its history entry and its size (and hash, when known) confirm it
// is this row's file. A row whose file was missing before the pass is logged
// and left alone.
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
	// The snapshot every "already moved" confirmation is checked against. It
	// is read before this pass writes anything, so a pass can never confirm a
	// move with an entry it wrote itself.
	history, err := qs.store.GetBookPathHistory(bookID)
	if err != nil {
		return fmt.Errorf("get path history: %w", err)
	}

	var from, dest string
	bookJournaled := false
	if last, ok := openQuarantineCycle(history, book.FilePath); ok {
		// An earlier pass started this cycle and did not finish. Resume it
		// exactly: its source layout and its destination, whatever the title
		// says now, so one book never ends up split across two folders.
		from, dest = last.OldPath, last.NewPath
		bookJournaled = true
		if !isUnder(failedRoot, dest) {
			return fmt.Errorf("recorded quarantine path %q is outside .failed", dest)
		}
	} else {
		if isUnder(failedRoot, book.FilePath) {
			return fmt.Errorf("book %s is under .failed but no open quarantine history entry records where it came from", bookID)
		}
		from = book.FilePath
		if _, err := os.Lstat(from); err != nil {
			// Nothing of this book is known to have moved; a destination that
			// happens to exist cannot be taken as this book's.
			return fmt.Errorf("move to .failed: %w", err)
		}
		destDir := filepath.Clean(filepath.Join(failedRoot, author, quarantineDirName(title, bookID)))
		dest = filepath.Clean(filepath.Join(destDir, filepath.Base(from)))
		// Boundary check: dest must stay inside .failed/
		if !isUnder(failedRoot, dest) || !isUnder(failedRoot, destDir) {
			return fmt.Errorf("quarantine path %q escapes .failed directory", dest)
		}
		if err := qs.store.RecordPathChange(&database.BookPathChange{
			BookID: bookID, OldPath: from, NewPath: dest, ChangeType: changeQuarantine,
		}); err != nil {
			return fmt.Errorf("journal quarantine (nothing moved): %w", err)
		}
		bookJournaled = true
	}

	// A file-shaped book's other files keep their layout relative to the book
	// file's ORIGINAL directory, so disc1/01.mp3 and disc2/01.mp3 cannot
	// collide -- on a resumed pass too, which is why srcBase comes from `from`
	// and never from the book's current path.
	destDir := filepath.Dir(dest)
	srcBase := filepath.Dir(from)
	fileTo := func(p string) (string, bool, string) {
		if isUnder(failedRoot, p) {
			return "", false, "" // already quarantined by an earlier pass
		}
		to := filepath.Join(destDir, filepath.Base(p))
		if rel, ok := relUnder(srcBase, p); ok {
			to = filepath.Join(destDir, rel)
		}
		to = filepath.Clean(to)
		if !isUnder(failedRoot, to) {
			return "", false, "destination escapes .failed"
		}
		return to, true, ""
	}

	rl, err := qs.relocate(relocateSpec{
		bookID: bookID, from: from, to: dest, fileTo: fileTo,
		fileChangeType: changeQuarantineFile, bookChangeType: changeQuarantine,
		bookJournaled: bookJournaled, history: history,
	})
	if err != nil {
		return err
	}
	complete := rl.bookAtDest && len(rl.blocked) == 0
	bookChanged := rl.bookAtDest && book.FilePath != dest

	now := time.Now()
	if bookChanged || complete {
		if rl.bookAtDest {
			book.FilePath = dest
		}
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
	}
	qs.logNotes(rl, "QuarantineBook", bookID)

	if !complete {
		return rl.err(bookID, "quarantine")
	}

	slog.Info("QuarantineBook moved",
		"bookID", logger.SanitizeLogValue(bookID),
		"oldPath", logger.SanitizeLogValue(from),
		"dest", logger.SanitizeLogValue(dest),
		"filesRepointed", rl.repointed,
		"reason", logger.SanitizeLogValue(reason))

	qs.events.Publish(context.Background(), plugin.NewEvent(plugin.EventBookQuarantined, bookID, map[string]any{
		"title":          book.Title,
		"author":         author,
		"file_path":      dest,
		"original_path":  from,
		"reason":         reason,
		"quarantined_at": now.Format(time.RFC3339),
	}))

	return nil
}

// quarantineDirName names a book's folder under .failed/{author}/. The book ID
// is part of it: author+title alone is shared by duplicate versions of a book,
// and two books in one folder let one book's "already moved" file be taken for
// another's.
func quarantineDirName(title, bookID string) string {
	return sanitizeDirName(title + " [" + bookID + "]")
}

// openQuarantineCycle returns the newest "quarantine" entry when it opens a
// cycle this book is still in: no "unquarantine" entry after it, and the book
// path is still that entry's source or destination (a book organized
// elsewhere since then starts a new cycle).
func openQuarantineCycle(history []database.BookPathChange, bookPath string) (database.BookPathChange, bool) {
	lastQ, ok := latestChange(history, changeQuarantine)
	if !ok {
		return lastQ, false
	}
	if lastU, ok := latestChange(history, changeUnquarantine); ok && !newer(lastQ, lastU) {
		return lastQ, false
	}
	if bookPath != lastQ.OldPath && bookPath != lastQ.NewPath {
		return lastQ, false
	}
	return lastQ, true
}

// UnquarantineBook moves a quarantined book back to its original path
// (retrieved from path history), repoints its book_file rows back with it,
// and clears the quarantine fields.
//
// Each file goes back to the path its NEWEST quarantine_file entry recorded.
// A row with no entry that sits in the book's own quarantine folder (books
// quarantined before the per-file journal existed, or rows repointed there by
// a repair) is mapped back by its position under that folder; the move
// happens only if that destination is free. Any other row under .failed/ with
// no recorded origin blocks the pass, so the book stays quarantined rather
// than being cleared with files left behind.
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
	// Chosen by timestamp, never by position: PebbleStore returns history
	// newest-first, and a loop that kept the last match restored a book that
	// had been quarantined twice to where it lived before the FIRST time.
	last, ok := latestChange(history, changeQuarantine)
	if !ok {
		return fmt.Errorf("no quarantine history entry found for book %s", bookID)
	}
	from, origPath := last.NewPath, last.OldPath
	fileOrig := latestFileChanges(history, changeQuarantineFile)

	failedRoot := ""
	if qs.cfg != nil && qs.cfg.RootDir != "" {
		failedRoot = filepath.Clean(filepath.Join(qs.cfg.RootDir, ".failed"))
	}
	quarDir, origDir := filepath.Dir(from), filepath.Dir(origPath)
	fileTo := func(p string) (string, bool, string) {
		if e, ok := fileOrig[p]; ok {
			return e.OldPath, true, ""
		}
		if rel, ok := relUnder(quarDir, p); ok && rel != "" {
			return filepath.Join(origDir, rel), true,
				"no quarantine_file entry; mapped back by its place in the book's quarantine folder"
		}
		if failedRoot != "" && isUnder(failedRoot, p) {
			return "", false, "under .failed with no recorded original path"
		}
		return "", false, "" // never moved, or already back
	}

	oldPath := book.FilePath
	rl, err := qs.relocate(relocateSpec{
		bookID: bookID, from: from, to: origPath, fileTo: fileTo,
		fileChangeType: changeUnquarantineFile, bookChangeType: changeUnquarantine,
		history: history,
	})
	if err != nil {
		return err
	}
	complete := rl.bookAtDest && len(rl.blocked) == 0
	bookChanged := rl.bookAtDest && oldPath != origPath

	if bookChanged || complete {
		if rl.bookAtDest {
			book.FilePath = origPath
		}
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
	}
	qs.logNotes(rl, "UnquarantineBook", bookID)

	if !complete {
		return rl.err(bookID, "unquarantine")
	}

	slog.Info("UnquarantineBook restored",
		"bookID", logger.SanitizeLogValue(bookID),
		"quarPath", logger.SanitizeLogValue(from),
		"origPath", logger.SanitizeLogValue(origPath),
		"filesRepointed", rl.repointed)

	qs.events.Publish(context.Background(), plugin.NewEvent(plugin.EventBookUnquarantined, bookID, map[string]any{
		"file_path":       origPath,
		"quarantine_path": from,
	}))

	return nil
}

// latestChange returns the newest history entry of changeType, by CreatedAt
// (ID, the write's nanosecond stamp, breaks ties) -- independent of the order
// the store returns rows in.
func latestChange(history []database.BookPathChange, changeType string) (database.BookPathChange, bool) {
	var best database.BookPathChange
	found := false
	for _, h := range history {
		if h.ChangeType != changeType {
			continue
		}
		if !found || newer(h, best) {
			best, found = h, true
		}
	}
	return best, found
}

// latestFileChanges maps each journaled NewPath to its newest entry, so a path
// reused by a later quarantine cycle resolves to that cycle's origin.
func latestFileChanges(history []database.BookPathChange, changeType string) map[string]database.BookPathChange {
	out := map[string]database.BookPathChange{}
	for _, h := range history {
		if h.ChangeType != changeType {
			continue
		}
		if cur, ok := out[h.NewPath]; !ok || newer(h, cur) {
			out[h.NewPath] = h
		}
	}
	return out
}

func newer(a, b database.BookPathChange) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// hasEntry reports whether history records a changeType move of exactly
// from -> to.
func hasEntry(history []database.BookPathChange, changeType, from, to string) bool {
	for _, h := range history {
		if h.ChangeType == changeType && h.OldPath == from && h.NewPath == to {
			return true
		}
	}
	return false
}

// sameFile reports whether the file at p is plausibly the file row describes:
// its size matches the row's recorded size, and its hash matches the row's
// FileHash when one is recorded (filehash is the one algorithm that column is
// written with). A row that records neither is judged by its history entry
// alone.
func sameFile(row database.BookFile, p string) bool {
	info, err := os.Lstat(p)
	if err != nil || info.IsDir() {
		return false
	}
	if row.FileSize > 0 && info.Size() != row.FileSize {
		return false
	}
	if row.FileHash != "" {
		h, err := filehash.BookFileHash(p)
		if err != nil || h != row.FileHash {
			return false
		}
	}
	return true
}

// diskMove is one rename relocate performed, kept so rollback can reverse it.
type diskMove struct{ from, to string }

// relocation is the outcome of one relocate pass.
type relocation struct {
	// bookAtDest: the book path is at its destination on disk now -- moved in
	// this pass, or confirmed moved by an earlier, interrupted one.
	bookAtDest bool
	moves      []diskMove
	// priors are full-record snapshots of every row repointed in this pass.
	priors    []database.BookFile
	repointed int
	// blocked names files that should have moved and could not (destination
	// taken, history write failed, rename failed). Only these keep a pass
	// incomplete.
	blocked []string
	// notes names rows left alone for a reason a retry cannot change (missing
	// before the pass, outside a directory-shaped book) and rows restored by
	// the positional fallback. Logged, never counted as failures.
	notes []string
}

func (r *relocation) err(bookID, verb string) error {
	return fmt.Errorf("%s book %s incomplete: %d file(s) blocked: %s",
		verb, bookID, len(r.blocked), strings.Join(r.blocked, "; "))
}

func (r *relocation) block(path, why string) {
	r.blocked = append(r.blocked, fmt.Sprintf("%s: %s", logger.SanitizeLogValue(path), why))
}

func (r *relocation) note(path, why string) {
	r.notes = append(r.notes, fmt.Sprintf("%s: %s", logger.SanitizeLogValue(path), why))
}

// relocateSpec says what one relocate pass moves. from/to are the book path's
// source and destination; they are passed explicitly (not read off the book
// row) so a resumed pass works from where the book came from, not from where
// a half-finished pass left it.
type relocateSpec struct {
	bookID   string
	from, to string
	// fileTo maps a non-book row to its destination. ok=false with a problem
	// blocks the row; ok=true with a problem is a note (the move proceeds).
	fileTo         func(string) (to string, ok bool, problem string)
	fileChangeType string
	bookChangeType string
	// bookJournaled: the bookChangeType entry for from->to is already written.
	bookJournaled bool
	// history is the snapshot read BEFORE this pass wrote anything.
	history []database.BookPathChange
}

// pathState classifies a source/destination pair by what is on disk.
type pathState int

const (
	stMove    pathState = iota // source present, destination free
	stAtDest                   // source gone, destination present
	stBlocked                  // both present: the destination is taken
	stMissing                  // neither present: missing before this pass
)

func classify(from, to string) (pathState, bool, error) {
	fi, ferr := os.Lstat(from)
	ti, terr := os.Lstat(to)
	for _, e := range []error{ferr, terr} {
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return 0, false, e
		}
	}
	switch {
	case from == to && terr == nil:
		return stAtDest, ti.IsDir(), nil
	case ferr == nil && terr != nil:
		return stMove, fi.IsDir(), nil
	case ferr != nil && terr == nil:
		return stAtDest, ti.IsDir(), nil
	case ferr == nil && terr == nil:
		return stBlocked, fi.IsDir(), nil
	}
	return stMissing, false, nil
}

// relocate moves a book's files to their destinations and repoints each
// book_file row in the same pass. It never deletes a row, never repoints a
// row to a path that does not exist, and writes each history entry BEFORE the
// rename it describes.
//
// "Source gone, destination present" is the state an interrupted pass leaves,
// but it is also what an unrelated file sitting at the destination looks like.
// It is accepted as this book's move only when the pre-pass history records
// exactly that move and (for files) the destination is this row's file by
// size and hash; otherwise the row is blocked and nothing is touched.
//
// When the book path is a directory it is renamed in one step (so cover art
// and sidecars travel with it) and each row under it is translated by prefix;
// the book-level entry covers every row. When it is a file, each row's file is
// handled on its own: the row at spec.from goes to spec.to, every other row
// goes where fileTo says.
//
// An error return means nothing was moved.
func (qs *QuarantineService) relocate(sp relocateSpec) (*relocation, error) {
	rows, err := qs.store.GetBookFiles(sp.bookID)
	if err != nil {
		return nil, fmt.Errorf("load book files: %w", err)
	}
	bookState, isDir, err := classify(sp.from, sp.to)
	if err != nil {
		return nil, fmt.Errorf("inspect book path: %w", err)
	}
	if bookState == stMissing {
		return nil, fmt.Errorf("book path %s is not on disk (nor at %s)", sp.from, sp.to)
	}
	if bookState == stAtDest && sp.from != sp.to && !hasEntry(sp.history, sp.bookChangeType, sp.from, sp.to) {
		return nil, fmt.Errorf("book path %s is not on disk, and nothing records that %s (which exists) is where this book was moved",
			sp.from, sp.to)
	}
	rl := &relocation{}

	journalBook := func() error {
		if sp.bookJournaled || sp.bookChangeType == "" {
			return nil
		}
		if err := qs.store.RecordPathChange(&database.BookPathChange{
			BookID: sp.bookID, OldPath: sp.from, NewPath: sp.to, ChangeType: sp.bookChangeType,
		}); err != nil {
			return fmt.Errorf("journal %s (not moved): %w", sp.bookChangeType, err)
		}
		sp.bookJournaled = true
		return nil
	}

	if isDir {
		switch bookState {
		case stBlocked:
			rl.block(sp.from, fmt.Sprintf("destination %s already exists", sp.to))
			return rl, nil
		case stMove:
			if err := journalBook(); err != nil {
				rl.block(sp.from, err.Error())
				return rl, nil
			}
			if err := moveNoClobber(sp.from, sp.to); err != nil {
				rl.block(sp.from, err.Error())
				return rl, nil
			}
			rl.moves = append(rl.moves, diskMove{from: sp.from, to: sp.to})
		}
		rl.bookAtDest = true
		for i := range rows {
			row := rows[i]
			if _, done := relUnder(sp.to, row.FilePath); done && sp.from != sp.to {
				continue // already repointed
			}
			rel, ok := relUnder(sp.from, row.FilePath)
			if !ok {
				rl.note(row.FilePath, "outside the book directory, not moved")
				continue
			}
			newPath := filepath.Join(sp.to, rel)
			if newPath == row.FilePath {
				continue
			}
			if _, err := os.Lstat(newPath); err != nil {
				// Missing before the move; repointing it would only change
				// where it is missing from.
				rl.note(row.FilePath, "missing before this pass, left in place")
				continue
			}
			if !sameFile(row, newPath) {
				rl.block(row.FilePath, fmt.Sprintf("%s does not match this row's size or hash", newPath))
				continue
			}
			if err := qs.repointRow(rl, row, newPath); err != nil {
				rl.block(row.FilePath, "repoint row: "+err.Error())
			}
		}
		return rl, nil
	}

	bookRowSeen := false
	for i := range rows {
		row := rows[i]
		isBookRow := false
		var to string
		switch {
		case row.FilePath == sp.from:
			bookRowSeen, isBookRow = true, true
			to = sp.to
		case row.FilePath == sp.to:
			bookRowSeen = true
			continue // already repointed
		default:
			var ok bool
			var problem string
			to, ok, problem = sp.fileTo(row.FilePath)
			if !ok {
				if problem != "" {
					rl.block(row.FilePath, problem)
				}
				continue
			}
			if problem != "" {
				rl.note(row.FilePath, problem)
			}
		}
		if row.FilePath == to {
			continue
		}
		st, _, err := classify(row.FilePath, to)
		if err != nil {
			rl.block(row.FilePath, err.Error())
			continue
		}
		switch st {
		case stMissing:
			rl.note(row.FilePath, "missing before this pass, left in place")
			continue
		case stBlocked:
			rl.block(row.FilePath, fmt.Sprintf("destination %s already exists", to))
			continue
		case stAtDest:
			// Only an interrupted pass of THIS book, confirmed by its own
			// history entry and by the file itself, may be adopted.
			if !hasEntry(sp.history, sp.fileChangeType, row.FilePath, to) || !sameFile(row, to) {
				rl.block(row.FilePath, fmt.Sprintf("destination %s is taken by a different file", to))
				continue
			}
		case stMove:
			if isBookRow {
				if err := journalBook(); err != nil {
					rl.block(row.FilePath, err.Error())
					continue
				}
			}
			if err := qs.store.RecordPathChange(&database.BookPathChange{
				BookID: row.BookID, OldPath: row.FilePath, NewPath: to, ChangeType: sp.fileChangeType,
			}); err != nil {
				rl.block(row.FilePath, fmt.Sprintf("journal %s (not moved): %v", sp.fileChangeType, err))
				continue
			}
			if err := moveNoClobber(row.FilePath, to); err != nil {
				rl.block(row.FilePath, err.Error())
				continue
			}
			rl.moves = append(rl.moves, diskMove{from: row.FilePath, to: to})
		}
		if err := qs.repointRow(rl, row, to); err != nil {
			// The row could not follow its file. Put the file back if this
			// pass moved it, so the row is right again.
			if st == stMove {
				if backErr := os.Rename(to, row.FilePath); backErr != nil {
					slog.Error("quarantine: row repoint failed and file could not be moved back",
						"fileID", logger.SanitizeLogValue(row.ID),
						"path", logger.SanitizeLogValue(row.FilePath),
						"movedTo", logger.SanitizeLogValue(to),
						"err", err, "moveBackErr", backErr)
				} else {
					rl.moves = rl.moves[:len(rl.moves)-1]
				}
			}
			rl.block(row.FilePath, "repoint row: "+err.Error())
		}
	}

	// A book whose own path has no book_file row (legacy single-file books)
	// still has to move.
	if !bookRowSeen {
		switch bookState {
		case stMove:
			if err := journalBook(); err != nil {
				rl.block(sp.from, err.Error())
			} else if err := moveNoClobber(sp.from, sp.to); err != nil {
				rl.block(sp.from, err.Error())
			} else {
				rl.moves = append(rl.moves, diskMove{from: sp.from, to: sp.to})
			}
		case stBlocked:
			rl.block(sp.from, fmt.Sprintf("destination %s already exists", sp.to))
		}
	}

	// Decided from the disk, not from bookkeeping: the book path is at its
	// destination when the destination exists and the source does not (the
	// history check at the top already refused an unconfirmed one).
	if st, _, err := classify(sp.from, sp.to); err == nil && st == stAtDest {
		rl.bookAtDest = true
	}
	return rl, nil
}

// repointRow rewrites one row's FilePath. row is the FULL record from
// GetBookFiles, because UpdateBookFile replaces the whole record. The history
// entry for the move was written before the move.
func (qs *QuarantineService) repointRow(rl *relocation, row database.BookFile, newPath string) error {
	updated := row
	updated.FilePath = newPath
	if err := qs.store.UpdateBookFile(row.ID, &updated); err != nil {
		return err
	}
	rl.priors = append(rl.priors, row)
	rl.repointed++
	return nil
}

func (qs *QuarantineService) logNotes(rl *relocation, op, bookID string) {
	for _, n := range rl.notes {
		slog.Warn(op+": row note", "bookID", logger.SanitizeLogValue(bookID), "detail", n)
	}
}

// rollback reverses a relocate pass after the book-row update failed. Each
// rename is undone newest-first; afterwards a row snapshot is restored only
// if its original file is back on disk, so a row never names a path its file
// is not at. History entries the pass wrote stay: each is looked up by a
// row's CURRENT path, and a rolled-back row is back at the entry's OldPath,
// so it can never match.
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
