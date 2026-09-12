// file: internal/itunes/parser.go
// version: 1.3.0
// guid: 9c3d7e51-2f84-4b60-ae91-d8c72b4f36e0
// last-edited: 2026-09-11

// Package itunes provides functionality for importing audiobooks from iTunes Library.xml files
package itunes

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Library represents the entire iTunes library structure
type Library struct {
	XMLName            xml.Name `xml:"plist"`
	MajorVersion       int      `xml:"-"`
	MinorVersion       int      `xml:"-"`
	ApplicationVersion string   `xml:"-"`
	MusicFolder        string   `xml:"-"`
	Tracks             map[string]*Track
	Playlists          []*Playlist
	// Carries records which playback fields this library's on-disk format
	// actually stores. Set by the format's constructor; see SourceFields.
	Carries SourceFields `xml:"-"`
}

// SourceFields records which per-track playback fields a library source
// format actually stores. The importer consults it before writing those
// fields onto a book, so a format with no slot for a field cannot overwrite a
// stored value with a zero it never read.
//
// The flag is per SOURCE FORMAT, not per track, on purpose. iTunes XML omits
// the "Bookmark" key when a track has no bookmark, and the plist decoder maps
// an absent key to 0, so on the XML path "key absent" and "bookmark is 0" are
// the same fact: the listener finished or cleared it. That 0 is a real reset
// and must still be written. The ITL binary parser, by contrast, decodes no
// bookmark offset at all (ITLTrack has no Bookmark field), so its 0 means
// "unknown", never "reset". A per-track presence bit cannot tell those two
// apart, and a plain `if v != 0` guard would block the genuine XML reset; a
// per-format capability gets both right.
//
// The zero value carries nothing (fail closed): a Library from a constructor
// that never declares its capabilities preserves stored values instead of
// zeroing them.
type SourceFields struct {
	Bookmark  bool // Track.Bookmark
	PlayCount bool // Track.PlayCount
	PlayDate  bool // Track.PlayDate
}

// XMLSourceFields is what an iTunes Library.xml plist carries: all three
// playback fields. An absent key there is a real zero (see SourceFields).
func XMLSourceFields() SourceFields {
	return SourceFields{Bookmark: true, PlayCount: true, PlayDate: true}
}

// ITLSourceFields is what ParseITLAsLibrary fills. Play count (mhit offset 76)
// and last-played date (offset 100) are decoded by itl_le.go / itl_be.go, but
// no bookmark offset is, so Track.Bookmark is always 0 from this source.
func ITLSourceFields() SourceFields {
	return SourceFields{PlayCount: true, PlayDate: true}
}

// Track represents a single track/audiobook in the iTunes library
type Track struct {
	TrackID      int       `xml:"-"`
	PersistentID string    `xml:"-"`
	Name         string    `xml:"-"`
	Artist       string    `xml:"-"`
	AlbumArtist  string    `xml:"-"`
	Album        string    `xml:"-"`
	Grouping     string    `xml:"-"`
	Genre        string    `xml:"-"`
	Kind         string    `xml:"-"`
	Year         int       `xml:"-"`
	Comments     string    `xml:"-"`
	Location     string    `xml:"-"`
	Size         int64     `xml:"-"`
	TotalTime    int64     `xml:"-"` // milliseconds
	DateAdded    time.Time `xml:"-"`
	PlayCount    int       `xml:"-"`
	PlayDate     int64     `xml:"-"` // Unix timestamp
	Rating       int       `xml:"-"` // 0-100 scale
	Bookmark     int64     `xml:"-"` // milliseconds
	Bookmarkable bool      `xml:"-"`
	TrackNumber  int       `xml:"-"`
	TrackCount   int       `xml:"-"`
	DiscNumber   int       `xml:"-"`
	DiscCount    int       `xml:"-"`
}

// Playlist represents an iTunes playlist
type Playlist struct {
	PlaylistID int    `xml:"-"`
	Name       string `xml:"-"`
	TrackIDs   []int  `xml:"-"`
}

// ParseLibrary parses an iTunes library file and returns a Library structure.
// It auto-detects the file format: if the file starts with the "hdfm" magic
// bytes it is treated as an ITL binary; otherwise it is parsed as an XML plist.
// This means callers can point at either iTunes Library.xml or iTunes Library.itl
// and the correct parser will be used transparently.
func ParseLibrary(path string) (*Library, error) {
	// Peek at the first four bytes to detect the ITL binary format.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open iTunes library file: %w", err)
	}
	magic := make([]byte, 4)
	_, readErr := io.ReadFull(f, magic)
	f.Close()

	if readErr == nil && string(magic) == "hdfm" {
		// ITL binary format
		return ParseITLAsLibrary(path)
	}

	// XML plist format (original behaviour)
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open iTunes library file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read iTunes library file: %w", err)
	}

	library, err := parsePlist(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse iTunes library XML: %w", err)
	}

	return library, nil
}

// IsAudiobook determines if a track is an audiobook based on various criteria
func IsAudiobook(track *Track) bool {
	if track == nil {
		return false
	}

	// Check Kind field (most reliable)
	kindLower := strings.ToLower(track.Kind)
	if strings.Contains(kindLower, "audiobook") {
		return true
	}
	if strings.Contains(kindLower, "spoken word") {
		return true
	}

	// Check Genre
	genreLower := strings.ToLower(track.Genre)
	if strings.Contains(genreLower, "audiobook") {
		return true
	}
	if strings.Contains(genreLower, "spoken") {
		return true
	}

	// Check file location contains "Audiobooks"
	if strings.Contains(track.Location, "Audiobooks") || strings.Contains(track.Location, "audiobooks") {
		return true
	}

	return false
}

// DecodeLocation decodes an iTunes file:// URL to a local filesystem path
func DecodeLocation(location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("location is empty")
	}

	// Remove "file://localhost" or "file://" prefix
	location = strings.TrimPrefix(location, "file://localhost")
	location = strings.TrimPrefix(location, "file://")

	// URL decode (handles %20, %2F, etc.)
	decoded, err := url.QueryUnescape(location)
	if err != nil {
		return "", fmt.Errorf("failed to URL decode location: %w", err)
	}

	// Handle Windows paths (C:/ vs /C:/)
	if runtime.GOOS == "windows" {
		decoded = strings.TrimPrefix(decoded, "/")
	}

	return decoded, nil
}

// EncodeLocation encodes a local filesystem path to an iTunes file:// URL
func EncodeLocation(path string) string {
	// Add leading slash for absolute paths on Windows
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// URL encode the path
	encoded := url.PathEscape(path)

	// Replace %2F back to / for path separators
	encoded = strings.ReplaceAll(encoded, "%2F", "/")

	// Add file://localhost prefix
	return "file://localhost" + encoded
}

// FindLibraryFile searches for iTunes Library.xml in common locations
func FindLibraryFile() (string, error) {
	// Get user's home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	// List of possible locations
	var searchPaths []string

	if runtime.GOOS == "darwin" {
		// macOS locations
		searchPaths = []string{
			filepath.Join(home, "Music", "Music", "Library.xml"),               // Modern Music.app
			filepath.Join(home, "Music", "iTunes", "iTunes Music Library.xml"), // Legacy iTunes
		}
	} else if runtime.GOOS == "windows" {
		// Windows locations
		searchPaths = []string{
			filepath.Join(home, "Music", "iTunes", "iTunes Music Library.xml"),
		}
	}

	// Try each path
	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("iTunes library file not found in standard locations")
}
