// file: internal/audiobooks/revert_retire.go
// version: 1.0.0
// guid: 070656eb-69ce-41aa-8f2b-19a5ca874882
// last-edited: 2026-10-23

package audiobooks

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Reverting a retire (retired-versions plan 6.1 to 6.5). A retire journals
// three change types (undo.ChangeTypeBookVersionCreate,
// undo.ChangeTypeBookFileRetire, undo.ChangeTypeRetiredVersionRehome) plus the
// shell's external-id moves and merged-into pointer, which already have revert
// cases. Every case here is a compare-and-set that resolves its store surface
// with database.AsCapability and refuses, never panics or half-writes, when the
// surface is missing: revertServiceStore is not widened for it (the
// interface-width ratchet).

// retireVersionAccess is the version read and delete a revert needs.
type retireVersionAccess interface {
	GetBookVersion(id string) (*database.BookVersion, error)
	DeleteBookVersion(id string) error
}

// lockMerge takes the merge lock (merge.LockMergeRMW) and returns its release,
// or does nothing when this service runs under a caller that already holds it
// (mergeLockHeld, set by the combine undo's retire reversal). The mutex is
// counted and not re-entrant, so a case that took it again would deadlock.
func (rs *RevertService) lockMerge() func() {
	if rs.mergeLockHeld {
		return func() {}
	}
	merge.LockMergeRMW()
	return merge.UnlockMergeRMW
}

func retireStoreMissing(what string) error {
	return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
		Detail: fmt.Sprintf("this store cannot %s", what)}
}

func (rs *RevertService) retireWriter() (database.RetiredBookFileWriter, error) {
	w, ok := database.AsCapability[database.RetiredBookFileWriter](rs.db)
	if !ok {
		return nil, retireStoreMissing("un-retire book_file rows")
	}
	return w, nil
}

// ensureShellPresent makes sure the trashed book a retire took rows from
// exists before a revert writes to it: nothing when it is there, a recreate
// from its purge snapshot when a purge deleted it (undo.PurgedShellRecreator),
// and the book_missing refusal when nothing can recreate it.
func (rs *RevertService) ensureShellPresent(shellID string) error {
	recreated, err := undo.EnsureShellPresent(rs.db, shellID)
	if err != nil {
		return err
	}
	if recreated {
		revertLog.Warn("revert: recreated purged shell %s from its purge snapshot", shellID)
	}
	return nil
}

// revertBookFileRetire un-retires one book_file row: it goes back under the
// shell, un-flagged, with the VersionID it carried before. The shell is made
// present first (it may have been purged).
func (rs *RevertService) revertBookFileRetire(c *database.OperationChange) error {
	r, err := undo.DecodeBookFileRetire(c)
	if err != nil {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable, Detail: err.Error()}
	}
	w, err := rs.retireWriter()
	if err != nil {
		return err
	}
	defer rs.lockMerge()()
	onHolder, err := rs.db.GetBookFileByID(r.Holder, r.FileID)
	if err != nil {
		return fmt.Errorf("read book_file %s on %s: %w", r.FileID, r.Holder, err)
	}
	onShell, err := rs.db.GetBookFileByID(r.Shell, r.FileID)
	if err != nil {
		return fmt.Errorf("read book_file %s on %s: %w", r.FileID, r.Shell, err)
	}
	if err := undo.CheckRetireCurrent(onHolder, onShell, r); err != nil {
		return err
	}
	if err := rs.ensureShellPresent(r.Shell); err != nil {
		return err
	}
	before := map[string]string{}
	if r.PriorVersionID != "" {
		before[r.FileID] = r.PriorVersionID
	}
	moves := []database.BookFileMove{{FileIDs: []string{r.FileID}, SourceBookID: r.Holder}}
	if err := w.UnretireBookFiles(moves, r.Shell, r.VersionID, before); err != nil {
		if errors.Is(err, database.ErrBookFileNotRetired) || errors.Is(err, database.ErrRetiredVersionMismatch) ||
			errors.Is(err, database.ErrBookFileOwnerMissing) {
			return &undo.ReferentError{Reason: undo.ReasonChangedSince,
				Detail: fmt.Sprintf("book_file %s could not be un-retired: %v", r.FileID, err)}
		}
		return fmt.Errorf("un-retire book_file %s back to %s: %w", r.FileID, r.Shell, err)
	}
	return nil
}

