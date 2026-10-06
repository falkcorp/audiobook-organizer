// file: internal/metadata/folder_parser_legacy.go
// version: 1.0.0
// guid: 533e69bc-e80c-4ec1-9f90-065dca107c40
// last-edited: 2026-10-05
//
// The folder parse as it stood before ParseBookName (main @ 2b969e5f9),
// FROZEN. It is evidence, not a parser anyone scans or searches with: the
// maintenance.reparse-folder-names fixer uses it to tell which stored titles,
// authors and series the old folder parse wrote, so it lists exactly those
// rows and never a value that came from a tag or an edit. Never change it;
// delete it with the fixer.

package metadata

import (
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// LegacyFolderParse returns what ExtractMetadataFromFolder returned for
// dirPath before 2026-10-05.
func LegacyFolderParse(dirPath string) *FolderMetadata {
	fm, _ := legacyExtractMetadataFromFolder(dirPath)
	return fm
}

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
func legacyExtractMetadataFromFolder(dirPath string) (*FolderMetadata, error) {
	// Normalise: if dirPath points to a file, use its parent directory.
	// We operate on path components, not the filesystem.
	segments := splitPathSegments(dirPath)

	fm := &FolderMetadata{}

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
	legacyParseInnermostSegment(innermost, fm)

	// --- Pass 2: scan second-from-innermost for series + title ---
	if n >= 2 {
		legacyParseSeriesTitleSegment(segments[n-2], fm)
	}

	// --- Pass 3: scan further-out segments for author ---
	// Try up to 3 levels out from innermost.
	for i := n - 3; i >= 0 && i >= n-5; i-- {
		seg := segments[i]
		if tryParseAuthorSegment(seg, fm) {
			break
		}
		// A segment that is author-SHAPED but work-NAMED ("The Stormlight
		// Archive") is a SERIES folder. Record it, then look exactly ONE step
		// further out: in the common "<author>/<series>/<title>/<disc>" layout
		// that is the author ("Stephen King/The Dark Tower/The Gunslinger/
		// Disc 1"), and it is the only author source for an untagged book. It
		// is accepted only when person-shaped AND not a genre or container
		// folder, because "/mnt/Science Fiction/The Stormlight Archive/..." has
		// the same shape and walking on unchecked credited "Science Fiction".
		// The walk stops after that step whatever it finds.
		if looksLikeAuthorSegment(seg) && personname.LooksLikeWorkTitle(seg) {
			if fm.SeriesConf == ConfidenceNone {
				fm.SeriesName = seg
				fm.SeriesConf = ConfidenceLow
			}
			if i-1 >= 0 {
				outer := strings.TrimSpace(segments[i-1])
				if looksLikeFolderAuthor(outer) {
					fm.Authors = splitMultipleAuthors(outer)
					fm.AuthorConf = ConfidenceMedium
				}
			}
			break
		}
	}

	// --- Pass 4: if author still not found, try the innermost segment's dash-split ---
	if fm.AuthorConf == ConfidenceNone && fm.Title != "" {
		tryExtractAuthorFromDashSplit(innermost, fm)
	}

	return fm, nil
}

// parseInnermostSegment extracts narrator (and optionally title/author) from the
// deepest directory name. The innermost dir often has the pattern:
//
//	"Title - Author - read by Narrator"
//	"(Series NN) Title - Author - read by Narrator"
func legacyParseInnermostSegment(seg string, fm *FolderMetadata) {
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
		legacyParseSeriesTitleSegment(remainder, fm)
	}
}

// parseSeriesTitleSegment parses a segment that typically looks like:
//
//	"(Long Earth 05) The Long Cosmos"
//	"(Discworld, Book 01) Guards! Guards!"
//	"The Long Cosmos"           ← no series prefix
//
// It sets fm.SeriesName, fm.SeriesPosition, fm.Title, and their confidences.
func legacyParseSeriesTitleSegment(seg string, fm *FolderMetadata) {
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
			titleOnly := legacyStripTrailingDashAuthor(titlePart)
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
			fm.Title = legacyStripTrailingDashAuthor(titlePart)
			fm.TitleConf = ConfidenceLow
		}
		return
	}

	// No series prefix — the segment itself may be the title.
	// Strip any trailing " - Author" pattern.
	titleCandidate := legacyStripTrailingDashAuthor(seg)
	if fm.TitleConf < ConfidenceLow && titleCandidate != "" {
		fm.Title = titleCandidate
		fm.TitleConf = ConfidenceLow
	}
}

// stripTrailingDashAuthor removes a trailing " - Something" chunk that looks like an author name
// appended to a title. Returns the title portion only.
func legacyStripTrailingDashAuthor(s string) string {
	// Split on " - " and take the first non-empty chunk that doesn't look like a person name.
	// If everything looks like it could be an author, return the whole thing.
	parts := strings.Split(s, " - ")
	if len(parts) <= 1 {
		return strings.TrimSpace(s)
	}
	// Return first part (most likely the title).
	return strings.TrimSpace(parts[0])
}
