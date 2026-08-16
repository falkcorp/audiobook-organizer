// file: internal/organizer/path_format.go
// version: 1.3.0
// guid: a7b3c1d2-e4f5-6789-abcd-ef0123456789
// last-edited: 2026-08-16

package organizer

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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
//
// The stages below run in a fixed order and the order is load-bearing:
//
//  1. NFC normalization -- must precede everything, so later stages see one
//     canonical form rather than two encodings of it
//  2. invisible sweep -- before the replacer, so a stripped character cannot
//     leave two spaces the collapse would miss
//  3. ".." neutralization
//  4. unsafe-character replacement + whitespace collapse
//  5. rune-aware truncation -- after all substitutions, which change length
//  6. trailing dot/space trim -- after truncation, which can expose a new one
//  7. reserved-name escape -- last, so it sees the final name
func SanitizePathComponent(s string) string {
	// Compose to NFC. The same title reaches us in two encodings depending on
	// where it came from: macOS hands out NFD, Linux and most taggers use NFC.
	// Untreated they are different byte strings, so one book produces two
	// directories that render identically and neither lookup finds the other.
	//
	// It matters most in Korean, where NFD does not merely detach an accent --
	// it decomposes a Hangul syllable into its jamo. "해리" is 6 bytes
	// composed and 12 decomposed, with no visual difference at all.
	s = norm.NFC.String(s)

	// Control characters and non-printable bytes never belong in a filename;
	// some are legal on POSIX and make the file effectively unaddressable.
	//
	// This is a DENY-LIST on purpose. "Strip everything invisible" is the
	// tempting version and it corrupts data: U+200C/U+200D are invisible but
	// select between conjunct and separated forms in Devanagari (changing what
	// the word says), and they bind emoji sequences. Everything removed here is
	// meaningless in a filename, not merely invisible.
	s = strings.Map(func(r rune) rune {
		switch {
		case r < 32 || r == 127:
			// C0 controls and DEL.
			return -1
		case r >= 0x80 && r <= 0x9f:
			// C1 controls. U+0085 NEL in particular is a line break that
			// strings.Map's predecessor let straight through.
			return -1
		case r == 0x200b || r == 0xfeff:
			// Zero-width space and BOM: invisible AND meaningless, so two
			// titles differing only by one of these produce two directories
			// that render identically.
			return -1
		case r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			// Bidi marks, embeddings, overrides and isolates. U+202E makes a
			// filename render as text it does not contain.
			return -1
		case r == 0x2028 || r == 0x2029:
			// Line and paragraph separators.
			return -1
		case r == utf8.RuneError:
			// Already-invalid input; do not carry it into a filename.
			return -1
		case r == 0x00a0 || r == 0x2007 || r == 0x202f:
			// Non-breaking spaces. Keep the word gap, but as a real space so
			// the whitespace collapse below can see it.
			return ' '
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

	// Most filesystems cap a single component at 255 BYTES, not characters.
	// Leave room for an extension and the ".tmp-rename" suffix the two-phase
	// rename parks files under.
	//
	// The cut must land on a rune boundary. This used to be a plain s[:200],
	// which is correct for ASCII and silently catastrophic otherwise: a CJK
	// rune is 3 bytes, so a Japanese or Korean title longer than ~67 characters
	// got sliced mid-rune and the result was not valid UTF-8. The filesystem
	// rejects that outright -- open() returns EILSEQ, "illegal byte sequence"
	// -- so no such book could be organized at all. It failed at the syscall,
	// not in any string comparison, which is why only a test that actually
	// creates the file catches it.
	s = truncateOnRuneBoundary(s, maxComponentBytes)

	// Windows strips trailing dots and spaces from a name rather than
	// rejecting it, so "Book Title." is created as "Book Title" and every
	// later lookup by the original name misses. The library is reachable over
	// SMB as W:\, so this has to hold even though ext4 and ZFS accept both.
	// Done AFTER truncation, which can expose a new trailing dot.
	s = strings.TrimRight(s, ". ")

	// MS-DOS device names are still reserved by Win32 in every directory, with
	// or without an extension: "NUL.m4b" is as unopenable as "NUL". Suffix
	// rather than drop, so the book keeps a recognizable name.
	if isWindowsReservedName(s) {
		s += "_"
	}
	return s
}

// maxComponentBytes is the budget for one path component. The filesystem limit
// is 255 bytes; the headroom covers the extension plus TmpRenameSuffix.
const maxComponentBytes = 200

// truncateOnRuneBoundary cuts s to at most max bytes without splitting a rune,
// then drops any combining marks left dangling at the cut. A trailing combining
// mark is legal UTF-8 but renders as a mark floating on nothing, and in Thai
// and Devanagari it is a fragment of a cluster whose base character is gone.
func truncateOnRuneBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}

	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	s = s[:cut]

	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if !unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc) {
			break
		}
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

// windowsReservedNames are the MS-DOS device names Win32 still reserves.
var windowsReservedNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com0": {}, "com1": {}, "com2": {}, "com3": {}, "com4": {},
	"com5": {}, "com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt0": {}, "lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {},
	"lpt5": {}, "lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

// isWindowsReservedName reports whether s collides with a reserved device name.
// The check is case-insensitive and ignores any extension, because Win32
// resolves "nul.m4b" and "NUL" to the same device.
func isWindowsReservedName(s string) bool {
	base := s
	if i := strings.Index(base, "."); i >= 0 {
		base = base[:i]
	}
	_, reserved := windowsReservedNames[strings.ToLower(strings.TrimSpace(base))]
	return reserved
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
