// file: internal/audiobooks/revert.go
// version: 1.51.0
// guid: d4e5f6a7-b8c9-d0e1-f2a3-b4c5d6e7f8a9
// last-edited: 2026-10-03

package audiobooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
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
	revertAuthorStore
	revertFieldStateStore
	// revertBookPrimaryDemote re-crowns a restored primary and demotes the
	// rest of its group (versionprimary.Crown).
	versionprimary.EnsureStore
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

// revertFieldStateStore lifts the field lock a repair set (revertFieldLock).
type revertFieldStateStore interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
}

// revertAuthorStore restores a book's author credits and removes an author
// row a title-as-author relink created (revertTitleRelinkCredits,
// revertTitleRelinkAuthorCreate, revertJunkAuthorCredits).
type revertAuthorStore interface {
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
	DeleteAuthor(id int) error
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
	// ModifyBookFile is the read-modify-write the repair_book_create revert
	// re-indexes a path with, so no snapshot overwrites a concurrent update.
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	// ClaimBookFilePathKey hands a vacated path's single-owner key back to a
	// row still at the path, writing nothing else (reindexVacatedPath).
	ClaimBookFilePathKey(bookID, fileID, path string) (bool, error)
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
	// ReadTags reads a file's current tag values by write key, in the same
	// form the organizer recorded its pre-write values in
	// (metadata.ReadTagProperties: a plain value for a one-property key, a
	// per-property snapshot for artist and narrator). A tag revert is a
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

// It writes each key to every file property it names and reads the file back
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
	// PartiallyRestored counts Restored tag_write rows whose tags were put
	// back only in part: a tag another tool changed after the operation was
	// kept, and Kept names each one.
	PartiallyRestored int `json:"partially_restored,omitempty"`
	// Kept lists, per partially restored row, the tags left as they are
	// because they changed since the operation.
	Kept []string `json:"kept,omitempty"`
	// ChangedSince counts the Failed rows the compare-and-set refused because
	// their target no longer holds what the operation wrote
	// (undo.ReasonChangedSince, or undo.ReasonSeriesRenamedSince for a
	// series_rename): the revert left a later edit in place instead of
	// overwriting it.
	ChangedSince int `json:"changed_since,omitempty"`
	// AlreadyRestored counts the Restored rows that needed no write: the
	// target already held OldValue (undo.ErrAlreadyRestored). A Repairs apply
	// journals each step BEFORE its write, so a step whose write never
	// happened (a cut-off run, a failed write) lands here, not in Failed.
	AlreadyRestored int `json:"already_restored,omitempty"`
	// Superseded counts rows marked reverted with NO write, because what
	// they would restore no longer applies, and SupersededDetail says why
	// for each. Not counted in Restored. The one case: a primary-flag demote
	// of a book that is retired now (soft-deleted, or merged into a live
	// survivor), so the restored flag could crown nobody; the book's
	// soft-delete revert brings it back non-primary under its group's
	// incumbent instead (revertBookPrimaryDemote).
	Superseded       int      `json:"superseded,omitempty"`
	SupersededDetail []string `json:"superseded_detail,omitempty"`
	// HandOffFailed lists the version groups the settle pass after the rows
	// (settleGroups) could not leave with one primary: group, the
	// operation's originals and the error, and whether the retry was
	// recorded. The rows themselves are restored and marked; the result is
	// Partial, and the next revert of the operation retries the recorded
	// groups even when every row is already reverted.
	HandOffFailed []string `json:"hand_off_failed,omitempty"`
}

