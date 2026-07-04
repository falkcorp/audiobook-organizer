// file: internal/itunes/import.go
// version: 1.4.1
// guid: 4b58a17d-b2b4-4743-9b7e-3462e2ed55ac
// last-edited: 2026-07-03

package itunes

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/models"
)

// ImportMode specifies how audiobooks should be imported from iTunes
type ImportMode string

const (
	// ImportModeOrganized treats iTunes files as already organized (no file operations)
	ImportModeOrganized ImportMode = "organized"

	// ImportModeImport adds books to database but doesn't organize them yet
	ImportModeImport ImportMode = "import"

	// ImportModeOrganize imports and immediately triggers organization
	ImportModeOrganize ImportMode = "organize"
)

// PathMapping defines a single from→to path prefix substitution.
type PathMapping struct {
	From string `json:"from"` // Original prefix (e.g. "file://localhost/W:/itunes/iTunes%20Media")
	To   string `json:"to"`   // Local prefix (e.g. "file://localhost/mnt/bigdata/books/itunes/iTunes Media")
}

// ImportOptions configures how the iTunes import should behave
type ImportOptions struct {
	LibraryPath      string        // Path to iTunes Library.xml
	ImportMode       ImportMode    // How to handle file organization
	PreserveLocation bool          // Keep files in iTunes location (don't move)
	ImportPlaylists  bool          // Import playlists as tags
	SkipDuplicates   bool          // Skip books already in library (by hash)
	PathMappings     []PathMapping // Path prefix remappings for cross-platform imports
}

