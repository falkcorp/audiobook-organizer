// file: internal/audiobooks/revert.go
// version: 1.19.0
// guid: d4e5f6a7-b8c9-d0e1-f2a3-b4c5d6e7f8a9
// last-edited: 2026-09-13

package audiobooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// revertServiceStore is the slice of the store this service uses: the ledger
// and book calls, the series calls the referent checks and rename-back need,
// and the book_file calls the fs-regroup-xml reversals need. It is split into
// those three pieces to stay within the interfacebloat limit that
// scripts/check-interface-width.sh ratchets. The method set is what every
// caller and stub must provide, whatever the grouping.
//
// It previously embedded database.BookReader, database.BookWriter and
// database.OperationStore wholesale -- the comment above it called that "the
// narrow slice", which it was only relative to database.Store.
type revertServiceStore interface {
	revertLedgerStore
	revertSeriesStore
	revertBookFileStore
}

// revertLedgerStore reads the operation's ledger, marks rows reverted, and
// reads and writes the books those rows name.
type revertLedgerStore interface {
	GetBookByID(id string) (*database.Book, error)
	// ModifyBook is every book write the revert makes: each restore is a
	// compare-and-set on the row as read under the book's write stripe, never
	// a whole-row UpdateBook of an earlier read.
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
	MarkOperationChangesReverted(operationID string, changeIDs []string) error

	// Needed by the embedded isProtectedPath call in revertTagWrite
	// (SERVER-GLOBAL-STORE-AUDIT phase 6).
	GetAllImportPaths() ([]database.ImportPath, error)
}

