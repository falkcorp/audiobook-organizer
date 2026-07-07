// file: internal/importer/collision.go
// version: 1.2.0
// guid: 5c7d8e9f-0a1b-2c3d-4e5f-6a7b8c9d0e1f
// last-edited: 2026-07-07
//
// Import-time collision preview. Before importing a file, check whether
// it collides with an existing book (by title match, file hash, or fingerprint)
// so the user can decide whether to skip, merge, or create a new version.

package importer

import (
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// CollisionPreviewRequest describes the parameters for checking import collisions.
type CollisionPreviewRequest struct {
	FilePath    string
	TorrentHash string
}

// CollisionPreviewResult contains the collision check results.
type CollisionPreviewResult struct {
	Collisions   []merge.CollisionCandidate
	Count        int
	HasCollision bool
}

// CheckImportCollisions performs three checks to detect collisions with existing library content:
// 1. Fingerprint check (purged/blocked content via torrent hash)
// 2. File hash check against existing books
// 3. Title match via metadata extraction
//
// It returns a CollisionPreviewResult with all detected candidates, allowing the user
// to decide whether to skip, merge, or create a new version.
func CheckImportCollisions(store database.Store, req *CollisionPreviewRequest) *CollisionPreviewResult {
	var candidates []merge.CollisionCandidate

	// 1. Fingerprint check (purged/blocked content).
	if req.TorrentHash != "" {
		match := versions.CheckFingerprint(store, req.TorrentHash, nil)
		if match != nil && match.Matched {
			candidates = append(candidates, merge.CollisionCandidate{
				BookID:    match.BookID,
				Title:     merge.BookTitle(store, match.BookID),
				MatchType: "fingerprint",
			})
		}
	}

	// Steps 2 and 3 read the file directly, so validate the path against the
	// allow-list first (go/path-injection). A disallowed path simply skips the
	// file-based checks — the actual import via ImportFile will reject it.
	safePath, pathErr := fileops.ValidateUserPath(store, req.FilePath)

	// 2. File hash check against existing books.
	// safePath is validated against the allow-list by ValidateUserPath above;
	// CodeQL does not model that barrier, so suppress the false positive.
	if pathErr == nil {
		if _, err := os.Stat(safePath); err == nil { // lgtm[go/path-injection]
			hash := merge.QuickHash(safePath)
			if hash != "" {
				existing, _ := store.GetBookByFileHash(hash)
				if existing != nil {
					candidates = append(candidates, merge.CollisionCandidate{
						BookID:    existing.ID,
						Title:     existing.Title,
						MatchType: "file_hash",
						FilePath:  existing.FilePath,
					})
				}
			}
		}
	}

	// 3. Title match via metadata extraction.
	var meta metadata.Metadata
	var err error
	if pathErr == nil {
		meta, err = metadata.ExtractMetadata(safePath, nil)
	} else {
		err = pathErr
	}
	if err == nil && meta.Title != "" {
		titleLower := strings.ToLower(strings.TrimSpace(meta.Title))
		books, _ := store.GetAllBooksCore(0, 0)
		for _, b := range books {
			if strings.ToLower(strings.TrimSpace(b.Title)) == titleLower {
				alreadyListed := false
				for _, c := range candidates {
					if c.BookID == b.ID {
						alreadyListed = true
						break
					}
				}
				if !alreadyListed {
					candidates = append(candidates, merge.CollisionCandidate{
						BookID:    b.ID,
						Title:     b.Title,
						MatchType: "title",
						FilePath:  b.FilePath,
					})
				}
			}
		}
	}

	if candidates == nil {
		candidates = []merge.CollisionCandidate{}
	}

	return &CollisionPreviewResult{
		Collisions:   candidates,
		Count:        len(candidates),
		HasCollision: len(candidates) > 0,
	}
}