// revertBookVersionCreate deletes the retired version a retire created. The
// store refuses while a retired row still names it (the rows revert first, the
// plan orders them newest first), which is the dependent-not-reverted refusal.
func (rs *RevertService) revertBookVersionCreate(c *database.OperationChange) error {
	id, ok := undo.BookVersionIDFromField(c.FieldName)
	if !ok {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable,
			Detail: fmt.Sprintf("no book_version id in %q", c.FieldName)}
	}
	vs, ok := database.AsCapability[retireVersionAccess](rs.db)
	if !ok {
		return retireStoreMissing("delete book versions")
	}
	defer rs.lockMerge()()
	cur, err := vs.GetBookVersion(id)
	if err != nil {
		return fmt.Errorf("read book version %s: %w", id, err)
	}
	if err := undo.CheckVersionCreateCurrent(cur, c); err != nil {
		return err
	}
	if err := vs.DeleteBookVersion(id); err != nil {
		if errors.Is(err, database.ErrRetiredVersionHasFiles) {
			return &undo.ReferentError{Reason: undo.ReasonDependentNotReverted,
				Detail: fmt.Sprintf("book version %s still holds retired book_file rows; they are un-retired first", id)}
		}
		return fmt.Errorf("delete book version %s: %w", id, err)
	}
	return nil
}

// revertRetiredVersionRehome moves a retired version (and its rows) back to
// the holder it was rehomed from.
func (rs *RevertService) revertRetiredVersionRehome(c *database.OperationChange) error {
	id, ok := undo.BookVersionIDFromField(c.FieldName)
	if !ok {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable,
			Detail: fmt.Sprintf("no book_version id in %q", c.FieldName)}
	}
	vs, ok := database.AsCapability[retireVersionAccess](rs.db)
	if !ok {
		return retireStoreMissing("read book versions")
	}
	w, err := rs.retireWriter()
	if err != nil {
		return err
	}
	defer rs.lockMerge()()
	cur, err := vs.GetBookVersion(id)
	if err != nil {
		return fmt.Errorf("read book version %s: %w", id, err)
	}
	if err := undo.CheckRehomeCurrent(cur, c); err != nil {
		return err
	}
	if err := w.RehomeRetiredVersion(id, c.OldValue); err != nil {
		return fmt.Errorf("rehome retired version %s back to %s: %w", id, c.OldValue, err)
	}
	return nil
}

// prepassShells recreates, before any row runs, every purged shell a retire
// row of the revert will write back to. A revert runs newest first, and the
// shell's external-id and merged-into rows are newer than the retire rows, so
// without this they would reach a purged shell and refuse book_missing before
// any retire row recreated it. A shell that cannot be made present is not an
// error here: the rows that need it refuse with the reason, on their own.
func (rs *RevertService) prepassShells(rows []*database.OperationChange) {
	var shells []string
	for _, c := range rows {
		if c.ChangeType != undo.ChangeTypeBookFileRetire {
			continue
		}
		if r, err := undo.DecodeBookFileRetire(c); err == nil && !slices.Contains(shells, r.Shell) {
			shells = append(shells, r.Shell)
		}
	}
	sort.Strings(shells)
	for _, shell := range shells {
		if err := rs.ensureShellPresent(shell); err != nil {
			revertLog.Warn("revert: shell %s not recreated before the retire rows run: %v", shell, err)
		}
	}
}

// revertScope selects the rows of an operation a scoped revert
// (RevertOperationForBooks, the combine undo's retire reversal) acts on.
type revertScope struct {
	// shells are the books whose rows are selected.
	shells map[string]bool
}

func newRevertScope(shellIDs []string) *revertScope {
	s := &revertScope{shells: make(map[string]bool, len(shellIDs))}
	for _, id := range shellIDs {
		s.shells[id] = true
	}
	return s
}

