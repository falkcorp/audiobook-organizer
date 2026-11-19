// file: internal/reconcile/apply_file_rows.go
// version: 1.2.0
// guid: 079ffa9c-f1f8-4388-a615-f9875fd5419f
// last-edited: 2026-11-18

package reconcile

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The file-row half of ExecuteReconcile.
//
// A confirmed match points a book at the file the scan found for it. The book
// row's file_path is only one of the places that names the file: the book's
// book_file row does too, and every reader that goes through the rows (the
// player, the Files tab, the missing-file flag, the single-owner path index)
// reads THAT. Until 2026-11-18 the apply wrote the book row alone, so a
// repointed book kept a row at the old path, still flagged Missing, and the
// matched file stayed an "untracked" file as far as the path index could tell.
// The row is now repointed in the same apply, never deleted: its path, Missing
// flag and size are the matched file's, and its hash is cleared (see
// fileRowMove.to).

// errFileRowChanged is a file row that no longer is what the apply read: moved
// to another path, flagged differently, or gone.
var errFileRowChanged = errors.New("file row changed since it was read")

// errBookRepointed is the book write's own refusal: another writer set the
// book's path after the plan read it. The callback saw that under the book's
// write lock and returned before writing, so nothing of this match landed.
var errBookRepointed = errors.New("book path changed since it was read")

// errBookTrashed is the book write's refusal of a book moved to the trash after
// the plan read it. Like errBookRepointed the callback returns it under the
// book's write lock before writing, so nothing of this match landed; it has its
// own sentinel because the advice for it differs (the book is not repointed by
// another writer, it is in the trash).
var errBookTrashed = errors.New("book was moved to the trash since it was read")

// errBookHashesChanged is the book write's refusal when the book's current-file
// hashes are no longer the ones the apply journaled as cleared: clearing them
// would overwrite a hash another writer set. Nothing landed.
var errBookHashesChanged = errors.New("book hashes changed since they were read")

// errTargetHeld is the row write's refusal when, at the write, a row already
// holds the matched file: repointing would take the single-owner path key from
// it, and two books would hold one file. Nothing landed.
var errTargetHeld = errors.New("matched file is now held by another file row")

// matchTypeHash is the scan's match type for a book found by its current-file
// hash (ReconcileMatch.MatchType). It is the only match that proves the matched
// file's content is what the book's hashes name (keepsHashes).
const matchTypeHash = "hash"

// The book hash fields a non-hash match clears, as their metadata_update rows
// name them (restorable: undo.RestoreBookField). The Missing files fixer
// journals the same rows (rmFieldFileHash, rmFieldOrganizedHash).
const (
	fieldFileHash      = "file_hash"
	fieldOrganizedHash = "organized_file_hash"
)

// keepsHashes reports whether a match of this type proves the matched file is the
// content the book's and its row's hashes name. Only a hash match does: the scan
// found the file BY Book.FileHash. "original_hash" finds the file the book was
// imported from, which the current-file hashes do not name, a filename match
// proves nothing about content, and an item that carries no type (a caller that
// predates the field) is treated as unproven. Those hashes are cleared in the
// same writes that repoint the book and its row, so dedup's exact-hash
// collectors never pair the book on content it no longer points at. The type is
// the confirming caller's claim about the scan's match, not something the apply
// re-derives (that would hash every matched file): an unproven claim only ever
// clears, and only an explicit "hash" keeps. Book.OriginalFileHash is never
// cleared, as the fixer's rmKeepsHashes explains.
func keepsHashes(matchType string) bool { return matchType == matchTypeHash }

