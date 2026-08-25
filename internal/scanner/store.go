// file: internal/scanner/store.go
// version: 1.4.0
// guid: 0a5f8c34-9b26-4e71-83d0-6f2a41e75b98
// last-edited: 2026-08-25

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The store surface this package needs, measured with an empty-interface
// compiler probe under -gcflags=-e: 22 direct calls plus two forwarding
// constraints, exhaustive. It was database.Store -- 398 methods -- until
// 2026-08-19, and could not be narrowed sooner: merge.FollowBookIDChange took
// the union until #2581 and its interface was unexported until #2587.
//
// Grouped so no group exceeds interfacebloat's limit of 8, and so the hash
// lookups -- the reason a scan is fast -- read as one thing.

// scanHashLookup is the dedup fast path: six different hashes, each answering
// "have I already seen this file/book?" before any expensive work happens.
type scanHashLookup interface {
	GetBookByFileHash(hash string) (*database.Book, error)
	GetBookByOriginalHash(hash string) (*database.Book, error)
	GetBookByOrganizedHash(hash string) (*database.Book, error)
	GetBookBySegmentFileHash(hash string) (*database.Book, error)
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
	IsHashBlocked(hash string) (bool, error)
}

type scanBookLookup interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookByFilePath(path string) (*database.Book, error)
	GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	// Added 2026-08-24 for the queued library.ai-parse operation: a batch that
	// runs after auto-organize must follow the version group to the primary
	// rather than writing to the row organize demoted. See
	// saveAIFieldsToPrimary.
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

type scanBookWriter interface {
	CreateBook(book *database.Book) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	BatchUpsertBookFiles(files []*database.BookFile) error
}

type scanEntityStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
	GetAllWorks() ([]database.Work, error)
	CreateWork(work *database.Work) (*database.Work, error)
}

// scanProgressStore is the per-path bookkeeping that lets a rescan skip files
// and back off on ones that keep failing.
type scanProgressStore interface {
	UpdateScanCache(bookID string, mtime int64, size int64) error
	// MarkNeedsRescan re-arms the per-book rescan flag that UpdateScanCache
	// clears. writeBackScanCache needs it to stop the rescan-age gate deferring
	// a file that is still being written.
	MarkNeedsRescan(bookID string) error
	IncrScanFailCount(pathHash string) (int, error)
	ResetScanFailCount(pathHash string) error
}

// scannerStore is the whole surface the package globals carry.
type scannerStore interface {
	scanHashLookup
	scanBookLookup
	scanBookWriter
	scanEntityStore
	scanProgressStore
	scanFieldStateReader

	// Forwarded, embedded by name so each re-narrows on its own.
	scanServiceStore
	merge.UserProgressMerger
}
