// file: internal/transcribe/parse.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-06-26

package transcribe

import (
	"regexp"
	"strings"
)

// IntroFields holds the structured data extracted from an audiobook intro.
type IntroFields struct {
	Title    string
	Author   string
	Narrator string
	Raw      string // original transcript, for debugging
}

// Common patterns seen in audiobook intros, in preference order.
// Group names: title, author, narrator.
var introPatterns = []*regexp.Regexp{
	// "The Name of the Wind by Patrick Rothfuss, read by Nick Podehl"
	regexp.MustCompile(`(?i)^(?P<title>.+?)\s+by\s+(?P<author>.+?)[,\.]\s+(?:read|narrated)\s+by\s+(?P<narrator>.+?)[\.\,]?$`),
	// "The Name of the Wind by Patrick Rothfuss. Read by Nick Podehl."
	regexp.MustCompile(`(?i)^(?P<title>.+?)\s+by\s+(?P<author>.+?)\.\s+(?:read|narrated)\s+by\s+(?P<narrator>.+?)\.?$`),
	// Without narrator: "The Name of the Wind by Patrick Rothfuss"
	regexp.MustCompile(`(?i)^(?P<title>.+?)\s+by\s+(?P<author>.+?)[\.\,]?$`),
}

// ParseAudiobookIntro extracts title, author, and narrator from a raw
// transcription of an audiobook's opening seconds. Returns zero-value
// IntroFields if the text doesn't match any known pattern.
func ParseAudiobookIntro(text string) IntroFields {
	out := IntroFields{Raw: text}

	// Normalize whitespace and trim filler words that sometimes appear before
	// the actual announcement (music cues, publisher logos, etc.).
	// We scan line-by-line and take the first line that looks like an intro.
	for _, line := range splitLines(text) {
		line = strings.TrimSpace(line)
		if len(line) < 5 {
			continue
		}
		for _, re := range introPatterns {
			m := reNamedMatch(re, line)
			if m == nil {
				continue
			}
			out.Title = clean(m["title"])
			out.Author = clean(m["author"])
			out.Narrator = clean(m["narrator"])
			if out.Title != "" && out.Author != "" {
				return out
			}
		}
	}

	// Second pass: the whole text as one string (in case Whisper didn't add newlines).
	norm := strings.Join(splitLines(text), " ")
	for _, re := range introPatterns {
		m := reNamedMatch(re, norm)
		if m == nil {
			continue
		}
		out.Title = clean(m["title"])
		out.Author = clean(m["author"])
		out.Narrator = clean(m["narrator"])
		if out.Title != "" && out.Author != "" {
			return out
		}
	}

	return out
}

// MatchesTrack returns true when the parsed intro's title/author overlap
// sufficiently with the given iTunes track Album and Artist strings.
// Used for disambiguation scoring.
func (f IntroFields) MatchesTrack(album, artist string) int {
	score := 0
	albumWords := titleWords(strings.ToLower(album))
	artistWords := titleWords(strings.ToLower(artist))

	titleL := strings.ToLower(f.Title)
	for _, w := range albumWords {
		if strings.Contains(titleL, w) {
			score += 3
		}
	}
	authorL := strings.ToLower(f.Author)
	narratorL := strings.ToLower(f.Narrator)
	for _, w := range artistWords {
		if strings.Contains(authorL, w) || strings.Contains(narratorL, w) {
			score += 2
		}
	}
	return score
}

// titleWords returns lowercase words >3 chars (mirrors the helper in itunes_heal.go).
func titleWords(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		if len(w) > 3 {
			out = append(out, w)
		}
	}
	return out
}

func splitLines(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".,;:")
	return strings.TrimSpace(s)
}

func reNamedMatch(re *regexp.Regexp, s string) map[string]string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	result := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if name != "" && i < len(m) {
			result[name] = m[i]
		}
	}
	return result
}