// revertSeriesStore is needed by undo.CheckRestoreReferent
// (revertMetadataUpdate, revertSeriesRename) and by the series rename-back.
type revertSeriesStore interface {
	GetSeriesByID(id int) (*database.Series, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	// RenameSeriesIf is the rename-back. It repeats CheckRestoreReferent's
	// current-name and name-free checks under the store's series name-index
	// lock, so nothing can land between check and write.
	RenameSeriesIf(id int, expectCurrent, newName string) error
}

// revertBookFileStore is needed by the maintenance.fs-regroup-xml reversals:
// a reassigned book_file row moves back, a track number is restored in place,
// and an external id moved onto the survivor moves back one at a time.
type revertBookFileStore interface {
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	// GetBookFiles finds the rows a file_move revert repoints.
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
	GetBookByExternalID(source, externalID string) (string, error)
	ReassignExternalID(source, externalID, newBookID string) error
}

var revertLog = logger.New("revert")

// revertPathLocker is the server's per-path file-write lock
// (writeBackPathLocks, the table organize write-back and metafetch share).
var revertPathLocker atomic.Pointer[func(path string) func()]

// SetRevertPathLocker wires the process-wide per-path write lock into every
// RevertService made afterwards. A revert that renames a file or rewrites its
// tags then never runs at the same moment as a write-back or apply on the
// same path. nil removes it.
func SetRevertPathLocker(lock func(path string) func()) {
	if lock == nil {
		revertPathLocker.Store(nil)
		return
	}
	revertPathLocker.Store(&lock)
}

// RevertService handles reverting operations by undoing recorded changes.
type RevertService struct {
	db revertServiceStore
	// ReadTags reads a file's current tag values by write key (the same map
	// the organizer recorded its pre-write values from). A tag revert is a
	// compare-and-set against it.
	ReadTags func(path string) (map[string]string, error)
	// WriteTags writes tags into one file.
	WriteTags func(path string, tags map[string]any) error
	// LockPath takes the per-path write lock and returns its release. It is
	// a leaf lock, taken before the book's write stripe, the order write-back
	// uses. nil takes none.
	LockPath func(path string) func()
	// ComputeITunesPath recomputes a repointed book_file's iTunes path.
	ComputeITunesPath func(localPath string) string
}

// NewRevertService creates a new RevertService.
func NewRevertService(db revertServiceStore) *RevertService {
	rs := &RevertService{
		db:                db,
		ReadTags:          metadata.ReadTagProperties,
		WriteTags:         defaultRevertWriteTags,
		ComputeITunesPath: metafetch.ComputeITunesPath,
	}
	if lock := revertPathLocker.Load(); lock != nil {
		rs.LockPath = *lock
	}
	return rs
}

// It writes each key to its one file property and reads the file back
// (metadata.WriteTagProperties): a key with no property, a value the writer
// did not store, or a removal it did not make is an error, so the row fails
// instead of being marked reverted. metadata.WriteMetadataToFile could not be
// used: its write map drops "" and turns one key into several properties.
func defaultRevertWriteTags(path string, tags map[string]any) error {
	values := make(map[string]string, len(tags))
	for k, v := range tags {
		values[k] = fmt.Sprint(v)
	}
	return metadata.WriteTagProperties(path, values)
}

// lockPaths takes the per-path lock on each distinct non-empty path, in
// sorted order so two reverts can never take them in opposite orders, and
// returns one release for all of them.
func (rs *RevertService) lockPaths(paths ...string) func() {
	if rs.LockPath == nil {
		return func() {}
	}
	var keys []string
	for _, p := range paths {
		if p != "" && !slices.Contains(keys, p) {
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)
	releases := make([]func(), 0, len(keys))
	for _, k := range keys {
		releases = append(releases, rs.LockPath(k))
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
}

// RevertResult reports what RevertOperation did with each row of the
// operation's change ledger. Only the Restored rows are marked reverted in the
// store; Failed and NotRestorable rows keep a nil RevertedAt so the ledger
// never claims an undo that did not happen.
type RevertResult struct {
	OperationID string `json:"operation_id"`
	// Total is the number of change rows the operation recorded.
	Total int `json:"total"`
	// Restored rows were reversed (or were no-op markers with nothing to
	// reverse) and are now marked reverted.
	Restored int `json:"restored"`
	// Failed rows have a change type the engine can reverse, but the reversal
	// returned an error. They are not marked reverted.
	Failed int `json:"failed"`
	// NotRestorable rows are ones the engine has no reversal for, as decided by
	// undo.NotRestorableLabel: record-only author_delete / narrator_delete
	// rows, a metadata_update on a field it cannot restore, or a change type it
	// does not recognise. They are not marked reverted.
	NotRestorable int `json:"not_restorable"`
	// NotRestorableTypes counts NotRestorable rows by their label (a change
	// type, or "metadata_update:<field>").
	NotRestorableTypes map[string]int `json:"not_restorable_types,omitempty"`
	// AlreadyReverted rows are restorable rows an earlier revert already
	// restored and marked; this call skipped them.
	AlreadyReverted int `json:"already_reverted"`
	// RestoredTypes counts this call's Restored rows by change type, so a
	// caller can drop the caches a restore made stale (the server clears the
	// series caches when a series_rename row was restored).
	RestoredTypes map[string]int `json:"restored_types,omitempty"`
	// ChangedSince counts the Failed rows the compare-and-set refused because
	// their target no longer holds what the operation wrote
	// (undo.ReasonChangedSince, or undo.ReasonSeriesRenamedSince for a
	// series_rename): the revert left a later edit in place instead of
	// overwriting it.
	ChangedSince int `json:"changed_since,omitempty"`
}

// Partial reports whether any row of the operation was left un-reverted.
func (r *RevertResult) Partial() bool {
	return r.Failed > 0 || r.NotRestorable > 0
}

// Summary is a one-line human-readable account of the result, used both in the
// log and as the HTTP response message.
func (r *RevertResult) Summary() string {
	var b strings.Builder
	if r.Partial() {
		fmt.Fprintf(&b, "operation partially reverted: %d of %d changes restored", r.Restored, r.Total)
	} else {
		fmt.Fprintf(&b, "operation reverted: %d of %d changes restored", r.Restored, r.Total)
	}
	if r.AlreadyReverted > 0 {
		fmt.Fprintf(&b, "; %d already reverted earlier", r.AlreadyReverted)
	}
	if r.Failed > 0 {
		fmt.Fprintf(&b, "; %d failed to restore", r.Failed)
		if r.ChangedSince > 0 {
			fmt.Fprintf(&b, " (%d changed since the operation and were left as they are)", r.ChangedSince)
		}
	}
	if r.NotRestorable > 0 {
		fmt.Fprintf(&b, "; %d cannot be undone automatically (%s)", r.NotRestorable, formatTypeCounts(r.NotRestorableTypes))
	}
	return b.String()
}

// NotRestorableError is returned by RevertOperation when no row of the
// operation has a change type the engine can reverse -- e.g. every row of a
// maintenance.purge-empty-authors run is an author_delete record. Nothing is
// reversed and nothing is marked reverted.
type NotRestorableError struct {
	OperationID string
	Total       int
	Types       map[string]int
}

func (e *NotRestorableError) Error() string {
	return fmt.Sprintf("this operation's changes are a record only and cannot be undone automatically: %s",
		formatTypeCounts(e.Types))
}

// RevertOperation undoes the restorable changes of an operation in reverse
// order and marks exactly those rows reverted.
//
//   - No row restorable: returns *NotRestorableError; nothing is touched.
//   - Some rows not restorable: the restorable ones are reversed and marked;
//     the rest are left unmarked and counted in the result.
//   - A restorable row whose reversal fails is left unmarked, counted in
//     Failed, and RevertOperation returns the result with a non-nil error.
//   - A restorable row an earlier revert already marked is skipped on its
//     own, so rows a partial or failed revert left unmarked can be retried.
//     Only when every restorable row is marked does it report "already been
//     reverted".
//
// Rows are classified by undo.NotRestorableLabel, the same classifier
// undo.PreflightUndoConflicts uses for the confirmation the UI shows first.
func (rs *RevertService) RevertOperation(operationID string) (*RevertResult, error) {
	changes, err := rs.db.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get operation changes: %w", err)
	}

	if len(changes) == 0 {
		return nil, fmt.Errorf("no changes found for operation %s", operationID)
	}

	result := &RevertResult{OperationID: operationID, Total: len(changes)}
	var restorable []*database.OperationChange
	restorableTotal := 0
	for _, c := range changes {
		if label := undo.NotRestorableLabel(c); label != "" {
			result.NotRestorable++
			if result.NotRestorableTypes == nil {
				result.NotRestorableTypes = map[string]int{}
			}
			result.NotRestorableTypes[label]++
			continue
		}
		restorableTotal++
		if c.RevertedAt != nil {
			result.AlreadyReverted++
			continue
		}
		restorable = append(restorable, c)
	}

	if restorableTotal == 0 {
		revertLog.Warn("revert refused: no restorable changes: operation=%s not_restorable=%d types=%s",
			logger.SanitizeLogValue(operationID), result.NotRestorable, formatTypeCounts(result.NotRestorableTypes))
		return nil, &NotRestorableError{OperationID: operationID, Total: result.Total, Types: result.NotRestorableTypes}
	}
	if len(restorable) == 0 {
		return nil, fmt.Errorf("operation %s has already been reverted: all %d restorable changes are marked reverted",
			operationID, restorableTotal)
	}

	// Process in reverse order
	var errMsgs []string
	var restoredIDs []string
	for _, c := range slices.Backward(restorable) {
		if err := rs.revertChange(c); err != nil {
			result.Failed++
			switch undo.RefusalReason(err) {
			case undo.ReasonChangedSince, undo.ReasonSeriesRenamedSince:
				result.ChangedSince++
			}
			errMsgs = append(errMsgs, fmt.Sprintf("change %s: %v", c.ID, err))
			revertLog.Warn("revert failed for change %s: %v", c.ID, err)
			continue
		}
		restoredIDs = append(restoredIDs, c.ID)
		if result.RestoredTypes == nil {
			result.RestoredTypes = map[string]int{}
		}
		result.RestoredTypes[c.ChangeType]++
	}

	// Mark only the rows that were actually restored.
	if len(restoredIDs) > 0 {
		if err := rs.db.MarkOperationChangesReverted(operationID, restoredIDs); err != nil {
			return nil, fmt.Errorf("restored %d changes but failed to mark them reverted: %w", len(restoredIDs), err)
		}
	}
	result.Restored = len(restoredIDs)

	revertLog.Info("revert finished: operation=%s total=%d restored=%d failed=%d not_restorable=%d types=%s",
		logger.SanitizeLogValue(operationID), result.Total, result.Restored, result.Failed,
		result.NotRestorable, formatTypeCounts(result.NotRestorableTypes))

	if len(errMsgs) > 0 {
		return result, fmt.Errorf("partially reverted with %d errors: %s", len(errMsgs), errMsgs[0])
	}
	return result, nil
}

// formatTypeCounts renders {"author_delete": 3} as "3 author_delete rows",
// sorted by type so the output is stable.
func formatTypeCounts(counts map[string]int) string {
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)
	parts := make([]string, 0, len(types))
	for _, t := range types {
		noun := "rows"
		if counts[t] == 1 {
			noun = "row"
		}
		parts = append(parts, fmt.Sprintf("%d %s %s", counts[t], t, noun))
	}
	return strings.Join(parts, ", ")
}

func (rs *RevertService) revertChange(c *database.OperationChange) error {
	switch c.ChangeType {
	case "file_move", "organize_rename":
		// organize_rename writes the same (OldValue, NewValue) shape as
		// file_move — old path → new path, Book.file_path updated. The
		// reversal is identical: move the file back and restore
		// Book.file_path.
		return rs.revertFileMove(c)
	case "metadata_update":
		return rs.revertMetadataUpdate(c)
	case "tag_write":
		return rs.revertTagWrite(c)
	case undo.ChangeTypeSeriesRename:
		return rs.revertSeriesRename(c)
	case undo.ChangeTypeBookFileReassign:
		return rs.revertBookFileReassign(c)
	case undo.ChangeTypeBookFileTrack:
		return rs.revertBookFileTrack(c)
	case undo.ChangeTypeBookPathUpdate:
		return rs.revertBookPathUpdate(c)
	case undo.ChangeTypeBookSoftDelete:
		return rs.revertBookSoftDelete(c)
	case undo.ChangeTypeBookPrimaryDemote:
		return rs.revertBookPrimaryDemote(c)
	case undo.ChangeTypeExternalIDReassign:
		return rs.revertExternalIDReassign(c)
	case "organize_failed", "organize_skipped", "organize_summary":
		// No filesystem or DB mutation recorded; nothing to reverse.
		return nil
	default:
		// Unreachable from RevertOperation, which filters on undo.NotRestorableLabel.
		return fmt.Errorf("unknown change type: %s", c.ChangeType)
	}
}

// loadBook returns the change's book, or a refusal when it cannot be read or no
// longer exists. PebbleStore.GetBookByID answers a missing id with (nil, nil);
// without this guard every restore path dereferenced that nil and panicked
// inside the revert endpoint instead of counting the row Failed. It is
// undo.CheckRestoreBook, the check the preflight runs, so the preflight never
// offers a row this refuses.
func (rs *RevertService) loadBook(id string) (*database.Book, error) {
	return undo.CheckRestoreBook(rs.db, id)
}

// revertFileMove moves an organized file (or book folder) back from NewValue
// to OldValue and points the book and every book_file row under it back too.
//
// The book's path is compare-and-set: the move runs only while the book still
// points at NewValue, and the ModifyBook that writes OldValue checks it again
// under the book's write stripe, moving the file back to NewValue if that
// check fails. Nothing is moved when the file is gone from NewValue or
// something now occupies OldValue; both fail the row instead of marking it
// reverted. A book already pointing at OldValue (an earlier revert that moved
// the file but failed on a book_file row) only has its book_file rows
// finished.
//
// The file I/O and the book_file updates (UpdateBookFile recomputes the book's
// aggregates, a book write) run outside the ModifyBook callback, per the LOCK
// RULES in database/pebble_store_book_lock.go.
func (rs *RevertService) revertFileMove(c *database.OperationChange) error {
	// Both paths are held from the checks through the rename, the book write
	// (and its rollback rename) and the book_file repoint, so no write-back
	// or apply writes into either place mid-move.
	release := rs.lockPaths(c.NewValue, c.OldValue)
	defer release()
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}
	_, newErr := os.Lstat(c.NewValue)
	_, oldErr := os.Lstat(c.OldValue)
	newThere, oldThere := newErr == nil, oldErr == nil
	switch {
	case book.FilePath == c.OldValue && oldThere && !newThere:
		return rs.repointBookFiles(c.BookID, c.NewValue, c.OldValue)
	case book.FilePath != c.NewValue:
		return driftRefusal("book %s path is %q, not %q as the operation left it", book.ID, book.FilePath, c.NewValue)
	case !newThere:
		return fmt.Errorf("file is no longer at %s; nothing moved back", c.NewValue)
	case oldThere:
		return fmt.Errorf("%s is occupied again; refusing to move %s over it", c.OldValue, c.NewValue)
	}

	if err := os.MkdirAll(filepath.Dir(c.OldValue), 0o775); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(c.OldValue), err)
	}
	if err := os.Rename(c.NewValue, c.OldValue); err != nil {
		return fmt.Errorf("failed to move file back from %s to %s: %w", c.NewValue, c.OldValue, err)
	}
	if err := rs.modifyBook(c.BookID, func(b *database.Book) error {
		if b.FilePath != c.NewValue {
			return driftRefusal("book %s path changed to %q during the revert", b.ID, b.FilePath)
		}
		b.FilePath = c.OldValue
		return nil
	}); err != nil {
		if rbErr := os.Rename(c.OldValue, c.NewValue); rbErr != nil {
			return fmt.Errorf("%w; moving the file back to %s also failed: %v", err, c.NewValue, rbErr)
		}
		return err
	}
	return rs.repointBookFiles(c.BookID, c.NewValue, c.OldValue)
}