// Partial reports whether any row of the operation was left un-reverted.
func (r *RevertResult) Partial() bool {
	return r.Failed > 0 || r.NotRestorable > 0 || len(r.HandOffFailed) > 0
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
	if r.PartiallyRestored > 0 {
		fmt.Fprintf(&b, "; %d restored in part (tags changed since the operation were left as they are)", r.PartiallyRestored)
	}
	if r.Superseded > 0 {
		fmt.Fprintf(&b, "; %d superseded and left as they are (%s)", r.Superseded, strings.Join(r.SupersededDetail, "; "))
	}
	if len(r.HandOffFailed) > 0 {
		fmt.Fprintf(&b, "; %d version group(s) not settled to one primary (%s)", len(r.HandOffFailed), strings.Join(r.HandOffFailed, " | "))
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
	// One revert of an operation at a time: the owed-settle record is read,
	// rewritten and cleared across the whole run.
	defer lockOperation(operationID)()
	changes, err := rs.db.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get operation changes: %w", err)
	}

	result := &RevertResult{OperationID: operationID, Total: len(changes)}
	retryOwed := func() (*RevertResult, error) {
		if msgs := rs.settleGroups(operationID, settleInput{
			crowned: crownedByHandOff(changes), priorPending: rs.settlePendingGroups(operationID),
		}, result); len(msgs) > 0 {
			return result, fmt.Errorf("partially reverted with %d errors: %s", len(msgs), msgs[0])
		}
		return result, nil
	}
	if len(changes) == 0 {
		if rs.hasSettleOwed(operationID) {
			// The rows are gone (purged) but a group settle is owed.
			return retryOwed()
		}
		return nil, fmt.Errorf("no changes found for operation %s", operationID)
	}

	var restorable []*database.OperationChange
	restorableTotal := 0
	for _, c := range changes {
		if c.Voided {
			continue // the write it describes never happened
		}
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
		if rs.hasSettleOwed(operationID) {
			// Every row is reverted but a group settle failed last time:
			// retry it (settleGroups).
			return retryOwed()
		}
		// A pending intent alone is stale here (rows are marked only after
		// their settle ran): drop it, nothing is owed.
		rs.clearSettleOwed(operationID)
		return nil, fmt.Errorf("operation %s has already been reverted: all %d restorable changes are marked reverted",
			operationID, restorableTotal)
	}

	// The order and the retired-book gate are undo.PlanRevert's, shared with
	// the preflight (undo.PreflightUndoConflicts) so it predicts the same
	// refusals: newest first, except that every row of a retired book waits
	// for, and is refused without, the rows that moved or repointed its file
	// elsewhere.
	var errMsgs []string
	var restoredIDs []string
	// markedIDs are rows marked reverted without being restored
	// (supersededRestore).
	var markedIDs []string
	// touched: the rows that wrote in this run and can change who a version
	// group's primary is; the settle pass after the loop reads them.
	var touched []settleTouch
	refusedDemote := map[string]bool{}
	plan, err := undo.PlanRevert(restorable, rs.db.GetBookFiles)
	if err != nil {
		return nil, err
	}
	// Settle intent, per group: a group goes into the pending record the
	// first time a settle row of a book in it WRITES (markSettlePending), so
	// a run that dies, or whose mark fails, between its rows and its settle
	// leaves exactly the groups it changed. The next run counts a row it
	// finds already restored as evidence only when the book's group is in
	// that earlier run's pending set (priorPending).
	priorPending := rs.settlePendingGroups(operationID)
	pendingNow := map[string]bool{}
	plan.NoteHandOffs(changes)
	for _, c := range plan.Order {
		err := plan.Gate(c)
		already := false
		if err == nil {
			err = rs.revertChangeIn(c, plan)
			if errors.Is(err, undo.ErrAlreadyRestored) {
				already, err = true, nil
			}
		}
		var superseded *supersededRestore
		if errors.As(err, &superseded) {
			// Nothing was written and nothing is owed: the row is done, so
			// it is marked reverted, but it is not reported Restored.
			result.Superseded++
			result.SupersededDetail = append(result.SupersededDetail, fmt.Sprintf("change %s: %s", c.ID, superseded.detail))
			revertLog.Info("revert of change %s superseded: %s", c.ID, superseded.detail)
			plan.Record(c, nil)
			markedIDs = append(markedIDs, c.ID)
			continue
		}
		var partial *partialTagRestore
		if errors.As(err, &partial) {
			// Every tag that still held what the operation wrote was put
			// back; the rest are later changes and stay. The row is done:
			// a retry could only overwrite those later changes.
			result.PartiallyRestored++
			result.Kept = append(result.Kept, fmt.Sprintf("change %s: %s", c.ID, partial.detail))
			revertLog.Warn("revert of change %s restored in part: %s", c.ID, partial.detail)
			err = nil
		}
		plan.Record(c, err)
		if err != nil {
			if c.ChangeType == undo.ChangeTypeBookPrimaryDemote {
				refusedDemote[c.BookID] = true
			}
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
		if already {
			result.AlreadyRestored++
		}
		if t, ok := settleNote(c); ok {
			gid := rs.bookGroup(c.BookID)
			switch {
			case !already:
				touched = append(touched, t)
				if gid != "" && !pendingNow[gid] {
					pendingNow[gid] = true
					rs.markSettlePending(operationID, gid)
				}
			case gid != "" && priorPending[gid]:
				touched = append(touched, t)
			}
		}
		if result.RestoredTypes == nil {
			result.RestoredTypes = map[string]int{}
		}
		result.RestoredTypes[c.ChangeType]++
	}

	result.Restored = len(restoredIDs)

	// One settle per version group the operation changed, after every row
	// has run (see revert_settle.go for why not per row), and BEFORE the
	// rows are marked: a mark that fails leaves the rows to be found
	// already restored by the next run, with the groups already settled.
	errMsgs = append(errMsgs, rs.settleGroups(operationID, settleInput{
		plan: plan, touched: touched, refusedDemote: refusedDemote, crowned: crownedByHandOff(changes),
		priorPending: priorPending,
	}, result)...)

	// Mark only the rows that were actually restored, and the superseded
	// ones (done, with nothing to write).
	if mark := append(append([]string(nil), restoredIDs...), markedIDs...); len(mark) > 0 {
		if err := rs.db.MarkOperationChangesReverted(operationID, mark); err != nil {
			return nil, fmt.Errorf("restored %d changes but failed to mark them reverted: %w", len(restoredIDs), err)
		}
	}

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
	return rs.revertChangeIn(c, nil)
}

// revertChangeIn reverts one row of the operation plan orders (nil: the row
// alone, with only its own soft-delete stamp and no knowledge of what the
// rest of the operation restored).
func (rs *RevertService) revertChangeIn(c *database.OperationChange, plan *undo.RevertPlan) error {
	var stamps undo.SoftDeleteStamps
	if plan != nil {
		stamps = plan.Stamps
	}
	switch c.ChangeType {
	case "file_move", "organize_rename":
		// organize_rename writes the same (OldValue, NewValue) shape as
		// file_move — old path → new path, Book.file_path updated. The
		// reversal is identical: move the file back and restore
		// Book.file_path.
		return rs.revertFileMove(c)
	case "metadata_update":
		return rs.revertMetadataUpdate(c, plan)
	case "tag_write":
		return rs.revertTagWrite(c)
	case undo.ChangeTypeSeriesRename:
		return rs.revertSeriesRename(c)
	case undo.ChangeTypeBookFileReassign:
		return rs.revertBookFileReassign(c)
	case undo.ChangeTypeBookFileRepoint:
		return rs.revertBookFileRepoint(c)
	case undo.ChangeTypeBookFileTrack:
		return rs.revertBookFileTrack(c)
	case undo.ChangeTypeBookPathUpdate:
		return rs.revertBookPathUpdate(c)
	case undo.ChangeTypeBookFileMove:
		return rs.revertBookFileMove(c)
	case undo.ChangeTypeBookSoftDelete:
		return rs.revertBookSoftDelete(c, stamps)
	case undo.ChangeTypeBookPrimaryDemote:
		return rs.revertBookPrimaryDemote(c)
	case undo.ChangeTypeBookPrimaryHandoff:
		// A note: the primary demote row's revert undoes the hand-off.
		return nil
	case undo.ChangeTypeExternalIDReassign:
		return rs.revertExternalIDReassign(c)
	case undo.ChangeTypeBookMergedInto:
		return rs.revertBookMergedInto(c)
	case undo.ChangeTypeUserStateFollow:
		return rs.revertUserStateFollow(c)
	case undo.ChangeTypeTitleRelinkCredits:
		return rs.revertTitleRelinkCredits(c)
	case undo.ChangeTypeTitleRelinkAuthorCreate:
		return rs.revertTitleRelinkAuthorCreate(c)
	case undo.ChangeTypeJunkAuthorCredits:
		return rs.revertJunkAuthorCredits(c)
	case undo.ChangeTypeJunkAuthorCreate:
		return rs.revertJunkAuthorCreate(c)
	case undo.ChangeTypeRepairBookCreate:
		return rs.revertRepairBookCreate(c)
	case undo.ChangeTypeFieldLock:
		return rs.revertFieldLock(c)
	case "organize_failed", "organize_skipped", "organize_summary":
		// No filesystem or DB mutation recorded; nothing to reverse.
		return nil
	default:
		// Unreachable from RevertOperation, which filters on undo.NotRestorableLabel.
		return fmt.Errorf("unknown change type: %s", c.ChangeType)
	}
}

// checkPairedFieldLock runs undo.CheckPairedFieldLock for a metadata_update
// row whose operation also locked that field. The lock rows come from the
// plan (every row of the operation); a lone row reverted without a plan reads
// the operation's journal.
func (rs *RevertService) checkPairedFieldLock(c *database.OperationChange, plan *undo.RevertPlan) error {
	var locks []*database.OperationChange
	if plan != nil {
		locks = plan.FieldLocksOf(c)
	} else {
		all, err := rs.db.GetOperationChanges(c.OperationID)
		if err != nil {
			return fmt.Errorf("read the journal of %s: %w", c.OperationID, err)
		}
		locks = all
	}
	if len(locks) == 0 {
		return nil
	}
	states, err := rs.db.GetMetadataFieldStates(c.BookID)
	if err != nil {
		return fmt.Errorf("read field states of %s: %w", c.BookID, err)
	}
	return undo.CheckPairedFieldLock(locks, states, c)
}

// revertFieldLock lifts the lock a repair operation set on one field, while
// the field still carries exactly that operation's lock
// (undo.CheckFieldLockCurrent): no override is already restored, and a
// person's override or lock, or another operation's lock, is refused. Only
// the lock changes; the provider value and the row's UpdatedAt stay, so the
// swapped title/author fixer's same-record check still compares the times the
// fields' values were recorded. Runs under the book's field-state stripe
// (database.LockMetadataState), like every other read-modify-write of it.
func (rs *RevertService) revertFieldLock(c *database.OperationChange) error {
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	unlock := database.LockMetadataState(c.BookID)
	defer unlock()
	states, err := rs.db.GetMetadataFieldStates(c.BookID)
	if err != nil {
		return fmt.Errorf("read field states of %s: %w", c.BookID, err)
	}
	if err := undo.CheckFieldLockCurrent(states, c); err != nil {
		return err
	}
	for i := range states {
		if states[i].Field != c.FieldName {
			continue
		}
		st := states[i]
		// Back to what the row recorded: no lock, or the earlier repair
		// operation's lock this one took over.
		st.OverrideLocked, st.LockSource = false, ""
		if src, taken := undo.FieldLockTakenSource(c.OldValue); taken {
			st.OverrideLocked, st.LockSource = true, src
		}
		if err := rs.db.UpsertMetadataFieldState(&st); err != nil {
			return fmt.Errorf("unlock %s of %s: %w", c.FieldName, c.BookID, err)
		}
		return nil
	}
	return undo.ErrAlreadyRestored
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
	// Exclusive, like the move it undoes: the Lstat above is a check, and a
	// file landing at OldValue after it must not be overwritten.
	if err := organizer.MoveExclusive(c.NewValue, c.OldValue); err != nil {
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
	// Not found is (nil, nil); an error is a failed read, never "absent".
	onTarget, err := rs.db.GetBookFileByID(c.BookID, fileID)
	if err != nil {
		return fmt.Errorf("read book_file %s on %s: %w", fileID, c.BookID, err)
	}
	onSource, err := rs.db.GetBookFileByID(c.OldValue, fileID)
	if err != nil {
		return fmt.Errorf("read book_file %s on %s: %w", fileID, c.OldValue, err)
	}
	if err := undo.CheckReassignCurrent(onTarget != nil, onSource != nil, c); err != nil {
		return err
	}
	if err := rs.db.MoveBookFilesToBook([]string{fileID}, c.BookID, c.OldValue); err != nil {
		return fmt.Errorf("move book_file %s from %s back to %s: %w", fileID, c.BookID, c.OldValue, err)
	}
	return nil
}

// partialTagRestore reports a snapshot tag_write row put back only in part:
// the kept properties changed since the operation. RevertOperation counts the
// row restored and reports detail.
type partialTagRestore struct{ detail string }

// supersededRestore is a row's revert that wrote nothing because what it
// would restore no longer applies (see RevertResult.Superseded). The row is
// marked reverted and counted Superseded, never Restored.
type supersededRestore struct{ detail string }

func (s *supersededRestore) Error() string { return "superseded: " + s.detail }

func (p *partialTagRestore) Error() string { return "tag restored in part: " + p.detail }

// revertTagSnapshot is revertTagWrite for a per-property snapshot row. The
// caller holds the path lock and has checked the file exists and is not
// protected.
func (rs *RevertService) revertTagSnapshot(c *database.OperationChange, tag, target string) error {
	current, err := rs.ReadTags(target)
	if err != nil {
		return fmt.Errorf("tag %s not restored: the current tags of %s cannot be read: %w", tag, target, err)
	}
	cur, known := current[tag]
	if !known {
		// ReadTagProperties leaves a key out when one of its properties
		// holds several values: none the organize wrote, so something
		// changed since, and one string per property cannot compare it.
		return driftRefusal("tag %s of %s now holds several values in one property, not %q as the organize wrote it; the later value is kept", tag, target, c.NewValue)
	}
	plan, err := metadata.PlanSnapshotRestore(tag, c.OldValue, c.NewValue, cur)
	if err != nil {
		return fmt.Errorf("tag %s not restored: %w", tag, err)
	}
	if len(plan.Restored) == 0 && len(plan.Already) == 0 {
		return driftRefusal("tag %s of %s: %s changed since the organize wrote %q; the later values are kept",
			tag, target, strings.Join(plan.Kept, ", "), c.NewValue)
	}
	if plan.Write != "" {
		if err := rs.WriteTags(target, map[string]any{tag: plan.Write}); err != nil {
			return fmt.Errorf("failed to write tag %s back to %s: %w", tag, target, err)
		}
	}
	if len(plan.Kept) > 0 {
		return &partialTagRestore{detail: fmt.Sprintf("tag %s of %s: restored %s; kept %s, changed since the organize wrote %q",
			tag, target, strings.Join(append(plan.Restored, plan.Already...), ", "), strings.Join(plan.Kept, ", "), c.NewValue)}
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
		// Already the old number (a journaled step never written) is no
		// drift; anything else is.
		return undo.CheckTrackCurrent(f.TrackNumber, c)
	}
	f.TrackNumber = old
	if err := rs.db.UpdateBookFile(f.ID, f); err != nil {
		return fmt.Errorf("restore track of book_file %s: %w", fileID, err)
	}
	return nil
}

// revertBookFileRepoint puts a book_file row back at the location it had
// before a repoint that moved nothing on disk: path, Missing flag, hash and
// size, exactly as recorded. Compare-and-set: the row must still hold every
// field of the location the repoint wrote, or the revert is refused as drift.
// No file is touched.
func (rs *RevertService) revertBookFileRepoint(c *database.OperationChange) error {
	fileID, ok := undo.BookFileIDFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no book_file id in field %q", c.FieldName)
	}
	was, err := undo.DecodeBookFileLocation(c.OldValue)
	if err != nil {
		return err
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	f, err := rs.db.GetBookFileByID(c.BookID, fileID)
	if err != nil || f == nil {
		return driftRefusal("book_file %s is no longer on book %s (err=%v)", fileID, c.BookID, err)
	}
	// Path and Missing only (undo.CheckRepointCurrent): a hash or size a scan
	// filled in since does not make the row someone else's; a row already back
	// where it was (a journaled repoint never written) is already restored.
	if err := undo.CheckRepointCurrent(f, c); err != nil {
		return err
	}
	vacated := f.FilePath
	was.Apply(f)
	if err := rs.db.UpdateBookFile(f.ID, f); err != nil {
		return fmt.Errorf("restore location of book_file %s: %w", fileID, err)
	}
	if vacated == f.FilePath {
		return nil
	}
	// A repoint at a file another row also names (the fragment fixer's moved
	// class, the duplicate-copies fixer's repoint onto a copy's present twin)
	// took the single-owner book_file_path key from that row; moving this row
	// off the path dropped the key. Hand it back so GetBookFileByPath names a
	// row at the path again.
	set, err := undo.DecodeBookFileLocation(c.NewValue)
	if err != nil {
		return err
	}
	atPath, _ := rs.db.(bookFilesAtPathReader)
	return rs.reindexVacatedPath(atPath, vacated, f.ID, set.KeyOwnerBook, set.KeyOwnerRow)
}

// reindexVacatedPath points the single-owner book_file_path key at one other
// row at path again after the row except left the path. Only the key is
// written (ClaimBookFilePathKey): no row write, no aggregate recompute, no
// change notification, so the key can go back to an iTunes copy's row
// without writing to the iTunes book.
//
// The row is the key's owner the repoint journaled (ownerBook/ownerRow),
// while it is still at path, iTunes or not, live or retired since. A repoint
// journaled before the owner was recorded, or an owner since gone or moved,
// falls back to the rows at path: a live book's first, then a retired one's.
// The fallback never picks an iTunes row (a row or book iTunes id, a row
// iTunes path), which it cannot tell was the owner; nor does anything act on
// a path under books/itunes/**. With no eligible row the key stays dropped.
func (rs *RevertService) reindexVacatedPath(atPath bookFilesAtPathReader, path, except, ownerBook, ownerRow string) error {
	if pathutil.UnderFrozenITunesTree(path) {
		return nil
	}
	var cands []database.BookFile
	if ownerRow != "" && ownerRow != except {
		o, err := rs.db.GetBookFileByID(ownerBook, ownerRow)
		if err != nil {
			return fmt.Errorf("read the path key's owner %s: %w", ownerRow, err)
		}
		if o != nil {
			cands = append(cands, *o)
		}
	}
	if atPath != nil {
		others, err := atPath.BookFilesAtPath(path)
		if err != nil {
			return fmt.Errorf("rows at %s: %w", path, err)
		}
		cands = append(cands, others...)
	}
	var owner, live, hidden []database.BookFile
	seen := map[string]bool{}
	for i := range cands {
		o := cands[i]
		if o.ID == except || seen[o.ID] || o.FilePath != path {
			continue
		}
		seen[o.ID] = true
		b, err := rs.db.GetBookByID(o.BookID)
		if err != nil {
			return fmt.Errorf("read %s: %w", o.BookID, err)
		}
		itunes := o.ITunesPersistentID != "" || o.ITunesPath != "" || (b != nil && b.ITunesPersistentID != nil && *b.ITunesPersistentID != "")
		switch {
		case o.ID == ownerRow && b != nil:
			owner = append(owner, o) // the key's owner before, iTunes or not, live or retired since
		case itunes:
		case b != nil && !b.IsSoftDeleted():
			live = append(live, o)
		default:
			hidden = append(hidden, o)
		}
	}
	for _, o := range append(append(owner, live...), hidden...) {
		claimed, err := rs.db.ClaimBookFilePathKey(o.BookID, o.ID, path)
		if err != nil {
			return fmt.Errorf("re-index %s at %s: %w", o.ID, path, err)
		}
		if claimed {
			return nil
		}
	}
	return nil
}

// revertBookMergedInto puts a retired book's merged_into_book_id back
// (OldValue "" is unset) while it still names the book the operation set.
func (rs *RevertService) revertBookMergedInto(c *database.OperationChange) error {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if err := undo.CheckMergedIntoCurrent(book, c); err != nil {
			return err
		}
		if c.OldValue == "" {
			book.MergedIntoBookID = nil
		} else {
			v := c.OldValue
			book.MergedIntoBookID = &v
		}
		return nil
	})
}

