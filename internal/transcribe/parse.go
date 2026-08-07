// file: internal/transcribe/parse.go
// version: 3.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-07

package transcribe

import (
	"regexp"
	"strings"
)

// IntroFields holds the structured data extracted from an audiobook intro.
type IntroFields struct {
	Title  string
	Author string
	// Translator is credited between Author and Narrator in translated works
	// ("<TITLE> by <AUTHOR>. Translated by <TRANSLATOR>. Narrated by <NARRATOR>").
	// Before this field existed the translator credit had no anchor of its own,
	// so Author ran straight through it: measured on prod 2026-08-07, roughly
	// half of all translated works carried a corrupted author such as
	// "Kugane Maruyama Translated by Emily Balistreri".
	Translator string
	Narrator   string
	// CoverArtist is the album/cover art credit ("Cover art by SoBin"). It was
	// previously only a truncation boundary — recognised solely so it would stop
	// contaminating the neighbouring name, then thrown away. Capturing it keeps
	// the credit that the announcement actually contained.
	CoverArtist string
	Raw         string // original transcript, for debugging
}

// Whisper transcripts of audiobook intros follow a consistent spoken grammar:
//
//	"[Publisher] (audio) presents [TITLE] written by [AUTHOR]. Read by [NARRATOR]. <noise>"
//
// but Whisper rarely emits punctuation, so end-anchored regexes fail and dump
// the entire post-"by" acknowledgements wall into Author. The staged extraction
// that solves this now lives in classify.go, which owns the split regexes; what
// remains here are the shared shaping helpers (name truncation, whitespace) and
// the boundary/prefix patterns both files use.
var (
	// "[Publisher] (audio) presents " — bounded to ~80 chars so a real title that
	// happens to contain "presents" later isn't eaten.
	presentsPrefixRe = regexp.MustCompile(`(?i)^.{0,80}?\b(?:audio\s+)?presents\b\s+`)

	// Common junk openers captured by the 90s clip before the real announcement.
	junkOpenerRe = regexp.MustCompile(`(?i)^(?:this is audible[.,]?\s+|audible hopes you enjoy[^.]*\.\s+|an audible original[.,]?\s+|brilliance audio[.,]?\s+)`)

	// Strong boundary marking the end of a name and the start of prose/noise.
	// Everything from here on is acknowledgements, blurb, or chapter text.
	//
	// 🔴 The structural markers are GENERALISED, not enumerated. This list used
	// to read "chapter one|chapter 1|prologue|part one|part 1", which terminated
	// only on chapter ONE — every other chapter number ran straight into the
	// name. Measured on prod 2026-08-07:
	//
	//	LEAKS       'Katana Jones, Chapter 24 Kongen Serven'
	//	LEAKS       'Victor Baveen. Chapter 12 Trickster Teeth'
	//	LEAKS       'Vofon. Translated by CoRansome. Chapter 0'
	//	TERMINATES  'Eric Rounds Chapter 1 Catalyst'
	//
	// Chapters 10-19 leaked too: "chapter 1" is followed by \b, which "chapter
	// 12" does not satisfy. Tier 0 hid this because a single-file book's clip
	// almost always opens at Chapter 1 — the one value that worked. Tiers 1-3
	// are multi-file books whose clips open at Chapter 7, 12, 24, so this would
	// have corrupted the entire long tail.
	//
	// The bare role words (introduction|foreword|...) matter separately: the
	// "<role> by" forms alone missed 'Lisa Zimmerman and Cale Williams.
	// Introduction' and 'Ronnie Rowlands and Tiffany Suzuki Foreword'.
	nameBoundaryRe = regexp.MustCompile(`(?i)\b(` +
		`with an introduction|with a foreword|with an afterword|with a preface|` +
		`introduction|foreword|afterword|preface|` +
		`cover art|cover design|cover illustration|illustrated by|artwork by|music by|` +
		// Any OTHER role credit ends this name. Without these the author ran
		// straight through "Translated by ..." into the next credit — the exact
		// corruption seen on prod ('Kugane Maruyama Translated by Emily
		// Balistreri'). Each role is extracted separately by its own anchor;
		// here they serve only to terminate the preceding name.
		`translated by|translation by|narrated by|read by|performed by|voiced by|` +
		`edited by|adapted by|produced by|directed by|` +
		`no one writes|and of course|and i would|i would like|would like to thank|` +
		`this is a production|this is an? [\w']+ production|a production of|` +
		`unabridged|abridged|copyright|all rights|published by|` +
		`prologue|epilogue|` +
		`(?:chapter|part|book|volume|disc|track|section)\s+[\w.]+` +
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
// IntroFields when the text is not a recognizable book-opening announcement.
//
// This is now a thin wrapper over ClassifyIntro, which is the real parser. The
// wrapper exists because "does this yield fields?" is the question two existing
// callers ask (reconcile/itunes_heal.go and the reparse path), and they have no
// file position to supply.
//
// The behavioural change from delegating: text that merely CONTAINS a standalone
// "by" no longer produces fields. It has to be an announcement. Measured against
// a 600-transcript production corpus, the old direct-split behaviour welded a
// leaked verb onto 168 titles ("Awakened Essence 1 Written") and turned ~900
// characters of narrative prose into a "title" whenever a chapter opened with
// text containing "by".
//
// ⚠️ Callers needing the three-way verdict — in particular anything deciding
// whether a file STARTS a book — must call ClassifyIntro directly. Absent fields
// here mean "not an announcement", which is NOT the same as "not a book start":
// only IntroKindUnknown vs IntroKindProse distinguishes those, and this
// signature cannot express it.
func ParseAudiobookIntro(text string) IntroFields {
	c := ClassifyIntro(text, UnknownPosition)
	if c.Kind != IntroKindCredits {
		return IntroFields{Raw: text}
	}
	return c.Fields
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
