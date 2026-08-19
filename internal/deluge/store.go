// file: internal/deluge/store.go
// version: 1.1.0
// guid: 1f7a2c98-6e34-4b05-9d81-3a5c0e27b6d4
// last-edited: 2026-08-19

package deluge

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Store is the store surface this package needs, measured with an
// empty-interface compiler probe under -gcflags=-e: two methods, no forwarding
// constraints. It was database.Store -- 398 methods -- until 2026-08-19.
//
// Exported: internal/server wraps NewLibraryImporterAdapter and forwards its
// own store into it.
type Store interface {
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}
