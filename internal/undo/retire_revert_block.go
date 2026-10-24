// file: internal/undo/retire_revert_block.go
// version: 1.0.1
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
