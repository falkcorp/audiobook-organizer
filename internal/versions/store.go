// file: internal/versions/store.go
// version: 1.11.0
// guid: 5e81b3d7-92c4-4a06-8f15-6b0d2a749c38
// last-edited: 2026-10-29

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

// TorrentHolderReader is what LiveTorrentHolder needs: the torrent-hash index
// plus the book lookup that tells a live holder from a stale index entry.
// Exported: internal/importer and internal/server forward their stores into it.
type TorrentHolderReader interface {
	FingerprintReader
	GetBookByID(id string) (*database.Book, error)
}

type versionPurger interface {
	versionUpdater
	bookFileLister

	// GetBookVersionsByBookID finds a book's active version, whose files
	// the purge must not delete.
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
	// BookFilesAtPath lists EVERY row at a path, retired rows included, so a
	// purge never removes a file another row (a retired version's) still names,
	// and finds ANOTHER live book that serves a file of the version: deluge and
	// import folders are shared. It returns database.ErrBookFilesAtPathUnavailable
	// when it cannot be complete; the purge then deletes nothing.
	BookFilesAtPath(path string) ([]database.BookFile, error)
	// GetBookByFilePath finds a book whose own FilePath is the path or a folder
	// above it (the served-file guard walks the ancestors).
	GetBookByFilePath(path string) (*database.Book, error)

	// The book-row side of "another book names it" (a book can name a file no
	// book_file row does, and a trashed book is restorable) is not here:
	// database.BookRowPathReader is not on Store, so otherBooksNamingPath finds
	// it with database.AsCapability and fails closed without it.

	GetBookByID(id string) (*database.Book, error)

	// GetBookVersion re-reads the version under the merge lock, after a wait
	// that can last minutes (purgeVersionLocked).
	GetBookVersion(id string) (*database.BookVersion, error)
}

type TrashedVersionCleaner interface {
	versionPurger

	// ListTrashedBookVersions lists the trash, the purges that were claimed and
	// did not finish (status purging), so the cleanup retries those too, and
	// the purges that gave up (status purge_failed), which it counts as
	// needing attention and does not retry.
	ListTrashedBookVersions() ([]database.BookVersion, error)
	// ListSwappingBookVersions lists the versions a swap left in swapping_*;
	// the cleanup reports their books (SwapIncompleteLister) and touches
	// nothing.
	ListSwappingBookVersions() ([]database.BookVersion, error)
	// GetBookVersion (from versionPurger) also re-reads each listed row before
	// it is stamped or purged, narrowing the window in which a mid-run restore
	// is undone.
}

// Exported: internal/importer forwards its store into CreateIngestVersion.
type IngestStore interface {
	// CheckFingerprint, plus LiveTorrentHolder's book lookup, so the function
	// refuses a torrent another live version holds on its own.
	TorrentHolderReader
	bookFileLister

	// CreateIngestVersion hands the store to fileops.ValidateUserPath, which
	// declares exactly this. Embedded by name so it re-narrows on its own.
	database.ImportPathStore

	CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error)
	GetActiveVersionForBook(bookID string) (*database.BookVersion, error)
	// GetBookVersion resolves a row's existing VersionID, so a re-run over an
	// already-linked row returns that version instead of minting another.
	GetBookVersion(id string) (*database.BookVersion, error)
	// GetBookVersionsByBookID finds a version an earlier run created but
	// never linked, so a re-run adopts it instead of creating a duplicate.
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
	// ModifyBookFile is the link write: a compare-and-set that sets only the
	// link fields on the CURRENT row. It replaced UpdateBookFile on 2026-10-11,
	// whose write-back of the row read at the start of CreateIngestVersion
	// reverted any other writer's change that landed in between.
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

type swapStore interface {
	versionUpdater
	bookFileLister

	GetBookByID(id string) (*database.Book, error)
	GetBookVersion(id string) (*database.BookVersion, error)
	RecordPathChange(change *database.BookPathChange) error
	// ModifyBook replaced UpdateBook on 2026-09-14: the swap's one book write
	// lands after the file moves, so it sets only file_path on the stored row
	// under the book's write lock. See database.BookMutator.
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	// ModifyBookFile replaced UpdateBookFile on 2026-10-28: the swap repoints a
	// row after its file moved, and writes only file_path, and only if the row
	// still names the path the file was moved from, so a row another writer
	// changed meanwhile is reported instead of being written over.
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// swapPlanStore is what the resume of an unfinished swap reads: the book, its
// versions and file rows, and every row that names a path (so a file another
// book's row names is not taken for the swap's).
type swapPlanStore interface {
	bookFileLister
	GetBookByID(id string) (*database.Book, error)
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
	// BookFilesAtPath lists EVERY row at a path, retired rows included; it
	// returns database.ErrBookFilesAtPathUnavailable when it cannot be complete.
	BookFilesAtPath(path string) ([]database.BookFile, error)
}

// SwapResumeStore is what ResumeVersionSwap needs: the reads of swapPlanStore
// and the writes RunVersionSwap makes (statuses, file rows, the book's path and
// its audit record). Exported: internal/server forwards its store into it.
type SwapResumeStore interface {
	swapPlanStore
	swapStore
}

// SwapIncompleteLister is what ListSwapIncompleteBooks needs: the versions a
// swap left in swapping_* and the reads that describe their books. The trash
// cleanup's TrashedVersionCleaner and internal/server forward their stores into
// it. Exported for that.
type SwapIncompleteLister interface {
	swapPlanStore
	ListSwappingBookVersions() ([]database.BookVersion, error)
}

// unusedStore is `any` because the one function that takes it is a stub that
// never touches the store: scanFileHashMatch does `_ = store` and returns nil
// pending a file_hash->version_id index. It declared database.Store -- 398
// methods -- to use none of them. When it is implemented, give it the
// interface it then actually needs rather than restoring the union.
type unusedStore = any
