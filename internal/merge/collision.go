// file: internal/merge/collision.go
// version: 1.1.0

package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// CollisionCandidate describes one existing book that may collide
// with a file about to be imported.
type CollisionCandidate struct {
	BookID    string `json:"book_id"`
	Title     string `json:"title"`
	MatchType string `json:"match_type"` // "title", "file_hash", "fingerprint"
	FilePath  string `json:"file_path,omitempty"`
}

// BookTitle returns the title for a book ID, or empty string if not found.
func BookTitle(store mergeBookReader, id string) string {
	b, _ := store.GetBookByID(id)
	if b != nil {
		return b.Title
	}
	return ""
}

// QuickHash computes a SHA-256 hash of the first 1 MiB of a file.
func QuickHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 1<<20)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
