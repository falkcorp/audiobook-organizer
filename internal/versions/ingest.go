// file: internal/versions/ingest.go
// version: 1.4.0
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

	// Compute file hash and update the BookFile row.
	hash, hashErr := filehash.BookFileHash(params.FilePath)
	if hashErr != nil {
		slog.Warn("hash", "params", params.FilePath, "hashErr", hashErr)
	} else {
		files, _ := store.GetBookFiles(params.BookID)
		for _, f := range files {
			if f.FilePath == params.FilePath {
				f.FileHash = hash
				f.VersionID = ver.ID
				if updateErr := store.UpdateBookFile(f.ID, &f); updateErr != nil {
					slog.Warn("update file hash", "f", f.ID, "updateErr", updateErr)
				}
				break
			}
		}
	}

	return ver, nil
}

// HashFile is deliberately gone. It computed a whole-file SHA-256 and its only
// caller stored the result in book_files.file_hash, which expects
// filehash.BookFileHash. Leaving an exported, plausibly-named whole-file hasher
// in this package is how the next writer picks the wrong algorithm; callers
// that genuinely want a whole-file digest should use
// fileops.ComputeFileHashAndSize.
