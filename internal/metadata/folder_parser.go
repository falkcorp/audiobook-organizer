// file: internal/metadata/folder_parser.go
// version: 1.2.0
// guid: f1e2d3c4-b5a6-7890-abcd-ef1234567890

package metadata

import (
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
)

// FieldConfidence describes how confident we are about a parsed field.
type FieldConfidence int

const (
	// ConfidenceNone means the field was not found.
	ConfidenceNone FieldConfidence = 0
	// ConfidenceLow means the field was guessed from a weak signal.
	ConfidenceLow FieldConfidence = 1
	// ConfidenceMedium means the field was found in a plausible location.
	ConfidenceMedium FieldConfidence = 2
	// ConfidenceHigh means the field was found with a strong structural pattern.
	ConfidenceHigh FieldConfidence = 3
)

// FolderMetadata holds metadata parsed from the directory hierarchy.
type FolderMetadata struct {
	Authors        []string // Split on " & " and "; "
	SeriesName     string
	SeriesPosition int // 0 if not found
	Title          string
	Narrator       string
	AuthorConf     FieldConfidence
	SeriesConf     FieldConfidence
	TitleConf      FieldConfidence
	NarratorConf   FieldConfidence
}

// Precompiled patterns — package-level to avoid per-call recompilation.
var (
	// Matches "(Series Name NN)" or "(Series Name, Book NN)" at start of a folder segment.
	// Examples:
	//   (Long Earth 05)
	//   (Discworld, Book 1)
	//   (Wheel of Time 14)
	reSeriesPrefix = regexp.MustCompile(`(?i)^\(([^)]+?)\s+(\d+(?:\.\d+)?)\)`)

	// Matches "(Series Name)" with no number — series only, no position.
	reSeriesNoNum = regexp.MustCompile(`(?i)^\(([^)]+?)\)`)

	// Matches narrator patterns at end of a path segment.
	// Examples:
	//   read by Michael Fenton Stevens
	//   narrated by John Smith
	//   narrator: Jane Doe
	reNarratorSuffix = regexp.MustCompile(
		`(?i)(?:[-–,]\s*)?(?:read\s+by|narrated\s+by|narrator[:\s]+)\s*(.+)$`,
	)

	// Matches a plain "Part N of M" filename (case-insensitive).
	// Used to detect whether a filename is a useless part number.
	rePartOfN = regexp.MustCompile(`(?i)^\d+\s+part\s+\d+\s+of\s+\d+`)

	// Matches leading track numbers: "01 ", "001 ", "01. ", "01 - ".
	reLeadingTrackNum = regexp.MustCompile(`^\d{1,3}(?:\.|-)?\s+`)
)

// ExtractMetadataFromFolder parses a directory path hierarchy to extract book metadata.
// dirPath should be the directory containing the audio files (or the audio file path itself;
// the function will walk up the path components).
//
// Example path:
//
//	/mnt/bigdata/books/Terry Pratchett & Stephen Baxter/(Long Earth 05) The Long Cosmos/
//	  (Long Earth 05) The Long Cosmos - Terry Pratchett & Stephen Baxter - read by Michael Fenton Stevens/
//
// Returns FolderMetadata with best-effort values and per-field confidence scores.
func ExtractMetadataFromFolder(dirPath string) (*FolderMetadata, error) {
	// Normalise: if dirPath points to a file, use its parent directory.
	// We operate on path components, not the filesystem.
	segments := splitPathSegments(dirPath)

	fm := &FolderMetadata{}
	slog.Debug("folder_parser parsing path segments for", "segments_count", len(segments), "dirPath", dirPath)

	// Walk segments from innermost outward. The innermost segment (segments[last])
	// is the deepest directory (closest to the files); outermost is the root.
	//
	// Convention observed in well-organised audiobook libraries:
	//   segments[-1]: narrator detail dir  e.g. "Title - Author - read by Narrator"
	//   segments[-2]: series + title dir   e.g. "(Series NN) Title"
	//   segments[-3]: author dir           e.g. "First Last & Co Author"

	n := len(segments)
	if n == 0 {
		return fm, nil
	}

	// --- Pass 1: scan innermost segment for narrator and full metadata ---
	innermost := segments[n-1]
	parseInnermostSegment(innermost, fm)

	// --- Pass 2: scan second-from-innermost for series + title ---
	if n >= 2 {
		parseSeriesTitleSegment(segments[n-2], fm)
	}

	// --- Pass 3: scan further-out segments for author ---
	// Try up to 3 levels out from innermost.
	for i := n - 3; i >= 0 && i >= n-5; i-- {
		seg := segments[i]
		if tryParseAuthorSegment(seg, fm) {
			break
		}
	}

	// --- Pass 4: if author still not found, try the innermost segment's dash-split ---
	if fm.AuthorConf == ConfidenceNone && fm.Title != "" {
		tryExtractAuthorFromDashSplit(innermost, fm)
	}

	slog.Debug("folder_parser result", "authors", fm.Authors, "series", fm.SeriesName, "seriesPos", fm.SeriesPosition, "title", fm.Title, "narrator", fm.Narrator)
	return fm, nil
}

