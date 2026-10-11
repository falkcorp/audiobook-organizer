// file: internal/versions/ingest.go
// version: 1.10.0
// guid: 3e1f2a9b-4c5d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-11
//
// Version creation on ingest (spec 3.1 task 5).
//
// Spec 3.1 calls for a BookVersion row on every ingest. Today only one path
// creates one: importer.ImportFile (POST /api/v1/import/file and the Deluge
// discover import), which creates the book_file row first and then calls
// CreateIngestVersion to create the version and link it. Scan and organize do
// not create versions. The version tracks the file's provenance (source,
// torrent hash) and its lifecycle status; the linked book_file row carries
// the identity hash.
//
// New books get an `active` version. Known books adding a second
// copy get an `alt` version — the user must explicitly promote it
// via the swap operation.

package versions

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/logger"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/keyedmutex"
)

// IngestVersionParams describes the provenance of a newly-ingested file.
type IngestVersionParams struct {
	BookID   string
	FilePath string
	// BookFileID names the book_file row the new version is linked to. When
	// set, only that row is linked; when empty, the book's row whose FilePath
	// equals FilePath is. Either way the row must ALREADY EXIST: this function
	// links a row, it never creates one. A caller that creates the row after
	// calling here gets an orphaned version -- which is exactly what every
	// import did until 2026-10-10 (IMPORT-ORPHANED-VERSIONS), because the
	// importer called this before CreateBookFile.
	BookFileID string
	Format     string
	// Source is "deluge" whenever TorrentHash is set.
	Source string // "imported", "scanned", "organized", "deluge"
	// TorrentHash is empty for non-torrent sources. When set, the linked
	// book_file row's DelugeHash is written in the same update that sets its
	// VersionID, so the row carries the hash only when it is linked to the
	// version carrying it -- never one half of the link without the other.
	// A hash a live version already holds is refused with
	// ErrTorrentLinkedElsewhere; see CreateIngestVersion.
	TorrentHash string
	// NewBook tells CreateIngestVersion the caller created BookID moments ago
	// (the importer does), so no earlier run can have stranded a version of
	// it and the stranded-version lookup (GetBookVersionsByBookID) is skipped.
	// Besides saving a read, that keeps a failure of THAT read from aborting
	// the version create, and with it the torrent index entry that turns a
	// retried discover import into a 409 instead of a second book.
	//
	// It narrows the failure surface; it does not remove it. A call with a
	// TorrentHash still reads the torrent index and the holder's book
	// (LiveTorrentHolder), and an error there is returned like any other, with
	// nothing created. That read already succeeded once in the importer's own
	// guard, so a second failure is unlikely, not impossible.
	NewBook bool
	// AdoptVersionID, when set, makes the call adopt-only: the row is linked
	// to the stranded version with this id (see strandedVersion) or to
	// nothing, and no version is ever created. Anything else -- no stranded
	// version, a different one, several, or a row that could not be read --
	// returns ErrVersionNotAdoptable with nothing written. A repair pass sets
	// it (maintenance.relink-pre-fix-imports): a pass that planned "link this
	// row to version V" must not mint a new version when V stopped being
	// adoptable between its plan and its write, because no repair can take a
	// created version back. A row that already names a version of BookID
	// returns that version as before; the caller compares its ID.
	AdoptVersionID string
}

// ErrTorrentLinkedElsewhere is returned by CreateIngestVersion when
// TorrentHash is held by another live version (see LiveTorrentHolder).
// Linking it would move the one-to-one torrent index off that version, and
// writing it onto the row would make the row claim a torrent another version
// owns. Nothing is created or written.
var ErrTorrentLinkedElsewhere = errors.New("torrent already linked to another live version")

// ErrVersionNotAdoptable is returned by an adopt-only CreateIngestVersion
// (IngestVersionParams.AdoptVersionID) when the version it names is not the
// book's one stranded version for the row. Nothing is created or written.
var ErrVersionNotAdoptable = errors.New("version is not the row's one stranded version")

// torrentLocks serializes torrent links per hash; see LockTorrent.
var torrentLocks keyedmutex.Mutex

