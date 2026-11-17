// file: internal/transcribe/marker.go
// version: 1.0.1
// guid: 5a2d9e04-7b1c-4f83-a6d0-c3e81f4b7295
// last-edited: 2026-11-17

package transcribe

import (
	"regexp"
	"strings"
	"unicode"
)

// markerClass says what a structural marker announces.
type markerClass int

const (
	// markerChapter is a position inside the book: chapter, track, episode.
	markerChapter markerClass = iota + 1
	// markerPart is a larger division: part, book, section, disc.
	markerPart
	// markerMatter is front or back matter: prologue, epilogue, foreword, ...
	markerMatter
)

// structuralMarker is one parsed marker at the start of a clip.
type structuralMarker struct {
	Class markerClass
	// Keyword is the lowercase word that announced it ("chapter", "prologue").
	// A bare "12." heading reports "chapter".
	Keyword string
	// Number is the announced number, 0 when none was stated (matter markers).
	Number int
	// HasNumber distinguishes "Chapter 0" from a marker with no number.
	HasNumber bool
	// End is the byte offset in the parsed string just past the marker.
	End int
}

var (
	// markerKeywordRe is the keyword of a numbered marker. "volume" is not here
	// on purpose: "Volume 2" opens a book as often as it continues one, and the
	// "volumes N through M" wording is a collection claim (see collectionRange),
	// not a position.
	markerKeywordRe = regexp.MustCompile(`(?i)^(chapter|part|book|disc|track|section|episode)\s+`)

	// matterRe is the un-numbered front/back matter markers.
	matterRe = regexp.MustCompile(`(?i)^(prologue|epilogue|interlude|afterword|foreword|introduction|preface|prelude|intermission|dedication|epigraph|acknowledg(?:e)?ments)\b`)

	// bareNumberRe is a heading that is only a number and a full stop: "12. The
	// Joker's Gambit". One to three digits, then a dot that ends the token. It
	// must not fire on a year ("1939. Bruce"), a time or decimal ("10.30",
	// "3.5 million"), all of which fail the digit cap or the trailing-space rule.
	bareNumberRe = regexp.MustCompile(`^([0-9]{1,3})\.(?:\s+|$)`)

	// midKeywordRe finds a numbered keyword anywhere in the clip.
	midKeywordRe = regexp.MustCompile(`(?i)\b(chapter|part|disc|track|section|book)\s+`)

	// midContinuationRe is explicit continuation wording that needs no number.
	midContinuationRe = regexp.MustCompile(`(?i)\bthis part (?:includes|contains)\b|\bend of (?:the )?(?:part|chapter|disc|book)\b`)

	continuedWordRe = regexp.MustCompile(`(?i)^[\s,.]*\(?continued\b`)
)

// markerClassOf maps a numbered keyword to what it announces.
func markerClassOf(keyword string) markerClass {
	switch keyword {
	case "chapter", "track", "episode":
		return markerChapter
	default:
		return markerPart
	}
}

// parseMarkerAt parses a structural marker at the START of s. It returns false
// for anything that merely begins with a marker word: "Part of me", "Book him"
// and "Chapter the best" have no number, so they are prose.
func parseMarkerAt(s string) (structuralMarker, bool) {
	if m := markerKeywordRe.FindStringSubmatch(s); m != nil {
		kw := strings.ToLower(m[1])
		n, used, ok := scanNumber(s[len(m[0]):])
		if !ok {
			return structuralMarker{}, false
		}
		return structuralMarker{
			Class: markerClassOf(kw), Keyword: kw, Number: n, HasNumber: true,
			End: len(m[0]) + used,
		}, true
	}
	if m := matterRe.FindStringSubmatch(s); m != nil && matterTailOK(s[len(m[0]):]) {
		return structuralMarker{Class: markerMatter, Keyword: strings.ToLower(m[1]), End: len(m[0])}, true
	}
	if m := bareNumberRe.FindStringSubmatch(s); m != nil {
		n := parseClipNumber(m[1])
		return structuralMarker{
			Class: markerChapter, Keyword: "chapter", Number: n, HasNumber: true,
			End: len(m[0]),
		}, true
	}
	return structuralMarker{}, false
}

// matterTailOK reports whether what follows a front/back matter word reads as a
// heading and not as a sentence that happens to start with the word: the end of
// the clip, punctuation, or a capitalised word or digit after a space.
// "Dedication was the one thing Bruce never lacked" is narration; "Dedication.
// For Alfred" and "Prologue Bruce Wayne stood" are headings.
func matterTailOK(rest string) bool {
	if rest == "" {
		return true
	}
	r := []rune(rest)
	switch r[0] {
	case '.', ',', ':', ';', '!', '?', '-', '\u2013', '\u2014', ')':
		return true
	}
	if !unicode.IsSpace(r[0]) {
		return false
	}
	for _, c := range r {
		if unicode.IsSpace(c) {
			continue
		}
		return unicode.IsUpper(c) || unicode.IsDigit(c) || strings.ContainsRune(".,:;!?-\u2013\u2014(", c)
	}
	return true
}

// startsWithMarker reports whether s opens with a structural marker.
func startsWithMarker(s string) bool {
	_, ok := parseMarkerAt(strings.TrimSpace(s))
	return ok
}

// midMarker looks for explicit continuation wording anywhere in the clip and
// returns the first announced number (0 when none) and whether there was any.
//
// "continued" counts ONLY directly after a numbered marker ("Disc two
// continued"). A bare "continued" is ordinary narration ("he continued to...")
// and used to turn every such clip into a chapter.
func midMarker(body string) (number int, ok bool) {
	number = firstMidNumber(body)
	if midContinuationRe.MatchString(body) {
		return number, true
	}
	for _, loc := range midKeywordRe.FindAllStringIndex(body, -1) {
		_, used, isNum := scanNumber(body[loc[1]:])
		if !isNum {
			continue
		}
		if continuedWordRe.MatchString(body[loc[1]+used:]) {
			return number, true
		}
	}
	return number, false
}

