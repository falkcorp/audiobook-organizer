// file: internal/database/iface_book.go
// version: 2.13.0
// guid: 668ec5a2-f8d9-4fdb-b0d5-09937b5d83ea
// last-edited: 2026-08-23

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

// BookByIDReader fetches books by their primary identifiers.
type BookByIDReader interface {
	GetBookByID(id string) (*Book, error)
	// GetBooksByIDs returns the full Book rows for ids, preserving input
	// order and silently skipping IDs that do not resolve (mirrors
	// GetBookByID's nil-on-not-found). Full fidelity: reads the complete
	// book:<id> row — heavy fields (AcoustIDFingerprint etc.) intact.
	GetBooksByIDs(ids []string) ([]Book, error)
	// ListBookIDs returns just the IDs of all non-deleted books, without
	// materializing Book structs. Saves ~50x memory vs GetAllBooksCore(0,0)
	// when the caller only needs the ID set (e.g., diff'ing against another set).
	ListBookIDs() ([]string, error)
}

// BookBulkReader reads the whole library in bulk shapes.
type BookBulkReader interface {
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
	GetAllBookSummaries(limit, offset int) ([]BookSummary, error)
}

// BookLookupReader resolves books by a natural key: path, hash, or external ID.
type BookLookupReader interface {
	GetBookByFilePath(path string) (*Book, error)
	GetBookByITunesPersistentID(persistentID string) (*Book, error)
	ListBooksByITunesPID(limit, offset int) ([]Book, error)
	GetBookByFileHash(hash string) (*Book, error)
	GetBookByOriginalHash(hash string) (*Book, error)
	GetBookByOrganizedHash(hash string) (*Book, error)
	// GetBookIDsByISBNASIN returns the distinct book IDs whose ISBN10, ISBN13,
	// or ASIN match any of the supplied non-empty values.  It is a set-union:
	// an ID is returned if it appears in any of the three index namespaces.
	// Returns IDs only — callers load the full Book via GetBookByID when needed.
	// Returns an empty slice (not nil, not error) when no match is found.
	// Only valid after the book_isbn_index_v1_done flag is set; callers must
	// gate on that flag themselves.
	GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error)
	GetBooksByMetadataSourceHash(hash string) ([]Book, error)
}

// BookDuplicateReader finds candidate duplicates.
type BookDuplicateReader interface {
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
}

// BookRelationReader reads books via their relations to other entities.
type BookRelationReader interface {
	// GetBooksBySeriesIDCore is Core-typed (STOREFID W4): the return type is
	// BookCore, not Book, so the nine heavy fields (Description,
	// VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments,
	// BookSigBuiltAt, BookSigCoveragePct, Author, Series) being absent is
	// compiler-enforced rather than silently nil'd. A caller that needs any
	// of those MUST fetch via GetBookByID / GetAllBooksFullFrom (full
	// Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBooksBySeriesIDCore(seriesID int) ([]BookCore, error)
	// GetBooksBySeriesIDAllVersions is GetBooksBySeriesIDCore without the
	// primary-version filter — the COMPLETE set of live books attached to
	// the series. Merges and deletes must use THIS one: a non-primary
	// version invisible to the listing getter is a book the merge will not
	// repoint before deleting the series, which strands it (series_bookref.go
	// measured 6,893 phantom series IDs held by 13,322 live books from
	// exactly that). Listing/display callers want the Core twin above.
	// Soft-deleted books are excluded from BOTH; the unfiltered
	// SeriesRefCounts counter is what covers trashed rows.
	// Core-typed on the same rationale as GetBooksBySeriesIDCore.
	GetBooksBySeriesIDAllVersions(seriesID int) ([]BookCore, error)
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
}

// BookSearchReader covers search and facet enumeration.
type BookSearchReader interface {
	SearchBooks(query string, limit, offset int) ([]Book, error)
	GetDistinctGenres() ([]string, error)
	GetDistinctLanguages() ([]string, error)
}

