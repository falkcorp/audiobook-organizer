// file: internal/organizer/path_format.go
// version: 1.2.0
// guid: a7b3c1d2-e4f5-6789-abcd-ef0123456789

package organizer

import (
	"fmt"
	"regexp"
	"strings"
)

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
//
//	'/' and '\' (path separators) → ' '
//	leading '.' (would create hidden dirs / could match parent ".")
//
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