// revertUserStateFollow puts every user's listening state back from a
// journaled follow (merge.RestoreFollowedProgress): the retired book's state
// and positions are written back, and the survivor's are restored only while
// they still hold what the follow left. A store without the user-progress
// surface refuses the row rather than drop the progress silently.
func (rs *RevertService) revertUserStateFollow(c *database.OperationChange) error {
	survivor, ok := undo.SurvivorFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no survivor in field %q", c.FieldName)
	}
	rec, err := undo.DecodeUserStateFollow(c.NewValue)
	if err != nil {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable, Detail: err.Error()}
	}
	var progress []merge.CombineUserProgress
	if err := json.Unmarshal(rec.Progress, &progress); err != nil {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable, Detail: fmt.Sprintf("progress: %v", err)}
	}
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	if _, err := rs.loadBook(survivor); err != nil {
		return err
	}
	db, ok := database.AsCapability[merge.UserProgressMerger](rs.db)
	if !ok {
		return fmt.Errorf("store cannot restore user progress; %d user(s) left on %s", len(progress), survivor)
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	warns, err := merge.RestoreFollowedProgress(db, survivor, c.BookID, rec.SyncRedirected, progress)
	for _, w := range warns {
		revertLog.Warn("revert of change %s: %s", c.ID, logger.SanitizeLogValue(w))
	}
	return err
}

