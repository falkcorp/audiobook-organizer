// file: internal/quarantine/service.go
// version: 1.5.0
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
// Rows always match the disk. A file that could not move because its
// destination is taken keeps its row, is named in the returned error, and
// leaves the book NOT marked quarantined; calling QuarantineBook again resumes
// from where the files actually are and finishes the job. A row whose file was
// already missing before the pass is left alone and logged -- it cannot move
// and does not block the quarantine.
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
	var from, destDir, dest string
	if isUnder(failedRoot, book.FilePath) {
		// An earlier pass moved the book path but did not finish. Resume from
		// where things actually are: the destination is where the book already
		// sits (a title edit since then must not move it a second time), and
		// the source layout is the path it was quarantined FROM.
		history, err := qs.store.GetBookPathHistory(bookID)
		if err != nil {
			return fmt.Errorf("get path history: %w", err)
		}
		last, ok := latestChange(history, changeQuarantine)
		if !ok {
			return fmt.Errorf("book %s is under .failed but has no quarantine history entry", bookID)
		}
		from, dest = last.OldPath, book.FilePath
		destDir = filepath.Dir(dest)
	} else {
		from = book.FilePath
		destDir = filepath.Clean(filepath.Join(failedRoot, author, title))
		dest = filepath.Clean(filepath.Join(destDir, filepath.Base(from)))
		// Boundary check: dest must stay inside .failed/
		if !isUnder(failedRoot, dest) || !isUnder(failedRoot, destDir) {
			return fmt.Errorf("quarantine path %q escapes .failed directory", dest)
		}
	}

	// A file-shaped book's other files keep their layout relative to the book
	// file's ORIGINAL directory, so disc1/01.mp3 and disc2/01.mp3 cannot
	// collide -- on a resumed pass too, which is why srcBase comes from `from`
	// and never from the book's current path.
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
			return "", false, logger.SanitizeLogValue(p) + ": destination escapes .failed, left in place"
		}
		return to, true, ""
	}

	oldPath := book.FilePath
	rl, err := qs.relocate(relocateSpec{
		bookID: bookID, from: from, to: dest,
		fileTo: fileTo, fileChangeType: changeQuarantineFile,
	})
	if err != nil {
		return err
	}
	complete := rl.bookAtDest && len(rl.blocked) == 0
	bookChanged := rl.bookAtDest && oldPath != dest

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
	if bookChanged {
		rl.journal = append(rl.journal, database.BookPathChange{
			BookID: bookID, OldPath: oldPath, NewPath: dest, ChangeType: changeQuarantine,
		})
	}
	qs.flushJournal(rl)
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