// repointBookFiles points every book_file row of the book at from (a file) or
// under from (a book folder) at the same place under to. Each row is re-read
// in full before it is written: UpdateBookFile replaces the whole record.
func (rs *RevertService) repointBookFiles(bookID, from, to string) error {
	files, err := rs.db.GetBookFiles(bookID)
	if err != nil {
		return fmt.Errorf("list book_file rows of %s: %w", bookID, err)
	}
	var errs []error
	for _, listed := range files {
		if _, ok := repointPath(listed.FilePath, from, to); !ok {
			continue
		}
		bf, gerr := rs.db.GetBookFileByID(bookID, listed.ID)
		if gerr != nil || bf == nil {
			errs = append(errs, fmt.Errorf("re-read book_file %s: %v", listed.ID, gerr))
			continue
		}
		np, ok := repointPath(bf.FilePath, from, to)
		if !ok {
			continue
		}
		bf.FilePath = np
		// An iTunes-linked row's location follows the file back.
		if bf.ITunesPath != "" && rs.ComputeITunesPath != nil {
			if ip := rs.ComputeITunesPath(np); ip != "" {
				bf.ITunesPath = ip
			}
		}
		if uerr := rs.db.UpdateBookFile(bf.ID, bf); uerr != nil {
			errs = append(errs, fmt.Errorf("repoint book_file %s: %w", bf.ID, uerr))
		}
	}
	return errors.Join(errs...)
}