// extractPathPrefixes finds distinct file:// location prefixes from raw iTunes locations.
// Groups by the path up to the drive/root + first two directory segments.
// Skips http/https URLs (podcast feeds).
func extractPathPrefixes(locations []string) []string {
	seen := make(map[string]bool)
	var prefixes []string
	for _, loc := range locations {
		if !strings.HasPrefix(loc, "file://") {
			continue
		}
		// Strip file://localhost/ then take drive + first directory segments
		// e.g. "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Author/file.mp3"
		// → prefix = "file://localhost/W:/itunes/iTunes%20Media"
		after := strings.TrimPrefix(loc, "file://localhost/")
		parts := strings.SplitN(after, "/", 4) // [drive, dir1, dir2, rest]
		var prefix string
		if len(parts) >= 3 {
			prefix = "file://localhost/" + parts[0] + "/" + parts[1] + "/" + parts[2]
		} else {
			prefix = "file://localhost/" + strings.Join(parts[:], "/")
		}
		if !seen[prefix] {
			seen[prefix] = true
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

// RemapPath applies all configured path mappings (first match wins).
func (o *ImportOptions) RemapPath(p string) string {
	if len(o.PathMappings) == 0 {
		return p
	}
	normalized := strings.ReplaceAll(p, "\\", "/")

	// Strip file://localhost/ or file:/// prefix for matching, but preserve
	// it in the output if the mapping's From also has it.
	stripped := normalized
	prefix := ""
	if strings.HasPrefix(normalized, "file://localhost/") {
		prefix = "file://localhost/"
		stripped = normalized[len("file://localhost/"):]
	} else if strings.HasPrefix(normalized, "file:///") {
		prefix = "file:///"
		stripped = normalized[len("file:///"):]
	}

	// URL-decode the stripped path for matching
	if strings.Contains(stripped, "%") {
		if decoded, err := url.PathUnescape(stripped); err == nil {
			stripped = decoded
		}
	}

	for _, m := range o.PathMappings {
		from := strings.ReplaceAll(m.From, "\\", "/")
		if from == "" || m.To == "" {
			continue
		}

		// Try matching against the stripped (no prefix, decoded) path
		if strings.HasPrefix(stripped, from) {
			return m.To + stripped[len(from):]
		}

		// Also try matching the full normalized path (for mappings that include file://)
		fromNorm := strings.ReplaceAll(from, "\\", "/")
		if strings.HasPrefix(normalized, fromNorm) {
			return m.To + normalized[len(fromNorm):]
		}
	}

	// If the path had a file:// prefix but no mapping matched, return the
	// decoded path (without the file:// prefix) so callers get a usable path.
	if prefix != "" {
		return stripped
	}

	// Raw Windows path with no file:// prefix (e.g. "X:/books/itunes/...").
	// Try each mapping's From value as a plain path prefix (case-insensitive,
	// backslash-normalised) so the caller ends up with a Linux path.
	if len(normalized) >= 2 && normalized[1] == ':' {
		for _, m := range o.PathMappings {
			from := strings.ReplaceAll(m.From, "\\", "/")
			if from == "" || m.To == "" {
				continue
			}
			// Strip any file:// prefix from the mapping's From for a plain comparison.
			plainFrom := from
			if strings.HasPrefix(plainFrom, "file://localhost/") {
				plainFrom = plainFrom[len("file://localhost/"):]
			} else if strings.HasPrefix(plainFrom, "file:///") {
				plainFrom = plainFrom[len("file:///"):]
			}
			if plainFrom == "" {
				continue
			}
			if strings.HasPrefix(normalized, plainFrom) {
				return m.To + normalized[len(plainFrom):]
			}
			// Case-insensitive fallback.
			if strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(plainFrom)) {
				return m.To + normalized[len(plainFrom):]
			}
		}
	}

	return p
}

// ReverseRemapPath converts a local path back to an iTunes path using the
// path mappings in reverse (first match wins). This is used during write-back
// to convert local paths like /mnt/bigdata/books/... back to iTunes paths
// like /W:/audiobook-organizer/...
func ReverseRemapPath(localPath string, mappings []PathMapping) string {
	if len(mappings) == 0 {
		return localPath
	}
	normalized := strings.ReplaceAll(localPath, "\\", "/")
	for _, m := range mappings {
		to := strings.ReplaceAll(m.To, "\\", "/")
		if to != "" && m.From != "" && strings.HasPrefix(normalized, to) {
			return m.From + normalized[len(to):]
		}
	}
	return localPath
}

// ValidationResult contains the results of validating an iTunes import
type ValidationResult struct {
	TotalTracks     int                 // Total tracks in iTunes library
	AudiobookTracks int                 // Tracks identified as audiobooks (individual files)
	AudiobookCount  int                 // Unique audiobooks (grouped by Album)
	FilesFound      int                 // Audiobook files that exist on disk
	FilesMissing    int                 // Audiobook files that are missing
	MissingPaths    []string            // List of missing file paths
	PathPrefixes    []string            // Distinct file:// path prefixes found in library (for path mapping UI)
	DuplicateHashes map[string][]string // hash -> list of titles
	EstimatedTime   string              // Estimated import time
}

// ImportResult contains the results of an iTunes import operation
type ImportResult struct {
	TotalProcessed int      // Total audiobooks processed
	Imported       int      // Successfully imported
	Skipped        int      // Skipped (duplicates)
	Failed         int      // Failed to import
	Errors         []string // Error messages
}

// trackCheck holds a decoded track path for parallel stat checking.
type trackCheck struct {
	name     string
	path     string
	rawLoc   string
	decodeOK bool
}

// ValidateImport validates an iTunes library file and checks file existence
func ValidateImport(opts ImportOptions) (*ValidationResult, error) {
	// Parse the iTunes library
	library, err := ParseLibrary(opts.LibraryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse iTunes library: %w", err)
	}

	result := &ValidationResult{
		TotalTracks:     len(library.Tracks),
		MissingPaths:    make([]string, 0),
		DuplicateHashes: make(map[string][]string),
	}

	slog.Info("iTunes validate parsed total tracks, filtering audiobooks...", "count", len(library.Tracks))

	// Pass 1: Filter audiobooks and decode paths (fast, single-threaded)
	var checks []trackCheck
	var rawLocations []string
	uniqueAlbums := make(map[string]bool)
	for _, track := range library.Tracks {
		if !IsAudiobook(track) {
			continue
		}
		result.AudiobookTracks++
		rawLocations = append(rawLocations, track.Location)

		// Count unique albums (books) using Album + Artist as key
		albumKey := strings.ToLower(strings.TrimSpace(track.Album))
		if albumKey == "" {
			albumKey = strings.ToLower(strings.TrimSpace(track.Name))
		}
		artistKey := strings.ToLower(strings.TrimSpace(track.Artist))
		uniqueAlbums[albumKey+"|"+artistKey] = true

		location := opts.RemapPath(track.Location)
		path, err := DecodeLocation(location)
		if err != nil {
			checks = append(checks, trackCheck{name: track.Name, rawLoc: track.Location, decodeOK: false})
		} else {
			checks = append(checks, trackCheck{name: track.Name, path: path, rawLoc: track.Location, decodeOK: true})
		}
	}

	result.AudiobookCount = len(uniqueAlbums)

	debugLog := config.AppConfig.LogLevel == "debug"
	slog.Debug("iTunes validate audiobook tracks ( unique books) found, checking file existence with 32 workers (debug)...", "checks_count", len(checks), "result", result.AudiobookCount, "debugLog", debugLog)

	// Pass 2: Parallel os.Stat checks
	type statResult struct {
		idx   int
		found bool
	}

	const numWorkers = 32
	jobs := make(chan int, len(checks))
	results := make(chan statResult, len(checks))

	// Start workers
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				tc := checks[idx]
				if !tc.decodeOK {
					if debugLog {
						slog.Info("DECODE_ERR", "idx", idx, "tc", tc.name, "rawLoc", tc.rawLoc)
					}
					results <- statResult{idx: idx, found: false}
					continue
				}
				_, err := os.Stat(tc.path)
				found := err == nil
				if debugLog {
					if found {
						slog.Info("FOUND", "idx", idx, "tc", tc.name, "path", tc.path)
					} else {
						slog.Info("MISSING", "idx", idx, "tc", tc.name, "path", tc.path)
					}
				}
				results <- statResult{idx: idx, found: found}
			}
		}()
	}

	// Send jobs
	for i := range checks {
		jobs <- i
	}
	close(jobs)

	// Collect results in background
	go func() {
		wg.Wait()
		close(results)
	}()

	firstFound := true
	processed := 0
	for sr := range results {
		processed++
		if processed%10000 == 0 {
			slog.Info("iTunes validate checked / audiobooks ( found, missing)...", "processed", processed, "checks_count", len(checks), "filesFound", result.FilesFound, "filesMissing", result.FilesMissing)
		}
		tc := checks[sr.idx]
		if !tc.decodeOK {
			result.FilesMissing++
			result.MissingPaths = append(result.MissingPaths, tc.rawLoc)
		} else if sr.found {
			result.FilesFound++
			if firstFound {
				slog.Info("iTunes validate first file found", "name", tc.name, "path", tc.path)
				firstFound = false
			}
		} else {
			result.FilesMissing++
			result.MissingPaths = append(result.MissingPaths, tc.path)
		}
	}

	// Extract distinct file:// path prefixes for mapping UI
	result.PathPrefixes = extractPathPrefixes(rawLocations)

	// Estimate import time (roughly 1 second per unique book for metadata extraction)
	seconds := result.AudiobookCount
	if seconds < 60 {
		result.EstimatedTime = fmt.Sprintf("%d seconds", seconds)
	} else if seconds < 3600 {
		result.EstimatedTime = fmt.Sprintf("%d minutes", seconds/60)
	} else {
		result.EstimatedTime = fmt.Sprintf("%d hours %d minutes", seconds/3600, (seconds%3600)/60)
	}

	return result, nil
}

