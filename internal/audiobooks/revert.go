// file: internal/audiobooks/revert.go
// version: 1.9.1
// guid: d4e5f6a7-b8c9-d0e1-f2a3-b4c5d6e7f8a9
// last-edited: 2026-09-12

package audiobooks

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// revertServiceStore is the slice of the store this service uses, measured
// with an empty-interface compiler probe: four direct calls plus
// GetAllImportPaths, which is what satisfies importPathLister when the store is
// forwarded to isProtectedPath.
//
// It previously embedded database.BookReader, database.BookWriter and
// database.OperationStore wholesale to reach those four -- the comment above it
// called that "the narrow slice", which it was only relative to database.Store.
type revertServiceStore interface {
	revertLedgerStore
	revertSeriesStore
	revertBookFileStore
}

// revertLedgerStore reads the operation's ledger, marks rows reverted, and
// reads and writes the books those rows name.
type revertLedgerStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
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
// a reassigned book_file row moves back, and a track number is restored in
// place.
type revertBookFileStore interface {
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// RevertService handles reverting operations by undoing recorded changes.
type RevertService struct {
	db revertServiceStore
}

// NewRevertService creates a new RevertService.
func NewRevertService(db revertServiceStore) *RevertService {
	return &RevertService{db: db}
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
		slog.Warn("revert refused: no restorable changes",
			"operation", logger.SanitizeLogValue(operationID), "not_restorable", result.NotRestorable,
			"types", formatTypeCounts(result.NotRestorableTypes))
		return nil, &NotRestorableError{OperationID: operationID, Total: result.Total, Types: result.NotRestorableTypes}
	}
	if len(restorable) == 0 {
		return nil, fmt.Errorf("operation %s has already been reverted: all %d restorable changes are marked reverted",
			operationID, restorableTotal)
	}

	// Process in reverse order
	var errors []string
	var restoredIDs []string
	for _, c := range slices.Backward(restorable) {
		if err := rs.revertChange(c); err != nil {
			result.Failed++
			errors = append(errors, fmt.Sprintf("change %s: %v", c.ID, err))
			slog.Warn("revert failed for change", "c", c.ID, "err", err)
			continue
		}
		restoredIDs = append(restoredIDs, c.ID)
	}

	// Mark only the rows that were actually restored.
	if len(restoredIDs) > 0 {
		if err := rs.db.MarkOperationChangesReverted(operationID, restoredIDs); err != nil {
			return nil, fmt.Errorf("restored %d changes but failed to mark them reverted: %w", len(restoredIDs), err)
		}
	}
	result.Restored = len(restoredIDs)

	slog.Info("revert finished", "operation", logger.SanitizeLogValue(operationID), "total", result.Total,
		"restored", result.Restored, "failed", result.Failed,
		"not_restorable", result.NotRestorable, "types", formatTypeCounts(result.NotRestorableTypes))

	if len(errors) > 0 {
		return result, fmt.Errorf("partially reverted with %d errors: %s", len(errors), errors[0])
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

func (rs *RevertService) revertFileMove(c *database.OperationChange) error {
	// Check file exists at new location
	if _, err := os.Stat(c.NewValue); os.IsNotExist(err) {
		slog.Warn("file no longer at , skipping revert", "c", c.NewValue)
		return nil
	}

	// Load the book BEFORE moving anything: a missing book must fail the row
	// with the file still where the operation left it, not after the move.
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}

	// Move file back
	if err := os.Rename(c.NewValue, c.OldValue); err != nil {
		return fmt.Errorf("failed to move file back from %s to %s: %w", c.NewValue, c.OldValue, err)
	}

	book.FilePath = c.OldValue
	if _, err := rs.db.UpdateBook(book.ID, book); err != nil {
		return fmt.Errorf("failed to update book path: %w", err)
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

// revertBookFileTrack puts a book_file row's track number back. The ledger is
// reversed newest first, so this runs while the row is still on BookID, before
// its book_file_reassign row moves it back.
func (rs *RevertService) revertBookFileTrack(c *database.OperationChange) error {
	fileID, ok := undo.BookFileIDFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no book_file id in field %q", c.FieldName)
	}
	old, err := strconv.Atoi(c.OldValue)
	if err != nil {
		return fmt.Errorf("track %q is not a number: %w", c.OldValue, err)
	}
	f, err := rs.db.GetBookFileByID(c.BookID, fileID)
	if err != nil {
		return fmt.Errorf("read book_file %s of %s: %w", fileID, c.BookID, err)
	}
	if f == nil {
		return fmt.Errorf("book_file %s is no longer on book %s", fileID, c.BookID)
	}
	f.TrackNumber = old
	if err := rs.db.UpdateBookFile(f.ID, f); err != nil {
		return fmt.Errorf("restore track of book_file %s: %w", fileID, err)
	}
	return nil
}

// revertBookPathUpdate restores a book's file_path that was changed with
// nothing moved on disk (unlike file_move, which moves the file back too).
func (rs *RevertService) revertBookPathUpdate(c *database.OperationChange) error {
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}
	book.FilePath = c.OldValue
	if _, err := rs.db.UpdateBook(book.ID, book); err != nil {
		return fmt.Errorf("restore path of book %s: %w", book.ID, err)
	}
	return nil
}

// revertBookSoftDelete clears a book's deletion mark. A book purged since is
// refused by loadBook; one already restored is left as it is.
func (rs *RevertService) revertBookSoftDelete(c *database.OperationChange) error {
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}
	if !book.IsSoftDeleted() {
		return nil
	}
	notMarked := false
	book.MarkedForDeletion = &notMarked
	book.MarkedForDeletionAt = nil
	if _, err := rs.db.UpdateBook(book.ID, book); err != nil {
		return fmt.Errorf("clear deletion mark on book %s: %w", book.ID, err)
	}
	return nil
}