// repointPath maps p from under from to under to. p == from maps to to.
func repointPath(p, from, to string) (string, bool) {
	if p == from {
		return to, true
	}
	if rest, ok := strings.CutPrefix(p, strings.TrimSuffix(from, string(os.PathSeparator))+string(os.PathSeparator)); ok {
		return filepath.Join(to, rest), true
	}
	return "", false
}

// modifyBook runs fn inside ModifyBook and turns "the book is gone" into the
// same refusal loadBook gives.
func (rs *RevertService) modifyBook(id string, fn func(*database.Book) error) error {
	updated, err := rs.db.ModifyBook(id, fn)
	if err != nil {
		return err
	}
	if updated == nil {
		return &undo.ReferentError{Reason: undo.ReasonBookMissing, Detail: fmt.Sprintf("book %s no longer exists", id)}
	}
	return nil
}

// revertBookFileReassign moves one book_file row from the book it was moved
// onto (BookID) back to the book it came from (OldValue). The store refuses the
// move when the row is no longer under BookID, so a row something else has
// moved since fails the change instead of being taken from its new owner.
// Nothing is deleted either way.
func (rs *RevertService) revertBookFileReassign(c *database.OperationChange) error {
	fileID, ok := undo.BookFileIDFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no book_file id in field %q", c.FieldName)
	}
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	if _, err := rs.loadBook(c.OldValue); err != nil {
		return err
	}
	if err := rs.db.MoveBookFilesToBook([]string{fileID}, c.BookID, c.OldValue); err != nil {
		return fmt.Errorf("move book_file %s from %s back to %s: %w", fileID, c.BookID, c.OldValue, err)
	}
	return nil
}

