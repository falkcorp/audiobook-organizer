// file: internal/audiobooks/revert_retire.go
// version: 1.4.0
// guid: 070656eb-69ce-41aa-8f2b-19a5ca874882
// last-edited: 2026-10-24

package audiobooks

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

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

// retireVersionAccess is the version read a revert needs. The delete of a
// retire's version is database.GuardedBookVersionDeleter's.
type retireVersionAccess interface {
	GetBookVersion(id string) (*database.BookVersion, error)
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

// withMergeLockHeld returns a RevertService over the same stores for a caller
// that already holds the merge lock (the combine undo's retire reversal): its
// cases then skip the lock (lockMerge). The one place the service is copied, so
// a lock added to RevertService later is a single, visible fix (go vet's
// copylocks check flags this copy) rather than a silent copy of a held mutex.
func (rs *RevertService) withMergeLockHeld() *RevertService {
	held := *rs
	held.mergeLockHeld = true
	return &held
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
// The compare-and-set (undo.CheckVersionCreateCurrent) runs inside the store's
// delete, under the version stripe, so an edit of the version cannot land
// between the check and the delete and be lost.
func (rs *RevertService) revertBookVersionCreate(c *database.OperationChange) error {
	id, ok := undo.BookVersionIDFromField(c.FieldName)
	if !ok {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable,
			Detail: fmt.Sprintf("no book_version id in %q", c.FieldName)}
	}
	deleter, ok := database.AsCapability[database.GuardedBookVersionDeleter](rs.db)
	if !ok {
		return retireStoreMissing("delete book versions")
	}
	defer rs.lockMerge()()
	var verdict error
	if err := deleter.DeleteBookVersionIf(id, func(cur *database.BookVersion) error {
		verdict = undo.CheckVersionCreateCurrent(cur, c)
		return verdict
	}); err != nil {
		if verdict != nil {
			return verdict
		}
		if errors.Is(err, database.ErrRetiredVersionHasFiles) {
			return &undo.ReferentError{Reason: undo.ReasonDependentNotReverted,
				Detail: fmt.Sprintf("book version %s still holds retired book_file rows; they are un-retired first", id)}
		}
		return fmt.Errorf("delete book version %s: %w", id, err)
	}
	return nil
}

// revertRetiredVersionRehome moves a retired version (and its rows) back to
// the holder it was rehomed from. That holder is trashed by definition (the
// chain case rehomed the version because the holder was being retired), and a
// revert does not un-trash it, so the store path is the one that accepts a
// trashed destination (database.RetiredVersionHolderRestorer); a destination a
// purge deleted is recreated from its purge snapshot first, like any shell.
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
	restorer, ok := database.AsCapability[database.RetiredVersionHolderRestorer](rs.db)
	if !ok {
		return retireStoreMissing("move a retired version back to its previous holder")
	}
	defer rs.lockMerge()()
	cur, err := vs.GetBookVersion(id)
	if err != nil {
		return fmt.Errorf("read book version %s: %w", id, err)
	}
	if err := undo.CheckRehomeCurrent(cur, c); err != nil {
		return err
	}
	if err := rs.ensureShellPresent(c.OldValue); err != nil {
		return err
	}
	if err := restorer.RestoreRetiredVersionHolder(id, c.OldValue); err != nil {
		if errors.Is(err, database.ErrBookFileOwnerMissing) || errors.Is(err, database.ErrRetiredVersionMismatch) {
			return &undo.ReferentError{Reason: undo.ReasonChangedSince,
				Detail: fmt.Sprintf("retired version %s could not go back to %s: %v", id, c.OldValue, err)}
		}
		return fmt.Errorf("rehome retired version %s back to %s: %w", id, c.OldValue, err)
	}
	return nil
}

