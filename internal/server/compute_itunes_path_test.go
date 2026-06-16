// file: internal/server/compute_itunes_path_test.go
// version: 1.1.0
// guid: d3e4f5a6-b7c8-d9e0-f1a2-itunes-path01

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// computeITunesPath: converts Linux paths → iTunes file:// URLs
// ---------------------------------------------------------------------------

func TestComputeITunesPath_AudiobookOrganizerPath(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
	}

	result := metafetch.ComputeITunesPath("/mnt/bigdata/books/audiobook-organizer/Author/Title/file.m4b")
	assert.Equal(t,
		"file://localhost/W:/audiobook-organizer/Author/Title/file.m4b",
		result)
}

func TestComputeITunesPath_ITunesMediaPath(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/itunes/iTunes Media", To: "/mnt/bigdata/books/itunes/iTunes Media"},
	}

	result := metafetch.ComputeITunesPath("/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Author/book.m4b")
	assert.Equal(t,
		"file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Author/book.m4b",
		result)
}

func TestComputeITunesPath_NoMatchingMapping(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
	}

	result := metafetch.ComputeITunesPath("/some/random/path/file.m4b")
	assert.Equal(t, "", result, "unmatched path should return empty string")
}

func TestComputeITunesPath_EmptyPath(t *testing.T) {
	result := metafetch.ComputeITunesPath("")
	assert.Equal(t, "", result)
}

func TestComputeITunesPath_NoMappingsConfigured(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = nil

	result := metafetch.ComputeITunesPath("/mnt/bigdata/books/audiobook-organizer/Author/file.m4b")
	assert.Equal(t, "", result, "no mappings should return empty string")
}

// ---------------------------------------------------------------------------
// Path encoding: spaces, special chars in audiobook titles
// ---------------------------------------------------------------------------

func TestComputeITunesPath_SpacesEncoded(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
	}

	result := metafetch.ComputeITunesPath("/mnt/bigdata/books/audiobook-organizer/Brandon Sanderson/The Way of Kings/file.m4b")
	assert.Contains(t, result, "Brandon%20Sanderson")
	assert.Contains(t, result, "The%20Way%20of%20Kings")
	// Slashes and colons should NOT be encoded
	assert.Contains(t, result, "W:/audiobook-organizer/")
	assert.NotContains(t, result, "%2F")
	assert.NotContains(t, result, "%3A")
}

func TestComputeITunesPath_SpecialCharsInTitle(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
	}

	tests := []struct {
		name     string
		path     string
		contains string
	}{
		{
			"apostrophe",
			"/mnt/bigdata/books/audiobook-organizer/O'Brien/Harry's Game/file.m4b",
			"O%27Brien",
		},
		{
			"parentheses",
			"/mnt/bigdata/books/audiobook-organizer/Author/Book (Unabridged)/file.m4b",
			"Book%20%28Unabridged%29",
		},
		{
			"underscore preserved",
			"/mnt/bigdata/books/audiobook-organizer/Author/Book_Title/file.m4b",
			"Book_Title", // underscores should NOT be encoded
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := metafetch.ComputeITunesPath(tt.path)
			assert.NotEmpty(t, result)
			assert.Contains(t, result, tt.contains)
		})
	}
}

// ---------------------------------------------------------------------------
// Critical: organized path must NOT produce iTunes Media URL
// (Bug: organized copies inherited stale itunes path from source)
// ---------------------------------------------------------------------------

func TestComputeITunesPath_OrganizedVsITunes_Distinction(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
		{From: "W:/itunes/iTunes Media", To: "/mnt/bigdata/books/itunes/iTunes Media"},
	}

	organizedPath := "/mnt/bigdata/books/audiobook-organizer/Author/Title/file.m4b"
	itunesPath := "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Author/file.m4b"

	organizedURL := metafetch.ComputeITunesPath(organizedPath)
	itunesURL := metafetch.ComputeITunesPath(itunesPath)

	assert.Contains(t, organizedURL, "W:/audiobook-organizer",
		"organized path should map to audiobook-organizer Windows path")
	assert.Contains(t, itunesURL, "W:/itunes",
		"iTunes path should map to iTunes Windows path")
	assert.NotEqual(t, organizedURL, itunesURL,
		"organized and iTunes paths must produce different URLs")
}

// ---------------------------------------------------------------------------
// Edge case: mapping order matters — first match wins
// ---------------------------------------------------------------------------

func TestComputeITunesPath_FirstMappingWins(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	// Both mappings could match a path starting with /mnt/bigdata/books/
	// but the more specific one should be listed first
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
		{From: "W:/books", To: "/mnt/bigdata/books"},
	}

	result := metafetch.ComputeITunesPath("/mnt/bigdata/books/audiobook-organizer/Author/file.m4b")
	assert.Contains(t, result, "W:/audiobook-organizer",
		"more specific mapping should win when listed first")
}

// ---------------------------------------------------------------------------
// Edge case: path mapping with trailing slash handling
// ---------------------------------------------------------------------------

func TestComputeITunesPath_TrailingSlashConsistency(t *testing.T) {
	old := config.AppConfig.ITunes.PathMappings
	defer func() { config.AppConfig.ITunes.PathMappings = old }()

	// No trailing slash in mapping
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: "/mnt/bigdata/books/audiobook-organizer"},
	}

	result := metafetch.ComputeITunesPath("/mnt/bigdata/books/audiobook-organizer/Author/file.m4b")
	assert.NotEmpty(t, result)
	// Should NOT produce double slashes
	assert.NotContains(t, result, "audiobook-organizer//Author")
}