// revertBookPathUpdate restores a book's file_path that was changed with
// nothing moved on disk (unlike file_move, which moves the file back too), but
// only while the path is still the one the operation set. The check and the
// write both run on the row as read under the book's write stripe.
func (rs *RevertService) revertBookPathUpdate(c *database.OperationChange) error {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if err := undo.CheckPathUpdateCurrent(book, c); err != nil {
			return err
		}
		book.FilePath = c.OldValue
		return nil
	})
}

// revertBookFileMove moves one file of a multi-file in-place organize back
// from NewValue to OldValue and repoints its book_file row. Compare-and-set on
// every side: the row must still name NewValue, the file must be there, and
// OldValue must be free; otherwise nothing moves and the row is refused. A
// failed row write moves the file forward again, so the row and the disk never
// disagree. Nothing is deleted.
func (rs *RevertService) revertBookFileMove(c *database.OperationChange) error {
	fileID, ok := undo.BookFileIDFromField(c.FieldName)
	if !ok {
		return fmt.Errorf("no book_file id in field %q", c.FieldName)
	}
	release := rs.lockPaths(c.NewValue, c.OldValue)
	defer release()
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	bf, err := rs.db.GetBookFileByID(c.BookID, fileID)
	if err != nil || bf == nil {
		return driftRefusal("book_file %s is no longer on book %s", fileID, c.BookID)
	}
	if bf.FilePath != c.NewValue {
		if bf.FilePath == c.OldValue {
			return nil // already moved back
		}
		return driftRefusal("book_file %s path is %q, not %q as the operation left it", fileID, bf.FilePath, c.NewValue)
	}
	if _, err := os.Lstat(c.NewValue); err != nil {
		// Two rows at one path moved as one file. The first row's revert
		// put the file back; this row only needs repointing, and only when
		// a sibling row of the same book already names OldValue.
		if rs.siblingRowAt(c.BookID, bf.ID, c.OldValue) {
			bf.FilePath = c.OldValue
			if err := rs.db.UpdateBookFile(bf.ID, bf); err != nil {
				return fmt.Errorf("repoint duplicate book_file %s: %w", bf.ID, err)
			}
			return nil
		}
		return fmt.Errorf("file is no longer at %s; nothing moved back", c.NewValue)
	}
	if _, err := os.Lstat(c.OldValue); err == nil {
		return fmt.Errorf("%s is occupied again; refusing to move %s over it", c.OldValue, c.NewValue)
	}
	if err := os.MkdirAll(filepath.Dir(c.OldValue), 0o775); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(c.OldValue), err)
	}
	if err := os.Rename(c.NewValue, c.OldValue); err != nil {
		return fmt.Errorf("failed to move file back from %s to %s: %w", c.NewValue, c.OldValue, err)
	}
	bf.FilePath = c.OldValue
	if bf.ITunesPath != "" && rs.ComputeITunesPath != nil {
		if ip := rs.ComputeITunesPath(c.OldValue); ip != "" {
			bf.ITunesPath = ip
		}
	}
	if err := rs.db.UpdateBookFile(bf.ID, bf); err != nil {
		if rbErr := organizer.MoveExclusive(c.OldValue, c.NewValue); rbErr != nil {
			return fmt.Errorf("repoint book_file %s: %w; moving the file forward again also failed: %v", bf.ID, err, rbErr)
		}
		return fmt.Errorf("repoint book_file %s: %w", bf.ID, err)
	}
	return nil
}