// fileRowStore is what repointing a file row needs beyond Store. Store does not
// carry it (Store only narrows, TestStoreFlattenedWidth); ExecuteReconcile
// resolves it with database.AsCapability and refuses a match that needs a row
// move when the store cannot do one, instead of moving the book alone.
type fileRowStore interface {
	database.BookFilePathLookup
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// fileRowMove is the one book_file row a match repoints: bookID's row fileID,
// from location was to location to.
type fileRowMove struct {
	rows           fileRowStore
	bookID, fileID string
	was, to        database.BookFileLocation
}

// applyPlan is what planFileRow decided for one match besides the book: move is
// the row to repoint (nil when none needs to), and rowAtNew is the row already at
// the matched file (the stranded state a cut-off earlier apply leaves). prior is
// the book's live journal rows written by the apply (no Source), read only when
// rowAtNew is set: the evidence a stranded match leaves, and the rows finishing
// it hands over (handOffRows).
type applyPlan struct {
	move     *fileRowMove
	rowAtNew *database.BookFile
	prior    []*database.OperationChange
}

// planFileRow decides, with no write, what the match moves besides the book. No
// row needs to move when the book has no file rows (the match repoints the book
// alone, as it always did), or when its row is already at newPath (a cut-off
// earlier apply, or a file the organizer moved, wrote the row and not the book;
// rowAtNew is then set). Otherwise plan.move is the row to repoint.
//
// A book with rows is repointed only when exactly one of them is at the book's
// current path and none is at newPath. The one other shape accepted is the
// stranded state, a row already at newPath and none at the book's path: for a
// book whose only row it is, as is; for a book with other rows, only when a live
// journal row of an earlier apply says it repointed THAT row from the book's
// path to newPath (a multi-file book's path set to one chapter, because one of
// its rows happens to sit there, is not a finished match). Any other shape
// (several rows at the old path, rows that are neither there nor at newPath, a
// row at both) is refused:
// repointing "the" row would be a guess, and the book alone must not move while
// its rows say something else. The row's own book must be the only one holding
// newPath: a row of another book there, or a retired row (a claim on the file
// whoever holds it, database.RetiredClaimOnPath), refuses the match, because
// the repoint writes the single-owner path index. The holder lookup is the
// store's write-path lookup (database.BookFileRowsAtPathForWrite): once a retired
// version exists, or when the store cannot say, a lookup that cannot be complete
// refuses too, wrapping database.ErrOwnershipLookupIncomplete. While the store
// can prove no retired version exists, the lookup falls back to the single-owner
// path index when memdb is not serving, which can report fewer holders than
// exist. This is the plan-time check; the row's compare-and-swap checks again
// (fileRowMove.checkTarget) and refuses while ANY row holds newPath.
//
// The row's hash is kept only for a hash match (keepsHashes).
func planFileRow(store Store, log logger.Logger, book *database.Book, m ReconcileApplyItem, newSize int64) (*applyPlan, error) {
	newPath := m.NewPath
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("read its file rows: %w", err)
	}
	if len(files) == 0 {
		return &applyPlan{}, nil
	}
	oldPath := book.FilePath
	var atOld, atNew []database.BookFile
	for _, f := range files {
		if f.FilePath == newPath {
			atNew = append(atNew, f)
		}
		if f.FilePath == oldPath && oldPath != newPath {
			atOld = append(atOld, f)
		}
	}
	var move *fileRowMove
	var rowAtNew *database.BookFile
	var prior []*database.OperationChange
	switch {
	case len(atNew) == 1 && len(atOld) == 0:
		// The row is where the book is going.
		rowAtNew = &atNew[0]
		prior = priorApplyRows(store, log, book.ID)
		if len(files) > 1 && len(repointRowsOf(prior, rowAtNew.ID, oldPath, newPath)) == 0 {
			return nil, fmt.Errorf("it has %d file rows, one at %q and none at its path %q, and no earlier apply's journal says it left that row there: not exactly one row to repoint",
				len(files), newPath, oldPath)
		}
	case len(atOld) == 1 && len(atNew) == 0:
		row := atOld[0]
		was := database.BookFileLocationOf(&row)
		to := database.BookFileLocation{Path: newPath, Missing: false, Size: newSize}
		if keepsHashes(m.MatchType) {
			// A hash match proves the matched file holds the content the row's
			// hash names. Any other match leaves the hash empty ("unknown", for
			// the backfill-file-hashes job to refill): a carried one could be a
			// wrong claim dedup's exact-hash collectors pair on.
			to.Hash = was.Hash
		}
		move = &fileRowMove{bookID: book.ID, fileID: row.ID, was: was, to: to}
	default:
		return nil, fmt.Errorf("it has %d file rows, %d at its path %q and %d at %q, not exactly one row to repoint",
			len(files), len(atOld), oldPath, len(atNew), newPath)
	}
	rows, ok := database.AsCapability[fileRowStore](store)
	if !ok {
		return nil, fmt.Errorf("the store cannot look up or rewrite file rows, so the file row of %q is not repointed and the book is left alone", oldPath)
	}
	holders, err := database.BookFileRowsAtPathForWrite(rows, newPath)
	if err != nil {
		return nil, fmt.Errorf("cannot tell who holds %s: %w", newPath, err)
	}
	for _, h := range holders {
		if h.BookID != book.ID {
			return nil, fmt.Errorf("%s is held by a file row of book %s", newPath, h.BookID)
		}
	}
	if move != nil {
		if r := database.RetiredClaimOnPath(holders); r != nil {
			return nil, fmt.Errorf("%s is claimed by a retired file row (%s) of book %s", newPath, r.ID, r.BookID)
		}
		move.rows = rows
	}
	return &applyPlan{move: move, rowAtNew: rowAtNew, prior: prior}, nil
}

