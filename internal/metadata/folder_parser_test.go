// file: internal/metadata/folder_parser_test.go
// version: 1.0.1
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210

package metadata_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func TestExtractMetadataFromFolder_LongEarthExample(t *testing.T) {
	// Real-world example from the user's library
	path := "/mnt/bigdata/books/audiobook-organizer/Terry Pratchett & Stephen Baxter/(Long Earth 05) The Long Cosmos/(Long Earth 05) The Long Cosmos - Terry Pratchett & Stephen Baxter - read by Michael Fenton Stevens"

	fm, err := metadata.ExtractMetadataFromFolderWith(path, metadata.NameEvidence{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Series
	if fm.SeriesName != "Long Earth" {
		t.Errorf("SeriesName: got %q, want %q", fm.SeriesName, "Long Earth")
	}
	if fm.SeriesPosition != 5 {
		t.Errorf("SeriesPosition: got %d, want 5", fm.SeriesPosition)
	}

	// Title
	if fm.Title != "The Long Cosmos" {
		t.Errorf("Title: got %q, want %q", fm.Title, "The Long Cosmos")
	}

	// Authors
	if len(fm.Authors) != 2 {
		t.Errorf("Authors: got %v (len %d), want 2 authors", fm.Authors, len(fm.Authors))
	} else {
		if fm.Authors[0] != "Terry Pratchett" {
			t.Errorf("Authors[0]: got %q, want %q", fm.Authors[0], "Terry Pratchett")
		}
		if fm.Authors[1] != "Stephen Baxter" {
			t.Errorf("Authors[1]: got %q, want %q", fm.Authors[1], "Stephen Baxter")
		}
	}

	// Narrator
	if fm.Narrator != "Michael Fenton Stevens" {
		t.Errorf("Narrator: got %q, want %q", fm.Narrator, "Michael Fenton Stevens")
	}
}

func TestExtractMetadataFromFolder_SingleAuthorNoSeries(t *testing.T) {
	path := "/media/audiobooks/Stephen King/The Shining - Stephen King - read by Campbell Scott"

	fm, err := metadata.ExtractMetadataFromFolderWith(path, metadata.NameEvidence{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fm.Title != "The Shining" {
		t.Errorf("Title: got %q, want %q", fm.Title, "The Shining")
	}
	if len(fm.Authors) == 0 || fm.Authors[0] != "Stephen King" {
		t.Errorf("Authors: got %v, want [Stephen King]", fm.Authors)
	}
	if fm.Narrator != "Campbell Scott" {
		t.Errorf("Narrator: got %q, want %q", fm.Narrator, "Campbell Scott")
	}
	if fm.SeriesName != "" {
		t.Errorf("SeriesName should be empty, got %q", fm.SeriesName)
	}
}

func TestExtractMetadataFromFolder_SeriesNoNarrator(t *testing.T) {
	path := "/audiobooks/Brandon Sanderson/(Stormlight 01) The Way of Kings"

	fm, err := metadata.ExtractMetadataFromFolderWith(path, metadata.NameEvidence{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fm.SeriesName != "Stormlight" {
		t.Errorf("SeriesName: got %q, want %q", fm.SeriesName, "Stormlight")
	}
	if fm.SeriesPosition != 1 {
		t.Errorf("SeriesPosition: got %d, want 1", fm.SeriesPosition)
	}
	if fm.Title != "The Way of Kings" {
		t.Errorf("Title: got %q, want %q", fm.Title, "The Way of Kings")
	}
	if fm.Narrator != "" {
		t.Errorf("Narrator should be empty, got %q", fm.Narrator)
	}
}

func TestExtractMetadataFromFolder_NarratedByVariant(t *testing.T) {
	path := "/books/Patrick Rothfuss/(Kingkiller 01) The Name of the Wind - narrated by Nick Podehl"

	fm, err := metadata.ExtractMetadataFromFolderWith(path, metadata.NameEvidence{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fm.Narrator != "Nick Podehl" {
		t.Errorf("Narrator: got %q, want %q", fm.Narrator, "Nick Podehl")
	}
}

func TestExtractMetadataFromFolder_EmptyPath(t *testing.T) {
	fm, err := metadata.ExtractMetadataFromFolderWith("", metadata.NameEvidence{})
	if err != nil {
		t.Fatalf("unexpected error on empty path: %v", err)
	}
	if fm == nil {
		t.Fatal("expected non-nil FolderMetadata on empty path")
	}
}

func TestIsGenericPartFilename(t *testing.T) {
	cases := []struct {
		filename string
		want     bool
	}{
		{"01 Part 1 of 67.mp3", true},
		{"67 Part 67 of 67.mp3", true},
		{"001.mp3", true},
		{"01.mp3", true},
		{"The Long Cosmos Chapter 1.mp3", false},
		{"Long_Cosmos_01.mp3", false},
		{"01 - Introduction.mp3", false},
	}
	for _, tc := range cases {
		got := metadata.IsGenericPartFilename(tc.filename)
		if got != tc.want {
			t.Errorf("IsGenericPartFilename(%q): got %v, want %v", tc.filename, got, tc.want)
		}
	}
}

func TestSplitMultipleAuthors(t *testing.T) {
	// Indirect test via ExtractMetadataFromFolderWith
	path := "/books/Arthur C. Clarke & Gregory Benford/Shipstar/(Bowl of Heaven 02) Shipstar"

	fm, err := metadata.ExtractMetadataFromFolderWith(path, metadata.NameEvidence{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fm.Authors) < 2 {
		t.Errorf("expected 2 authors, got %v", fm.Authors)
	}
}