// siblingRowAt reports whether book bookID has a book_file row other than
// exceptID at path.
func (rs *RevertService) siblingRowAt(bookID, exceptID, path string) bool {
	rows, err := rs.db.GetBookFiles(bookID)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if r.ID != exceptID && r.FilePath == path {
			return true
		}
	}
	return false
}

// revertBookSoftDelete clears a book's deletion mark. A book purged since is
// refused; one already restored is left as it is.
//
// The library state follows the shared restore rule
// (database.RestoreLibraryStateFromTrash): a trash path that labelled the row
// "deleted" (reconcile's) left it labelled after the revert until 2026-10-01.
// MergedIntoBookID is NOT cleared here, unlike a user restore: an operation
// that set it journals that change itself, and reverting it restores the
// value the book had before the operation, which may be a pointer that
// predates it.
func (rs *RevertService) revertBookSoftDelete(c *database.OperationChange, stamps undo.SoftDeleteStamps) error {
	files, err := rs.db.GetBookFiles(c.BookID)
	if err != nil {
		return fmt.Errorf("read files of %s: %w", c.BookID, err)
	}
	env := merge.TrashRestoreEnv()
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	// The group's incumbent is read and the yield written under the group's
	// lock (merge lock, then group lock, then the book's write lock, the
	// order merge.RestoreFromTrash uses), so no hand-off changes the group
	// in between.
	cur, err := rs.db.GetBookByID(c.BookID)
	if err != nil {
		return fmt.Errorf("read %s: %w", c.BookID, err)
	}
	gid, incumbent := "", ""
	if cur != nil && cur.VersionGroupID != nil {
		gid = strings.TrimSpace(*cur.VersionGroupID)
	}
	unlock := func() {}
	if gid != "" {
		unlock = versionprimary.LockGroup(gid)
		if incumbent, err = versionprimary.IncumbentExcept(rs.db, gid, c.BookID); err != nil {
			unlock()
			return err
		}
	}
	err = rs.modifyBook(c.BookID, func(book *database.Book) error {
		// A live book is already restored; a stamped row is reverted only
		// while the book carries a stamp this operation journaled for it
		// (undo.CheckSoftDeleteCurrent).
		if err := undo.CheckSoftDeleteCurrent(book, c, stamps); err != nil {
			return err
		}
		notMarked := false
		book.MarkedForDeletion = &notMarked
		book.MarkedForDeletionAt = nil
		database.RestoreLibraryStateFromTrash(book, files, env)
		// Its group has had another primary since it was retired (a
		// retire's hand-off): it comes back non-primary, as a trash restore
		// does (versionprimary.YieldToIncumbent), so a stale true, or a nil
		// read as primary, never doubles the group. A demote revert later
		// in the ledger (same operation) re-crowns it deliberately.
		if incumbent != "" && book.VersionGroupID != nil && strings.TrimSpace(*book.VersionGroupID) == gid {
			versionprimary.YieldToIncumbent(book, incumbent)
			if book.IsPrimaryVersion == nil {
				no := false
				book.IsPrimaryVersion = &no
			}
		}
		// A row that comes back as the loser of a live merge survivor is
		// never a primary: Crown and EnsureSinglePrimary skip it (not
		// Electable), so a true it carries (a demote revert that wrote it
		// while the book was retired) would stay, and ABSLibraryFilter,
		// which does not read MergedIntoBookID, would list it next to the
		// real primary. The survivor is read inside the callback, as close
		// to the write as it can be; holding this book's stripe does not
		// stop the survivor being trashed meanwhile (a batch soft-delete or
		// DeleteBook takes no lock this one waits on), so this narrows the
		// window rather than closing it.
		if book.MergedIntoBookID != nil && *book.MergedIntoBookID != "" && versionprimary.StoreAlive(rs.db)(*book.MergedIntoBookID) {
			no := false
			book.IsPrimaryVersion = &no
		}
		return nil
	})
	// The group's primary is settled once, after every row of the
	// operation has run (settleGroups).
	unlock()
	return err
}