// firstMidNumber is the first announced chapter/part/disc/track/section number
// anywhere in body, 0 when there is none. ("book" is not consulted here: it is
// a start marker, and "book 2" in the middle of a sentence is a reference.)
func firstMidNumber(body string) int {
	for _, loc := range midKeywordRe.FindAllStringSubmatchIndex(body, -1) {
		if strings.EqualFold(body[loc[2]:loc[3]], "book") {
			continue
		}
		if n, _, ok := scanNumber(body[loc[1]:]); ok {
			return n
		}
	}
	return 0
}

// connectorTitleWords may sit inside a chapter title without a capital.
var connectorTitleWords = map[string]bool{
	"a": true, "an": true, "and": true, "at": true, "for": true, "from": true,
	"in": true, "of": true, "on": true, "or": true, "the": true, "to": true,
	"with": true, "vs": true,
}

const maxChapterTitleWords = 9

// chapterTitleAfter extracts a chapter title from the text after a marker, or
// "" when there is not one.
//
// A wrong title is worse than a missing one, so a title is accepted only when
// its END is unambiguous:
//
//   - it ends at sentence punctuation ("Chapter 5. The Joker's Gambit. Bruce
//     stared...") or at the end of the clip; or
//   - it is an ALL-CAPS heading, which ends at the first word that is not all
//     caps ("CHAPTER XV THE KILLING JOKE Enough steel beams...").
//
// A run of Capitalised words that is followed by a lowercase word with no
// punctuation is NOT accepted: "Chapter 5 The Joker's Gambit Bruce Wayne walked"
// cannot be split into a title and narration, and "Chapter 5. Bruce stared at the
// ceiling" has no title at all.
func chapterTitleAfter(rest string) string {
	rest = strings.TrimLeft(rest, " \t.,:;-\u2013\u2014")
	words := strings.Fields(rest)
	if len(words) == 0 {
		return ""
	}
	if first := strings.Trim(words[0], ".,;:!?\"'()"); isAllCaps(first) {
		var title []string
		for _, w := range words {
			bare := strings.Trim(w, ".,;:!?\"'()")
			if bare == "" || !isAllCaps(bare) || len(title) >= maxChapterTitleWords {
				break
			}
			title = append(title, bare)
			if strings.ContainsAny(w[len(w)-1:], ".!?") {
				break
			}
		}
		return strings.Join(title, " ")
	}

	var title []string
	terminated := false
	for i, w := range words {
		bare := strings.Trim(w, ".,;:!?\"'()")
		if bare == "" {
			break
		}
		isCap := startsUpperOrDigit(bare)
		if !isCap && !(connectorTitleWords[strings.ToLower(bare)] && len(title) > 0) {
			break
		}
		title = append(title, bare)
		if len(title) >= maxChapterTitleWords {
			break
		}
		if strings.ContainsAny(w[len(w)-1:], ".!?") || i == len(words)-1 {
			terminated = true
			break
		}
	}
	// Trailing connectors belong to the narration that follows.
	for len(title) > 0 && connectorTitleWords[strings.ToLower(title[len(title)-1])] {
		title = title[:len(title)-1]
		terminated = false
	}
	if len(title) == 0 || !terminated {
		return ""
	}
	return strings.Join(title, " ")
}

// isAllCaps reports a token with at least two letters and no lowercase ones, or
// a number: the shape of a shouted heading.
func isAllCaps(s string) bool {
	letters := 0
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= 'A' && r <= 'Z':
			letters++
		}
	}
	return letters >= 2 || (letters == 0 && s != "")
}

func startsUpperOrDigit(s string) bool {
	for _, r := range s {
		return (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	return false
}

// collectionRange reads "Books 1 through 3", "Books one to three", "Books 13,
// 14, and 15" and "Volumes 1 and 2" out of an announcement. It returns the
// first and last book number and true only for a rising run of two or more
// numbers: a lone "Book 13, Echoes of Divinity" is a position, not a range.
func collectionRange(s string) (from, to int, ok bool) {
	for _, loc := range collectionKeywordRe.FindAllStringIndex(s, -1) {
		nums := scanNumberList(s[loc[1]:])
		if len(nums) < 2 {
			continue
		}
		first, last := nums[0], nums[len(nums)-1]
		if last > first {
			return first, last, true
		}
	}
	return 0, 0, false
}

var (
	collectionKeywordRe = regexp.MustCompile(`(?i)\b(?:books|volumes)\s+`)
	listSepRe           = regexp.MustCompile(`(?i)^(?:\s*,\s*(?:and\s+)?|\s+(?:through|thru|to|and)\s+|\s*[-–]\s*)`)
)

// scanNumberList reads numbers joined by commas, "and", "to", "through", "thru"
// or a dash, stopping at the first thing that is not one. Roman numerals are not
// read here: the list form is a spoken title, and "Books I, II" is not heard.
func scanNumberList(s string) []int {
	var out []int
	for {
		n, used, ok := scanNumber(s)
		if !ok || (used > 0 && isRomanToken(s[:used])) {
			return out
		}
		out = append(out, n)
		s = s[used:]
		sep := listSepRe.FindString(s)
		if sep == "" {
			return out
		}
		s = s[len(sep):]
	}
}

func isRomanToken(tok string) bool {
	tok = strings.TrimSpace(tok)
	return tok != "" && strings.Trim(tok, "IVXLC") == ""
}