// driftRefusal refuses a row whose field no longer holds the value the
// operation wrote: restoring OldValue would overwrite a later change. The
// preflight reports the same state (undo.ReasonChangedSince).
func driftRefusal(format string, args ...any) error {
	return &undo.ReferentError{Reason: undo.ReasonChangedSince, Detail: fmt.Sprintf(format, args...)}
}

// revertBookFileTrack puts a book_file row's track number back, but only while
// the row still carries the number the operation set (compare-and-set under
// merge.LockMergeRMW, the lock the apply wrote under). The ledger is reversed
// newest first, so this runs while the row is still on BookID, before its
// book_file_reassign row moves it back.
func (rs *RevertService) revertBookFileTrack(c *database.OperationChange) error {
	fileID, ok := undo.BookFileIDFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no book_file id in field %q", c.FieldName)
	}
	old, err := strconv.Atoi(c.OldValue)
	if err != nil {
		return fmt.Errorf("track %q is not a number: %w", c.OldValue, err)
	}
	set, err := strconv.Atoi(c.NewValue)
	if err != nil {
		return fmt.Errorf("track %q is not a number: %w", c.NewValue, err)
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	f, err := rs.db.GetBookFileByID(c.BookID, fileID)
	if err != nil || f == nil {
		return driftRefusal("book_file %s is no longer on book %s (err=%v)", fileID, c.BookID, err)
	}
	if f.TrackNumber != set {
		return driftRefusal("book_file %s track is %d, not the %d the operation set", fileID, f.TrackNumber, set)
	}
	f.TrackNumber = old
	if err := rs.db.UpdateBookFile(f.ID, f); err != nil {
		return fmt.Errorf("restore track of book_file %s: %w", fileID, err)
	}
	return nil
}

