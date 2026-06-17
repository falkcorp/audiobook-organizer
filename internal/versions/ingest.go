// file: internal/versions/ingest.go
// version: 1.1.0
// guid: 3e1f2a9b-4c5d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-06-17
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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
// Also computes and stores the file's SHA-256 hash on the BookFile row
// (if one exists for the book + file path).
func CreateIngestVersion(store database.Store, params IngestVersionParams) (*database.BookVersion, error) {
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
	hash, hashErr := HashFile(params.FilePath)
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

// HashFile computes the SHA-256 hex digest of the file at path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
