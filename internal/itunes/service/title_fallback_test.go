// file: internal/itunes/service/title_fallback_test.go
// version: 1.0.0
// guid: 5a3c1f8b-2e74-4d09-b6a1-7c8e0f2d4b69
// last-edited: 2026-06-19

package itunesservice

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// TestBuildBookFromAlbumGroup_EmptyAlbumUsesFolder is the CONS-17 (Path A)
// regression guard: when a multi-file iTunes group has no Album tag, the book
// title must come from the common parent FOLDER (the book/album directory), not
// from the first chapter's track Name. Tracks like "Opening Credits" or
// "Big Finish Ident" have no chapter marker to strip, so they used to leak into
// Book.Title and collide across unrelated books.
func TestBuildBookFromAlbumGroup_EmptyAlbumUsesFolder(t *testing.T) {
	tmpDir := t.TempDir()
	bookDir := filepath.Join(tmpDir, "The Hitchhikers Guide")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var tracks []*itunes.Track
	names := []string{"Opening Credits", "Chapter 1", "Closing Credits"}
	for i, name := range names {
		fp := filepath.Join(bookDir, "track"+string(rune('1'+i))+".m4b")
		if err := os.WriteFile(fp, bytes.Repeat([]byte("x"), 64), 0o644); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, &itunes.Track{
			Name:        name,
			Album:       "", // <-- empty album tag is the trigger
			TrackNumber: i + 1,
			TotalTime:   int64(60000 * (i + 1)),
			Size:        int64(1000 * (i + 1)),
			Location:    itunes.EncodeLocation(fp),
		})
	}

	imp := newTestImporter()
	group := albumGroup{key: "|", tracks: tracks}
	book, err := imp.buildBookFromAlbumGroup(group, "/library.xml", itunes.ImportOptions{})
	if err != nil {
		t.Fatalf("buildBookFromAlbumGroup error: %v", err)
	}

	if book.Title != "The Hitchhikers Guide" {
		t.Errorf("title = %q, want %q (folder name, not first track name)",
			book.Title, "The Hitchhikers Guide")
	}
	if book.FilePath != bookDir {
		t.Errorf("filePath = %q, want book directory %q", book.FilePath, bookDir)
	}
}

// TestBuildBookFromAlbumGroup_SingleFileEmptyAlbumKeepsTrackName verifies the
// fallback is scoped to multi-file groups: a single-file book with no album tag
// still derives its title from the (stripped) track name, since there is no
// meaningful shared folder to prefer.
func TestBuildBookFromAlbumGroup_SingleFileEmptyAlbumKeepsTrackName(t *testing.T) {
	tmpDir := t.TempDir()
	fp := filepath.Join(tmpDir, "solo.m4b")
	if err := os.WriteFile(fp, bytes.Repeat([]byte("x"), 64), 0o644); err != nil {
		t.Fatal(err)
	}
	track := &itunes.Track{
		Name:      "A Standalone Audiobook",
		Album:     "",
		TotalTime: 120000,
		Size:      2048,
		Location:  itunes.EncodeLocation(fp),
	}

	imp := newTestImporter()
	group := albumGroup{key: "|", tracks: []*itunes.Track{track}}
	book, err := imp.buildBookFromAlbumGroup(group, "/library.xml", itunes.ImportOptions{})
	if err != nil {
		t.Fatalf("buildBookFromAlbumGroup error: %v", err)
	}
	if book.Title != "A Standalone Audiobook" {
		t.Errorf("title = %q, want track name for single-file book", book.Title)
	}
}
