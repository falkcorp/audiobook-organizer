// file: internal/metabatch/store.go
// version: 1.5.0
// guid: 9d4e6b02-8a15-4c73-b2f9-7e1a3d508c62
// last-edited: 2026-09-27

package metabatch

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Measured with an empty-interface compiler probe under -gcflags=-e: four
// methods, no forwarding constraints (six since 2026-09-27: the gate's live
// author read). Was database.Store -- 398 methods --
// until 2026-08-19.

// operationResultReader is all LatestMatchedBookIDs reads.
//
// ListOperationsV2Since joined GetRecentOperations when candidate-fetch runs
// stopped writing a v1 row: discovery now spans both keyspaces via
// CandidateFetchOps, so this slice needs one listing method per keyspace.
type operationResultReader interface {
	GetRecentOperations(limit int) ([]database.Operation, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
}

// Store is the metabatch consumer slice. Exported so a caller that constructs a
// MetadataUpgradeService (internal/scheduler) can name this instead of reaching
// for database.Store.
type Store interface {
	operationResultReader

	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBooksByTag(tag string) ([]string, error)
	// The series name the owner-manual-only guard (applygate.IsOwnerManualOnly)
	// checks before the metadata upgrade searches a book.
	GetSeriesByID(id int) (*database.Series, error)
	// The metadata upgrade's persisted sweep cursor (upgradeCursorKey).
	GetOperationState(opID string) ([]byte, error)
	SaveOperationState(opID string, state []byte) error
	// The author credits the certainty gate judges a candidate's author
	// against (database.LiveBookAuthorNames), never the Book.Author snapshot.
	database.BookAuthorReader
}