// splitPathSegments splits a path into its directory components, filtering empty strings
// and common library root names that would never be author/title/series.
func splitPathSegments(path string) []string {
	// Normalise separators
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")

	// Filter noise
	skipSegments := map[string]bool{
		// The organizer's own placeholder directory. Without this entry the
		// layout the organizer ITSELF writes -- <root>/Unknown Author/<title>/
		// -- reads the placeholder back as a real author, and this parser hands
		// it out at ConfidenceHigh. Nothing downstream catches it: scanner.go
		// assigns bm.PrimaryAuthor() to book.Author, whose recovery guard is
		// `Author == ""`, so a non-empty placeholder SKIPS the guard whose
		// defer would have cleared it, and resolveAuthorID then creates or
		// attaches a real "Unknown Author" row.
		//
		// This parser was missed when the same guard was added to the two
		// parsers now in internal/authorname -- three copies, not two. Measured
		// 2026-09-01 by driving ExtractMetadataFromFolder over organizer-layout
		// paths; see folder_parser_placeholder_test.go.
		strings.ToLower(authorname.Placeholder): true,

		"":                    true,
		".":                   true,
		"audiobooks":          true,
		"audiobook":           true,
		"books":               true,
		"library":             true,
		"media":               true,
		"audio":               true,
		"imports":             true,
		"import":              true,
		"downloads":           true,
		"organized":           true,
		"newbooks":            true,
		"collection":          true,
		"mnt":                 true,
		"bigdata":             true,
		"audiobook-organizer": true,
	}

	var segs []string
	for _, p := range parts {
		lower := strings.ToLower(strings.TrimSpace(p))
		if !skipSegments[lower] && p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

// parseInnermostSegment extracts narrator (and optionally title/author) from the
// deepest directory name. The innermost dir often has the pattern:
//
//	"Title - Author - read by Narrator"
//	"(Series NN) Title - Author - read by Narrator"
func parseInnermostSegment(seg string, fm *FolderMetadata) {
	// Extract narrator first, then work on the remainder.
	remainder := seg
	if m := reNarratorSuffix.FindStringSubmatchIndex(seg); m != nil {
		narratorRaw := strings.TrimSpace(seg[m[2]:m[3]])
		fm.Narrator = cleanNarratorValue(narratorRaw)
		fm.NarratorConf = ConfidenceHigh
		// Strip narrator portion from remainder for further parsing.
		remainder = strings.TrimSpace(seg[:m[0]])
		remainder = strings.TrimSuffix(remainder, " -")
		remainder = strings.TrimSuffix(remainder, "-")
		remainder = strings.TrimSpace(remainder)
	}

	// If title not yet set, try to parse series+title from remainder.
	if fm.TitleConf == ConfidenceNone {
		parseSeriesTitleSegment(remainder, fm)
	}
}

// parseSeriesTitleSegment parses a segment that typically looks like:
//
//	"(Long Earth 05) The Long Cosmos"
//	"(Discworld, Book 01) Guards! Guards!"
//	"The Long Cosmos"           ← no series prefix
//
// It sets fm.SeriesName, fm.SeriesPosition, fm.Title, and their confidences.
func parseSeriesTitleSegment(seg string, fm *FolderMetadata) {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return
	}

	// Try "(Series NN) Title" pattern.
	if m := reSeriesPrefix.FindStringSubmatchIndex(seg); m != nil {
		rawSeries := strings.TrimSpace(seg[m[2]:m[3]])
		rawPos := strings.TrimSpace(seg[m[4]:m[5]])
		pos, _ := strconv.Atoi(strings.Split(rawPos, ".")[0]) // handle "05.1" -> 5
		titlePart := strings.TrimSpace(seg[m[1]:])            // everything after the "(…)" prefix

		// Title may repeat the series prefix; strip it.
		if strings.HasPrefix(strings.ToLower(titlePart), strings.ToLower(rawSeries)) {
			titlePart = strings.TrimSpace(titlePart[len(rawSeries):])
			titlePart = strings.TrimPrefix(titlePart, ",")
			titlePart = strings.TrimSpace(titlePart)
		}

		if fm.SeriesConf < ConfidenceHigh {
			fm.SeriesName = rawSeries
			fm.SeriesPosition = pos
			fm.SeriesConf = ConfidenceHigh
		}
		if fm.TitleConf < ConfidenceMedium && titlePart != "" {
			// Strip trailing " - Author" dash portion from title.
			titleOnly := stripTrailingDashAuthor(titlePart)
			fm.Title = titleOnly
			fm.TitleConf = ConfidenceMedium
		}
		return
	}

	// Try "(Series Name)" with no number.
	if m := reSeriesNoNum.FindStringSubmatchIndex(seg); m != nil {
		rawSeries := strings.TrimSpace(seg[m[2]:m[3]])
		titlePart := strings.TrimSpace(seg[m[1]:])
		if fm.SeriesConf < ConfidenceMedium {
			fm.SeriesName = rawSeries
			fm.SeriesConf = ConfidenceMedium
		}
		if fm.TitleConf < ConfidenceLow && titlePart != "" {
			fm.Title = stripTrailingDashAuthor(titlePart)
			fm.TitleConf = ConfidenceLow
		}
		return
	}

	// No series prefix — the segment itself may be the title.
	// Strip any trailing " - Author" pattern.
	titleCandidate := stripTrailingDashAuthor(seg)
	if fm.TitleConf < ConfidenceLow && titleCandidate != "" {
		fm.Title = titleCandidate
		fm.TitleConf = ConfidenceLow
	}
}

// stripTrailingDashAuthor removes a trailing " - Something" chunk that looks like an author name
// appended to a title. Returns the title portion only.
func stripTrailingDashAuthor(s string) string {
	// Split on " - " and take the first non-empty chunk that doesn't look like a person name.
	// If everything looks like it could be an author, return the whole thing.
	parts := strings.Split(s, " - ")
	if len(parts) <= 1 {
		return strings.TrimSpace(s)
	}
	// Return first part (most likely the title).
	return strings.TrimSpace(parts[0])
}

// tryParseAuthorSegment attempts to interpret a path segment as an author name.
// Returns true if an author was found and stored in fm.
//
// Author segments look like:
//
//	"Terry Pratchett"
//	"Terry Pratchett & Stephen Baxter"
//	"Tolkien, J. R. R."
func tryParseAuthorSegment(seg string, fm *FolderMetadata) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return false
	}

	// Skip segments that look like titles (contain series prefix notation).
	if reSeriesPrefix.MatchString(seg) || reSeriesNoNum.MatchString(seg) {
		return false
	}

	// Skip segments that look like file listing noise.
	lower := strings.ToLower(seg)
	noiseWords := []string{"audiobooks", "audio books", "books", "library", "collection", "ebooks"}
	for _, nw := range noiseWords {
		if lower == nw {
			return false
		}
	}

	// If segment contains " - ", the first part is likely the author in "Author - Title" patterns.
	if strings.Contains(seg, " - ") {
		parts := strings.SplitN(seg, " - ", 2)
		candidate := strings.TrimSpace(parts[0])
		if looksLikeAuthorSegment(candidate) {
			fm.Authors = splitMultipleAuthors(candidate)
			fm.AuthorConf = ConfidenceMedium
			return true
		}
		return false
	}

	// Plain segment: treat whole thing as author if it looks like one.
	if looksLikeAuthorSegment(seg) {
		fm.Authors = splitMultipleAuthors(seg)
		fm.AuthorConf = ConfidenceHigh
		return true
	}

	return false
}