// primaryFlagString renders a primary flag for a message: "nil", "true" or
// "false".
func primaryFlagString(v *bool) string {
	if v == nil {
		return "nil"
	}
	return strconv.FormatBool(*v)
}

// revertBookPrimaryDemote restores a retired book's primary flag ("" is
// nil, the never-set shape, which reads as primary) while it is still the
// false the operation wrote. It writes only the flag: re-crowning the book
// over the sibling the operation handed the group to is the settle pass's,
// once every row has run (settleGroups crowns the book whose demote this
// run reverted), so a nil written here never stays beside that sibling's
// true, and no later row of the same book finds a flag it did not expect.
//
// A LIVE book merged into a live survivor is left as it is: such a loser is
// not Electable, and an explicit true on it would put it in the ABS library
// (ABSLibraryFilter does not read MergedIntoBookID) next to the group's
// primary. The row is marked reverted as Superseded (supersededRestore),
// never Restored.
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
	crowns := restored == nil || *restored
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		live := !book.IsSoftDeleted()
		if err := undo.CheckPrimaryDemoteCurrent(book, c); err != nil {
			return err
		}
		// The survivor's liveness is read inside the callback, as close to
		// the write as it can be. It is not serialized against trashing the
		// survivor (a batch soft-delete or DeleteBook takes neither the
		// merge lock nor this book's stripe), so this narrows the window in
		// which a stale answer could be acted on; it does not close it.
		if crowns && live && book.MergedIntoBookID != nil && *book.MergedIntoBookID != "" &&
			versionprimary.StoreAlive(rs.db)(*book.MergedIntoBookID) {
			return &supersededRestore{detail: fmt.Sprintf("book %s is merged into live book %s; its primary flag is left %s",
				c.BookID, *book.MergedIntoBookID, primaryFlagString(book.IsPrimaryVersion))}
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
	if err := undo.CheckExternalIDOwnerCurrent(owner, c); err != nil {
		return err
	}
	if err := rs.db.ReassignExternalID(source, extID, c.OldValue); err != nil {
		return fmt.Errorf("move external id %s/%s back to %s: %w", source, extID, c.OldValue, err)
	}
	return nil
}

