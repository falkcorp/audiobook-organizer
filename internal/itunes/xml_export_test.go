// file: internal/itunes/xml_export_test.go
// version: 1.0.1
// guid: f1e2d3c4-b5a6-7890-abcd-0123456789ab
// last-edited: 2026-09-02

package itunes

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportBooksToITunesXML_EmptyList(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "empty.xml")

	err := ExportBooksToITunesXML(nil, outPath)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	// Must be valid XML
	assert.True(t, xml.Unmarshal(data, new(any)) == nil || isValidPlist(data),
		"output should be valid XML")

	// Should contain the playlist but no track entries
	content := string(data)
	assert.Contains(t, content, "Audiobook Organizer Import")
	assert.NotContains(t, content, "<key>1</key>")
}

func TestExportBooksToITunesXML_SingleBook(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "single.xml")

	books := []ExportableBook{
		{
			Title:    "The Great Adventure",
			Author:   "Jane Author",
			FilePath: `W:\audiobook-organizer\Jane Author\The Great Adventure\book.m4b`,
			Duration: 3600000,
			Format:   "m4b",
			Genre:    "Audiobook",
			Year:     2024,
		},
	}

	err := ExportBooksToITunesXML(books, outPath)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	content := string(data)

	// Verify XML header
	assert.True(t, strings.HasPrefix(content, `<?xml version="1.0" encoding="UTF-8"?>`))
	assert.Contains(t, content, `<!DOCTYPE plist`)

	// Verify track content
	assert.Contains(t, content, "<key>Name</key><string>The Great Adventure</string>")
	assert.Contains(t, content, "<key>Artist</key><string>Jane Author</string>")
	assert.Contains(t, content, "<key>Album</key><string>The Great Adventure</string>")
	assert.Contains(t, content, "<key>Kind</key><string>AAC audio file</string>")
	assert.Contains(t, content, "<key>Total Time</key><integer>3600000</integer>")
	assert.Contains(t, content, "<key>Year</key><integer>2024</integer>")
	assert.Contains(t, content, "<key>Genre</key><string>Audiobook</string>")

	// Verify playlist references the track
	assert.Contains(t, content, `<dict><key>Track ID</key><integer>1</integer></dict>`)
}

func TestExportBooksToITunesXML_FiveBooks(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "five.xml")

	books := []ExportableBook{
		{Title: "Book One", Author: "Author A", FilePath: `W:\books\a\one.mp3`, Duration: 1000, Format: "mp3"},
		{Title: "Book Two", Author: "Author B", FilePath: `W:\books\b\two.m4b`, Duration: 2000, Format: "m4b"},
		{Title: "Book Three", Author: "Author C", FilePath: `W:\books\c\three.m4a`, Duration: 3000, Format: "m4a"},
		{Title: "Book Four", Author: "Author D", FilePath: `W:\books\d\four.mp3`, Duration: 4000, Format: "mp3", TrackNumber: 1, DiscNumber: 2},
		{Title: "Book Five", Author: "Author E", FilePath: `W:\books\e\five.m4b`, Duration: 5000, Format: "m4b", Year: 2023},
	}

	err := ExportBooksToITunesXML(books, outPath)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	content := string(data)

	// All five track IDs should be present
	for i := 1; i <= 5; i++ {
		assert.Contains(t, content, "<key>Track ID</key><integer>"+strings.Repeat("", 0)+string(rune('0'+i))+"</integer>")
	}

	// Check format mapping
	assert.Contains(t, content, "<key>Kind</key><string>MPEG audio file</string>")
	assert.Contains(t, content, "<key>Kind</key><string>AAC audio file</string>")

	// Track number and disc number only appear when set
	assert.Contains(t, content, "<key>Track Number</key><integer>1</integer>")
	assert.Contains(t, content, "<key>Disc Number</key><integer>2</integer>")

	// Default genre should be Audiobook
	// Count occurrences of Audiobook genre (all 5 books have no genre set or "Audiobook")
	assert.Equal(t, 5, strings.Count(content, "<key>Genre</key><string>Audiobook</string>"))
}

func TestExportBooksToITunesXML_SpecialCharacters(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "special.xml")

	books := []ExportableBook{
		{
			Title:    `O'Brien's "Greatest" <Hits> & More`,
			Author:   `Müller & Señor`,
			FilePath: `W:\books\Müller & Señor\O'Brien's Hits\book.m4b`,
			Duration: 1000,
			Format:   "m4b",
			Genre:    `Sci-Fi & Fantasy`,
		},
	}

	err := ExportBooksToITunesXML(books, outPath)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	content := string(data)

	// XML-escaped special characters in text content
	assert.Contains(t, content, "O&#39;Brien&#39;s &#34;Greatest&#34; &lt;Hits&gt; &amp; More")
	assert.Contains(t, content, "Müller &amp; Señor")
	assert.Contains(t, content, "Sci-Fi &amp; Fantasy")

	// The output must be parseable by the existing ParseLibrary (which uses howett.net/plist)
	library, err := ParseLibrary(outPath)
	require.NoError(t, err, "output with special characters should be parseable")
	require.Len(t, library.Tracks, 1)
	// Verify the parsed track has the correct unescaped values
	track := library.Tracks["1"]
	assert.Equal(t, `O'Brien's "Greatest" <Hits> & More`, track.Name)
	assert.Equal(t, `Müller & Señor`, track.Artist)
}

