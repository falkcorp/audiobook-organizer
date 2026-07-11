// file: internal/database/iface_book.go
// version: 2.11.1
// guid: 668ec5a2-f8d9-4fdb-b0d5-09937b5d83ea
// last-edited: 2026-07-11

package database

import "time"

// UpdateBookRatingRequest carries partial-update fields for user ratings.
// Each field uses a pointer so the caller can distinguish "omitted" (nil)
// from "set to zero/empty" (non-nil pointing to zero value).
// To clear a rating to NULL, set ClearOverall = true (etc.).
type UpdateBookRatingRequest struct {
	Overall      *float64
	ClearOverall bool
	Story        *float64
	ClearStory   bool
	Performance  *float64
	ClearPerf    bool
	Notes        *string
	ClearNotes   bool
}

// BookReader is the read-only slice of Store for callers that only
// read books. See spec 2026-04-17-store-interface-segregation-design.md.
type BookReader interface {
	GetBookByID(id string) (*Book, error)
	// GetBooksByIDs returns the full Book rows for ids, preserving input
	// order and silently skipping IDs that do not resolve (mirrors
	// GetBookByID's nil-on-not-found). Full fidelity: reads the complete
	// book:<id> row — heavy fields (AcoustIDFingerprint etc.) intact.
	GetBooksByIDs(ids []string) ([]Book, error)
	// GetAllBooksCore is Core-typed (STOREFID W5a/W5z): the return type is
	// BookCore, not Book, so the nine heavy fields (Description,
	// VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments,
	// BookSigBuiltAt, BookSigCoveragePct, Author, Series) being absent is
	// compiler-enforced rather than silently nil'd. A caller that needs any
	// of the heavy fields MUST fetch via GetBookByID / GetAllBooksFullFrom
	// (full Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetAllBooksCore(limit, offset int) ([]BookCore, error)
	// GetAllBooksFullFrom returns up to limit non-deleted books whose PebbleDB key
	// sorts after "book:<afterID>". Pass afterID="" to start from the beginning.
	// This is an O(1) seek vs GetAllBooksCore's O(offset) skip — use for search
	// index backfill and other full-table cursor scans.
	GetAllBooksFullFrom(afterID string, limit int) ([]Book, error)
	// ListBookIDs returns just the IDs of all non-deleted books, without
	// materializing Book structs. Saves ~50x memory vs GetAllBooksCore(0,0)
	// when the caller only needs the ID set (e.g., diff'ing against another set).
	ListBookIDs() ([]string, error)
	GetAllBookSummaries(limit, offset int) ([]BookSummary, error)
	GetBookByFilePath(path string) (*Book, error)
	GetBookByITunesPersistentID(persistentID string) (*Book, error)
	ListBooksByITunesPID(limit, offset int) ([]Book, error)
	GetBookByFileHash(hash string) (*Book, error)
	GetBookByOriginalHash(hash string) (*Book, error)
	GetBookByOrganizedHash(hash string) (*Book, error)
	GetDuplicateBooks() ([][]Book, error)
	// GetFolderDuplicatesCore is Core-typed (STOREFID W6): the return type is
	// BookCore, not Book, so the nine heavy fields being absent is
	// compiler-enforced rather than silently nil'd. A caller that needs any
	// of the heavy fields MUST fetch via GetBookByID (full Pebble). See
	// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetFolderDuplicatesCore() ([][]BookCore, error)
	// GetDuplicateBooksByMetadataCore is Core-typed (STOREFID W6): see
	// GetFolderDuplicatesCore's doc comment.
	GetDuplicateBooksByMetadataCore(threshold float64) ([][]BookCore, error)
	GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]Book, error)
	// GetBooksBySeriesIDCore is Core-typed (STOREFID W4): the return type is
	// BookCore, not Book, so the nine heavy fields (Description,
	// VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments,
	// BookSigBuiltAt, BookSigCoveragePct, Author, Series) being absent is
	// compiler-enforced rather than silently nil'd. A caller that needs any
	// of those MUST fetch via GetBookByID / GetAllBooksFullFrom (full
	// Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksBySeriesIDCore(seriesID int) ([]BookCore, error)
	// GetBooksByAuthorIDCore is Core-typed (STOREFID P3-W2): the memdb
	// projection is stripped of the nine heavy fields (Description,
	// VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments,
	// BookSigBuiltAt, BookSigCoveragePct, Author, Series), and that fact is
	// now enforced by the return TYPE rather than left as a comment a caller
	// can miss. A caller that needs any of those MUST fetch via GetBookByID
	// / GetAllBooksFullFrom (full Pebble). See
	// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksByAuthorIDCore(authorID int) ([]BookCore, error)
	GetBooksByVersionGroup(groupID string) ([]Book, error)
	GetBooksByMetadataSourceHash(hash string) ([]Book, error)
	// GetBookIDsByISBNASIN returns the distinct book IDs whose ISBN10, ISBN13,
	// or ASIN match any of the supplied non-empty values.  It is a set-union:
	// an ID is returned if it appears in any of the three index namespaces.
	// Returns IDs only — callers load the full Book via GetBookByID when needed.
	// Returns an empty slice (not nil, not error) when no match is found.
	// Only valid after the book_isbn_index_v1_done flag is set; callers must
	// gate on that flag themselves.
	GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error)
	SearchBooks(query string, limit, offset int) ([]Book, error)
	CountPrimaryBooks() (int, error)
	// CountAllBooks returns the total number of non-deleted books regardless of
	// IsPrimaryVersion. Use this when iterating with GetAllBooksCore/PageBooks so
	// progress denominators match what the iterator actually visits.
	CountAllBooks() (int, error)
	GetDistinctGenres() ([]string, error)
	GetDistinctLanguages() ([]string, error)
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]Book, error)
	GetBookSnapshots(id string, limit int) ([]BookSnapshot, error)
	GetBookAtVersion(id string, ts time.Time) (*Book, error)
	GetBookTombstone(id string) (*Book, error)
	ListBookTombstones(limit int) ([]Book, error)
	GetITunesDirtyBooks() ([]Book, error)
	GetITunesPurgePendingBooks() ([]Book, error)
	GetQuarantinedBooks(limit, offset int) ([]Book, error)
	CountQuarantinedBooks() (int, error)
}

