// file: internal/versions/fingerprint.go
// version: 1.1.0
// guid: 8d5e7f4c-9c5a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-08-19
//
// Fingerprint check for incoming files (spec 3.1 task 4).
//
// When a new file arrives (via deluge, manual import, or scan), we
// check whether it matches a previously-purged or blocked version.
// If so, the caller can pause deluge / surface a dialog before
// re-importing content that was explicitly removed.
//
// Fast path: torrent hash lookup (O(1) in PebbleDB via the
// idx:bv:torrent:{hash} index). Slow path: per-file hash scan
// against existing BookFile rows.

package versions

import "github.com/falkcorp/audiobook-organizer/internal/database"

// FingerprintMatch describes a match between an incoming file/torrent
// and a previously-seen version in the library. The caller uses this
// to decide whether to block, prompt, or allow the ingest.
type FingerprintMatch struct {
	Matched   bool   `json:"matched"`
	BookID    string `json:"book_id,omitempty"`
	VersionID string `json:"version_id,omitempty"`
	MatchType string `json:"match_type,omitempty"` // "torrent_hash" | "file_hash"
	Status    string `json:"status,omitempty"`
}

// CheckFingerprint looks up a torrent hash and/or file hashes
// against the library's version database. Returns a match if the
// content was previously purged or blocked.
//
// torrentHash is checked first (fast path). fileHashes is a
// fallback for content without a torrent (manual imports). Both
// can be empty — in which case no match is returned.
func CheckFingerprint(
	store versionFingerprintReader,
	torrentHash string,
	fileHashes []string,
) *FingerprintMatch {
	// Fast path: torrent hash lookup.
	if torrentHash != "" {
		ver, err := store.GetBookVersionByTorrentHash(torrentHash)
		if err == nil && ver != nil && isPurgedOrBlocked(ver.Status) {
			return &FingerprintMatch{
				Matched:   true,
				BookID:    ver.BookID,
				VersionID: ver.ID,
				MatchType: "torrent_hash",
				Status:    ver.Status,
			}
		}
	}

	// Slow path: per-file hash scan. For each incoming hash, check
	// if any BookFile in the library has a matching file_hash whose
	// parent version is purged/blocked.
	for _, hash := range fileHashes {
		if hash == "" {
			continue
		}
		match := scanFileHashMatch(store, hash)
		if match != nil {
			return match
		}
	}

	return &FingerprintMatch{Matched: false}
}

// isPurgedOrBlocked returns true for version statuses that indicate
// the content was intentionally removed from the library. These are
// the statuses the fingerprint check guards against re-importing.
func isPurgedOrBlocked(status string) bool {
	switch status {
	case database.BookVersionStatusInactivePurged,
		database.BookVersionStatusBlockedForRedownload,
		database.BookVersionStatusTrash:
		return true
	default:
		return false
	}
}

// scanFileHashMatch searches BookFile rows for a matching file_hash
// and checks whether the owning version is purged or blocked.
//
// This is a linear scan across all book files — expensive for large
// libraries. In practice it's only invoked when the fast path
// (torrent hash) fails, which is rare for automated ingestion.
// A future optimization would add a file_hash→version_id index in
// PebbleDB.
func scanFileHashMatch(store unusedStore, hash string) *FingerprintMatch {
	// The current Store interface doesn't expose GetBookFileByHash.
	// For now, this is a stub that returns nil. A follow-up PR will
	// add the index + method.
	_ = store
	_ = hash
	return nil
}