// tryExtractAuthorFromDashSplit tries to find an author embedded in innermost segment
// using " - Author" patterns after narrator is already stripped.
func tryExtractAuthorFromDashSplit(seg string, fm *FolderMetadata) {
	// Remove narrator portion again if still present.
	if m := reNarratorSuffix.FindStringIndex(seg); m != nil {
		seg = strings.TrimSpace(seg[:m[0]])
	}

	parts := strings.Split(seg, " - ")
	// With "(Series) Title - Author - read by Narrator" → after stripping narrator:
	// "(Series) Title - Author"
	// parts = ["(Series) Title", "Author"]
	if len(parts) >= 2 {
		candidate := strings.TrimSpace(parts[len(parts)-1])
		if looksLikeAuthorSegment(candidate) && fm.AuthorConf == ConfidenceNone {
			fm.Authors = splitMultipleAuthors(candidate)
			fm.AuthorConf = ConfidenceMedium
		}
	}
}

// looksLikeAuthorSegment returns true when s looks like a person name or multi-author string.
// Accepts:
//
//	"Terry Pratchett"
//	"Terry Pratchett & Stephen Baxter"
//	"J. R. R. Tolkien"
//	"Tolkien, J. R. R."
func looksLikeAuthorSegment(s string) bool {
	if len(s) < 3 {
		return false
	}
	// Any multi-author " & " makes this very likely an author segment.
	if strings.Contains(s, " & ") {
		return true
	}
	// Has a period (initials pattern) and capital letters.
	if strings.Contains(s, ".") {
		upperCount := 0
		for _, r := range s {
			if r >= 'A' && r <= 'Z' {
				upperCount++
			}
		}
		if upperCount >= 2 {
			return true
		}
	}
	// 2-4 capitalised words (Firstname Lastname or Firstname Middle Lastname).
	words := strings.Fields(s)
	if len(words) < 2 || len(words) > 5 {
		return false
	}
	// Allow last-name-first: "Smith, John"
	if strings.Contains(s, ",") {
		return true
	}
	for _, w := range words {
		if len(w) == 0 {
			return false
		}
		if w[0] < 'A' || w[0] > 'Z' {
			return false
		}
	}
	return true
}