func (rs *RevertService) revertMetadataUpdate(c *database.OperationChange, plan *undo.RevertPlan) error {
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
	// A repair that wrote a field also locked it (undo.ChangeTypeFieldLock).
	// The value goes back only while that lock is still the repair's or
	// gone: one a person took over makes the value theirs
	// (undo.CheckPairedFieldLock). The lock row is newer, so in this run it
	// was lifted first when it was still the repair's.
	if err := rs.checkPairedFieldLock(c, plan); err != nil {
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
// It is a compare-and-set on the file properties the tag key names
// (metadata.TagProperty), under the per-path write lock: the file must still
// hold what the organize wrote (NewValue). A later write-back or apply that
// changed the tag is kept and the row refused as changed since. A file that
// already holds the pre-organize value is left alone and the row counts as
// restored. A tag that was absent before (undo.TagAbsentValue) is removed. A
// key with no property fails the row: nothing is written for it.
//
// A per-property snapshot row (artist: ARTIST and ALBUMARTIST; narrator:
// NARRATOR and PERFORMER) is compared property by property
// (metadata.PlanSnapshotRestore): each property still holding what the
// organize wrote is put back or removed, one already in its pre-write state
// is left alone, and one changed since is kept and reported
// (partialTagRestore) instead of refusing the whole row. Only when every
// property changed since is the row refused. The plain-value comparison could
// not do this: it read artist only when ARTIST and ALBUMARTIST agreed, so an
// ALBUMARTIST changed by another tool, or an ALBUMARTIST absent before the
// organize on a second revert, refused the row.
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
		return fmt.Errorf("tag %s not restored: it maps to no file property the revert can write", tag)
	}
	// A plain pre-write value for a key that now owns several properties was
	// recorded before per-property snapshots, by an organize that wrote artist
	// to ARTIST only and narrator to NARRATOR only. It is compared and written
	// back through that one property, so ALBUMARTIST, COMPOSER and PERFORMER
	// are left as they are.
	if !metadata.IsTagSnapshot(c.OldValue) {
		if legacy, ok := metadata.LegacyTagKey(tag); ok {
			tag = legacy
		}
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

	if metadata.IsTagSnapshot(c.OldValue) {
		return rs.revertTagSnapshot(c, tag, target)
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

// revertTitleRelinkCredits puts back a book's author credits as they were
// before author-strip-merge's title-as-author relink moved them: the junction
// exactly as journaled (order, roles, co-authors) and the primary AuthorID.
//
// Compare-and-set on both: the junction is replaced only while it still
// carries the real author the relink wrote and not the junk row it removed
// (undo.CheckTitleRelinkCreditsCurrent, under the book's author lock via
// ModifyBookAuthors), and the primary only while it names that real author or
// is already the journaled value. Anything else is a later change and fails
// the row as changed-since.
//
// The junk author row itself may have been deleted by the same run; its
// credit is restored regardless (the row id is what the book had), and
// author-id-repair / a re-run of the op handle a credit to a deleted row.
func (rs *RevertService) revertTitleRelinkCredits(c *database.OperationChange) error {
	snap, move, err := undo.DecodeTitleRelinkCredits(c)
	if err != nil {
		return err
	}
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	var primary *database.Author
	if snap.AuthorID != nil {
		a, err := rs.db.GetAuthorByID(*snap.AuthorID)
		if err != nil {
			return fmt.Errorf("load journaled primary author %d: %w", *snap.AuthorID, err)
		}
		primary = a
	}

	if _, err := rs.db.ModifyBookAuthors(c.BookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		if sameCredits(cur, snap.Credits) {
			return nil, database.ErrSkipBookAuthorsWrite // an earlier revert got this far
		}
		if err := undo.CheckTitleRelinkCreditsCurrent(c.BookID, cur, move); err != nil {
			return nil, err
		}

		out := make([]database.BookAuthor, len(snap.Credits))
		copy(out, snap.Credits)
		return out, nil
	}); err != nil && !errors.Is(err, database.ErrSkipBookAuthorsWrite) {
		return err
	}
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		switch {
		case sameIntPtr(book.AuthorID, snap.AuthorID):
			return database.ErrSkipBookWrite
		case book.AuthorID == nil || *book.AuthorID != move.IntoAuthorID:
			return driftRefusal("book %s primary author changed since the relink", book.ID)
		}
		book.AuthorID = nil
		book.Author = nil
		if snap.AuthorID != nil {
			id := *snap.AuthorID
			book.AuthorID = &id
			book.Author = primary
		}
		return nil
	})
}

// revertJunkAuthorCredits puts back the junction and primary a junk-author
// repair replaced. Both are compare-and-set against EXACTLY what the repair
// wrote (the row journals the post-write junction): a credit fixed by hand, a
// co-author added or the junk row re-added since is a later change, and the
// row is refused with nothing written. The primary is checked BEFORE the
// junction is touched, so a changed primary never leaves a half-reverted
// book; both writes then re-check under their own locks. A book already back
// at the snapshot counts as restored.
func (rs *RevertService) revertJunkAuthorCredits(c *database.OperationChange) error {
	snap, move, err := undo.DecodeJunkAuthorCredits(c)
	if err != nil {
		return err
	}
	book, err := rs.loadBook(c.BookID)
	if err != nil {
		return err
	}
	var primary *database.Author
	if snap.AuthorID != nil {
		a, err := rs.db.GetAuthorByID(*snap.AuthorID)
		if err != nil {
			return fmt.Errorf("load journaled primary author %d: %w", *snap.AuthorID, err)
		}
		primary = a
	}
	primaryDone := false
	if perr := undo.CheckJunkAuthorPrimaryCurrent(c.BookID, book.AuthorID, snap, move); perr != nil {
		if !errors.Is(perr, undo.ErrAlreadyRestored) {
			return perr
		}
		primaryDone = true
	}
	// The junction: exactly the repair's (or already the snapshot's), under
	// the author lock. A primary already back with the junction still the
	// repair's is a hand edit, not the revert's to finish.
	if _, err := rs.db.ModifyBookAuthors(c.BookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		if undo.SameJunction(cur, snap.Credits) {
			return nil, database.ErrSkipBookAuthorsWrite
		}
		if err := undo.CheckJunkAuthorCreditsCurrent(c.BookID, cur, move); err != nil {
			return nil, err
		}
		if primaryDone {
			return nil, driftRefusal("book %s primary author changed since the junk-author repair", c.BookID)
		}
		out := make([]database.BookAuthor, len(snap.Credits))
		copy(out, snap.Credits)
		return out, nil
	}); err != nil && !errors.Is(err, database.ErrSkipBookAuthorsWrite) {
		return err
	}
	if !move.PrimaryChanged || primaryDone {
		return nil
	}
	return rs.modifyBook(c.BookID, func(book *database.Book) error {
		if perr := undo.CheckJunkAuthorPrimaryCurrent(c.BookID, book.AuthorID, snap, move); perr != nil {
			if errors.Is(perr, undo.ErrAlreadyRestored) {
				return database.ErrSkipBookWrite
			}
			return perr
		}
		book.AuthorID = nil
		book.Author = nil
		if snap.AuthorID != nil {
			id := *snap.AuthorID
			book.AuthorID = &id
			book.Author = primary
		}
		return nil
	})
}

// revertJunkAuthorCreate removes the author row a junk-author repair created,
// by the ID the row journaled -- never another row of the same name -- and
// only while nothing credits it and it still has the name it was created
// with (a renamed row is someone's since). Rows are reverted newest first, so
// the credit rows that used it are put back before this runs.
func (rs *RevertService) revertJunkAuthorCreate(c *database.OperationChange) error {
	v, err := undo.DecodeJunkAuthorCreate(c)
	if err != nil {
		return err
	}
	a, err := rs.db.GetAuthorByID(v.AuthorID)
	if err != nil {
		return fmt.Errorf("look up created author %d: %w", v.AuthorID, err)
	}
	if a == nil || a.ID != v.AuthorID {
		return nil // already gone (or merged away)
	}
	if a.Name != v.Name {
		revertLog.Info("revert kept author %d: renamed since the repair created it", a.ID)
		return nil
	}
	books, err := rs.db.GetBooksByAuthorIDForRelinkCore(a.ID)
	if err != nil {
		return fmt.Errorf("read credits of created author %d: %w", a.ID, err)
	}
	if len(books) > 0 {
		revertLog.Info("revert kept author %d %q: still credited on %d books", a.ID, logger.SanitizeLogValue(a.Name), len(books))
		return nil
	}
	if err := rs.db.DeleteAuthor(a.ID); err != nil {
		return fmt.Errorf("delete created author %d: %w", a.ID, err)
	}
	return nil
}

