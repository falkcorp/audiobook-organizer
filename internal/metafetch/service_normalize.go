// file: internal/metafetch/service_normalize.go
// version: 1.0.0
// guid: eceba49a-b99f-476f-9d43-fd6fd39a8e24
// last-edited: 2026-05-01

package metafetch

import (
	"encoding/json"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"regexp"
	"strconv"
	"strings"
)

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func derefIntAsString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}
func jsonEncodeString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// normalizeMetaSeries splits an embedded "Series Name, Book N" pattern
// out of meta.Title or meta.Series into separate Series + SeriesPosition
// fields. Audible/Audnexus sometimes return the series name with the
// book number baked in (e.g. "Mistborn, Book 3") instead of using their
// own Sequence field, which leaves us with a series row named
// "Mistborn, Book 3" if we apply the candidate as-is.
//
// Safe to call multiple times: a no-match leaves meta untouched, and an
// already-split series field will not match Pattern 3.
func NormalizeMetaSeries(meta *metadata.BookMetadata) {
	// Strip contamination (embedded title/position) from the series field first.
	if meta.Series != "" {
		cleaned, pos, flagged := metadata.StripSeriesContamination(meta.Series, meta.Title)
		if !flagged && cleaned != meta.Series {
			meta.Series = cleaned
			if pos != "" && meta.SeriesPosition == "" {
				meta.SeriesPosition = pos
			}
		}
	}

	// Existing logic: parse series info embedded in the title field.
	parsedSeries, parsedPosition, parsedTitle := ParseSeriesFromTitle(meta.Title)
	if parsedSeries == "" && meta.Series != "" {
		parsedSeries, parsedPosition, parsedTitle = ParseSeriesFromTitle(meta.Series)
		if parsedTitle == "" {
			parsedTitle = meta.Title
		}
	}
	if parsedSeries == "" {
		return
	}
	meta.Series = parsedSeries
	if parsedPosition != "" {
		meta.SeriesPosition = parsedPosition
	}
	if parsedTitle != "" {
		meta.Title = parsedTitle
	}
}

// parseSeriesFromTitle extracts series name, position, and title from strings like:
//   - "(Long Earth 05) The Long Cosmos" -> series="Long Earth", pos="5", title="The Long Cosmos"
//   - "(Series Name 3) Title" -> series="Series Name", pos="3", title="Title"
//   - "Long Earth 05 - The Long Cosmos" -> series="Long Earth", pos="5", title="The Long Cosmos"
func ParseSeriesFromTitle(s string) (series, position, title string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", ""
	}

	// Pattern 1: "(Series Name NN) Title"
	parenRe := regexp.MustCompile(`^\((.+?)\s+(\d+)\)\s*(.*)$`)
	if m := parenRe.FindStringSubmatch(s); m != nil {
		pos := strings.TrimLeft(m[2], "0")
		if pos == "" {
			pos = "0"
		}
		return strings.TrimSpace(m[1]), pos, strings.TrimSpace(m[3])
	}

	// Pattern 2: "(Series Name #NN) Title"
	parenHashRe := regexp.MustCompile(`^\((.+?)\s+#(\d+)\)\s*(.*)$`)
	if m := parenHashRe.FindStringSubmatch(s); m != nil {
		pos := strings.TrimLeft(m[2], "0")
		if pos == "" {
			pos = "0"
		}
		return strings.TrimSpace(m[1]), pos, strings.TrimSpace(m[3])
	}

	// Pattern 3: "Series Name, Book NN" (no title extraction)
	commaBookRe := regexp.MustCompile(`^(.+?),\s*[Bb]ook\s+(\d+)$`)
	if m := commaBookRe.FindStringSubmatch(s); m != nil {
		pos := strings.TrimLeft(m[2], "0")
		if pos == "" {
			pos = "0"
		}
		return strings.TrimSpace(m[1]), pos, ""
	}

	return "", "", ""
}

// significantWords returns the deduplicated set of words longer than 2 chars
// that are not stop-words, all lowercased.
// SignificantWords extracts meaningful words from a string for title matching.
func SignificantWords(s string) map[string]bool {
	words := map[string]bool{}
	var allWords []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		// Strip leading/trailing punctuation (apostrophes, commas, etc.)
		w = strings.Trim(w, ".,;:!?\"'()")
		if w == "" {
			continue
		}
		allWords = append(allWords, w)
		if len(w) > 2 && !scoreTitleStop[w] {
			words[w] = true
		}
	}
	// If all words were filtered out (e.g. title is "14", "IT", "Us"),
	// include them all so scoring can still work.
	if len(words) == 0 {
		for _, w := range allWords {
			words[w] = true
		}
	}
	return words
}

// isCompilation returns true when the title appears to be a box-set,
// collection, omnibus, anthology, or other multi-title compilation.
func isCompilation(title string) bool {
	lower := strings.ToLower(title)
	for _, phrase := range compilationPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return compilationRe.MatchString(lower)
}
func extractTrailingNumber(title string) string {
	// Strip common suffixes that aren't numbers
	clean := regexp.MustCompile(`(?i)\s*\((un)?abridged\)\s*$`).ReplaceAllString(title, "")
	clean = regexp.MustCompile(`\s*\[.*?\]\s*$`).ReplaceAllString(clean, "")
	clean = strings.TrimSpace(clean)

	m := trailingNumberRe.FindStringSubmatch(clean)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}
func normalizeSeriesNumber(pos string) string {
	m := seriesNumRe.FindStringSubmatch(pos)
	if len(m) >= 2 {
		// Normalize "8.0" → "8"
		if strings.HasSuffix(m[1], ".0") {
			return strings.TrimSuffix(m[1], ".0")
		}
		return m[1]
	}
	return ""
}