// BookWriter is the write-only slice of Store for callers that only
// mutate books.
type BookWriter interface {
	CreateBook(book *Book) (*Book, error)
	UpdateBook(id string, book *Book) (*Book, error)
	UpdateBookRating(id string, req UpdateBookRatingRequest) error
	DeleteBook(id string) error
	SetLastWrittenAt(id string, t time.Time) error
	MarkITunesSynced(bookIDs []string) (int64, error)
	RevertBookToVersion(id string, ts time.Time) (*Book, error)
	PruneBookSnapshots(id string, keepCount int) (int, error)
	CreateBookTombstone(book *Book) error
	DeleteBookTombstone(id string) error
	// Scan-fail counter for auto-quarantine (keyed on sha256[:8] of path).
	GetScanFailCount(pathHash string) (int, error)
	IncrScanFailCount(pathHash string) (int, error)
	ResetScanFailCount(pathHash string) error
	// MergeChapterBooks absorbs srcIDs into primaryID: moves all book_files to
	// primaryID, marks source books as non-primary (is_primary_version=0,
	// merged_into_book_id=primaryID), and updates the primary book's duration
	// (rounded to nearest second) and title. Runs in a single transaction.
	MergeChapterBooks(primaryID string, srcIDs []string, commonTitle string, totalDuration float64) error
	// FlagMetadataHashDuplicate marks duplicateID as absorbed into primaryID by
	// setting merged_into_book_id=primaryID and is_primary_version=0 on the
	// duplicate. Used by MATCH-4 auto-dedup at metadata-apply time.
	FlagMetadataHashDuplicate(primaryID, duplicateID string) error
	// RecomputeBookAggregates sums Duration and FileSize from the book's
	// BookFile records and writes the result back to the Book row.
	// Applies the partial-data rule: if the existing snapshot was computed
	// from more files-with-durations than the current file set exposes, the
	// old value is preserved and a warning is logged instead of overwriting
	// with a less-complete sum. Idempotent; safe to call from BookFile
	// create/update/delete paths as a best-effort hook.
	RecomputeBookAggregates(bookID string) error
}

// BookStore combines BookReader and BookWriter for callers that need both.
type BookStore interface {
	BookReader
	BookWriter
}