// BookCountReader reports library counts.
type BookCountReader interface {
	CountPrimaryBooks() (int, error)
	// CountAllBooks returns the total number of non-deleted books regardless of
	// IsPrimaryVersion. Use this when iterating with GetAllBooksCore/PageBooks so
	// progress denominators match what the iterator actually visits.
	CountAllBooks() (int, error)
	CountQuarantinedBooks() (int, error)
}

// BookSnapshotReader reads historical versions of a book.
type BookSnapshotReader interface {
	GetBookSnapshots(id string, limit int) ([]BookSnapshot, error)
	GetBookAtVersion(id string, ts time.Time) (*Book, error)
}

// BookLifecycleReader reads books in non-active lifecycle states.
type BookLifecycleReader interface {
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]Book, error)
	GetBookTombstone(id string) (*Book, error)
	ListBookTombstones(limit int) ([]Book, error)
	GetQuarantinedBooks(limit, offset int) ([]Book, error)
}

// BookITunesReader reads iTunes sync work queues.
type BookITunesReader interface {
	GetITunesDirtyBooks() ([]Book, error)
	GetITunesPurgePendingBooks() ([]Book, error)
}

// BookReader is the read-only slice of Store for callers that only
// read books. See spec 2026-04-17-store-interface-segregation-design.md.
//
// Split into the ten interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves.
// The type checker proves that: every implementation -- PebbleStore (496
// methods) and database.MockStore (399) among them -- fails to compile if a
// method is dropped or re-signatured in the regrouping.
//
// Consumers should migrate to whichever of the ten they use; this composition
// is the transitional shape, not the destination.
type BookReader interface { //nolint:interfacebloat // transitional composition of the ten
	// interfaces above, deleted once consumers migrate to the piece each uses. Was 35
	// methods; this is 10 embeds and shrinks to 0 as migration proceeds.
	BookByIDReader
	BookBulkReader
	BookLookupReader
	BookDuplicateReader
	BookRelationReader
	BookSearchReader
	BookCountReader
	BookSnapshotReader
	BookLifecycleReader
	BookITunesReader
}

// BookMutator creates, updates and deletes books.
type BookMutator interface {
	CreateBook(book *Book) (*Book, error)
	UpdateBook(id string, book *Book) (*Book, error)
	UpdateBookRating(id string, req UpdateBookRatingRequest) error
	DeleteBook(id string) error
}

// BookSyncMarker records that a book has been written out or synced.
type BookSyncMarker interface {
	SetLastWrittenAt(id string, t time.Time) error
	MarkITunesSynced(bookIDs []string) (int64, error)
}

// BookVersionMutator manipulates a book's version history.
type BookVersionMutator interface {
	RevertBookToVersion(id string, ts time.Time) (*Book, error)
	PruneBookSnapshots(id string, keepCount int) (int, error)
	// CountBookSnapshots reports how many snapshots a book has WITHOUT reading
	// their payloads, so a prune can be planned without paying for one.
	CountBookSnapshots(id string) (int, error)
}

// BookTombstoneWriter manages tombstones for deleted books.
type BookTombstoneWriter interface {
	CreateBookTombstone(book *Book) error
	DeleteBookTombstone(id string) error
}

// BookScanFailStore tracks consecutive scan failures per book.
type BookScanFailStore interface {
	// Scan-fail counter for auto-quarantine (keyed on sha256[:8] of path).
	GetScanFailCount(pathHash string) (int, error)
	IncrScanFailCount(pathHash string) (int, error)
	ResetScanFailCount(pathHash string) error
}

// BookAggregateWriter performs merges and aggregate recomputation.
type BookAggregateWriter interface {
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

// BookWriter is the write-only slice of Store for callers that only
// mutate books.
//
// Split into the 6 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it, because every implementation -- PebbleStore (496 methods)
// and database.MockStore (399) among them -- fails to compile on a dropped or
// re-signatured method.
type BookWriter interface {
	BookMutator
	BookSyncMarker
	BookVersionMutator
	BookTombstoneWriter
	BookScanFailStore
	BookAggregateWriter
}

// BookStore combines BookReader and BookWriter for callers that need both.
type BookStore interface {
	BookReader
	BookWriter
}