// ConvertTrack converts an iTunes track to an audiobook model
func ConvertTrack(track *Track, opts ImportOptions) (*models.Audiobook, error) {
	// Apply path remapping on raw location, then decode
	location := opts.RemapPath(track.Location)
	filePath, err := DecodeLocation(location)
	if err != nil {
		return nil, fmt.Errorf("failed to decode location: %w", err)
	}

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("file does not exist: %s", filePath)
	}

	// Helper functions for pointer values
	stringPtr := func(s string) *string { return &s }
	intPtr := func(i int) *int { return &i }
	int64Ptr := func(i int64) *int64 { return &i }
	timePtr := func(t time.Time) *time.Time { return &t }

	// Convert duration from milliseconds to int (duration in seconds)
	var duration *int
	if track.TotalTime > 0 {
		seconds := int(track.TotalTime / 1000)
		duration = &seconds
	}

	// Determine audiobook release year
	var releaseYear *int
	if track.Year > 0 {
		releaseYear = &track.Year
	}

	// Create audiobook model
	book := &models.Audiobook{
		Title:                track.Name,
		FilePath:             filePath,
		Format:               strings.ToLower(filepath.Ext(filePath)[1:]), // e.g., "m4b", "mp3"
		Duration:             duration,
		AudiobookReleaseYear: releaseYear,

		// iTunes-specific fields
		ITunesPersistentID: stringPtr(track.PersistentID),
		ITunesDateAdded:    timePtr(track.DateAdded),
		ITunesPlayCount:    intPtr(track.PlayCount),
		ITunesRating:       intPtr(track.Rating),
		ITunesBookmark:     int64Ptr(track.Bookmark),
		ITunesImportSource: stringPtr(opts.LibraryPath),
	}

	// Convert iTunes play date (Unix timestamp) to time.Time
	if track.PlayDate > 0 {
		lastPlayed := time.Unix(track.PlayDate, 0)
		book.ITunesLastPlayed = &lastPlayed
	}

	// Extract narrator from Album Artist if different from Artist
	if track.AlbumArtist != "" && track.AlbumArtist != track.Artist {
		book.Narrator = stringPtr(track.AlbumArtist)
	}

	// Comments field typically contains the book description/synopsis
	if track.Comments != "" {
		book.Description = stringPtr(track.Comments)
	}

	// Try to extract series information from Album field
	// Common patterns: "Series Name, Book X", "Series Name - Book X"
	if track.Album != "" {
		series, position := extractSeriesFromAlbum(track.Album)
		if series != "" {
			// Note: We store series as text here, need to create Series record later
			// and link via SeriesID in actual import process
			_ = series
			_ = position
		}
	}

	return book, nil
}