func (rs *RevertService) revertMetadataUpdate(c *database.OperationChange) error {
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}

	// Refuse before writing anything: a series_id whose series row the same
	// operation deleted would be restored as a dangling reference. The
	// preflight runs the same check, so it reports this row as a conflict.
	if err := undo.CheckRestoreReferent(rs.db, c); err != nil {
		return fmt.Errorf("book %s: not restored, %w", c.BookID, err)
	}
	// Exactly OldValue goes back, typed by the Book field it names; "" clears
	// a pointer field to nil. An unparsable value is an error, not a no-op, so
	// the row is counted Failed instead of being marked reverted.
	if err := undo.RestoreBookField(book, c.FieldName, c.OldValue); err != nil {
		return err
	}

	if _, err := rs.db.UpdateBook(book.ID, book); err != nil {
		return fmt.Errorf("failed to update book: %w", err)
	}
	return nil
}

// revertSeriesRename renames the series a series_rename row names back to
// OldValue. CheckRestoreReferent refuses first when the series is gone, has
// been renamed again since, or its old name now belongs to another series, and
// on any store error; nothing is written in those cases. RenameSeriesIf then
// repeats the current-name and name-free checks under the store's series
// name-index lock, which CreateSeries and UpdateSeriesName also hold, so a
// create or rename that lands after the first check is refused, not clobbered.
func (rs *RevertService) revertSeriesRename(c *database.OperationChange) error {
	if err := undo.CheckRestoreReferent(rs.db, c); err != nil {
		return fmt.Errorf("series rename not reverted, %w", err)
	}
	if err := rs.db.RenameSeriesIf(*c.SeriesID, c.NewValue, c.OldValue); err != nil {
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

func (rs *RevertService) revertTagWrite(c *database.OperationChange) error {
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(book.FilePath); os.IsNotExist(statErr) {
		slog.Warn("file not found, skipping tag revert", "book", book.FilePath)
		return nil
	}

	if isProtectedPath(rs.db, book.FilePath) {
		slog.Info("skipping tag revert for protected path", "book", book.FilePath)
		return nil
	}

	tagMap := map[string]any{
		c.FieldName: c.OldValue,
	}
	opConfig := fileops.OperationConfig{VerifyChecksums: true}
	if err := metadata.WriteMetadataToFile(book.FilePath, tagMap, opConfig); err != nil {
		return fmt.Errorf("failed to write tag %s back to %s: %w", c.FieldName, book.FilePath, err)
	}

	return nil
}
