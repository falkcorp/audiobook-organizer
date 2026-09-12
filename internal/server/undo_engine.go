// file: internal/server/undo_engine.go
// version: 1.8.0
// guid: 0b8c9d6e-1f7a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-12
//
// Backward-compatibility wrapper for the undo engine, now in internal/undo.
// This file re-exports the undo preflight API from internal/undo.
//
// The actual undo implementation lives in internal/undo/engine.go

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// UndoConflictReport is re-exported from the undo package.
type UndoConflictReport = undo.UndoConflictReport

// UndoConflictItem is re-exported from the undo package.
type UndoConflictItem = undo.UndoConflictItem

// PreflightUndoConflicts is re-exported from the undo package.
func PreflightUndoConflicts(store undoConflictChecker, operationID string) (*UndoConflictReport, error) {
	return undo.PreflightUndoConflicts(store, operationID)
}
