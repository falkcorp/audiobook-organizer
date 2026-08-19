// file: internal/server/handlers/operations/interfaces.go
// version: 1.1.0
// guid: 37502068-5061-401b-841e-0b191567f0bf
// last-edited: 2026-08-18

// Narrow dependency interfaces for the operations domain handlers (scan /
// organize / optimize / transcode triggers, operation status / logs / result /
// changes / revert, stale-op management, DB optimize, tasks, and the
// maintenance window). Each interface lists only what the handlers actually
// call so package operations stays decoupled from the concrete
// scheduler / registry / pipeline / store implementations and never imports
// package server (which would create an import cycle).

package operations

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
)

// OperationsStore is what this handler actually calls, measured by emptying it
// and reading the compiler's enumeration: fourteen direct calls, plus three
// methods for sweep.tombstoneSweeper and database.SettingsStore for
// config.SaveConfigToDatabase.
//
// It previously embedded database.BookStore with the note "structural
// satisfaction requires the full" — true until #2566 narrowed the sweep/audit
// parameters that demanded it. The constraint is now sweep.fileAuditor (one
// method) and sweep.tombstoneSweeper (three).
//
// database.SettingsStore stays embedded rather than method-listed: it is four
// methods, already the right size, and this is what using the domain pieces
// looks like.
type OperationsStore interface {
	database.SettingsStore

	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookByID(id string) (*database.Book, error)
	ListBookTombstones(limit int) ([]database.Book, error)
	DeleteBookTombstone(id string) error

	CreateAuthor(name string) (*database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	CreateNarrator(name string) (*database.Narrator, error)
	GetNarratorByName(name string) (*database.Narrator, error)
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error

	GetOperationByID(id string) (*database.Operation, error)
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
	GetRecentOperations(limit int) ([]database.Operation, error)
	UpdateOperationStatus(id, status string, progress, total int, message string) error
	DeleteOperationsByStatus(statuses []string) (int, error)
}

// OperationsRegistry is the narrow operations-registry subset the operations
// handlers require: EnqueueOp (scan / organize / optimize / transcode starters)
// and Cancel (cancelOperation v2 path). The variadic opts param on EnqueueOp is
// preserved so the concrete *opsregistry.Registry satisfies the interface.
type OperationsRegistry interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
	Cancel(opID string) error
}

// Scheduler is the narrow *scheduler.TaskScheduler subset used by the task and
// maintenance-window handlers.
type Scheduler interface {
	ListTasks() []scheduler.TaskInfo
	RunTaskManual(name string) (*database.Operation, error)
	RunMaintenanceWindow(ctx context.Context) error
	IsMaintenanceRunning() bool
	GetLastMaintenanceRunDate() string
}

// ScanCanceler is the narrow *aiscan.PipelineManager subset used by
// cancelOperation to cancel an in-flight AI scan by scan ID.
type ScanCanceler interface {
	CancelScan(scanID int) error
}

// AIScanLister is the narrow *database.AIScanStore subset used by
// cancelOperation to find the AI scan whose OperationID matches the op being
// canceled.
type AIScanLister interface {
	ListScans() ([]database.Scan, error)
}
