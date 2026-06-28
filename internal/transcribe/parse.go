// file: internal/transcribe/parse.go
// version: 2.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-06-28

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

// Whisper transcripts of audiobook intros follow a consistent spoken grammar:
//
//	"[Publisher] (audio) presents [TITLE] by [AUTHOR]. Read by [NARRATOR]. <noise>"
//
// but Whisper rarely emits punctuation, so the old end-anchored regexes failed
// and dumped the entire post-"by" acknowledgements wall into Author. We parse in
// explicit stages instead: strip the credit prefix, split the title on the first
// " by ", split author/narrator on "read by", then truncate each name field at
// the first prose/acknowledgement boundary.
var (
	// "[Publisher] (audio) presents " — bounded to ~80 chars so a real title that
	// happens to contain "presents" later isn't eaten.
	presentsPrefixRe = regexp.MustCompile(`(?i)^.{0,80}?\b(?:audio\s+)?presents\b\s+`)

	// Common junk openers captured by the 90s clip before the real announcement.
	junkOpenerRe = regexp.MustCompile(`(?i)^(?:this is audible[.,]?\s+|audible hopes you enjoy[^.]*\.\s+|an audible original[.,]?\s+|brilliance audio[.,]?\s+)`)

	// First standalone "by" separating title from author.
	byRe = regexp.MustCompile(`(?i)\bby\b`)

	// "read|narrated|performed by" separating author from narrator.
	readByRe = regexp.MustCompile(`(?i)\b(?:read|narrated|performed)\s+by\b`)

	// Strong boundary marking the end of a name and the start of prose/noise.
	// Everything from here on is acknowledgements, blurb, or chapter text.
	nameBoundaryRe = regexp.MustCompile(`(?i)\b(` +
		`with an introduction|with a foreword|with an afterword|with a preface|` +
		`introduction by|foreword by|afterword by|preface by|` +
		`no one writes|and of course|and i would|i would like|would like to thank|` +
		`this is a production|this is an? [\w']+ production|a production of|` +
		`unabridged|abridged|copyright|all rights|published by|` +
		`chapter one|chapter 1|prologue|part one|part 1` +
		`)\b`)

	wsRe = regexp.MustCompile(`\s+`)
)

// connectorWords are stripped from the trailing edge of a name field — they are
// dangling joiners left behind after boundary truncation (e.g. "Stephen King and").
var connectorWords = map[string]bool{
	"and": true, "with": true, "by": true, "read": true, "the": true,
	"a": true, "an": true, "for": true, "of": true, "to": true, "narrated": true,
	"performed": true,
}

// ParseAudiobookIntro extracts title, author, and narrator from a raw
// transcription of an audiobook's opening seconds. Returns zero-value
// IntroFields if the text doesn't contain a recognizable "<title> by <author>"
// announcement.
func ParseAudiobookIntro(text string) IntroFields {
	out := IntroFields{Raw: text}

	norm := collapseWS(strings.Join(splitLines(text), " "))
	if norm == "" {
		return out
	}

	// 1. Strip junk openers, then the "[Publisher] presents" credit prefix.
	norm = junkOpenerRe.ReplaceAllString(norm, "")
	norm = presentsPrefixRe.ReplaceAllString(norm, "")
	norm = strings.TrimSpace(norm)

	// 2. Split title from the rest on the FIRST standalone "by". Without the
	// "<title> by <author>" grammar there is no trustworthy announcement — a 90s
	// Whisper clip with no "by" is almost always a jingle/disclaimer ("This is
	// Audible.") or chapter text, so we extract nothing rather than guess a title.
	title, rest, hasBy := splitOnFirstRe(byRe, norm)
	if !hasBy {
		return out
	}
	out.Title = clean(title)

	// 3. Split author (before "read by") from narrator (after).
	author, narrator, hasReadBy := splitOnFirstRe(readByRe, rest)
	out.Author = truncateName(author)
	if hasReadBy {
		out.Narrator = truncateName(narrator)
	}

	if out.Title != "" && out.Author != "" {
		return out
	}

	// 4. Last-resort fallback: the old line-by-line / whole-text patterns, in
	// case staged extraction produced nothing usable (e.g. an unusual intro).
	if f := legacyPatternParse(text); f.Title != "" && f.Author != "" {
		f.Raw = text
		return f
	}
	return out
}

// truncateName trims a candidate name field down to just the name: it cuts at
// the first prose/acknowledgement boundary, caps the word count (names are
// short), and strips trailing connector words and punctuation.
func truncateName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Cut at the first strong boundary (start of acknowledgements / blurb).
	if loc := nameBoundaryRe.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	// Names are short; a long run is runaway noise. Cap to 6 words (allows a
	// two-person "X Y and A B" credit).
	fields := strings.Fields(s)
	if len(fields) > 6 {
		fields = fields[:6]
	}
	// Strip trailing dangling connectors left by the boundary cut.
	for len(fields) > 0 {
		last := strings.ToLower(strings.Trim(fields[len(fields)-1], ".,;:"))
		if connectorWords[last] {
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	return clean(strings.Join(fields, " "))
}

// legacyPatternParse is the previous regex-list approach, kept only as a
// last-resort fallback for intros the staged parser can't handle.
func legacyPatternParse(text string) IntroFields {
	var out IntroFields
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?P<title>.+?)\s+by\s+(?P<author>.+?)[,\.]\s+(?:read|narrated)\s+by\s+(?P<narrator>.+?)[\.\,]?$`),
		regexp.MustCompile(`(?i)^(?P<title>.+?)\s+by\s+(?P<author>.+?)\.\s+(?:read|narrated)\s+by\s+(?P<narrator>.+?)\.?$`),
		regexp.MustCompile(`(?i)^(?P<title>.+?)\s+by\s+(?P<author>.+?)[\.\,]?$`),
	}
	candidates := append(splitLines(text), strings.Join(splitLines(text), " "))
	for _, line := range candidates {
		line = strings.TrimSpace(line)
		if len(line) < 5 {
			continue
		}
		for _, re := range patterns {
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

// splitOnFirstRe splits s at the first match of re. Returns the text before the
// match, the text after it, and whether a match was found. When no match,
// before=s, after="".
func splitOnFirstRe(re *regexp.Regexp, s string) (before, after string, found bool) {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s, "", false
	}
	return s[:loc[0]], s[loc[1]:], true
}

func collapseWS(s string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(s, " "))
}

func splitLines(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ".,;:")
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