// extractSeriesFromAlbum attempts to extract series name and position from an album field
func extractSeriesFromAlbum(album string) (series string, position float64) {
	album = strings.TrimSpace(album)
	if album == "" {
		return "", 0.0
	}

	// Common patterns:
	// "Series Name, Book 1"
	// "Series Name - Book 1"
	// "Series Name: Book 1"

	// Try comma separator
	if parts := strings.SplitN(album, ",", 2); len(parts) == 2 {
		series = strings.TrimSpace(parts[0])
		// Try to extract number from second part
		// This is simplified - in production you'd use regex
		return series, 0.0
	}

	// Try dash separator
	if parts := strings.SplitN(album, "-", 2); len(parts) == 2 {
		series = strings.TrimSpace(parts[0])
		return series, 0.0
	}

	// Try colon separator
	if parts := strings.SplitN(album, ":", 2); len(parts) == 2 {
		series = strings.TrimSpace(parts[0])
		return series, 0.0
	}

	// If no pattern matches, return the album as series name
	return album, 0.0
}

// computeFileHash computes SHA256 hash of a file
func computeFileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// ExtractPlaylistTags extracts playlist names as tags for a given track
func ExtractPlaylistTags(trackID int, playlists []*Playlist) []string {
	tags := make([]string, 0)

	for _, playlist := range playlists {
		// Skip built-in playlists (like "Music", "Movies", etc.)
		if isBuiltInPlaylist(playlist.Name) {
			continue
		}

		// Check if track is in this playlist
		for _, id := range playlist.TrackIDs {
			if id == trackID {
				// Add playlist name as tag (lowercase for consistency)
				tags = append(tags, strings.ToLower(playlist.Name))
				break
			}
		}
	}

	return tags
}

// isBuiltInPlaylist checks if a playlist name is a built-in iTunes playlist
func isBuiltInPlaylist(name string) bool {
	builtIn := []string{
		"Music",
		"Movies",
		"TV Shows",
		"Podcasts",
		"Audiobooks",
		"iTunes U",
		"Books",
		"Genius",
		"Recently Added",
		"Recently Played",
		"Top 25 Most Played",
	}

	for _, builtin := range builtIn {
		if name == builtin {
			return true
		}
	}

	return false
}