// LockTorrent blocks until no other goroutine in this process holds the
// per-torrent link lock for hash, takes it, and returns the matching unlock.
// An empty hash takes no lock (the returned unlock is a no-op).
//
// A caller that may link a torrent holds it from its own already-linked check
// through CreateIngestVersion's return. That check, the version create, and
// the index write are otherwise a check-then-act sequence: two concurrent
// imports of one torrent (a double click, a proxy retry) both pass the check,
// both create a version carrying the hash, and the index ends up on whichever
// wrote last while the other book's row still claims the torrent.
// Process-local is enough: the store is single-process and
// CreateIngestVersion is the only creator of book versions. It is not the only
// writer of the torrent index, though: UpdateBookVersion also rewrites it
// (idx:bv:torrent:) without this lock whenever it changes a version's
// TorrentHash. A holder can therefore appear between a
// caller's guard and its create, which is why CreateIngestVersion re-checks
// the holder itself and why the importer keeps a fallback for
// ErrTorrentLinkedElsewhere. CreateIngestVersion does not take the lock
// itself, because the caller's guard has to sit inside the same critical
// section and the lock is not reentrant.
func LockTorrent(hash string) (unlock func()) {
	if hash == "" {
		return func() {}
	}
	return torrentLocks.Lock(hash)
}

