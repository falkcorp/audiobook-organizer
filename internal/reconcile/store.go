// file: internal/reconcile/store.go
// version: 1.1.0
// guid: 7e3b1a95-2d68-4f04-8b57-0c91e6d4a273
// last-edited: 2026-10-05

package reconcile

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// reconcileStore is what the iTunes heal op needs, measured with an
// empty-interface compiler probe under -gcflags=-e: four direct calls plus one
// forwarding constraint. It was database.Store -- 398 methods -- until
// 2026-08-19, and could not be narrowed sooner: dedup.MergeBooks still took the
// union until #2587.
//
// The forwarding constraint is merge.Store since 2026-10-05: the heal's
// duplicate collapse runs merge.Service (collapseHealDuplicates) instead of
// the legacy dedup.MergeBooks. Embedded BY NAME, so this re-narrows on its own
// when merge.Store narrows.
type reconcileStore interface {
	merge.Store

	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetBookFileByPID(itunesPID string) (*database.BookFile, error)
}