// journalRow is the undo row for the move: the row's location before and after,
// in the change type the operation revert restores
// (database.ChangeTypeBookFileRepoint), so reverting the operation puts the row
// back with the book. It is recorded before the write, as the book's own change
// is. A write that then does not land leaves a row the revert reads as already
// restored (undo.CheckRepointCurrent: the row is still at OldValue).
func (m *fileRowMove) journalRow(operationID string) *database.OperationChange {
	return &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: operationID,
		BookID:      m.bookID,
		ChangeType:  database.ChangeTypeBookFileRepoint,
		FieldName:   "book_file:" + m.fileID,
		OldValue:    m.was.Encode(),
		NewValue:    m.to.Encode(),
	}
}

// recordKeyOwner reads the row that holds the single-owner path key at the
// matched file and records it on the repoint's NewValue (KeyOwnerBook/KeyOwnerRow),
// as repairs.Writer.RepointBookFile does, so a revert hands the key back to
// exactly that row. It runs before the row is journaled. The holder check refuses
// a match while any row holds the file, so in practice nobody does and the key
// has no owner; what this records is a key whose owner the path lookup no longer
// lists. A lookup that fails refuses the match: nothing is written yet.
func (m *fileRowMove) recordKeyOwner() error {
	owner, err := database.BookFileByPathIncludingRetired(m.rows, m.to.Path)
	if err != nil {
		return fmt.Errorf("cannot tell who holds the path key of %s: %w", m.to.Path, err)
	}
	m.to.KeyOwnerBook, m.to.KeyOwnerRow = "", ""
	if owner != nil && owner.ID != m.fileID {
		m.to.KeyOwnerBook, m.to.KeyOwnerRow = owner.BookID, owner.ID
	}
	return nil
}

// checkTarget refuses while any row, live or retired, holds the matched file.
// It runs inside the row's compare-and-swap (forward), the last read before the
// write: a library scan that imports the still-untracked file as a new book's row
// after the plan would otherwise have its single-owner path key overwritten by
// the repoint, and two books would hold one file. The store offers no atomic
// "repoint unless held", so a row created between this read and the commit is
// still not seen; that window is the callback's own length.
func (m *fileRowMove) checkTarget() error {
	holders, err := database.BookFileRowsAtPathForWrite(m.rows, m.to.Path)
	if err != nil {
		return fmt.Errorf("cannot tell who holds %s: %w", m.to.Path, err)
	}
	if len(holders) > 0 {
		h := holders[0]
		return fmt.Errorf("%w: %s is now held by file row %s of book %s", errTargetHeld, m.to.Path, h.ID, h.BookID)
	}
	return nil
}

