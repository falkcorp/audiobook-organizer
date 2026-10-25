// file: internal/undo/preflight_helper_test.go
// version: 1.0.0
// guid: 73e8fa91-cd08-4f6a-8617-8ea39f68ce9c
// last-edited: 2026-10-25

package undo

import (
	"fmt"
)

// preflightUndoConflicts is the test-side form of the preflight: it reads the
// operation's journal rows from the store and runs the production scan over
// them. Production reads the rows itself (the preflight handler checks them
// first), so there is no production wrapper; this keeps the tests asserting
// the same thing they did when one existed.
func preflightUndoConflicts(store ConflictChecker, operationID string) (*UndoConflictReport, error) {
	changes, err := store.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("load changes: %w", err)
	}
	return PreflightUndoConflictsFromChanges(store, changes)
}