// htmlEntityRe matches an HTML entity ("&#169;", "&amp;") so its terminating
// semicolon can be protected from the author-separator split below. Splitting
// "&#169;2013 by HarperCollinsPublishers" on the raw ";" sheared it into an
// "&#169" author row (id 46583) — the entity's ";" is not a separator.
var htmlEntityRe = regexp.MustCompile(`&#?[a-zA-Z0-9]+;`)

// splitMultipleAuthors splits "Author A & Author B" or "Author A; Author B" into a slice.
// Each author is trimmed. Returns a slice with at least one element.
func splitMultipleAuthors(s string) []string {
	// Protect HTML-entity semicolons (see htmlEntityRe) with a sentinel that
	// cannot appear in tag text, split, then restore.
	const sentinel = "\x00"
	s = htmlEntityRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.TrimSuffix(m, ";") + sentinel
	})
	// Split on " & " first, then " and " (case-insensitive), then ";".
	var parts []string
	for _, chunk := range strings.Split(s, " & ") {
		for _, sub := range strings.Split(chunk, ";") {
			sub = strings.ReplaceAll(sub, sentinel, ";")
			trimmed := strings.TrimSpace(sub)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	if len(parts) == 0 {
		return []string{strings.TrimSpace(s)}
	}
	return parts
}

// cleanNarratorValue strips trailing noise from a narrator string such as
// trailing file extensions, parenthetical comments, or extraneous whitespace.
func cleanNarratorValue(s string) string {
	s = strings.TrimSpace(s)
	// Remove trailing parenthetical: "Michael Smith (unabridged)" → "Michael Smith"
	if idx := strings.LastIndex(s, "("); idx > 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

// IsGenericPartFilename returns true when a filename is a useless part-number filename
// such as "01 Part 1 of 67.mp3" that carries no real metadata.
// Call this before deciding whether to attempt filename-based metadata extraction.
func IsGenericPartFilename(filename string) bool {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	base = strings.TrimSpace(base)
	return rePartOfN.MatchString(base) || isLeadingNumberOnly(base)
}

// isLeadingNumberOnly returns true when the filename, after stripping a leading
// track number, contains no alphabetical metadata (e.g. "01", "001 -", "02.").
func isLeadingNumberOnly(base string) bool {
	stripped := reLeadingTrackNum.ReplaceAllString(base, "")
	stripped = strings.TrimSpace(stripped)
	// If nothing left, or only a dash/underscore, it's a pure track number.
	for _, r := range stripped {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
