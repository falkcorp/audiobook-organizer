// file: internal/metabatch/store.go
// version: 1.0.0
// guid: 9d4e6b02-8a15-4c73-b2f9-7e1a3d508c62
// last-edited: 2026-08-19

package metabatch

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Measured with an empty-interface compiler probe under -gcflags=-e: four
// methods, no forwarding constraints. Was database.Store -- 398 methods --
// until 2026-08-19.

// operationResultReader is all LatestMatchedBookIDs reads.
type operationResultReader interface {
	GetRecentOperations(limit int) ([]database.Operation, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
}

type metabatchStore interface {
	operationResultReader

	GetBookByID(id string) (*database.Book, error)
	GetBooksByTag(tag string) ([]string, error)
}
