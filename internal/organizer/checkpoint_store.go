// file: internal/organizer/checkpoint_store.go
// version: 1.0.0
// guid: 8b3d5f17-4c92-4e60-a781-2d0f96c4e5a3
// last-edited: 2026-08-19

package organizer

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Measured with an empty-interface compiler probe under -gcflags=-e: these two
// functions need two methods between them. Both took database.Store -- 398
// methods -- until 2026-08-19. Deliberately NOT organizer.Store (22 methods):
// that is the service's surface, and neither of these is the service.

// checkpointStore is what sweeping stale organize checkpoints requires.
type checkpointStore interface {
	GetBookByID(id string) (*database.Book, error)
}

// bookMover is what recording a moved file on its book requires.
type bookMover interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}