// CreateIngestVersion creates a BookVersion for a newly-ingested file and
// links it to the file's existing book_file row (see IngestVersionParams).
// If the book already has an active version, the new one gets status=alt.
// If no active version exists, the new one becomes active.
//
// It returns the version and whether the link landed (linked). linked=false
// with a nil error means the version exists but no row points at it: the row
// was not found, could not be read, or its update failed. Each is logged.
//
// It is idempotent per row: when the target row already carries a VersionID
// naming a version of this book, that version is returned (linked=true) and
// nothing is created, so a repair that re-runs ingest over a row never mints a
// second version. A row left without a VersionID is linked on the next run:
// after a failed version create a version is created then; after a failed
// link write (or an import from before 2026-10-10, which never linked) the
// stranded version is adopted rather than duplicated (see strandedVersion).
// Nothing is deleted on any path.
//
// It refuses (ErrTorrentLinkedElsewhere) to link a TorrentHash that a live
// version other than the one it would link already holds, whether it is
// creating a version or adopting a stranded one. A row whose VersionID names
// another book's version (merged rows keep the absorbed book's VersionID) is
// relinked for this book, but never by taking that version's torrent. Callers
// linking a torrent hold LockTorrent(params.TorrentHash) across the call.
//
// It also stores the file's identity hash on the linked row when the row does
// not carry one yet. A caller that has already hashed the file (the importer
// writes FileHash at CreateBookFile) is trusted, so the file is read once per
// ingest, not twice. The hash MUST come from filehash.BookFileHash:
// book_files.file_hash is the column dedup's exact-file collector treats as
// certainty, and a whole-file SHA-256 written here — which is what this
// function used to do — disagrees with every scanner-written row above 100 MB,
// so the duplicate is silently never found.
func CreateIngestVersion(store IngestStore, params IngestVersionParams) (*database.BookVersion, bool, error) {
	if params.BookID == "" || params.FilePath == "" {
		return nil, false, fmt.Errorf("book_id and file_path required")
	}

	// Validate the path is inside an allowed directory before hashing the file
	// (go/path-injection); use the cleaned absolute path downstream.
	validPath, err := fileops.ValidateUserPath(store, params.FilePath)
	if err != nil {
		return nil, false, err
	}
	params.FilePath = validPath

	// Find the row to link BEFORE creating anything, so an already-linked row
	// short-circuits (idempotency) instead of gaining a second version.
	//
	// A store failure here is not fatal: the version is still recorded (it
	// holds the torrent index, which is what stops a retried torrent import
	// from creating a second linked book), but it is reported unlinked.
	// Previously discarded, which made a store failure look identical to "this
	// book has no files" — with no log line either way.
	files, filesErr := store.GetBookFiles(params.BookID)
	if filesErr != nil {
		slog.Warn("versions.CreateIngestVersion: cannot load book files; version will NOT be linked to any file row",
			"book_id", logger.SanitizeLogValue(params.BookID), "err", filesErr)
	}
	target := -1
	for i := range files {
		if params.BookFileID != "" {
			if files[i].ID == params.BookFileID {
				target = i
				break
			}
		} else if files[i].FilePath == params.FilePath {
			target = i
			break
		}
	}

	if target >= 0 && files[target].VersionID != "" {
		existing, getErr := store.GetBookVersion(files[target].VersionID)
		if getErr != nil {
			// Cannot tell whether the row is already linked. Creating anyway
			// could mint a duplicate version, so refuse; the row is untouched.
			return nil, false, fmt.Errorf("read linked version %s: %w", files[target].VersionID, getErr)
		}
		if existing != nil && existing.BookID == params.BookID {
			return existing, true, nil
		}
		// A VersionID naming a missing version, or another book's (the row was
		// repointed), is stale: fall through and relink the row to a new one.
	}

	// Who holds the torrent now. Checked here and not only by the importer,
	// so no caller -- a relink pass over merged rows included -- can move the
	// one-to-one index off a version that is still live.
	var holder *database.BookVersion
	if params.TorrentHash != "" {
		h, holdErr := LiveTorrentHolder(store, params.TorrentHash)
		if holdErr != nil {
			return nil, false, fmt.Errorf("read torrent index for %s: %w", params.TorrentHash, holdErr)
		}
		holder = h
	}

	// Adopt a version stranded by an earlier run, before creating one. A run
	// whose link write failed (or an import from before 2026-10-10, which
	// never linked) leaves a version no row points at and a row with no
	// VersionID. Creating again would leave the first orphaned and ACTIVE and
	// make the new one an alt that takes over the torrent index; adopting it
	// makes a re-run the repair. Only when there is a row to link and the
	// rows were read, so "unreferenced" is known, and never for a book the
	// caller just created (NewBook), which has nothing to strand.
	var ver *database.BookVersion
	if target >= 0 && filesErr == nil && !params.NewBook {
		stranded, strandErr := strandedVersion(store, params, files)
		if strandErr != nil {
			// Cannot tell whether a stranded version exists; creating could
			// duplicate it. The row is untouched and a later run can link it.
			return nil, false, fmt.Errorf("list versions of book %s: %w", params.BookID, strandErr)
		}
		ver = stranded
	}
	if params.AdoptVersionID != "" && (ver == nil || ver.ID != params.AdoptVersionID) {
		found := "none"
		if ver != nil {
			found = ver.ID
		}
		return nil, false, fmt.Errorf("%w: book %s, wanted %s, found %s",
			ErrVersionNotAdoptable, params.BookID, params.AdoptVersionID, found)
	}

	if holder != nil && (ver == nil || holder.ID != ver.ID) {
		return nil, false, fmt.Errorf("%w: torrent %s is held by version %s of book %s",
			ErrTorrentLinkedElsewhere, params.TorrentHash, holder.ID, holder.BookID)
	}

	if ver == nil {
		// Check fingerprint first — refuse if this file was previously purged.
		if params.TorrentHash != "" {
			match := CheckFingerprint(store, params.TorrentHash, nil)
			if match != nil && match.Matched {
				return nil, false, fmt.Errorf("fingerprint match: this content was previously %s (book %s, version %s)",
					match.Status, match.BookID, match.VersionID)
			}
		}

		// Determine status: active if no existing active, alt otherwise.
		status := database.BookVersionStatusActive
		existing, err := store.GetActiveVersionForBook(params.BookID)
		if err == nil && existing != nil {
			status = database.BookVersionStatusAlt
		}

		created, err := store.CreateBookVersion(&database.BookVersion{
			BookID:      params.BookID,
			Status:      status,
			Format:      params.Format,
			Source:      params.Source,
			TorrentHash: params.TorrentHash,
		})
		if err != nil {
			return nil, false, fmt.Errorf("create version: %w", err)
		}
		ver = created
	}

	if target < 0 {
		if filesErr == nil {
			// A real condition, not a nit: CreateIngestVersion did half its job.
			// Paths drift (organize renames under RootDir), and a caller that
			// creates its row after calling here lands here every time.
			slog.Warn("versions.CreateIngestVersion: no book_file row matched the ingest path; version is orphaned",
				"book_id", logger.SanitizeLogValue(params.BookID),
				"book_file_id", logger.SanitizeLogValue(params.BookFileID),
				"file_path", logger.SanitizeLogValue(params.FilePath),
				"version_id", ver.ID, "candidate_files", len(files))
		}
		return ver, false, nil
	}

	// Link the new version to its book_file row, and stamp the identity hash
	// onto the same row while we are there.
	//
	// These are two separate units of work and must not share one error gate.
	// They used to: the row update lived in the `else` of the hash check, so a
	// hash failure — a concurrent organize renaming the file out from under us,
	// EACCES, EIO on the NAS — skipped `f.VersionID = ver.ID` as well. The
	// version row was created, nothing pointed at it, and CreateIngestVersion
	// returned (ver, nil). An orphaned version is a data-integrity bug; a
	// missing hash is a backfill job's problem. Only the second is acceptable.
	f := files[target]
	if f.FileHash == "" {
		hash, hashErr := filehash.BookFileHash(params.FilePath)
		if hashErr != nil {
			slog.Warn("versions.CreateIngestVersion: identity hash failed; version linkage still written, file_hash left empty for backfill",
				"book_id", logger.SanitizeLogValue(params.BookID),
				"file_path", logger.SanitizeLogValue(params.FilePath),
				"version_id", ver.ID, "err", hashErr)
		} else {
			f.FileHash = hash
		}
	}
	f.VersionID = ver.ID
	if params.TorrentHash != "" {
		f.DelugeHash = params.TorrentHash
	}
	if updateErr := store.UpdateBookFile(f.ID, &f); updateErr != nil {
		if errors.Is(updateErr, database.ErrBookFileDurabilityUnknown) {
			// The write was applied; only its fsync is in doubt. The row IS
			// linked as far as every reader can see.
			logger.New("versions").Warn("CreateIngestVersion: book file %s link to version %s (book %s) applied but not confirmed durable: %s",
				logger.SanitizeLogValue(f.ID), logger.SanitizeLogValue(ver.ID), logger.SanitizeLogValue(params.BookID),
				logger.SanitizeLogValue(updateErr.Error()))
			return ver, true, nil
		}
		slog.Warn("versions.CreateIngestVersion: book file update failed; version created but NOT linked",
			"book_id", logger.SanitizeLogValue(params.BookID), "book_file_id", f.ID, "version_id", ver.ID, "err", updateErr)
		return ver, false, nil
	}
	return ver, true, nil
}