// revertBookPathUpdate restores a book's file_path that was changed with
// nothing moved on disk (unlike file_move, which moves the file back too), but
// only while the path is still the one the operation set. The check and the
// write both run on the row as read under the book's write stripe.
func (rs *RevertService) revertBookPathUpdate(c *database.OperationChange) error {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if book.FilePath != c.NewValue {
			return driftRefusal("book %s path changed since the operation", book.ID)
		}
		book.FilePath = c.OldValue
		return nil
	})
}

// revertBookSoftDelete clears a book's deletion mark. A book purged since is
// refused; one already restored is left as it is.
func (rs *RevertService) revertBookSoftDelete(c *database.OperationChange) error {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if !book.IsSoftDeleted() {
			return database.ErrSkipBookWrite
		}
		notMarked := false
		book.MarkedForDeletion = &notMarked
		book.MarkedForDeletionAt = nil
		return nil
	})
}

// revertBookPrimaryDemote restores a retired shell's primary flag ("" is nil,
// the never-set shape) while it is still the false the operation wrote.
func (rs *RevertService) revertBookPrimaryDemote(c *database.OperationChange) error {
	var restored *bool
	switch c.OldValue {
	case "":
	case "true", "false":
		v := c.OldValue == "true"
		restored = &v
	default:
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable, Detail: fmt.Sprintf("is_primary_version %q", c.OldValue)}
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if book.IsPrimaryVersion == nil || *book.IsPrimaryVersion {
			return driftRefusal("book %s was made primary again since the operation", book.ID)
		}
		book.IsPrimaryVersion = restored
		return nil
	})
}

