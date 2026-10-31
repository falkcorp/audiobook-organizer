// file: internal/database/iface_retired.go
// version: 1.4.3
// guid: 4d6b0c1e-8f3a-4b57-9e21-6a7c52d9f0b3
// last-edited: 2026-10-31

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
//     and must skip a Retired one; the fpidx: value names the holder, since the
//     retire batch rewrites it with the row).
//
// THE ONE RULE for what a retired row means to a caller. Two different questions
// get two different answers, and a caller must know which one it is asking:
//
//   - "Which live book owns this file?" (identifying an owner: the twin of a
//     leftover, the survivor of a same-path retire, the book a scan attributes a
//     file to). A retired row is NOT an owner. It is keyed under its holder only
//     to keep the row addressable, so counting it would make the holder look like
//     a second owner of a file its live sibling owns.
//   - "May I put a row on this file?" (creating a book_file, repointing one onto
//     the path, building rows from a folder, regrouping a gap). A retired row IS a
//     claim, whoever holds it, whether or not the holder is live and even when
//     the holder is the book doing the writing: the file is that retired
//     version's file, and two rows on one file is the corruption the retired-
//     version model exists to prevent. Gate such a write on the complete
//     BookFilesAtPath answer and refuse on RetiredClaimOnPath.
//
// Never answer the second question with GetBookFileByPath: it filters the
// retired row out and so says "free" for a file a retired row holds.
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
	// GetBookFileByPathIncludingRetired is GetBookFileByPath that also answers a
	// path held only by a retired row. It reads the single-owner path index, so
	// it is one row per CRC (last writer wins); a caller gating a write on
	// "nothing holds this path" must use BookFilesAtPath instead.
	GetBookFileByPathIncludingRetired(path string) (*BookFile, error)
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

// RetiredPathKeyClaimer hands a path's single-owner key back to a retired row.
// ClaimBookFilePathKey (the Store method) refuses a retired row on purpose; an
// undo that vacated a path a retired row keyed uses this instead, and it never
// takes the key from a live row. See PebbleStore.ClaimRetiredBookFilePathKey.
type RetiredPathKeyClaimer interface {
	ClaimRetiredBookFilePathKey(bookID, fileID, path string) (claimed bool, err error)
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

// GuardedBookVersionDeleter is DeleteBookVersion with a compare-and-set. It is
// its own capability, not a Store method, so the Store interface and its mocks
// are not widened by it.
type GuardedBookVersionDeleter interface {
	// DeleteBookVersionIf deletes version id like DeleteBookVersion, but first
	// runs guard on the version as it stands (nil when it does not exist),
	// under the version's write stripe: UpdateBookVersion and the retired-row
	// writers hold the same stripe across read and commit, so no edit can land
	// between the guard and the delete. A guard error aborts with nothing
	// written and is returned as it is. With a nil guard, a missing version is
	// a no-op, as for DeleteBookVersion.
	DeleteBookVersionIf(id string, guard func(cur *BookVersion) error) error
}

var (
	_ RetiredBookFileReader = (*PebbleStore)(nil)
	_ RetiredBookFileWriter = (*PebbleStore)(nil)
	_ RetiredPathKeyClaimer = (*PebbleStore)(nil)

	_ RetiredVersionHolderRestorer = (*PebbleStore)(nil)
	_ GuardedBookVersionDeleter    = (*PebbleStore)(nil)
)
