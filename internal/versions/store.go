// file: internal/versions/store.go
// version: 1.1.0
// guid: 5e81b3d7-92c4-4a06-8f15-6b0d2a749c38
// last-edited: 2026-08-19

package versions

import "github.com/falkcorp/audiobook-organizer/internal/database"

// The store surface this package needs, measured with an empty-interface
// compiler probe under -gcflags=-e: 12 methods plus one forwarding constraint
// (fileops.ValidateUserPath, which already takes database.ImportPathStore and
// so is embedded by name). Every function here took database.Store -- 398
// methods -- until 2026-08-19.

type bookFileLister interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

type versionUpdater interface {
	UpdateBookVersion(v *database.BookVersion) error
}

// Exported: internal/importer forwards its store into CheckFingerprint.
type FingerprintReader interface {
	GetBookVersionByTorrentHash(hash string) (*database.BookVersion, error)
}

type altPromoter interface {
	versionUpdater

	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
}

type versionPurger interface {
	versionUpdater
	bookFileLister

	GetBookByID(id string) (*database.Book, error)
}

type TrashedVersionCleaner interface {
	versionPurger

	ListTrashedBookVersions() ([]database.BookVersion, error)
}

// Exported: internal/importer forwards its store into CreateIngestVersion.
type IngestStore interface {
	FingerprintReader
	bookFileLister

	// CreateIngestVersion hands the store to fileops.ValidateUserPath, which
	// declares exactly this. Embedded by name so it re-narrows on its own.
	database.ImportPathStore

	CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error)
	GetActiveVersionForBook(bookID string) (*database.BookVersion, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

type swapStore interface {
	versionUpdater
	bookFileLister

	GetBookByID(id string) (*database.Book, error)
	GetBookVersion(id string) (*database.BookVersion, error)
	RecordPathChange(change *database.BookPathChange) error
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// unusedStore is `any` because the two functions that take it are stubs which
// never touch the store: scanFileHashMatch does `_ = store` and returns nil
// pending a file_hash->version_id index, and ResumeVersionSwaps carries a TODO
// for ListBookVersionsByStatus. Both declared database.Store -- 398 methods to
// use none of them. When either is implemented, give it the interface it then
// actually needs rather than restoring the union.
type unusedStore = any