// revertExternalIDReassign moves one external id back from the book it was
// moved onto (NewValue) to the book it came from (OldValue), while the id
// still names NewValue.
func (rs *RevertService) revertExternalIDReassign(c *database.OperationChange) error {
	source, extID, ok := undo.ExternalIDFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no external id in field %q", c.FieldName)
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	if _, err := rs.loadBook(c.OldValue); err != nil {
		return err
	}
	owner, err := rs.db.GetBookByExternalID(source, extID)
	if err != nil {
		return fmt.Errorf("look up external id %s/%s: %w", source, extID, err)
	}
	if owner != c.NewValue {
		return driftRefusal("external id %s/%s now names %q, not %s", source, extID, owner, c.NewValue)
	}
	if err := rs.db.ReassignExternalID(source, extID, c.OldValue); err != nil {
		return fmt.Errorf("move external id %s/%s back to %s: %w", source, extID, c.OldValue, err)
	}
	return nil
}

func (rs *RevertService) revertMetadataUpdate(c *database.OperationChange) error {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}

	// Refuse before writing anything: a series_id whose series row the same
	// operation deleted would be restored as a dangling reference. The
	// preflight runs the same check, so it reports this row as a conflict. It
	// reads series rows, so it runs before the book's write stripe is taken.
	if err := undo.CheckRestoreReferent(rs.db, c); err != nil {
		return fmt.Errorf("book %s: not restored, %w", c.BookID, err)
	}
	// Compare-and-set, the same three-way check series_rename rows get, on the
	// row as read under the book's write stripe: restore only while the field
	// still holds what the operation wrote, so a user edit made since is never
	// overwritten. A field that already holds OldValue (a rescan put
	// library_state back, say) is already restored (undo.ErrAlreadyRestored):
	// nothing is written and the row is marked reverted. Anything else changed
	// since and is refused. The preflight runs the same check. Only this field
	// is written; the rest of the row is whatever the store holds now.
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if err := undo.CheckBookFieldCurrent(book, c); err != nil {
			if errors.Is(err, undo.ErrAlreadyRestored) {
				return database.ErrSkipBookWrite
			}
			return fmt.Errorf("book %s: not restored, %w", c.BookID, err)
		}
		// Exactly OldValue goes back, typed by the Book field it names; ""
		// clears a pointer field to nil. An unparsable value is an error, not
		// a no-op, so the row is counted Failed instead of being marked
		// reverted.
		return undo.RestoreBookField(book, c.FieldName, c.OldValue)
	})
}

// revertSeriesRename renames the series a series_rename row names back to
// OldValue. CheckRestoreReferent refuses first when the series is gone, has
// been renamed again since, or its old name now belongs to another series, and
// on any store error; nothing is written in those cases. RenameSeriesIf then
// repeats the current-name and name-free checks under the store's series
// name-index lock, which CreateSeries and UpdateSeriesName also hold, so a
// create or rename that lands after the first check is refused, not clobbered.
//
// A series already named OldValue is counted restored with nothing written
// (undo.ErrAlreadyRestored), both when the first check sees it and when a
// concurrent rename-back lands between that check and RenameSeriesIf.
func (rs *RevertService) revertSeriesRename(c *database.OperationChange) error {
	if err := undo.CheckRestoreReferent(rs.db, c); err != nil {
		if errors.Is(err, undo.ErrAlreadyRestored) {
			return nil
		}
		return fmt.Errorf("series rename not reverted, %w", err)
	}
	if err := rs.db.RenameSeriesIf(*c.SeriesID, c.NewValue, c.OldValue); err != nil {
		if errors.Is(err, database.ErrRenameSeriesRenamedSince) {
			if s, gerr := rs.db.GetSeriesByID(*c.SeriesID); gerr == nil && s != nil && s.Name == c.OldValue {
				return nil
			}
		}
		return fmt.Errorf("series rename not reverted, %w", renameRefusal(err))
	}
	return nil
}