// forward repoints the row, only while it still is where the plan read it and
// nothing holds the matched file. unsynced reports a write that landed but whose
// fsync failed.
func (m *fileRowMove) forward() (unsynced bool, err error) {
	return m.repoint(m.was, m.to, m.checkTarget)
}

// rollBack puts the row back, only while it still is where forward put it.
func (m *fileRowMove) rollBack() (unsynced bool, err error) { return m.repoint(m.to, m.was, nil) }

// repoint writes to onto the row under the store's compare-and-swap, only while
// the row is at from (path and Missing flag: a hash or size filled in since does
// not count, database.SameFileLocation). The callback may run more than once
// (ModifyBookFile retries a lost race) and checks the row every time.
//
// guard, when set, runs inside the callback after the location check and refuses
// the write by returning an error.
//
// A write the store reports as database.ErrBookFileDurabilityUnknown was
// APPLIED (only its fsync failed, and the caller must not roll back or retry it
// as if it were lost): repoint returns nil with unsynced set.
func (m *fileRowMove) repoint(from, to database.BookFileLocation, guard func() error) (unsynced bool, err error) {
	written, err := m.rows.ModifyBookFile(m.bookID, m.fileID, func(f *database.BookFile) error {
		if cur := database.BookFileLocationOf(f); !database.SameFileLocation(cur, from) {
			return fmt.Errorf("%w: it is at %q (missing=%t), not %q (missing=%t)",
				errFileRowChanged, cur.Path, cur.Missing, from.Path, from.Missing)
		}
		if guard != nil {
			if err := guard(); err != nil {
				return err
			}
		}
		to.Apply(f)
		return nil
	})
	if errors.Is(err, database.ErrBookFileDurabilityUnknown) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if written == nil {
		return false, fmt.Errorf("%w: it is gone", errFileRowChanged)
	}
	return false, nil
}

// applyJournal is the journal rows one match has recorded. They are written
// BEFORE the writes they describe (so a crash leaves them), and they are
// reversible (book_path_update, book_file_repoint_location), so a revert acts on
// every one that is not voided. A match whose writes did not land must therefore
// take its rows back: a live row for a write that never happened lets a revert of
// this operation undo a later, successful apply of the same match (it reads the
// row's NewValue as current and "restores" it).
type applyJournal struct {
	store Store
	rows  []*database.OperationChange
}

// record writes one row and remembers it.
func (j *applyJournal) record(c *database.OperationChange) error {
	if err := database.RecordOperationChange(j.store, c); err != nil {
		return err
	}
	j.rows = append(j.rows, c)
	return nil
}

// void takes back every recorded row the way every journaling writer does
// (credits_backfill voidJournal, repairs.Writer): the row is rewritten under its
// id with Voided and RevertedAt set, so no revert acts on it. It returns the
// failures joined; a row it could not void stays live.
func (j *applyJournal) void() error {
	var errs []error
	for _, c := range j.rows {
		v := *c
		now := time.Now()
		v.RevertedAt, v.Voided = &now, true
		if err := database.RecordOperationChange(j.store, &v); err != nil {
			errs = append(errs, fmt.Errorf("row %s: %w", c.ID, err))
		}
	}
	j.rows = nil
	return errors.Join(errs...)
}

// bookChangeLookup is the journal read finishing a stranded match needs. It
// is not part of Store (Store only narrows); a store without it finishes the
// match without the row's twin and without the hand-off.
type bookChangeLookup interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// changeReverter marks another operation's rows reverted with no write
// (PebbleStore.MarkOperationChangesReverted): the hand-off's only write.
type changeReverter interface {
	MarkOperationChangesReverted(operationID string, changeIDs []string) error
}

