// file: internal/metafetch/service_normalize.go
// version: 1.1.0
// guid: eceba49a-b99f-476f-9d43-fd6fd39a8e24
// last-edited: 2026-09-02

package metafetch

import (
	"encoding/json"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"log/slog"
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
		c := metadata.StripSeriesContamination(meta.Series, meta.Title)
		switch {
		case c.Flag:
			// Nothing was rewritten. Logged anyway so the owner has a route to
			// the names the normalizer deliberately declined to touch --
			// "when we find one I'll manually override" needs something to find.
			slog.Info("series normalize: left a series name alone for review",
				"series", meta.Series, "reason", string(c.FlagReason),
				"candidate_series", c.CandidateName, "candidate_position", c.CandidatePosition,
				"title", meta.Title, "asin", meta.ASIN)
		case c.Changed(meta.Series):
			// A silent rewrite of user-visible data is the pattern this repo
			// keeps getting burned by, so every strip is logged with the rule
			// that made it. There is no book id here: this runs on a metadata
			// CANDIDATE, before any store write, so the title and ASIN are the
			// only identity available.
			slog.Info("series normalize: moved the book position out of the series name",
				"rule", c.Rule, "series_before", meta.Series, "series_after", c.Name,
				"position", c.Position, "title", meta.Title, "asin", meta.ASIN)
			meta.Series = c.Name
			if c.Position != "" && meta.SeriesPosition == "" {
				meta.SeriesPosition = c.Position
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
	// ⚠️ This guard is NEW. It used to be a bare `if parsedPosition != ""`, which
	// overwrote SeriesPosition unconditionally -- including a position the strip
	// block above had just lifted out of the series name, and including a
	// provider's own explicit Sequence field. A number parsed out of prose by a
	// regex does not outrank a number that arrived in a dedicated field, and a
	// write-back that a later line silently clobbers is not a write-back at all.
	if parsedPosition != "" && meta.SeriesPosition == "" {
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
	for w := range strings.FieldsSeq(strings.ToLower(s)) {
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
		if before, ok := strings.CutSuffix(m[1], ".0"); ok {
			return before
		}
		return m[1]
	}
	return ""
}
