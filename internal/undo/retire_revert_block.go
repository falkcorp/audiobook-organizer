// file: internal/undo/retire_revert_block.go
// version: 1.1.0
// guid: 5b1d8e47-2c93-4fa6-9d0e-a3c7f6128b54
// last-edited: 2026-10-25

package undo

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TODO(#4146): the undo cases for the retire change types land in PR 5 (#4146).
// Until they do the engine cannot reverse a retire's rows, yet the rows that
// ride with them in the same operation (external_id_reassign, book_merged_into)
// ARE reversible on their own. A partial revert would put a retired book's
// external ids back onto the shell and clear its merged-into link while its
// files still sit retired under the target: the shell would look live and own
// nothing, and a purge of it would then delete the only record of where the
// files went. So an operation holding any retire-family row the engine cannot
// reverse is refused as a WHOLE.
//
// The predicate keys on NotRestorableLabel, not on the change type alone: when
// PR 5 teaches the classifier these types the label turns "" and this goes
// inert by itself, with no edit here. The literal strings are kept apart from
// PR 5's exported constants (internal/undo/retire.go) on purpose so the two
// branches do not collide; they are the change types
// maintenance.journaledRetireWriter journals.
var retireFamilyChangeTypes = []string{
	"book_version_create",
	"book_file_retire",
	"retired_version_rehome",
}

// RetireRevertBlockedError is returned by every revert entry point, and by the
// preflight, for an operation that retired books' files while the engine
// cannot yet undo that. Nothing was touched.
type RetireRevertBlockedError struct {
	OperationID string
	// Types counts the retire rows the engine cannot reverse, by change type.
	Types map[string]int
}

func (e *RetireRevertBlockedError) Error() string {
	return fmt.Sprintf("this operation retired books' files and cannot be undone yet, so none of it was undone "+
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
// retire-family change the engine cannot reverse, or nil. A voided row
// describes a write that never happened and does not count.
func RetireRevertBlock(operationID string, changes []*database.OperationChange) *RetireRevertBlockedError {
	var types map[string]int
	for _, c := range changes {
		if c == nil || c.Voided || !slices.Contains(retireFamilyChangeTypes, c.ChangeType) {
			continue
		}
		if NotRestorableLabel(c) == "" {
			continue // the engine can reverse it (PR 5 landed): not blocked
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
// file from: the book_id of its OldValue, a JSON {"book_id","version_id"} (what
// maintenance.journaledRetireWriter.RetireRows writes, and what PR 5's
// RetirePlace decodes). A row that is not one, or whose OldValue does not parse
// or names no book, answers false: it exempts nothing.
func retireRowShell(c *database.OperationChange) (string, bool) {
	if c == nil || c.Voided || c.ChangeType != "book_file_retire" {
		return "", false
	}
	var old struct {
		BookID string `json:"book_id"`
	}
	if err := json.Unmarshal([]byte(c.OldValue), &old); err != nil || old.BookID == "" {
		return "", false
	}
	return old.BookID, true
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
