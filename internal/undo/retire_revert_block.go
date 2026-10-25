// file: internal/undo/retire_revert_block.go
// version: 1.1.1
// guid: 5b1d8e47-2c93-4fa6-9d0e-a3c7f6128b54
// last-edited: 2026-10-28

package undo

import (
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The retire change types are reversible (retire.go: part 5 teaches the
// classifier and the engine their undo cases), but a retire row the classifier
// cannot read (NotRestorableLabel answers "<type>:(unparsable)": a malformed
// plan record, a version row that does not describe a retired version) is still
// skipped by a revert, as every not-restorable row is. The rows that ride with
// it in the same operation (external_id_reassign, book_merged_into) ARE
// reversible on their own, and reverting only those would put a retired book's
// external ids back onto the shell and clear its merged-into link while its
// files still sit retired under the target: the shell would look live and own
// nothing, and a purge of it would then delete the only record of where the
// files went. So an operation holding any retire-family row the engine cannot
// read is refused as a WHOLE.
//
// The predicate keys on NotRestorableLabel, not on the change type alone: a
// well-formed retire row is reversible and never blocks.
var retireFamilyChangeTypes = []string{
	ChangeTypeBookVersionCreate,
	ChangeTypeBookFileRetire,
	ChangeTypeRetiredVersionRehome,
}

// RetireRevertBlockedError is returned by every revert entry point, and by the
// preflight, for an operation holding a retire row the engine cannot read.
// Nothing was touched.
type RetireRevertBlockedError struct {
	OperationID string
	// Types counts the retire rows the engine cannot read, by change type.
	Types map[string]int
}

func (e *RetireRevertBlockedError) Error() string {
	return fmt.Sprintf("this operation holds retire rows the undo cannot read, so none of it was undone "+
		"(undoing only its id and merge rows would leave the retired books looking live and empty): %s",
		formatRetireTypes(e.Types))
}

func formatRetireTypes(types map[string]int) string {
	keys := make([]string, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s x%d", k, types[k])
	}
	return out
}

// RetireRevertBlock returns the refusal for an operation whose rows hold a
// retire-family change the engine cannot read, or nil. A voided row
// describes a write that never happened and does not count.
func RetireRevertBlock(operationID string, changes []*database.OperationChange) *RetireRevertBlockedError {
	var types map[string]int
	for _, c := range changes {
		if c == nil || c.Voided || !slices.Contains(retireFamilyChangeTypes, c.ChangeType) {
			continue
		}
		if NotRestorableLabel(c) == "" {
			continue // well-formed: the engine can reverse it, so it does not block
		}
		if types == nil {
			types = map[string]int{}
		}
		types[c.ChangeType]++
	}
	if types == nil {
		return nil
	}
	return &RetireRevertBlockedError{OperationID: operationID, Types: types}
}

// retireRowShell returns the shell a non-voided book_file_retire row took its
// file from, read with DecodeBookFileRetire (the decode the retire undo itself
// uses). A row that is not one, or does not decode, answers false: it exempts
// nothing.
func retireRowShell(c *database.OperationChange) (string, bool) {
	if c == nil || c.Voided || c.ChangeType != ChangeTypeBookFileRetire {
		return "", false
	}
	r, err := DecodeBookFileRetire(c)
	if err != nil {
		return "", false
	}
	return r.Shell, true
}

// CheckNotRetiredShellReceiver refuses (a *ReferentError, ReasonBookIsRetiredShell
// or ReasonBookLookupFailed) handing an external id or a merged-into link back
// to book id while id is the shell of a retired version that still holds rows.
//
// Why: an external_id_reassign or book_merged_into row moved the id or the link
// OFF the shell, onto the holder, because the shell's audio went retired under
// the holder. Putting them back on a shell whose rows stay retired leaves a
// shell that looks live (it owns the id, the link is gone) and owns no file,
// which is the state the retire exists to prevent: the next iTunes import
// resolves the PID to the trashed shell, and a purge of it deletes the only
// record of where the audio went. The operation that did the retire may undo
// the ids and links only together with its book_file_retire rows (the shell is
// then no shell), so a shell the plan's operation retired is exempt. Any OTHER
// operation that moved ids or links off a shell, such as the tail a fresh plan
// finishes for a cut-off retire (it journals no retire row), is refused.
//
// A nil plan has no exemption. The exemption is read from every row of the
// operation (RevertPlan.NoteHandOffs), so the revert and the preflight agree.
// Callers run it under the merge lock a retire holds, and only for a row that
// would write, so a row already restored is still counted restored.
func (p *RevertPlan) CheckNotRetiredShellReceiver(store any, id, action string) error {
	if p != nil && p.retires[id] {
		return nil
	}
	return CheckNotRetiredShell(store, id, action)
}
