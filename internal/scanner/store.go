// file: internal/scanner/store.go
// version: 1.8.0
// guid: 0a5f8c34-9b26-4e71-83d0-6f2a41e75b98
// last-edited: 2026-09-13

package scanner

import (
	"context"

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
	scanWorksStore
}

// scanWorksStore is the works surface: the per-book association lookups, and
// the works lookup cache's load (see scanner.go worksLookupCache).
type scanWorksStore interface {
	GetAllWorks() ([]database.Work, error)
	// ForEachWork is the cancelable works load the works lookup cache uses.
	ForEachWork(ctx context.Context, visit func(database.Work) error) error
	// WorksGeneration lets the works lookup cache survive a scan restart: a
	// cache whose generation still matches the store's is reused, not reloaded.
	WorksGeneration() uint64
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
	// GetRaw/SetRaw hold the batch AI parse's per-file give-up markers
	// (ai_parse_giveup.go) -- the same kind of per-path "stop retrying a file
	// that keeps failing" bookkeeping as the scan fail count above. Raw KV
	// rather than a Book column: the marker is keyed by PATH so a rename makes
	// the file eligible again with no reset step, and it needs no schema
	// change. Both are on database.Store (RawKVStore), so the production
	// indexedStore satisfies this without a type assertion.
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
}

// scannerStore is the whole surface the package globals carry.
type scannerStore interface {
	scanHashLookup
	scanBookLookup
	scanBookWriter
	scanEntityStore
	scanProgressStore
	// The lock guard's surface (field-state rows + the legacy blob), owned by
	// internal/database so the scanner and every metafetch apply path read
	// locks the same way.
	database.MetadataFieldStateReader

	// Forwarded, embedded by name so each re-narrows on its own.
	scanServiceStore
	merge.UserProgressMerger
}