// renameRefusal turns a RenameSeriesIf refusal into the undo.ReferentError
// carrying the reason the preflight gives for the same state. Any other error
// is returned unchanged.
func renameRefusal(err error) error {
	for _, m := range []struct {
		sentinel error
		reason   string
	}{
		{database.ErrRenameSeriesNotFound, undo.ReasonSeriesDeleted},
		{database.ErrRenameSeriesRenamedSince, undo.ReasonSeriesRenamedSince},
		{database.ErrRenameSeriesNameTaken, undo.ReasonSeriesNameTaken},
	} {
		if errors.Is(err, m.sentinel) {
			return &undo.ReferentError{Reason: m.reason, Detail: err.Error()}
		}
	}
	return err
}

// revertTagWrite writes one tag's pre-organize value back into the book_file
// the organize wrote it to. The row names the file by id (undo.TagWriteField),
// so the file is found at its current path. NotRestorableLabel has already set
// aside rows with no pre-write value and legacy rows that name no file; this
// refuses them again rather than write "", which deletes the tag. A missing
// file or a protected path fails the row.
//
// It is a compare-and-set on the one file property the tag key names
// (metadata.TagProperty), under the per-path write lock: the file must still
// hold what the organize wrote (NewValue). A later write-back or apply that
// changed the tag is kept and the row refused as changed since. A file that
// already holds the pre-organize value is left alone and the row counts as
// restored. A tag that was absent before (undo.TagAbsentValue) is removed. A
// key with no single property fails the row: nothing is written for it.
//
// Only that property is written, and the writer reads it back. The write used
// to go through the organizer's write map, which dropped "" (so an absent tag
// was never removed, yet the row was marked reverted) and turned one key into
// several properties (reverting artist wrote COMPOSER="", erasing the narrator).
func (rs *RevertService) revertTagWrite(c *database.OperationChange) error {
	tag, fileID, ok := undo.TagWriteFromField(c.FieldName)
	if !ok || c.OldValue == "" {
		return fmt.Errorf("tag_write row %s has no restorable pre-write value", c.ID)
	}
	if rs.ReadTags == nil || rs.WriteTags == nil {
		return fmt.Errorf("tag %s not restored: no tag reader or writer is configured", tag)
	}
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}
	bf, err := rs.db.GetBookFileByID(c.BookID, fileID)
	if err != nil || bf == nil {
		return driftRefusal("book_file %s is no longer on book %s (err=%v)", fileID, c.BookID, err)
	}
	// Checked after the book and file, so a row whose book is gone reports
	// that reason; either way nothing is written and the row fails.
	if _, writable := metadata.TagProperty(tag); !writable {
		return fmt.Errorf("tag %s not restored: it maps to no single file property the revert can write", tag)
	}
	target := bf.FilePath
	release := rs.lockPaths(book.FilePath, target)
	defer release()
	if _, statErr := os.Stat(target); statErr != nil {
		return fmt.Errorf("tag %s not restored: %w", tag, statErr)
	}
	if isProtectedPath(rs.db, target) {
		return fmt.Errorf("tag %s not restored: %s is a protected path", tag, target)
	}

	restore := c.OldValue
	if restore == undo.TagAbsentValue {
		restore = ""
	}
	current, err := rs.ReadTags(target)
	if err != nil {
		return fmt.Errorf("tag %s not restored: the current tags of %s cannot be read: %w", tag, target, err)
	}
	cur, known := current[tag]
	switch {
	case !known:
		return driftRefusal("tag %s of %s cannot be read back, so whether it still holds what the organize wrote is unknown", tag, target)
	case cur == restore:
		// Already the pre-organize value (an absent tag reads ""): nothing to
		// write.
		return nil
	case cur != c.NewValue:
		return driftRefusal("tag %s of %s is %q, not %q as the organize wrote it; the later value is kept", tag, target, cur, c.NewValue)
	}
	if err := rs.WriteTags(target, map[string]any{tag: restore}); err != nil {
		return fmt.Errorf("failed to write tag %s back to %s: %w", tag, target, err)
	}
	return nil
}
