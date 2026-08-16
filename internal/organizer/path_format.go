// file: internal/organizer/path_format.go
// version: 1.2.0
// guid: a7b3c1d2-e4f5-6789-abcd-ef0123456789

package organizer

import (
	"fmt"
	"regexp"
	"strings"
)

// FormatVars holds all variables available for path/title formatting.
type FormatVars struct {
	Author      string
	Title       string
	Series      string
	SeriesPos   string
	Year        int
	Narrator    string
	Lang        string // ISO 639-1 (en, de, fr)
	Track       int
	TotalTracks int
	TrackTitle  string // pre-computed segment title
	Ext         string
}

var formatVarPattern = regexp.MustCompile(`\{(\w+)(?::([^}]+))?\}`)

const (
	DefaultPathFormat         = "{author}/{series_prefix}{title}/{track_title}.{ext}"
	DefaultSegmentTitleFormat = "{title} - {track}_{total_tracks}"
)

// FormatSegmentTitle formats a per-segment title using the template.
// For single-file books (totalTracks == 1), returns just the title without numbering.
// title is scrubbed of path separators — segment titles are path components.
func FormatSegmentTitle(format string, title string, track, totalTracks int) string {
	title = scrubVar(title)
	if totalTracks <= 1 {
		return title
	}
	result := format
	result = strings.ReplaceAll(result, "{title}", title)
	result = strings.ReplaceAll(result, "{total_tracks}", fmt.Sprintf("%d", totalTracks))

	// Handle {track} with optional format spec like {track:02d}
	result = formatVarPattern.ReplaceAllStringFunc(result, func(match string) string {
		parts := formatVarPattern.FindStringSubmatch(match)
		name := parts[1]
		spec := parts[2]
		if name == "track" {
			if spec != "" {
				return fmt.Sprintf("%"+spec, track)
			}
			return fmt.Sprintf("%d", track)
		}
		return match
	})
	return result
}

// scrubVar strips characters that would create unintended path separators
// or hidden directories if they leaked from metadata into a template
// substitution. Called on EVERY variable value before it's interpolated
// into the path format. Without this, a Title like "Tarkin - Star Wars - 3/85"
// (real prod data, 2026-05-28) splits into a "Tarkin - Star Wars - 3/"
// directory + "85.mp3" file — and the scanner then sees 85 single-file
// directories and creates 85 separate Book records instead of one Book
// with 85 BookFiles.
//
// Replaces:
//   '/' and '\' (path separators) → ' '
//   leading '.' (would create hidden dirs / could match parent ".")
// Whitespace is collapsed at the per-component SanitizePathComponent step.
func scrubVar(s string) string {
	if s == "" {
		return s
	}
	// Drop path separators outright — they have no place inside a single
	// metadata value. Anyone wanting a "/" in a path should put it in the
	// template structure, not in {title} / {series} / etc.
	s = strings.ReplaceAll(s, "/", " ")
	s = strings.ReplaceAll(s, "\\", " ")
	// Leading dots create hidden files/dirs on POSIX and ".." is parent.
	s = strings.TrimLeft(s, ".")
	return s
}