func TestExportBooksToITunesXML_LocationEncoding(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "location.xml")

	books := []ExportableBook{
		{
			Title:    "Space Book",
			Author:   "Author",
			FilePath: `W:\audiobook-organizer\Some Author\Book With Spaces\file name.m4b`,
			Duration: 1000,
			Format:   "m4b",
		},
	}

	err := ExportBooksToITunesXML(books, outPath)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	content := string(data)

	// Spaces should be %20, slashes should be preserved
	assert.Contains(t, content, "file://localhost/W:/audiobook-organizer/Some%20Author/Book%20With%20Spaces/file%20name.m4b")
	// Backslashes should have been converted to forward slashes
	assert.NotContains(t, content, `\`)
}

func TestExportBooksToITunesXML_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "roundtrip.xml")

	books := []ExportableBook{
		{
			Title:       "First Book",
			Author:      "Author One",
			FilePath:    `W:\books\Author One\First Book\part1.mp3`,
			Duration:    7200000,
			Format:      "mp3",
			Genre:       "Audiobook",
			TrackNumber: 1,
			DiscNumber:  1,
			Year:        2022,
		},
		{
			Title:    "Second Book",
			Author:   "Author Two",
			FilePath: `W:\books\Author Two\Second Book\audio.m4b`,
			Duration: 5400000,
			Format:   "m4b",
			Year:     2023,
		},
	}

	err := ExportBooksToITunesXML(books, outPath)
	require.NoError(t, err)

	// Parse it back using the existing parser
	library, err := ParseLibrary(outPath)
	require.NoError(t, err)

	// Verify tracks were parsed correctly
	require.Len(t, library.Tracks, 2)

	// Track 1
	track1, ok := library.Tracks["1"]
	require.True(t, ok, "track 1 should exist")
	assert.Equal(t, "First Book", track1.Name)
	assert.Equal(t, "Author One", track1.Artist)
	assert.Equal(t, "First Book", track1.Album)
	assert.Equal(t, "MPEG audio file", track1.Kind)
	assert.Equal(t, int64(7200000), track1.TotalTime)
	assert.Equal(t, 1, track1.TrackNumber)
	assert.Equal(t, 1, track1.DiscNumber)
	assert.Equal(t, 2022, track1.Year)
	assert.Equal(t, 1, track1.TrackID)

	// Track 2
	track2, ok := library.Tracks["2"]
	require.True(t, ok, "track 2 should exist")
	assert.Equal(t, "Second Book", track2.Name)
	assert.Equal(t, "Author Two", track2.Artist)
	assert.Equal(t, "AAC audio file", track2.Kind)
	assert.Equal(t, int64(5400000), track2.TotalTime)
	assert.Equal(t, 2023, track2.Year)

	// Verify playlist
	require.Len(t, library.Playlists, 1)
	assert.Equal(t, "Audiobook Organizer Import", library.Playlists[0].Name)
	assert.Equal(t, []int{1, 2}, library.Playlists[0].TrackIDs)
}

func TestExportBooksToITunesXML_DefaultGenre(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "genre.xml")

	books := []ExportableBook{
		{
			Title:    "No Genre Book",
			Author:   "Author",
			FilePath: `W:\books\file.mp3`,
			Duration: 1000,
			Format:   "mp3",
			// Genre intentionally left empty
		},
	}

	err := ExportBooksToITunesXML(books, outPath)
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	assert.Contains(t, string(data), "<key>Genre</key><string>Audiobook</string>")
}

func TestFormatToKind(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{"m4b", "AAC audio file"},
		{"M4B", "AAC audio file"},
		{".m4b", "AAC audio file"},
		{"m4a", "AAC audio file"},
		{"mp3", "MPEG audio file"},
		{".mp3", "MPEG audio file"},
		{"unknown", "MPEG audio file"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			assert.Equal(t, tt.want, formatToKind(tt.format))
		})
	}
}

func TestEncodeWindowsPathToURL(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "simple path",
			path:     `W:\books\author\title\file.m4b`,
			expected: "file://localhost/W:/books/author/title/file.m4b",
		},
		{
			name:     "path with spaces",
			path:     `W:\books\Some Author\Great Book\my file.m4b`,
			expected: "file://localhost/W:/books/Some%20Author/Great%20Book/my%20file.m4b",
		},
		{
			name:     "already forward slashes",
			path:     "W:/books/author/title/file.m4b",
			expected: "file://localhost/W:/books/author/title/file.m4b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, encodeWindowsPathToURL(tt.path))
		})
	}
}

// isValidPlist is a simple check that the data looks like a valid plist.
func isValidPlist(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "<plist") && strings.Contains(s, "</plist>")
}