// includes reports whether row c belongs to the scope on its own: the rows of
// the three retire types by the shell or version they decode to, any other row
// by its BookID. A row that does not decode is not selected (it is not
// restorable either way; NotRestorableLabel names it in a whole-op revert).
// The rehomes are decided by filter, from the versions the other rows touch.
func (s *revertScope) includes(c *database.OperationChange) (selected bool, version string) {
	if undo.IsLedgerOnly(c) {
		return false, ""
	}
	switch c.ChangeType {
	case undo.ChangeTypeBookFileRetire:
		r, err := undo.DecodeBookFileRetire(c)
		return err == nil && s.shells[r.Shell], r.VersionID
	case undo.ChangeTypeBookVersionCreate:
		v, err := undo.DecodeBookVersionCreate(c)
		return err == nil && s.shells[v.RetiredFromBookID], v.ID
	case undo.ChangeTypeRetiredVersionRehome:
		return false, ""
	}
	return s.shells[c.BookID], ""
}

// filter returns the rows of changes in the scope, in their original order.
func (s *revertScope) filter(changes []*database.OperationChange) []*database.OperationChange {
	picked := make([]bool, len(changes))
	versions := map[string]bool{}
	for i, c := range changes {
		if ok, v := s.includes(c); ok {
			picked[i] = true
			if v != "" {
				versions[v] = true
			}
		}
	}
	var out []*database.OperationChange
	for i, c := range changes {
		if !picked[i] && c.ChangeType == undo.ChangeTypeRetiredVersionRehome {
			if id, ok := undo.BookVersionIDFromField(c.FieldName); ok && versions[id] {
				picked[i] = true
			}
		}
		if picked[i] {
			out = append(out, c)
		}
	}
	return out
}

// retireUndoer is merge.RetireUndoer over a RevertService: how UndoCombine
// reverses the retire of a shell the combine absorbed (retired-versions plan
// 6.5). Both methods run with the merge lock held by UndoCombine, so they work
// on a copy of the service that never takes it (mergeLockHeld), and neither
// takes the operation lock: the retire op's own RevertOperation takes the
// operation lock and then the merge lock, so taking them in the opposite order
// here could deadlock. A concurrent RevertOperation of the same op finds its
// rows already reverted (a row back on the shell is already restored) and
// finishes.
type retireUndoer struct{ rs *RevertService }

// retireUndoer returns the merge.RetireUndoer to wire into a merge.Service the
// revert builds for a combine row.
func (rs *RevertService) retireUndoer() merge.RetireUndoer { return retireUndoer{rs: rs} }

// pending returns the shell's un-reverted, un-voided rows of retire op opID.
func (u retireUndoer) pending(opID, shellID string) ([]*database.OperationChange, error) {
	changes, err := u.rs.db.GetOperationChanges(opID)
	if err != nil {
		return nil, fmt.Errorf("read the changes of retire op %s: %w", opID, err)
	}
	var out []*database.OperationChange
	for _, c := range newRevertScope([]string{shellID}).filter(changes) {
		if !c.Voided && c.RevertedAt == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

// CheckRetireRevert implements merge.RetireUndoer. It is read-only.
func (u retireUndoer) CheckRetireRevert(opID, shellID string) (bool, []string, error) {
	rows, err := u.pending(opID, shellID)
	if err != nil || len(rows) == 0 {
		return false, nil, err
	}
	cc, ok := u.rs.db.(undo.ConflictChecker)
	if !ok {
		return true, []string{fmt.Sprintf("the store cannot check the retire of shell %s", shellID)}, nil
	}
	reasons, err := undo.RetireScopeRefusals(cc, rows)
	if err != nil {
		return true, nil, err
	}
	return true, reasons, nil
}

// RevertRetire implements merge.RetireUndoer. It refuses, before writing
// anything, a row the check refuses to predict (outside the retire stage): such
// a row's case could take a lock the caller holds.
func (u retireUndoer) RevertRetire(opID, shellID string) error {
	rows, err := u.pending(opID, shellID)
	if err != nil || len(rows) == 0 {
		return err
	}
	for _, c := range rows {
		if !undo.IsRetireStageType(c.ChangeType) || c.ChangeType == undo.ChangeTypeRetiredVersionRehome {
			return fmt.Errorf("retire op %s: change %s (%s) is not a row the combine undo reverses", opID, c.ID, c.ChangeType)
		}
	}
	scoped := *u.rs
	scoped.mergeLockHeld = true
	if _, err := scoped.revertOperation(opID, newRevertScope([]string{shellID})); err != nil {
		return fmt.Errorf("reverse the retire of shell %s (op %s): %w", shellID, opID, err)
	}
	return nil
}
