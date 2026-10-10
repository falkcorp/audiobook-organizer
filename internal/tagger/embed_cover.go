// file: internal/tagger/embed_cover.go
// version: 2.4.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-10

package tagger

import (
	"context"
	"fmt"
	"os"
)

// EmbedCoverArtSafe embeds a cover image into an audio file, importing
// the file from a protected (Deluge) path into the library first if needed.
// See WriteImageSafe for the protection semantics.
func EmbedCoverArtSafe(ctx context.Context, audioPath string, coverPath string, deps SafeWriteDeps) error {
	if audioPath == "" {
		return fmt.Errorf("empty audio path")
	}
	if coverPath == "" {
		return fmt.Errorf("empty cover path")
	}
	if _, err := os.Stat(audioPath); err != nil {
		return fmt.Errorf("audio file not found: %w", err)
	}
	data, err := os.ReadFile(coverPath)
	if err != nil {
		return fmt.Errorf("cover file not found: %w", err)
	}
	return WriteImageSafe(ctx, audioPath, data, deps)
}
