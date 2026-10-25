// file: internal/server/preflight_helper_test.go
// version: 1.0.0
// guid: dccc9171-4cf3-4fa0-a72c-dc654b94059c
// last-edited: 2026-10-25

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// preflightUndoConflicts is the test-side form of the preflight: it reads the
// operation's journal rows from the store and runs the production scan over
// them. Production reads the rows itself (the preflight handler checks them
// first), so there is no production wrapper; this keeps the tests asserting
// the same thing they did when one existed.
func preflightUndoConflicts(store undo.ConflictChecker, operationID string) (*undo.UndoConflictReport, error) {
	changes, err := store.GetOperationChanges(operationID)
	if err != nil {
		return nil, fmt.Errorf("load changes: %w", err)
	}
	return undo.PreflightUndoConflictsFromChanges(store, changes)
}
