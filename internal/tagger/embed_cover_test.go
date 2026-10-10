// file: internal/tagger/embed_cover_test.go
// version: 2.1.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-10

package tagger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbedCoverArtSafe_EmptyAudioPath(t *testing.T) {
	t.Parallel()
	if err := EmbedCoverArtSafe(context.Background(), "", "/some/cover.jpg", SafeWriteDeps{}); err == nil {
		t.Error("expected error for empty audio path")
	}
}

func TestEmbedCoverArtSafe_EmptyCoverPath(t *testing.T) {
	t.Parallel()
	if err := EmbedCoverArtSafe(context.Background(), "/some/audio.mp3", "", SafeWriteDeps{}); err == nil {
		t.Error("expected error for empty cover path")
	}
}

func TestEmbedCoverArtSafe_MissingAudioFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	coverPath := filepath.Join(dir, "cover.jpg")
	os.WriteFile(coverPath, []byte("fake"), 0644) //nolint:errcheck
	err := EmbedCoverArtSafe(context.Background(), "/nonexistent/audio.mp3", coverPath, SafeWriteDeps{})
	if err == nil {
		t.Error("expected error for missing audio file")
	}
}

func TestEmbedCoverArtSafe_MissingCoverFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "test.mp3")
	os.WriteFile(audioPath, []byte("fake audio"), 0644) //nolint:errcheck
	err := EmbedCoverArtSafe(context.Background(), audioPath, "/nonexistent/cover.jpg", SafeWriteDeps{})
	if err == nil {
		t.Error("expected error for missing cover file")
	}
}
