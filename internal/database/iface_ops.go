// file: internal/database/iface_ops.go
// version: 1.5.0
// guid: b93b0da0-8afb-46fb-983e-c43f238ea67c
// last-edited: 2026-09-12

package database

import "time"

// OperationLifecycleStore creates operations and moves them through their statuses.
type OperationLifecycleStore interface {
	// Operation CRUD
	CreateOperation(id, opType string, folderPath *string) (*Operation, error)
	UpdateOperationStatus(id, status string, progress, total int, message string) error
	UpdateOperationError(id, errorMessage string) error
	UpdateOperationResultData(id string, resultData string) error
}

// OperationReader reads operation rows.
type OperationReader interface {
	GetOperationByID(id string) (*Operation, error)
	GetRecentOperations(limit int) ([]Operation, error)
	ListOperations(limit, offset int) ([]Operation, int, error)
	GetRecentCompletedOperations(limit int) ([]Operation, error)
	GetInterruptedOperations() ([]Operation, error)
}

// OperationStateStore persists resumable state and params for an operation.
type OperationStateStore interface {
	// State persistence (resumable operations)
	SaveOperationState(opID string, state []byte) error
	GetOperationState(opID string) ([]byte, error)
	SaveOperationParams(opID string, params []byte) error
	GetOperationParams(opID string) ([]byte, error)
	DeleteOperationState(opID string) error
}

// OperationChangeStore records and reverts the per-entity changes an operation made.
type OperationChangeStore interface {
	// Change tracking (undo/rollback)
	CreateOperationChange(change *OperationChange) error
	GetOperationChanges(operationID string) ([]*OperationChange, error)
	GetBookChanges(bookID string) ([]*OperationChange, error)
	// MarkOperationChangesReverted stamps RevertedAt on exactly the listed
	// change rows of the operation. The revert engine passes only the rows it
	// actually restored, so a row it could not reverse never reads as undone.
	MarkOperationChangesReverted(operationID string, changeIDs []string) error
}

// OperationLogStore covers operation logs and summary logs.
type OperationLogStore interface {
	// Logs
	AddOperationLog(operationID, level, message string, details *string) error
	GetOperationLogs(operationID string) ([]OperationLog, error)
	// Summary logs (persistent across restarts)
	SaveOperationSummaryLog(op *OperationSummaryLog) error
	GetOperationSummaryLog(id string) (*OperationSummaryLog, error)
	ListOperationSummaryLogs(limit, offset int) ([]OperationSummaryLog, error)
}

// OperationResultStore covers per-item operation results.
type OperationResultStore interface {
	// Per-book result rows
	CreateOperationResult(result *OperationResult) error
	GetOperationResults(operationID string) ([]OperationResult, error)
	// GetOperationResultsPage returns one page of results plus the total count for the operation.
	// limit=0 means no cap (returns everything from offset onward).
	GetOperationResultsPage(operationID string, limit, offset int) ([]OperationResult, int, error)
}

// OperationPruner covers retention: pruning and per-run deletion. The
// by-status bulk delete that used to live here backed only the retired
// DELETE /operations/history (SV-01, 2026-09-11) and went with it.
type OperationPruner interface {
	// Retention
	PruneOperationLogs(olderThan time.Time) (int, error)
	PruneOperationChanges(olderThan time.Time) (int, error)
	// DeleteOperationWithLogs removes the operation record (operation:<id>) and all
	// associated log lines (operationlog:<id>:*) in a single atomic batch.
	// This is the correct deletion primitive for the retention sweep — deleting the
	// operation key alone would orphan its log lines in PebbleDB indefinitely.
	DeleteOperationWithLogs(id string) error
}

// OperationStore covers the full operation-tracking surface:
// Operation + logs + state + results + changes + summary + retention.
//
// Split into the 7 interfaces above on 2026-08-18. This name is retained
// as their composition so the method set is byte-identical and no consumer moves;
// the type checker proves it, because every implementation -- PebbleStore (496
// methods) and database.MockStore (399) among them -- fails to compile if a method
// is dropped or re-signatured in the regrouping.
//
// Consumers should migrate to whichever pieces they use; this composition is the
// transitional shape, not the destination.
type OperationStore interface {
	OperationLifecycleStore
	OperationReader
	OperationStateStore
	OperationChangeStore
	OperationLogStore
	OperationResultStore
	OperationPruner
}