// revertRepairBookCreate soft-deletes a book a Repairs fixer created
// (undo.ChangeTypeRepairBookCreate), only while it is still only what the
// operation made (undo.CheckRepairBookCreate). Its rows stay. The
// single-owner book_file_path index then still names the hidden book's rows
// (last writer wins), so each path is handed back to a live book's row there
// by re-writing that row. The re-index runs on every path out, the
// already-hidden one included (a book hidden by an earlier revert that was
// cut off before its re-index, or by an apply cut off before its un-hide),
// and it is idempotent; a store that cannot list a path's rows refuses
// before anything is written.
func (rs *RevertService) revertRepairBookCreate(c *database.OperationChange) error {
	st, ok := rs.db.(undo.RepairBookCreateStore)
	if !ok {
		return fmt.Errorf("revert repair_book_create %s: this store cannot check the created book", c.ID)
	}
	atPath, ok := rs.db.(bookFilesAtPathReader)
	if !ok {
		return fmt.Errorf("revert repair_book_create %s: this store cannot list a path's rows", c.ID)
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	if err := undo.CheckRepairBookCreate(st, c); err != nil {
		if errors.Is(err, undo.ErrAlreadyRestored) {
			return rs.repointPathIndexAwayFrom(atPath, c.BookID)
		}
		return err
	}
	_, err := rs.db.ModifyBook(c.BookID, func(book *database.Book) error {
		if book.IsSoftDeleted() {
			return database.ErrSkipBookWrite
		}
		t := true
		now := time.Now().UTC()
		book.MarkedForDeletion = &t
		book.MarkedForDeletionAt = &now
		return nil
	})
	if err != nil && !errors.Is(err, database.ErrSkipBookWrite) {
		return err
	}
	return rs.repointPathIndexAwayFrom(atPath, c.BookID)
}

// bookFilesAtPathReader is the multi-valued path read the re-index needs.
type bookFilesAtPathReader interface {
	BookFilesAtPath(path string) ([]database.BookFile, error)
}

// repointPathIndexAwayFrom re-writes, for each row of the hidden book
// bookID, a live book's row at the same path, so GetBookFileByPath names the
// live row again. A path no live book holds keeps the hidden row. A book
// still live is left alone (nothing hidden to re-index away from); a book
// that is gone still has its rows re-indexed away from.
func (rs *RevertService) repointPathIndexAwayFrom(atPath bookFilesAtPathReader, bookID string) error {
	hidden, err := rs.db.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("read hidden book %s: %w", bookID, err)
	}
	if hidden != nil && !hidden.IsSoftDeleted() {
		return nil
	}
	rows, err := rs.db.GetBookFiles(bookID)
	if err != nil {
		return fmt.Errorf("rows of hidden book %s: %w", bookID, err)
	}
	for _, r := range rows {
		others, err := atPath.BookFilesAtPath(r.FilePath)
		if err != nil {
			return fmt.Errorf("rows at %s: %w", r.FilePath, err)
		}
		// Every live candidate gone or moved off the path leaves no live book
		// holding it, the "keeps the hidden row" case above: not an error.
		for i := range others {
			o := others[i]
			if o.BookID == bookID {
				continue
			}
			b, err := rs.db.GetBookByID(o.BookID)
			if err != nil {
				return fmt.Errorf("read %s: %w", o.BookID, err)
			}
			if b == nil || b.IsSoftDeleted() {
				continue
			}
			// A no-op ModifyBookFile re-reads the row under its stripe and
			// re-writes it as stored, indexes included: the key moves to it
			// without overwriting a concurrent writer's update with the
			// snapshot BookFilesAtPath returned. A row that is gone (nil) or
			// that a writer outside the merge lock moved off the path is
			// skipped for the next live candidate.
			want := r.FilePath
			got, err := rs.db.ModifyBookFile(o.BookID, o.ID, func(bf *database.BookFile) error {
				if bf.FilePath != want {
					return errRepointRowMoved
				}
				return nil
			})
			if errors.Is(err, errRepointRowMoved) || (err == nil && got == nil) {
				continue
			}
			if err != nil {
				return fmt.Errorf("re-index %s at %s: %w", o.ID, r.FilePath, err)
			}
			break
		}
	}
	return nil
}

// errRepointRowMoved: a candidate row no longer names the path being
// re-indexed.
var errRepointRowMoved = errors.New("row moved off the path")

// revertTitleRelinkAuthorCreate removes an author row the relink created,
// only when nothing credits it any more. Rows are reverted newest first, so
// the credit rows the relink wrote for this author are put back before this
// runs; an author still credited (a later link, or a book whose credit revert
// was refused) is KEPT, and the row counts restored because there is nothing
// safe left to undo. The row carries the name, not the id (the id did not
// exist when it was journaled), so the author is found by name.
func (rs *RevertService) revertTitleRelinkAuthorCreate(c *database.OperationChange) error {
	a, err := rs.db.GetAuthorByName(c.NewValue)
	if err != nil {
		return fmt.Errorf("look up created author %q: %w", c.NewValue, err)
	}
	if a == nil || a.ID <= 0 {
		return nil // already gone
	}
	books, err := rs.db.GetBooksByAuthorIDForRelinkCore(a.ID)
	if err != nil {
		return fmt.Errorf("read credits of created author %d: %w", a.ID, err)
	}
	if len(books) > 0 {
		revertLog.Info("revert kept author %d %q: still credited on %d books", a.ID, logger.SanitizeLogValue(a.Name), len(books))
		return nil
	}
	if err := rs.db.DeleteAuthor(a.ID); err != nil {
		return fmt.Errorf("delete created author %d: %w", a.ID, err)
	}
	return nil
}

func sameCredits(a, b []database.BookAuthor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].AuthorID != b[i].AuthorID || a[i].Role != b[i].Role || a[i].Position != b[i].Position {
			return false
		}
	}
	return true
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
