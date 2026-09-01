// file: internal/versions/ingest.go
// version: 1.5.0
// guid: 3e1f2a9b-4c5d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-01
//
// Version creation on ingest (spec 3.1 task 5).
//
// Every time a new book enters the library (import, scan, organize)
// or a new file is added to an existing book, a BookVersion row is
// created. The version tracks the file's provenance (source, hash,
// torrent hash) and its lifecycle status.
//
// New books get an `active` version. Known books adding a second
// copy get an `alt` version — the user must explicitly promote it
// via the swap operation.

package versions

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
)

// IngestVersionParams describes the provenance of a newly-ingested file.
type IngestVersionParams struct {
	BookID      string
	FilePath    string
	Format      string
	Source      string // "imported", "scanned", "organized", "deluge"
	TorrentHash string // empty for non-torrent sources
}

// CreateIngestVersion creates a BookVersion for a newly-ingested file.
// If the book already has an active version, the new one gets status=alt.
// If no active version exists, the new one becomes active.
//
// Also computes and stores the file's identity hash on the BookFile row (if
// one exists for the book + file path). The hash MUST come from
// filehash.BookFileHash: book_files.file_hash is the column dedup's exact-file
// collector treats as certainty, and a whole-file SHA-256 written here — which
// is what this function used to do — disagrees with every scanner-written row
// above 100 MB, so the duplicate is silently never found.
func CreateIngestVersion(store IngestStore, params IngestVersionParams) (*database.BookVersion, error) {
	if params.BookID == "" || params.FilePath == "" {
		return nil, fmt.Errorf("book_id and file_path required")
	}

	// Validate the path is inside an allowed directory before hashing the file
	// (go/path-injection); use the cleaned absolute path downstream.
	validPath, err := fileops.ValidateUserPath(store, params.FilePath)
	if err != nil {
		return nil, err
	}
	params.FilePath = validPath

	// Check fingerprint first — refuse if this file was previously purged.
	if params.TorrentHash != "" {
		match := CheckFingerprint(store, params.TorrentHash, nil)
		if match != nil && match.Matched {
			return nil, fmt.Errorf("fingerprint match: this content was previously %s (book %s, version %s)",
				match.Status, match.BookID, match.VersionID)
		}
	}

	// Determine status: active if no existing active, alt otherwise.
	status := database.BookVersionStatusActive
	existing, err := store.GetActiveVersionForBook(params.BookID)
	if err == nil && existing != nil {
		status = database.BookVersionStatusAlt
	}

	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID:      params.BookID,
		Status:      status,
		Format:      params.Format,
		Source:      params.Source,
		TorrentHash: params.TorrentHash,
	})
	if err != nil {
		return nil, fmt.Errorf("create version: %w", err)
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
	hash, hashErr := filehash.BookFileHash(params.FilePath)
	if hashErr != nil {
		slog.Warn("versions.CreateIngestVersion: identity hash failed; version linkage still written, file_hash left empty for backfill",
			"book_id", params.BookID, "file_path", params.FilePath, "version_id", ver.ID, "err", hashErr)
	}

	files, filesErr := store.GetBookFiles(params.BookID)
	if filesErr != nil {
		// Previously discarded, which made a store failure look identical to
		// "this book has no files" — with no log line either way.
		slog.Warn("versions.CreateIngestVersion: cannot load book files; version created but NOT linked to any file row",
			"book_id", params.BookID, "version_id", ver.ID, "err", filesErr)
		return ver, nil
	}

	linked := false
	for _, f := range files {
		if f.FilePath == params.FilePath {
			if hashErr == nil {
				f.FileHash = hash
			}
			f.VersionID = ver.ID
			if updateErr := store.UpdateBookFile(f.ID, &f); updateErr != nil {
				slog.Warn("versions.CreateIngestVersion: book file update failed; version created but NOT linked",
					"book_id", params.BookID, "book_file_id", f.ID, "version_id", ver.ID, "err", updateErr)
			} else {
				linked = true
			}
			break
		}
	}
	if !linked {
		// A real condition, not a nit: CreateIngestVersion did half its job and
		// its caller cannot tell. Paths drift (organize renames under RootDir),
		// so the ingest path may no longer match any row by the time we look.
		slog.Warn("versions.CreateIngestVersion: no book_file row matched the ingest path; version is orphaned",
			"book_id", params.BookID, "file_path", params.FilePath, "version_id", ver.ID,
			"candidate_files", len(files))
	}

	return ver, nil
}

// HashFile is deliberately gone. It computed a whole-file SHA-256 and its only
// caller stored the result in book_files.file_hash, which expects
// filehash.BookFileHash. Leaving an exported, plausibly-named whole-file hasher
// in this package is how the next writer picks the wrong algorithm; callers
// that genuinely want a whole-file digest should use
// fileops.ComputeFileHashAndSize.
