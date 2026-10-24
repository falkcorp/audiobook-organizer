// file: internal/database/iface_retired.go
// version: 1.3.0
// guid: 4d6b0c1e-8f3a-4b57-9e21-6a7c52d9f0b3
// last-edited: 2026-10-24

package database

// Retired book versions.
//
// A retired book_file row is a row that was a live file of a trashed or merged
// "shell" book and was repointed to the live book that replaced it, so no row
// is ever deleted: it is re-keyed under the live book (the holder), flagged
// BookFile.Retired, and tied to a book_versions row of status
// BookVersionStatusRetired. Which reads skip it:
//
//   - SKIP retired rows: GetBookFiles, GetAllBookFilesCore (and the Core and
//     visitor variants, GetBookFilesForIDsCore), the counts and duplicate-hash
//     stats, GetBookFileByPath, GetBookFileByPID, GetBookFileByAcoustID,
//     GetBookFileByAcoustIDFuzzy and GetBookBySegmentFileHash (each answers
//     "which live file has this identity", so an identity held only by a
//     retired row answers nil).
//   - RETURN retired rows by design, and the caller must decide what a retired
//     row means: GetBookFileByID (an explicit identity), BookFilesAtPath and
//     BookFilesWithHash (the complete "every row" claim lookups), and the
//     ID-only LookupAcoustIDCandidates and LSHProbe (their callers read the row
//     and must skip a Retired one; the fpidx: value still names the shell).
//
// These two interfaces, plus the include-retired readers below, are the only
// ways to see or move a retired row through the default-skipping reads.
//
// They are capabilities, not part of Store (nothing here widens the Store
// interface): resolve them with AsCapability[RetiredBookFileWriter](store).

// RetiredBookFileReader reads the retired side of the book_file table.
type RetiredBookFileReader interface {
	// GetBookFilesIncludingRetired returns every row keyed under bookID, live
	// and retired, in GetBookFiles' order (disc, track, file_path).
	GetBookFilesIncludingRetired(bookID string) ([]BookFile, error)
	// GetRetiredBookFiles returns only the retired rows keyed under holderID
	// (the live book they were retired into), in GetBookFiles' order.
	GetRetiredBookFiles(holderID string) ([]BookFile, error)
	// GetAllBookFilesCoreIncludingRetired is GetAllBookFilesCore with the
	// retired rows kept (memdb when published, a Pebble scan otherwise).
	GetAllBookFilesCoreIncludingRetired() ([]BookFileCore, error)
	// GetRetiredVersionsFromBook returns the retired versions that were
	// retired out of shellID, however many the book has.
	GetRetiredVersionsFromBook(shellID string) ([]BookVersion, error)
	// GetBookFileByPIDIncludingRetired is GetBookFileByPID that also answers a
	// PID held only by a retired row (the iTunes importers use it to recognise a
	// track whose version was retired into another book).
	GetBookFileByPIDIncludingRetired(itunesPID string) (*BookFile, error)
	// GetBookFileByPathIncludingRetired is GetBookFileByPath that also answers
	// a path held only by a retired row (the single-row path-index hit). Every
	// "is this path already claimed?" check uses it, or BookFilesAtPath, never
	// GetBookFileByPath, which would read a retired version's path as free.
	GetBookFileByPathIncludingRetired(filePath string) (*BookFile, error)
}

// RetiredBookFileWriter retires, restores and re-homes book_file rows.
//
// All three are one atomic batch, refuse rather than skip, and leave the
// book_file row count unchanged: nothing here deletes a row.
type RetiredBookFileWriter interface {
	// RetireBookFiles re-keys the rows named by moves from their source book
	// (the shell) to holderID, flags them Retired and sets their VersionID to
	// versionID. versionID must be an existing version of BookID holderID and
	// Status BookVersionStatusRetired. See retired_book_files.go for the
	// index rule and the refusals.
	RetireBookFiles(moves []BookFileMove, holderID, versionID string) error
	// UnretireBookFiles is the exact inverse of RetireBookFiles: the rows named
	// by moves (keyed under the holder, flagged Retired, carrying
	// retireVersionID) go back under backToBookID, un-flagged, with their
	// VersionID restored from versionIDBefore (absent key = empty).
	UnretireBookFiles(moves []BookFileMove, backToBookID, retireVersionID string, versionIDBefore map[string]string) error
	// RehomeRetiredVersion moves a retired version and all its retired rows to
	// newHolderID, for the chain case where the holder is itself retired. The
	// new holder must be live; the inverse is RetiredVersionHolderRestorer.
	RehomeRetiredVersion(versionID, newHolderID string) error
}

// RetiredVersionHolderRestorer is the revert of RehomeRetiredVersion. It is its
// own capability, not a RetiredBookFileWriter method, so the writer's other
// implementers (the retired-aware test fakes) are not widened by it.
type RetiredVersionHolderRestorer interface {
	// RestoreRetiredVersionHolder moves a retired version and all its retired
	// rows back to previousHolderID, the holder a rehome took it from. Unlike
	// RehomeRetiredVersion it accepts a soft-deleted previousHolderID (the
	// chain case trashed it, and a revert does not un-trash it) and refuses one
	// that does not exist (ErrBookFileOwnerMissing).
	RestoreRetiredVersionHolder(versionID, previousHolderID string) error
}

var (
	_ RetiredBookFileReader = (*PebbleStore)(nil)
	_ RetiredBookFileWriter = (*PebbleStore)(nil)

	_ RetiredVersionHolderRestorer = (*PebbleStore)(nil)
)