// priorApplyRows is the book's journal rows that this apply could have written
// and that still stand: not voided, not reverted, and no Source. The apply
// journals no Source; a row that names one is another writer's (a repairs
// fixer's), and the twin and the hand-off must not copy or mark it, as the
// Missing files fixer requires its own Source at every step. It returns nil when
// the store cannot read the journal.
func priorApplyRows(store Store, log logger.Logger, bookID string) []*database.OperationChange {
	lookup, ok := database.AsCapability[bookChangeLookup](store)
	if !ok {
		return nil
	}
	changes, err := lookup.GetBookChanges(bookID)
	if err != nil {
		log.Warn("reconcile: book %s: cannot read its journal, finishing without a revertible row repoint or a hand-off: %v", bookID, err)
		return nil
	}
	var out []*database.OperationChange
	for _, c := range changes {
		if c == nil || c.Voided || c.RevertedAt != nil || c.Source != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// repointRowsOf is the rows among prior that repointed row fileID from oldPath
// to a present row at newPath: the footprint of an earlier apply of this match.
func repointRowsOf(prior []*database.OperationChange, fileID, oldPath, newPath string) []*database.OperationChange {
	var out []*database.OperationChange
	for _, c := range prior {
		if c.ChangeType != database.ChangeTypeBookFileRepoint || c.FieldName != "book_file:"+fileID {
			continue
		}
		was, werr := database.DecodeBookFileLocation(c.OldValue)
		to, terr := database.DecodeBookFileLocation(c.NewValue)
		if werr != nil || terr != nil || was.Path != oldPath || to.Path != newPath || to.Missing {
			continue
		}
		out = append(out, c)
	}
	return out
}

// strandedRowTwin is the journal-only row that makes the apply finishing a
// stranded match revertible ON ITS OWN, the pattern of the Missing files fixer's
// rmFinishStepTwo. The stranded state is the row at the matched file and the book
// at the old path, left by a failed rollback or a crash between the row write and
// the book write. The earlier operation journaled the row's repoint; this apply
// writes only the book. Reverting this apply alone would put the book back and
// leave the row at the matched file, so it journals a twin of the earlier
// operation's live repoint row (nothing is written for it: the row already holds
// its NewValue). Reverting this apply then restores book and row together, and
// the earlier operation's rows find their values already restored, in either
// order. The twin is the other half of a pair with handOff: the twin makes THIS
// apply self-sufficient, the hand-off stops the earlier operation's rows from
// undoing it. It returns nil when no such row exists (the organizer, not an
// earlier apply, left the row there) or when the earlier row belongs to this very
// operation (a resume: it is already in this journal).
func strandedRowTwin(prior []*database.OperationChange, row *database.BookFile, oldPath, newPath, operationID, bookID string) *database.OperationChange {
	var latest *database.OperationChange
	for _, c := range repointRowsOf(prior, row.ID, oldPath, newPath) {
		if latest == nil || c.CreatedAt.After(latest.CreatedAt) || (c.CreatedAt.Equal(latest.CreatedAt) && c.ID > latest.ID) {
			latest = c
		}
	}
	if latest == nil || latest.OperationID == operationID {
		return nil
	}
	return &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: operationID,
		BookID:      bookID,
		ChangeType:  database.ChangeTypeBookFileRepoint,
		FieldName:   latest.FieldName,
		OldValue:    latest.OldValue,
		NewValue:    latest.NewValue,
	}
}

// handOffRows is, by operation, the ids of the live rows of OTHER operations that
// describe this match's writes: the book's path change from oldPath to newPath,
// the repoint of the row, and each hash clear this apply made (hashOld is the
// value it cleared, by field). The apply that has just written the match owns
// those changes, so these rows must not stay live: whichever operation is
// reverted first would undo the book, and the other's rows would then undo it
// again, or undo a LATER apply of the same match (revert op-2, apply op-3, revert
// op-1 undoes op-3).
func handOffRows(prior []*database.OperationChange, operationID, fileID, oldPath, newPath string, hashOld map[string]string) map[string][]string {
	out := map[string][]string{}
	add := func(c *database.OperationChange) {
		if c.OperationID != operationID {
			out[c.OperationID] = append(out[c.OperationID], c.ID)
		}
	}
	for _, c := range repointRowsOf(prior, fileID, oldPath, newPath) {
		add(c)
	}
	for _, c := range prior {
		switch {
		case c.ChangeType == undo.ChangeTypeBookPathUpdate && c.FieldName == "file_path" && c.OldValue == oldPath && c.NewValue == newPath:
			add(c)
		case c.ChangeType == "metadata_update" && c.NewValue == "":
			if old, ok := hashOld[c.FieldName]; ok && c.OldValue == old {
				add(c)
			}
		}
	}
	return out
}

// handOff marks the rows handOffRows chose reverted with no write, the way the
// Missing files fixer's rmHandOff does (Writer.SupersedeChanges): the other
// operation's revert then counts them already reverted and leaves the book alone,
// and this operation's revert undoes the whole move.
//
// Best effort, after the write that landed. A store that cannot mark, or a failed
// mark, leaves both operations' rows live and is logged; so does a crash between
// the write and the mark. That is the state before the hand-off existed:
// reverting this operation alone still restores everything, reverting the other
// alone, or after a later apply of the same match, does not.
func handOff(store Store, log logger.Logger, bookID, operationID string, byOp map[string][]string) {
	if len(byOp) == 0 {
		return
	}
	ops := make([]string, 0, len(byOp))
	for op := range byOp {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	marker, ok := database.AsCapability[changeReverter](store)
	for _, op := range ops {
		var err error
		if !ok {
			err = errors.New("the store cannot mark journal rows reverted")
		} else {
			err = marker.MarkOperationChangesReverted(op, byOp[op])
		}
		if err != nil {
			log.Warn("reconcile: book %s: operation %s's rows for this match were not handed to operation %s; reverting %s alone will not undo it cleanly: %v",
				logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(op), logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(op), err)
		}
	}
}

// strOf is a hash pointer's value, "" when nil.
func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// applyMatch applies one confirmed match and returns "" when it landed, or the
// message for the result's errors when it did not. See ExecuteReconcile for the
// order of the steps.
//
// What a failure leaves, step by step (the journal rows are reversible, so each
// must be voided unless its write landed or may have):
//   - refused before the first journal row: nothing written, nothing to void;
//   - a journal write fails: the rows recorded so far are voided;
//   - the row write is refused or fails: all rows are voided (a write that landed
//     with a failed fsync is applied, not failed);
//   - the book write is refused under its lock (the book was repointed, trashed
//     or had its hashes changed since the plan): nothing of it landed. The row is
//     put back and, when that works, the rows are voided;
//   - the book write fails for any other reason: the book is read BACK before
//     anything is undone, because ModifyBook's error alone cannot tell a failure
//     from a write that landed. At the matched path the write landed: the row is
//     not rolled back, the match counts as applied (with a warning). Unreadable:
//     nothing is undone and nothing voided (rolling the row back under a book
//     that may be at the matched file would leave the one state a rerun cannot
//     finish). At the old path it did not land: the row is put back and, when
//     that works, the rows are voided. Anywhere else (another writer's path) the
//     row is put back and the rows stay live;
//   - the rollback fails: nothing is voided. The row landed, so its repoint row
//     stays live, and so does the book's, so reverting this operation restores
//     both even if a rerun sets the book.
func applyMatch(store Store, operationID string, m ReconcileApplyItem, log logger.Logger) string {
	book, err := store.GetBookByID(m.BookID)
	if err != nil || book == nil {
		return fmt.Sprintf("book %s: not found", m.BookID)
	}
	if book.IsSoftDeleted() {
		return fmt.Sprintf("book %s: it is in the trash, not repointed", m.BookID)
	}

	// The matched file must exist and be a file: a directory would give the book
	// and its row a path nothing can play.
	newInfo, err := os.Stat(m.NewPath)
	if err != nil {
		return fmt.Sprintf("book %s: new path does not exist: %s", m.BookID, m.NewPath)
	}
	if !newInfo.Mode().IsRegular() {
		return fmt.Sprintf("book %s: new path is not a regular file: %s", m.BookID, m.NewPath)
	}

	oldPath := book.FilePath

	// The book's file row at the old path moves with it. Planned (and refused)
	// before anything is journaled or written.
	plan, err := planFileRow(store, log, book, m, newInfo.Size())
	if err != nil {
		return fmt.Sprintf("book %s: not applied, nothing written: %v", m.BookID, err)
	}
	move := plan.move
	if move != nil {
		if err := move.recordKeyOwner(); err != nil {
			return fmt.Sprintf("book %s: not applied, nothing written: %v", m.BookID, err)
		}
	}

	// A match that does not prove the file's content clears the book's two
	// current-file hashes along with the row's (keepsHashes). Only a hash that is
	// there is cleared and journaled.
	hashOld := map[string]string{}
	if !keepsHashes(m.MatchType) {
		if h := strOf(book.FileHash); h != "" {
			hashOld[fieldFileHash] = h
		}
		if h := strOf(book.OrganizedFileHash); h != "" {
			hashOld[fieldOrganizedHash] = h
		}
	}
	clearHashes := len(hashOld) > 0
	oldHash, oldOrganized := strOf(book.FileHash), strOf(book.OrganizedFileHash)

	j := &applyJournal{store: store}
	// voided is what a failure path appends once it has taken its rows back.
	voided := func() string {
		if err := j.void(); err != nil {
			return fmt.Sprintf("; its journal rows could not be voided and stay live: %v", err)
		}
		return "; its journal rows are voided"
	}

	// Record the change for undo support
	// book_path_update, not the legacy "file_path_update" this apply
	// journaled until 2026-11-18: that type is one the revert engine
	// cannot reverse, so with the file row's repoint row (which it CAN
	// reverse) beside it a revert would put the row back and leave the
	// book at the matched file. With both reversible, reverting the
	// operation restores the book and its row together.
	if err := j.record(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: operationID,
		BookID:      book.ID,
		ChangeType:  undo.ChangeTypeBookPathUpdate,
		FieldName:   "file_path",
		OldValue:    oldPath,
		NewValue:    m.NewPath,
	}); err != nil {
		return fmt.Sprintf("book %s: failed to record change: %v", m.BookID, err)
	}
	for _, field := range []string{fieldFileHash, fieldOrganizedHash} {
		old, ok := hashOld[field]
		if !ok {
			continue
		}
		if err := j.record(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      book.ID,
			ChangeType:  "metadata_update",
			FieldName:   field,
			OldValue:    old,
			NewValue:    "",
		}); err != nil {
			return fmt.Sprintf("book %s: failed to record the %s change: %v%s", m.BookID, field, err, voided())
		}
	}

	var rowRow *database.OperationChange
	switch {
	case move != nil:
		rowRow = move.journalRow(operationID)
	case plan.rowAtNew != nil:
		rowRow = strandedRowTwin(plan.prior, plan.rowAtNew, oldPath, m.NewPath, operationID, book.ID)
	}
	if rowRow != nil {
		if err := j.record(rowRow); err != nil {
			return fmt.Sprintf("book %s: failed to record the file row change: %v%s", m.BookID, err, voided())
		}
	}
	if move != nil {
		unsynced, err := move.forward()
		if err != nil {
			return fmt.Sprintf("book %s: file row %s not repointed, the book is left at %s: %v%s", m.BookID, move.fileID, oldPath, err, voided())
		}
		if unsynced {
			log.Warn("reconcile: book %s: file row %s was repointed but its fsync failed, durability unknown", m.BookID, move.fileID)
		}
	}

	// Update the book's file path through ModifyBook: FilePath alone is set
	// on the row as it is under the book's write lock, so a column another
	// writer committed after the GetBookByID above (the os.Stat and the
	// change record sit between them) is not reverted (audit A1#15). The
	// path it replaces must still be the one the row move and the journal
	// were made from, and the book must still be live with the hashes the
	// journal says it cleared.
	written, err := store.ModifyBook(book.ID, func(b *database.Book) error {
		if b.FilePath == m.NewPath {
			// Already there: nothing to write, whatever else has happened to
			// the book. Refusing a trashed book here would roll the row back
			// under a book that is at the matched file.
			return database.ErrSkipBookWrite
		}
		if b.IsSoftDeleted() {
			return errBookTrashed
		}
		if b.FilePath != oldPath {
			return fmt.Errorf("%w: it now points at %q, not %q", errBookRepointed, b.FilePath, oldPath)
		}
		if clearHashes {
			if strOf(b.FileHash) != oldHash || strOf(b.OrganizedFileHash) != oldOrganized {
				return fmt.Errorf("%w: its hashes are no longer the ones the journal records as cleared", errBookHashesChanged)
			}
			b.FileHash, b.OrganizedFileHash = nil, nil
		}
		b.FilePath = m.NewPath
		return nil
	})
	if err == nil && written == nil {
		err = errors.New("it vanished before its path could be updated")
	}
	if err != nil {
		// A refusal from the callback saw the book under its lock and returned
		// before writing: it proves nothing landed. Any other error does not.
		refused := errors.Is(err, errBookRepointed) || errors.Is(err, errBookTrashed) || errors.Is(err, errBookHashesChanged)
		msg := fmt.Sprintf("book %s: failed to update: %v", m.BookID, err)
		var cur *database.Book
		if !refused {
			var gerr error
			cur, gerr = store.GetBookByID(book.ID)
			switch {
			case gerr != nil:
				// Nothing is undone: see the doc above.
				if move != nil {
					msg += fmt.Sprintf("; the book could not be read back (%v), so it is not known whether the write landed: file row %s is left at %s and the journal rows stay live, check the book before rerunning the match",
						gerr, move.fileID, m.NewPath)
				} else {
					msg += fmt.Sprintf("; the book could not be read back (%v), so it is not known whether the write landed and the journal rows stay live, check the book before rerunning the match", gerr)
				}
				return msg
			case cur != nil && cur.FilePath == m.NewPath:
				// The write landed: keep the row, count the match as applied.
				log.Warn("reconcile: book %s: the book write reported %v but the book is at the matched file, treating the match as applied", m.BookID, err)
				err = nil
			}
		}
		if err != nil {
			rolledBack := true
			if move != nil {
				unsynced, rerr := move.rollBack()
				switch {
				case rerr != nil:
					rolledBack = false
					switch {
					case errors.Is(err, errBookRepointed):
						msg += fmt.Sprintf("; file row %s is left at %s and could not be put back to %s (%v), another writer changed the book's path: check the book before rerunning the match",
							move.fileID, m.NewPath, oldPath, rerr)
					case errors.Is(err, errBookTrashed):
						msg += fmt.Sprintf("; file row %s is left at %s and could not be put back to %s (%v), the book is in the trash: restore it and rerun the match to finish it, or revert this operation",
							move.fileID, m.NewPath, oldPath, rerr)
					default:
						msg += fmt.Sprintf("; file row %s is left at %s and could not be put back to %s (%v), rerun the match to finish it",
							move.fileID, m.NewPath, oldPath, rerr)
					}
				default:
					if unsynced {
						log.Warn("reconcile: book %s: file row %s was put back but its fsync failed, durability unknown", m.BookID, move.fileID)
					}
					msg += fmt.Sprintf("; file row %s put back to %s", move.fileID, oldPath)
				}
			}
			if rolledBack {
				// Void only when the book is the witness that the write did not
				// land: refused under its lock, or read back at the old path.
				if refused || (cur != nil && cur.FilePath == oldPath) {
					msg += voided()
				} else {
					msg += "; the book could not be confirmed unwritten, so its journal rows stay live"
				}
			}
			return msg
		}
	}

	if plan.rowAtNew != nil {
		// This apply finished a stranded match: the earlier operation's rows for
		// it must not stay live (handOff).
		handOff(store, log, book.ID, operationID, handOffRows(plan.prior, operationID, plan.rowAtNew.ID, oldPath, m.NewPath, hashOld))
	}
	if move != nil {
		log.Info("Updated book %s and its file row %s: %s -> %s", book.ID, move.fileID, oldPath, m.NewPath)
	} else {
		log.Info("Updated book %s: %s -> %s", book.ID, oldPath, m.NewPath)
	}
	return ""
}