// strandedVersion returns the one version of params.BookID that an earlier
// ingest created but never linked: live (active or alt), carrying the same
// TorrentHash (empty matches empty), and named by no row in files. It returns
// nil when there is none, or more than one -- several candidates means the
// history is not the simple failed-link case, and picking one would be a
// guess, so a new version is created as before.
func strandedVersion(store IngestStore, params IngestVersionParams, files []database.BookFile) (*database.BookVersion, error) {
	vers, err := store.GetBookVersionsByBookID(params.BookID)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]bool, len(files))
	for _, f := range files {
		if f.VersionID != "" {
			referenced[f.VersionID] = true
		}
	}
	var found *database.BookVersion
	for i := range vers {
		v := &vers[i]
		if v.Status != database.BookVersionStatusActive && v.Status != database.BookVersionStatusAlt {
			continue
		}
		if v.TorrentHash != params.TorrentHash || referenced[v.ID] {
			continue
		}
		if found != nil {
			return nil, nil
		}
		found = v
	}
	return found, nil
}

// HashFile is deliberately gone. It computed a whole-file SHA-256 and its only
// caller stored the result in book_files.file_hash, which expects
// filehash.BookFileHash. Leaving an exported, plausibly-named whole-file hasher
// in this package is how the next writer picks the wrong algorithm; callers
// that genuinely want a whole-file digest should use
// fileops.ComputeFileHashAndSize.
