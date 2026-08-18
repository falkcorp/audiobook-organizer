// file: internal/database/iface_bookfile.go
// version: 1.0.0
// guid: 5247968b-3814-4892-879d-a8a5531c2960
// last-edited: 2026-08-18

package database

import (
	"context"
)

// Book files, segments, and hash bookkeeping.
//
// Split out of iface_misc.go on 2026-08-18, which held 27 interface
// declarations in one file. A file named `misc` is where wide interfaces go to
// avoid review: BookFileStore reached 27 methods while living there.

// BookFileStore covers the canonical BookFile surface.
type BookFileStore interface {
	CreateBookFile(file *BookFile) error
	UpdateBookFile(id string, file *BookFile) error
	GetBookFiles(bookID string) ([]BookFile, error)
	// GetAllBookFilesCore returns the BookFileCore projection (SLIM — memdb
	// projection under UseMemDB, projected via .Core() under Pebble-direct) —
	// see docs/specs/2026-07-05-store-getter-fidelity-unification.md. The
	// BookFileCore return type makes reading a stripped fingerprint field a
	// compile error instead of a silent nil.
	GetAllBookFilesCore() ([]BookFileCore, error)
	// GetBookFilesNeedingDelugeImportCore returns book_files that have a
	// deluge_hash but have not yet been copied into the library
	// (imported_from_deluge_at IS NULL). Core-typed (STOREFID W6): the return
	// type is BookFileCore, not BookFile, so the heavy fingerprint-diagnostic
	// fields (FingerprintFailureReason/Detail/DiagnosticJSON,
	// AcoustIDFingerprint, AcoustIDSeg0..6) being absent is compiler-enforced
	// rather than silently nil'd (FingerprintFailedAt and
	// AcoustIDFingerprintDurationSec are retained on Core). A caller that
	// needs any of the stripped fields MUST fetch via GetBookFiles(bookID)
	// (full Pebble). See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
	GetBookFilesNeedingDelugeImportCore() ([]BookFileCore, error)
	GetBookFileByID(bookID, fileID string) (*BookFile, error)
	GetBookFileByPID(itunesPID string) (*BookFile, error)
	// ClearITunesPID surgically clears itunes_persistent_id and itunes_path
	// on the book_file row matching the given PID. Used by the iTunes
	// orphan-cleanup path so that DB state converges after a successful
	// ITL remove. Returns (false, nil) if no matching row exists.
	ClearITunesPID(itunesPID string) (cleared bool, err error)
	GetBookFileByPath(filePath string) (*BookFile, error)
	GetBookFileByAcoustID(fingerprint string) (*BookFile, error)
	GetBookFileByAcoustIDFuzzy(fingerprint string, minSimilarity float64) (*BookFile, error)
	DeleteBookFile(id string) error
	// DeleteBookFilesByIDs deletes many rows in one Pebble batch, one memdb
	// transaction, and one aggregate recompute PER AFFECTED BOOK — as opposed to
	// DeleteBookFile, which pays that entire fixed cost once per row (~1.35s/row
	// measured on production). Prefer it for any caller deleting more than a
	// couple of rows.
	//
	// Fail-closed: if ANY id does not resolve to a live row, nothing is deleted
	// and an error naming the unresolved ids is returned. Chunk large id sets so
	// one stale id defers only its own chunk.
	DeleteBookFilesByIDs(ids []string) error
	DeleteBookFilesForBook(bookID string) error
	UpsertBookFile(file *BookFile) error
	BatchUpsertBookFiles(files []*BookFile) error
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	// UpdateBookFileHashes is a surgical update that records pre-write and post-write
	// SHA-256 hashes without touching any other BookFile fields.
	UpdateBookFileHashes(id, originalHash, postMetadataHash string) error
	// MarkFileImportedFromDeluge records that a file has been imported from a
	// Deluge download directory. originalPath is the source (download) path,
	// libraryPath is the destination inside the organized library, and
	// torrentHash is the Deluge info-hash (optional). Implementations SHOULD
	// match by originalPath first, then fall back to matching by torrentHash.
	MarkFileImportedFromDeluge(ctx context.Context, originalPath, libraryPath, torrentHash string) error
	// GetDuplicateFilesByHash returns groups of book_files that share the same
	// original_file_hash (non-empty). Each group has ≥2 entries and represents
	// the same physical audio file in multiple locations.
	GetDuplicateFilesByHash(limit int) ([]DuplicateFileGroup, error)
	// GetBookBySegmentFileHash looks up a BookFile by file_hash or
	// original_file_hash and returns the parent Book. Used by the scanner's
	// multi-file dedup tally to match individual segment files across folders
	// without assuming that whole-directory == same book.
	GetBookBySegmentFileHash(hash string) (*Book, error)
	// SetBookFileHash sets file_hash on a book_file row, mirroring what the
	// scanner does on initial import. Also sets original_file_hash if it is
	// currently empty. Used by the backfill handler to populate hashes for
	// files that were imported before hash tracking was added.
	SetBookFileHash(id, hash string) error
	// GetBookFileHashStats returns aggregate hash-coverage statistics for all
	// book_files in the library, including a per-library-path breakdown.
	GetBookFileHashStats() (*BookFileHashStats, error)
	// GetBookMetadataHashStats returns aggregate metadata_source_hash coverage
	// across all books, including a per-library-path breakdown.
	GetBookMetadataHashStats() (*BookMetadataHashStats, error)
	// GetFilesWithFingerprintFailures returns book_files where FingerprintFailedAt is set,
	// optionally filtered by reason. Returns the filtered page plus total matching count.
	GetFilesWithFingerprintFailures(reason string, limit, offset int) ([]BookFile, int64, error)
	// GetFilesWithZeroDurationFingerprint returns book_files where AcoustIDFingerprint is
	// set but AcoustIDFingerprintDurationSec==0 (STOREFID DurationSec invariant violation
	// — legacy rows the memdb-proxy-based fingerprint ops silently skip). Returns the
	// filtered page plus total matching count.
	GetFilesWithZeroDurationFingerprint(limit, offset int) ([]BookFile, int64, error)
	// GetAcoustIDStats returns AcoustID fingerprint coverage across all book files,
	// including a per-library-root breakdown.
	GetAcoustIDStats() (*AcoustIDStats, error)
}

type BookFileHashUpdater interface {
	// UpdateBookFileHashes records the SHA-256 fingerprints taken before and
	// after a tag write. original_file_hash is written only if the row has
	// no existing value (first-write semantics); post_metadata_hash is always
	// overwritten with the latest post-write fingerprint.
	UpdateBookFileHashes(fileID, originalHash, postHash string) error
}

// BookSegmentStore covers the deprecated segment surface, kept until
// the segment-removal PR.
type BookSegmentStore interface {
	CreateBookSegment(bookNumericID int, segment *BookSegment) (*BookSegment, error)
	UpdateBookSegment(segment *BookSegment) error
	ListBookSegments(bookNumericID int) ([]BookSegment, error)
	MergeBookSegments(bookNumericID int, newSegment *BookSegment, supersedeIDs []string) error
	GetBookSegmentByID(segmentID string) (*BookSegment, error)
	MoveSegmentsToBook(segmentIDs []string, targetBookNumericID int) error
}

// HashBlocklistStore covers DoNotImport entries.
type HashBlocklistStore interface {
	IsHashBlocked(hash string) (bool, error)
	AddBlockedHash(hash, reason string) error
	RemoveBlockedHash(hash string) error
	GetAllBlockedHashes() ([]DoNotImport, error)
	GetBlockedHashByHash(hash string) (*DoNotImport, error)
}