// prepassShells recreates, before any row runs, every purged shell a retire
// row of the revert will write back to: the shell a file row left, and the
// holder a rehome took a version from. A revert runs newest first, and the
// shell's external-id and merged-into rows are newer than the retire rows, so
// without this they would reach a purged shell and refuse book_missing before
// any retire row recreated it. A shell that cannot be made present is not an
// error here: the rows that need it refuse with the reason, on their own.
//
// It runs under the merge lock (taken here unless the caller holds it), like
// every other recreate: undo.PurgedShellRecreator is always called with the
// merge lock held and never takes it. Nothing is taken when no row names a
// shell, so a revert without retire rows is unchanged.
func (rs *RevertService) prepassShells(rows []*database.OperationChange) {
	var shells []string
	add := func(id string) {
		if id != "" && !slices.Contains(shells, id) {
			shells = append(shells, id)
		}
	}
	for _, c := range rows {
		switch c.ChangeType {
		case undo.ChangeTypeBookFileRetire:
			if r, err := undo.DecodeBookFileRetire(c); err == nil {
				add(r.Shell)
			}
		case undo.ChangeTypeRetiredVersionRehome:
			add(c.OldValue)
		}
	}
	if len(shells) == 0 {
		return
	}
	sort.Strings(shells)
	defer rs.lockMerge()()
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

// recordID names the owed-settle record a scoped run of operationID reads and
// writes: its own, never the whole operation's, so a scoped run cannot clear
// or rewrite the intent a dead earlier run of the whole operation left for
// other books' groups, and a dead scoped run's intent is retried by the next
// scoped run of the same books.
func (s *revertScope) recordID(operationID string) string {
	ids := make([]string, 0, len(s.shells))
	for id := range s.shells {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return operationID + scopedRecordMarker + strings.Join(ids, ",")
}

// isRetireRowType reports one of the three types a retire adds.
func isRetireRowType(changeType string) bool {
	switch changeType {
	case undo.ChangeTypeBookFileRetire, undo.ChangeTypeBookVersionCreate, undo.ChangeTypeRetiredVersionRehome:
		return true
	}
	return false
}

// includes reports whether row c belongs to the scope on its own: the rows of
// the three retire types by the shell or version they decode to, the rehomes
// that took a version off a scoped shell (the shell was the holder of a version
// an earlier retire put there), any other row by its BookID. A retire row that
// does not decode is selected, whichever shell it belongs to (that cannot be
// told): it is not restorable, so a revert counts it by name
// (undo.NotRestorableLabel) and the combine undo refuses it, instead of
// restoring the shell around a file that stays retired. The rehomes of the
// versions the other rows create are decided by filter.
func (s *revertScope) includes(c *database.OperationChange) (selected bool, version string) {
	if undo.IsLedgerOnly(c) {
		return false, ""
	}
	if isRetireRowType(c.ChangeType) && undo.NotRestorableLabel(c) != "" {
		return true, ""
	}
	switch c.ChangeType {
	case undo.ChangeTypeBookFileRetire:
		r, err := undo.DecodeBookFileRetire(c)
		return err == nil && s.shells[r.Shell], r.VersionID
	case undo.ChangeTypeBookVersionCreate:
		v, err := undo.DecodeBookVersionCreate(c)
		return err == nil && s.shells[v.RetiredFromBookID], v.ID
	case undo.ChangeTypeRetiredVersionRehome:
		return c.OldValue != "" && s.shells[c.OldValue], ""
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

// requireShellsOf refuses a scope that names a book the operation never names
// as a shell: a shell is the book a retire took file rows from, the book a
// retired version was created from, the holder a rehome took a version off, or
// a book whose external ids or merged-into pointer the retire moved. Selecting
// by anything else (the holder, say, whose BookID keys every retire row)
// would pick up every row keyed by it, of every type and every shell.
func (s *revertScope) requireShellsOf(operationID string, changes []*database.OperationChange) error {
	named := map[string]bool{}
	for _, c := range changes {
		switch c.ChangeType {
		case undo.ChangeTypeBookFileRetire:
			if r, err := undo.DecodeBookFileRetire(c); err == nil {
				named[r.Shell] = true
			}
		case undo.ChangeTypeBookVersionCreate:
			if v, err := undo.DecodeBookVersionCreate(c); err == nil {
				named[v.RetiredFromBookID] = true
			}
		case undo.ChangeTypeRetiredVersionRehome:
			if c.OldValue != "" {
				named[c.OldValue] = true
			}
		case undo.ChangeTypeBookMergedInto, undo.ChangeTypeExternalIDReassign:
			named[c.BookID] = true
		}
	}
	var notShells []string
	for id := range s.shells {
		if !named[id] {
			notShells = append(notShells, id)
		}
	}
	if len(notShells) == 0 {
		return nil
	}
	sort.Strings(notShells)
	return fmt.Errorf("operation %s does not name %s as a shell (a book a retire took rows from): a scoped revert takes shell ids only", operationID, strings.Join(notShells, ", "))
}

// retireUndoer is merge.RetireUndoer over a RevertService: how UndoCombine
// reverses the retire of a shell the combine absorbed (retired-versions plan
// 6.5). Both its steps run with the merge lock held by UndoCombine, so the
// reversal works on a copy of the service that never takes it
// (withMergeLockHeld). It takes the retire ops' operation locks with a TRY
// (LockRetireOps): the retire op's own RevertOperation takes the operation lock
// and then the merge lock, so waiting for the operation lock here, under the
// merge lock, could deadlock. A busy lock refuses with a retryable error, and
// UndoCombine takes them before its first write, so the undo is run again with
// nothing changed.
type retireUndoer struct{ rs *RevertService }

// retireUndoer returns the merge.RetireUndoer to wire into a merge.Service the
// revert builds for a combine row.
func (rs *RevertService) retireUndoer() merge.RetireUndoer { return retireUndoer{rs: rs} }

// retireOpRead is merge.RetireOp: one retire op's changes, read once.
type retireOpRead struct {
	u       retireUndoer
	opID    string
	changes []*database.OperationChange
}

// ReadRetireOp implements merge.RetireUndoer: it reads retire op opID once.
// Every shell of the op is planned from that read (retireOpRead.PlanShell).
func (u retireUndoer) ReadRetireOp(opID string) (merge.RetireOp, error) {
	changes, err := u.rs.db.GetOperationChanges(opID)
	if err != nil {
		return nil, fmt.Errorf("read the changes of retire op %s: %w", opID, err)
	}
	return retireOpRead{u: u, opID: opID, changes: changes}, nil
}

// PlanShell implements merge.RetireOp: it returns the shell's rows, every
// refusal, and the reversal that runs from the same read. It writes nothing.
func (o retireOpRead) PlanShell(shellID string) (*merge.RetireReversal, error) {
	u, opID, changes := o.u, o.opID, o.changes
	scope := newRevertScope([]string{shellID})
	rev := &merge.RetireReversal{}
	var pending []*database.OperationChange
	for _, c := range scope.filter(changes) {
		if c.Voided {
			continue // the write it describes never happened
		}
		done := c.RevertedAt != nil
		if c.ChangeType == undo.ChangeTypeBookFileRetire {
			// The file rows of this shell (a row that does not decode names no
			// file: it is refused below).
			if r, derr := undo.DecodeBookFileRetire(c); derr == nil {
				if !done {
					rev.PendingFileIDs = append(rev.PendingFileIDs, r.FileID)
				}
			}
		}
		if !done {
			pending = append(pending, c)
		}
	}
	if len(pending) == 0 {
		return rev, nil
	}
	rev.Live = true
	cc, ok := u.rs.db.(undo.ConflictChecker)
	if !ok {
		rev.Reasons = []string{fmt.Sprintf("the store cannot check the retire of shell %s", shellID)}
		return rev, nil
	}
	reasons, err := undo.RetireScopeRefusals(cc, shellID, pending)
	if err != nil {
		return nil, err
	}
	rev.Reasons = reasons
	if len(reasons) == 0 {
		rev.Revert = func() error { return u.revert(opID, shellID, scope, changes, pending) }
	} else {
		rev.Revert = func() error {
			return fmt.Errorf("retire op %s of shell %s cannot be reversed: %s", opID, shellID, strings.Join(reasons, "; "))
		}
	}
	return rev, nil
}

// LockRetireOps implements merge.RetireUndoer: it try-locks the operation lock
// of each retire op, which the owed-settle record the reversal reads, rewrites
// and clears is guarded by (the retire op's own RevertOperation holds it for its
// whole run), and holds them until the release. Busy frees what it took and
// refuses with errRetireOpBusy.
func (u retireUndoer) LockRetireOps(opIDs []string) (func(), error) {
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, id := range opIDs {
		release, ok := tryLockOperation(id)
		if !ok {
			releaseAll()
			return nil, fmt.Errorf("retire op %s: %w", id, errRetireOpBusy)
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
}

// errRetireOpBusy is the retryable refusal of a retire reversal whose operation
// lock another revert of the same operation holds.
var errRetireOpBusy = errors.New("another revert of this retire operation is running; try the undo again when it finishes")

// revert reverses pending (the shell's un-reverted rows, read by
// PlanShell from changes) with the merge lock and the retire op's
// operation lock (LockRetireOps) already held by the caller. It refuses, before writing anything, a row the plan refuses to
// predict: such a row's case could take a lock the caller holds.
func (u retireUndoer) revert(opID, shellID string, scope *revertScope, changes, pending []*database.OperationChange) error {
	for _, c := range pending {
		if !undo.IsRetireStageType(c.ChangeType) || undo.NotRestorableLabel(c) != "" ||
			(c.ChangeType == undo.ChangeTypeRetiredVersionRehome && !undo.RehomeFromShell(c, shellID)) {
			return fmt.Errorf("retire op %s: change %s (%s) is not a row the combine undo reverses", opID, c.ID, c.ChangeType)
		}
	}
	if _, err := u.rs.withMergeLockHeld().revertLoaded(opID, scope, changes); err != nil {
		return fmt.Errorf("reverse the retire of shell %s (op %s): %w", shellID, opID, err)
	}
	return nil
}

// filesIncludingRetired lists every book_file row keyed under bookID, the rows
// retired into it as well as its live ones (database.RetiredBookFileReader),
// for the reads that decide who claims a path or what a file move must repoint.
// GetBookFiles alone would call a retired version's path free and leave its row
// pointing at a folder that moved. A store without the reader has no retired
// rows, so its live listing is the whole set.
func (rs *RevertService) filesIncludingRetired(bookID string) ([]database.BookFile, error) {
	if r, ok := database.AsCapability[database.RetiredBookFileReader](rs.db); ok {
		return r.GetBookFilesIncludingRetired(bookID)
	}
	return rs.db.GetBookFiles(bookID)
}
