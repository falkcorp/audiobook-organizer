// file: internal/reconcile/store.go
// version: 1.0.0
// guid: 7e3b1a95-2d68-4f04-8b57-0c91e6d4a273
// last-edited: 2026-08-19

package reconcile

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// reconcileStore is what the iTunes heal op needs, measured with an
// empty-interface compiler probe under -gcflags=-e: four direct calls plus one
// forwarding constraint. It was database.Store -- 398 methods -- until
// 2026-08-19, and could not be narrowed sooner: dedup.MergeBooks still took the
// union until #2587.
//
// dedup.Store is embedded BY NAME, so this re-narrows on its own when dedup
// narrows further.
type reconcileStore interface {
	dedup.Store

	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetBookFileByPID(itunesPID string) (*database.BookFile, error)
}
