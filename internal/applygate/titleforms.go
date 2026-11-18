// file: internal/applygate/titleforms.go
// version: 1.1.1
// guid: 5c1d7e93-2b48-4a0f-9e36-71d4b8a2c605
// last-edited: 2026-11-18

package applygate

import (
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Whole forms built from DECLARED fields only.
//
// A title is confirmed when a whole form of the candidate EQUALS a whole form
// of the book. Every form here is made of fields a side declares (its title,
// subtitle, series name, series position, author, narrator) joined in a fixed
// order and nothing else: no word is ever removed because another field
// mentions it. Equal declared forms say the two sides state the same thing in
// the same words, so they AGREE before any conflict test runs (checkTitle
// states the precedence).
//
// The book side offers D's forms (its title and subtitle, each also behind its
// series name; a side with a subtitle has no bare-title form). The candidate
// side offers those plus the agree-only forms below. What a candidate may say
// besides "title" and "title subtitle":
//
//   - a subtitle that is nothing but a closed edition phrase ("A Novel",
//     "Unabridged") or exactly its own declared series and declared position in
//     one of the closed spellings ("Series, Book 5", "Book 5 of Series") is not
//     a subtitle: the candidate also has the forms without it. A series name
//     WITHOUT the declared position is never dropped, and neither is a
//     statement on a self-titled candidate (title = series name).
//   - for a candidate with NO real subtitle (none, or only a closed
//     non-subtitle): the series name, the position (bare or behind a closed
//     marker) and the title in the other usual orders, "S N T" and "T S N";
//     and the title beside a declared author or narrator, "T X", "X T",
//     "S N T X" and "X S N T" (X is the credit). On a SELF-TITLED candidate
//     (title = series name) the bare forms and the credit forms "T X", "X T"
//     say no volume, so checkTitle makes them NEUTRAL, not AGREE, and a file
//     name never agrees through them either; only the forms that state the
//     position ("S N T", "T S N", "S N T X", "X S N T") agree.
//   - for a candidate WITH a real subtitle U: only the forms that say U,
//     "S N T U" and "U N T" (N is the position, bare or behind a closed marker).
//     See "A SUBTITLE IS NEVER LEFT OUT" below.
//
// ROLES. A form records which declared fields it is built from where that
// matters (titleForm). Equal text is not equal meaning: a candidate's title
// "Owls" and narrator "Dark Hush" spell "owls dark hush", which is also a book's
// series "Owls Dark" in front of its title "Hush". A form with a credit in it
// never equals a form with a series in it (titleForm.matches). The book's own
// title and subtitle are free text and may equal any form, which is the point of
// the credit and position forms.
//
// A SUBTITLE IS NEVER LEFT OUT. A candidate that carries a real subtitle offers
// only the forms that say it. The forms that omit the subtitle ("S N T",
// "T S N", "T X", "X T", "S N T X", "X S N T") are offered only when the
// candidate has none, once the closed non-subtitles (an edition phrase, its own series
// statement) are dropped. Without this, words added to a candidate as a subtitle
// could still reach AGREE through a form that leaves them out.

// positionMarkers are the closed spellings of a volume marker in front of a
// position. "part" is not one of them: a part number is a piece of one book,
// not a position in a series.
var positionMarkers = []string{"book", "books", "volume", "vol"}

// titleSide is what a candidate declares about its title.
type titleSide struct {
	title, subtitle, series, position, author, narrator string
}

func candidateTitleSide(c *metafetch.MetadataCandidate) titleSide {
	return titleSide{c.Title, c.Subtitle, c.Series, c.SeriesPosition, c.Author, c.Narrator}
}

func joinToks(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// editionOnly reports whether a subtitle says nothing but closed edition
// phrases.
func editionOnly(s string) bool {
	any := false
	for _, part := range splitSegments(s) {
		if len(segTokens(part, false)) == 0 {
			continue
		}
		any = true
		if len(segTokens(part, true)) > 0 {
			return false
		}
	}
	return any
}

// positionToks is the declared position as tokens, nil when there is none.
func (s titleSide) positionToks() []string {
	if strings.TrimSpace(s.position) == "" {
		return nil
	}
	return formTokens(s.position)
}

// selfTitled reports whether the title is the series name: the bare title then
// names the series, not a volume.
func (s titleSide) selfTitled() bool {
	S := formTokens(s.series)
	return len(S) > 0 && slices.Equal(formTokens(s.title), S)
}

// seriesStatement reports whether sub is exactly the declared series and
// declared position in a closed spelling. The series name alone never is.
func (s titleSide) seriesStatement(sub []string) bool {
	S, N := formTokens(s.series), s.positionToks()
	if len(S) == 0 || len(N) == 0 || s.selfTitled() {
		return false
	}
	for _, name := range [][]string{S, joinToks(S, []string{"series"})} {
		if slices.Equal(sub, joinToks(name, N)) {
			return true
		}
		for _, m := range positionMarkers {
			if slices.Equal(sub, joinToks(name, []string{m}, N)) ||
				slices.Equal(sub, joinToks([]string{m}, N, []string{"of"}, name)) ||
				slices.Equal(sub, joinToks([]string{m}, N, []string{"of", "the"}, name)) {
				return true
			}
		}
	}
	return false
}

// effectiveSubtitle is the subtitle the side says once the closed non-subtitles
// are dropped ("" when none is left).
func (s titleSide) effectiveSubtitle() string {
	if strings.TrimSpace(s.subtitle) == "" {
		return ""
	}
	if editionOnly(s.subtitle) || s.seriesStatement(formTokens(s.subtitle)) {
		return ""
	}
	return s.subtitle
}

// titleForm is one whole form of a side: its tokens joined, and which declared
// fields built it where a match must know (see ROLES above).
type titleForm struct {
	text   string
	series bool // the side's series name is part of it
	credit bool // the side's author or narrator is part of it
}

// matches reports whether two forms agree: the same text, and no credit on one
// side standing for a series on the other.
func (a titleForm) matches(b titleForm) bool {
	return a.text == b.text && !(a.credit && b.series) && !(a.series && b.credit)
}

// firstMatch returns the first form of cand that matches a form of book, and
// the book form it matched.
func firstMatch(cand, book []titleForm) (cf, bf titleForm, ok bool) {
	for _, cf = range cand {
		for _, bf = range book {
			if cf.matches(bf) {
				return cf, bf, true
			}
		}
	}
	return titleForm{}, titleForm{}, false
}

// formSet builds the forms of one side in order, each once.
type formSet struct{ forms []titleForm }

func (fs *formSet) add(series, credit bool, parts ...[]string) {
	fs.push(titleForm{text: strings.Join(joinToks(parts...), " "), series: series, credit: credit})
}

func (fs *formSet) push(f titleForm) {
	if f.text != "" && !slices.Contains(fs.forms, f) {
		fs.forms = append(fs.forms, f)
	}
}

// titleForms are the whole forms of a side's declared title fields: the title,
// or the title and subtitle, each also behind the series name. A side with a
// subtitle has NO bare-title forms: the bare title is a part of what it says,
// not the whole of it.
func titleForms(title, subtitle, series string) []titleForm {
	t, sub, sr := formTokens(title), formTokens(subtitle), formTokens(series)
	if len(t) == 0 {
		return nil
	}
	var fs formSet
	if len(sub) == 0 {
		fs.add(false, false, t)
		if len(sr) > 0 {
			fs.add(true, false, sr, t)
		}
		return fs.forms
	}
	fs.add(false, false, t, sub)
	if len(sr) > 0 {
		fs.add(true, false, sr, t, sub)
	}
	return fs.forms
}

// forms returns every whole form of the candidate.
func (s titleSide) forms() []titleForm {
	T := formTokens(s.title)
	if len(T) == 0 {
		return nil
	}
	var fs formSet
	// The declared fields as they stand, then with the closed non-subtitles
	// dropped.
	fs.forms = titleForms(s.title, s.subtitle, s.series)
	sub := s.effectiveSubtitle()
	if sub != s.subtitle {
		for _, f := range titleForms(s.title, sub, s.series) {
			fs.push(f)
		}
	}
	U, S, N := formTokens(sub), formTokens(s.series), s.positionToks()
	var marked [][]string
	if len(N) > 0 {
		marked = append(marked, N)
		for _, m := range positionMarkers {
			marked = append(marked, joinToks([]string{m}, N))
		}
	}
	// The forms that say the subtitle.
	if len(U) > 0 {
		for _, mn := range marked {
			if len(S) > 0 {
				fs.add(true, false, S, mn, T, U)
			}
			fs.add(false, false, U, mn, T)
		}
		return fs.forms
	}
	// The forms that leave a subtitle out are for a candidate that has none.
	for _, mn := range marked {
		if len(S) > 0 {
			fs.add(true, false, S, mn, T)
			fs.add(true, false, T, S, mn)
		}
	}
	for _, name := range []string{s.author, s.narrator} {
		X := formTokens(name)
		// A credit that is the same words as the title or the series name is not
		// evidence of the credit form: the form would be a field stated twice.
		if len(X) == 0 || slices.Equal(X, T) || slices.Equal(X, S) {
			continue
		}
		fs.add(false, true, T, X)
		fs.add(false, true, X, T)
		for _, mn := range marked {
			if len(S) > 0 {
				fs.add(true, true, S, mn, T, X)
				fs.add(true, true, X, S, mn, T)
			}
		}
	}
	return fs.forms
}

// segmentKey is the title field's whole segments, sorted: two fields with the
// same key say the same segments in another order.
func segmentKey(title, subtitle string) string {
	var js []string
	for _, seg := range titleSegments(fieldText(title, subtitle)) {
		js = append(js, strings.Join(seg, " "))
	}
	slices.Sort(js)
	return strings.Join(js, "|")
}