// UnquarantineBook moves a quarantined book back to its original path
// (retrieved from path history), repoints its book_file rows back with it,
// and clears the quarantine fields.
//
// Each file goes back to the path its NEWEST quarantine_file journal row
// recorded. A book quarantined before that journal existed had only its book
// path moved (its other rows were never repointed), so a row with no journal
// entry is left alone; one that sits under .failed/ is logged.
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
	fileTo := func(p string) (string, bool, string) {
		if e, ok := fileOrig[p]; ok {
			return e.OldPath, true, ""
		}
		if failedRoot != "" && isUnder(failedRoot, p) {
			return "", false, logger.SanitizeLogValue(p) + ": no recorded original path, left under .failed"
		}
		return "", false, "" // never moved, or already back
	}

	oldPath := book.FilePath
	rl, err := qs.relocate(relocateSpec{
		bookID: bookID, from: from, to: origPath,
		fileTo: fileTo, fileChangeType: changeUnquarantineFile,
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
	if bookChanged {
		rl.journal = append(rl.journal, database.BookPathChange{
			BookID: bookID, OldPath: oldPath, NewPath: origPath, ChangeType: changeUnquarantine,
		})
	}
	qs.flushJournal(rl)
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

// diskMove is one rename relocate performed, kept so rollback can reverse it.
type diskMove struct{ from, to string }

// relocation is the outcome of one relocate pass.
type relocation struct {
	// bookAtDest: the book path is at its destination on disk now -- moved in
	// this pass, or already there from an earlier pass or an interrupted one.
	bookAtDest bool
	moves      []diskMove
	// priors are full-record snapshots of every row repointed in this pass.
	priors    []database.BookFile
	repointed int
	// blocked names files that should have moved and could not (destination
	// taken, rename failed). Only these keep a pass incomplete.
	blocked []string
	// notes names rows that were left alone for a reason that a retry cannot
	// change: the file was missing before the pass, or lies outside a
	// directory-shaped book. Logged, never counted as failures.
	notes []string
	// journal holds the per-file path-history rows this pass owes. They are
	// written by flushJournal only after the book row commits, so a rolled
	// back pass leaves no stale entry for UnquarantineBook to follow.
	journal []database.BookPathChange
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
	bookID         string
	from, to       string
	fileTo         func(string) (to string, ok bool, note string)
	fileChangeType string
}

// pathState classifies a source/destination pair by what is on disk.
type pathState int

const (
	stMove    pathState = iota // source present, destination free
	stAtDest                   // source gone, destination present: already moved
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
// book_file row in the same pass. It never deletes a row and never repoints a
// row to a path that does not exist.
//
// When the book path is a directory it is renamed in one step (so cover art
// and sidecars travel with it) and each row under it is translated by prefix.
// When it is a file, each row's file is handled on its own: the row at
// spec.from goes to spec.to, every other row goes where fileTo says.
//
// A file already at its destination with its source gone -- the state a crash
// between a rename and its row write leaves behind -- is repointed without a
// move, so a retry repairs an interrupted pass instead of failing on it.
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
	rl := &relocation{}

	if isDir {
		switch bookState {
		case stBlocked:
			rl.block(sp.from, fmt.Sprintf("destination %s already exists", sp.to))
			return rl, nil
		case stMove:
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
			if err := qs.repointRow(rl, row, newPath, ""); err != nil {
				rl.block(row.FilePath, "repoint row: "+err.Error())
			}
		}
		return rl, nil
	}

	bookRowSeen := false
	for i := range rows {
		row := rows[i]
		var to string
		switch {
		case row.FilePath == sp.from:
			bookRowSeen = true
			to = sp.to
		case row.FilePath == sp.to:
			bookRowSeen = true
			continue // already repointed
		default:
			var ok bool
			var why string
			to, ok, why = sp.fileTo(row.FilePath)
			if why != "" {
				rl.notes = append(rl.notes, why)
			}
			if !ok {
				continue
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
		case stMove:
			if err := moveNoClobber(row.FilePath, to); err != nil {
				rl.block(row.FilePath, err.Error())
				continue
			}
			rl.moves = append(rl.moves, diskMove{from: row.FilePath, to: to})
		case stAtDest:
			// Moved by an interrupted pass; only the row is behind.
		}
		if err := qs.repointRow(rl, row, to, sp.fileChangeType); err != nil {
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
			if err := moveNoClobber(sp.from, sp.to); err != nil {
				rl.block(sp.from, err.Error())
			} else {
				rl.moves = append(rl.moves, diskMove{from: sp.from, to: sp.to})
			}
		case stBlocked:
			rl.block(sp.from, fmt.Sprintf("destination %s already exists", sp.to))
		}
	}

	// Decided from the disk, not from bookkeeping: the book path is at its
	// destination when the destination exists and the source does not.
	if st, _, err := classify(sp.from, sp.to); err == nil && st == stAtDest {
		rl.bookAtDest = true
	}
	return rl, nil
}

// repointRow rewrites one row's FilePath. row is the FULL record from
// GetBookFiles, because UpdateBookFile replaces the whole record. When
// changeType is set a path-history row is queued on rl.journal.
func (qs *QuarantineService) repointRow(rl *relocation, row database.BookFile, newPath, changeType string) error {
	updated := row
	updated.FilePath = newPath
	if err := qs.store.UpdateBookFile(row.ID, &updated); err != nil {
		return err
	}
	rl.priors = append(rl.priors, row)
	rl.repointed++
	if changeType != "" {
		rl.journal = append(rl.journal, database.BookPathChange{
			BookID: row.BookID, OldPath: row.FilePath, NewPath: newPath, ChangeType: changeType,
		})
	}
	return nil
}

// flushJournal writes the path-history rows a committed pass owes.
func (qs *QuarantineService) flushJournal(rl *relocation) {
	for i := range rl.journal {
		c := rl.journal[i]
		if err := qs.store.RecordPathChange(&c); err != nil {
			slog.Warn("quarantine: could not journal path change; the reverse move will not find it",
				"bookID", logger.SanitizeLogValue(c.BookID),
				"changeType", c.ChangeType,
				"path", logger.SanitizeLogValue(c.NewPath), "err", err)
		}
	}
	rl.journal = nil
}

func (qs *QuarantineService) logNotes(rl *relocation, op, bookID string) {
	for _, n := range rl.notes {
		slog.Warn(op+": row left in place", "bookID", logger.SanitizeLogValue(bookID), "detail", n)
	}
}

// rollback reverses a relocate pass after the book-row update failed. Each
// rename is undone newest-first; afterwards a row snapshot is restored only
// if its original file is back on disk, so rows keep matching the disk even
// when a rename back fails. The pass's journal is discarded unwritten.
func (qs *QuarantineService) rollback(rl *relocation, op string) {
	rl.journal = nil
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
