// file: internal/undo/retire_revert_block.go
// version: 1.0.0
// guid: 5b1d8e47-2c93-4fa6-9d0e-a3c7f6128b54
// last-edited: 2026-10-24

package undo

import (
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