// FormatPath formats a full file path using the path_format template.
func FormatPath(format string, vars FormatVars) string {
	// SCRUB every variable BEFORE substitution so no metadata value can
	// introduce an unintended path separator. The post-substitution split
	// at line ~150 below treats every '/' as a directory boundary; if
	// {title} leaks a '/', the directory tree explodes (see scrubVar comment).
	author := scrubVar(vars.Author)
	title := scrubVar(vars.Title)
	series := scrubVar(vars.Series)
	seriesPos := scrubVar(vars.SeriesPos)
	narrator := scrubVar(vars.Narrator)
	lang := scrubVar(vars.Lang)

	trackTitle := scrubVar(vars.TrackTitle)
	if trackTitle == "" && vars.Track > 0 {
		// Use scrubbed title here too — segment title is a path component.
		trackTitle = FormatSegmentTitle(DefaultSegmentTitleFormat, title, vars.Track, vars.TotalTracks)
		// FormatSegmentTitle could in theory emit a '/' if a future template
		// uses one — re-scrub defensively.
		trackTitle = scrubVar(trackTitle)
	}

	seriesPrefix := ""
	if series != "" {
		seriesPrefix = series
		if seriesPos != "" {
			seriesPrefix += " " + seriesPos
		}
		seriesPrefix += " - "
	}

	yearStr := ""
	if vars.Year > 0 {
		yearStr = fmt.Sprintf("%d", vars.Year)
	}

	result := format
	result = strings.ReplaceAll(result, "{author}", author)
	result = strings.ReplaceAll(result, "{title}", title)
	result = strings.ReplaceAll(result, "{series}", series)
	result = strings.ReplaceAll(result, "{series_position}", seriesPos)
	result = strings.ReplaceAll(result, "{series_prefix}", seriesPrefix)
	result = strings.ReplaceAll(result, "{year}", yearStr)
	result = strings.ReplaceAll(result, "{narrator}", narrator)
	result = strings.ReplaceAll(result, "{lang}", lang)
	result = strings.ReplaceAll(result, "{track_title}", trackTitle)
	result = strings.ReplaceAll(result, "{ext}", vars.Ext)

	// Handle {track} and {total_tracks} with optional format specs
	result = formatVarPattern.ReplaceAllStringFunc(result, func(match string) string {
		parts := formatVarPattern.FindStringSubmatch(match)
		name := parts[1]
		spec := parts[2]
		switch name {
		case "track":
			if spec != "" {
				return fmt.Sprintf("%"+spec, vars.Track)
			}
			return fmt.Sprintf("%d", vars.Track)
		case "total_tracks":
			return fmt.Sprintf("%d", vars.TotalTracks)
		}
		return match
	})

	result = CollapseEmptySegments(result)

	// Sanitize each path component
	parts := strings.Split(result, "/")
	for i, part := range parts {
		parts[i] = SanitizePathComponent(part)
	}
	result = strings.Join(parts, "/")

	// Final cleanup of empty segments after sanitization
	for strings.Contains(result, "//") {
		result = strings.ReplaceAll(result, "//", "/")
	}
	result = strings.Trim(result, "/")

	// Collapse redundant "X - X" duplication in the final segment only.
	// This catches cases where series name equals title (e.g., "Cobra Outlaw - Cobra Outlaw").
	pathParts := strings.Split(result, "/")
	if len(pathParts) > 0 {
		pathParts[len(pathParts)-1] = collapseRedundantDup(pathParts[len(pathParts)-1])
		result = strings.Join(pathParts, "/")
	}

	return result
}

// CollapseEmptySegments cleans up paths with empty variable substitutions.
func CollapseEmptySegments(path string) string {
	for strings.Contains(path, "..") {
		path = strings.ReplaceAll(path, "..", ".")
	}
	path = strings.ReplaceAll(path, "./", "/")
	path = strings.ReplaceAll(path, "/.", "/")
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	path = strings.Trim(path, "/.")
	return path
}

// SanitizePathComponent removes filesystem-unsafe characters from a path
// component. It is the ONLY sanitizer in this package.
//
// It used to have a rival: organizer.go's sanitizeFilename, which ran a second
// time over output BuildPath had already sanitized. Almost all of its rules were
// dead by then (there is no ':' left to replace after this function has run) —
// the one rule that still fired was stripping '[' and ']', so the second pass
// existed only to undo the first. Two sanitizers is the same defect as two path
// builders. This one absorbed the rules that were genuinely unique to it —
// control characters and ".." — and the other was deleted.
//
// Square brackets are deliberately NOT stripped. They are legal on every
// filesystem we target (ext4, APFS, NTFS, ZFS) and are idiomatic in audiobook
// naming — "[Unabridged]", "[AAC 128kbps]".
func SanitizePathComponent(s string) string {
	// Control characters and non-printable bytes never belong in a filename;
	// some are legal on POSIX and make the file effectively unaddressable.
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)

	// Neutralize ".." so no single component can be a parent-directory
	// reference. BuildPath's CollapseEmptySegments already folds these, but
	// this function is also called directly, and a traversal guard that only
	// holds on one call path is not a guard.
	s = strings.ReplaceAll(s, "..", "_")

	replacer := strings.NewReplacer(
		"/", " ",
		"\\", " ",
		":", " -",
		"*", "",
		"?", "",
		"\"", "'",
		"<", "",
		">", "",
		"|", " -",
	)
	s = replacer.Replace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)

	// Most filesystems cap a single component at 255 bytes. Leave room for an
	// extension and the ".tmp" suffix the two-phase rename parks files under.
	if len(s) > 200 {
		s = strings.TrimSpace(s[:200])
	}
	return s
}

// collapseRedundantDup strips "X - X" → "X" in a single path segment,
// case-insensitive, whitespace-normalized. Handles only 2-part duplicates.
// Idempotent.
func collapseRedundantDup(segment string) string {
	parts := strings.Split(segment, " - ")
	if len(parts) != 2 {
		return segment
	}
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(s), " "))
	}
	if norm(parts[0]) == norm(parts[1]) {
		return strings.TrimSpace(parts[0])
	}
	return segment
}
