// file: internal/applygate/unattended.go
// version: 1.1.0
// guid: 9a4e6c28-1d73-4b05-8f2a-c5b7e0d19364
// last-edited: 2026-11-17

package applygate

import (
	"sort"
	"strconv"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// Two conditions the sequence and evidence legs cannot express on their own
// because they combine legs. Both only ever REFUSE; both are lifted by an
// owner review like any certainty refusal (OwnerReviewOverridable).
//
// The invariant behind both: a refusal is removed only where the remaining
// evidence positively supports the match. Where the only thing left is a
// possibly stale stored position or an unknown runtime, the gate keeps
// refusing.

// checkNamed returns the evidence check called name (the zero CheckResult,
// outcome "", when absent).
func checkNamed(ev EvidenceVerdict, name string) CheckResult {
	for _, ch := range ev.Checks {
		if ch.Name == name {
			return ch
		}
	}
	return CheckResult{Name: name}
}

// corroborationRefusal is "" when a sequence verdict that set a weak number
// aside against a stored number (SequenceVerdict.Corroborate) is confirmed,
// else why it is not. Confirmed means all of:
//
//   - the runtime check AGREES: the runtime is known and within
//     RuntimeAgreeRatio. Unknown is not confirmation;
//   - the title check AGREES (not merely neutral);
//   - the book's stored title is not just the series' own words: a title that
//     is the series name names no volume, so agreeing with it proves nothing
//     about which volume this is;
//   - every word of the stored title that is neither a series word, a stop
//     word nor the set-aside number appears in the candidate's title or
//     subtitle (titleWordsMissing). The general title check agrees on a shared
//     prefix ("Batman" of "Batman: Year Two" and "Batman: Year One"), which
//     two volumes of one storyline share; the words that tell them apart ("Two"
//     against "One") must be on both sides.
func corroborationRefusal(book *database.Book, c *metafetch.MetadataCandidate, seq SequenceVerdict, ev EvidenceVerdict) string {
	if rt := checkNamed(ev, "runtime"); rt.Outcome != OutcomeAgree {
		return "the runtime is " + outcomeWord(rt.Outcome) + ", not confirming (" + rt.Detail + ")"
	}
	if t := checkNamed(ev, "title"); t.Outcome != OutcomeAgree {
		return "the title check is " + outcomeWord(t.Outcome) + ", not agreeing (" + t.Detail + ")"
	}
	if titleIsSeriesWords(book, c) {
		return "the book's title " + quote(book.Title) + " is only the series' own words, so agreeing with it does not say which volume this is"
	}
	if missing := titleWordsMissing(book, c, seq); len(missing) > 0 {
		return "the book's title " + quote(book.Title) + " has words the candidate's title does not (" + strings.Join(missing, ", ") + "), so it may be another volume"
	}
	return ""
}

// titleWordsMissing is the words of the book's stored title that are not
// series words, stop words or a set-aside weak number, and that the candidate's
// title and subtitle do not carry.
func titleWordsMissing(book *database.Book, c *metafetch.MetadataCandidate, seq SequenceVerdict) []string {
	weakValues := map[float64]bool{}
	for _, s := range seq.BookNumbers {
		if s.Weak {
			if n, ok := seqnum.ParsePosition(s.Number); ok {
				weakValues[n.Value] = true
			}
		}
	}
	series := map[string]bool{}
	for _, name := range []string{c.Series, seriesName(book)} {
		for t := range tokens(name, true) {
			series[t] = true
		}
	}
	have := tokens(c.Title+" "+c.Subtitle, false)
	var missing []string
	for t := range tokens(book.Title, true) {
		if series[t] || have[t] {
			continue
		}
		if allDigits(t) {
			if f, err := strconv.ParseFloat(t, 64); err == nil && weakValues[f] {
				continue
			}
		}
		missing = append(missing, t)
	}
	sort.Strings(missing)
	return missing
}

func outcomeWord(o string) string {
	if o == "" {
		return "absent"
	}
	return o
}

// titleIsSeriesWords reports whether every non-numeric word of the book's
// stored title is a word of the book's series or of the candidate's series
// (or the title has no such word).
func titleIsSeriesWords(book *database.Book, c *metafetch.MetadataCandidate) bool {
	words := map[string]bool{}
	for t := range tokens(book.Title, true) {
		if !allDigits(t) {
			words[t] = true
		}
	}
	if len(words) == 0 {
		return true
	}
	for _, series := range []string{c.Series, seriesName(book)} {
		st := tokens(series, true)
		if len(st) == 0 {
			continue
		}
		inside := true
		for w := range words {
			if !st[w] {
				inside = false
				break
			}
		}
		if inside {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// collectedRuntimeRefusal is "" unless the book or the candidate is a
// collected edition and the runtime check did not AGREE. A collected
// edition's identity is a number and a few words, and two editions of one
// author share both, so the runtime is the only thing that tells them apart;
// an unknown or merely-neutral runtime is review, never an unattended apply.
func collectedRuntimeRefusal(book *database.Book, c *metafetch.MetadataCandidate, seq SequenceVerdict, ev EvidenceVerdict) string {
	side := collectedSide(book, c, seq)
	if side == "" {
		return ""
	}
	rt := checkNamed(ev, "runtime")
	if rt.Outcome == OutcomeAgree {
		return ""
	}
	return side + " is a collected edition and the runtime is " + outcomeWord(rt.Outcome) + ", not confirming (" + rt.Detail + ")"
}

// collectedSide names the side that is a collected edition: "the book", "the
// candidate", both, or "" for neither. Both sides use the same word-boundary
// reader as the penalty lift (metafetch.IsCollectedEdition: a stated range or
// collected-edition wording, never a substring), on the texts that are the
// edition's own:
//
//   - the book's stored title and stored series position (a range in a FOLDER
//     or file name labels the files, so a single volume inside a box-set
//     folder is not a collected edition by that name alone; the candidate's
//     side still catches the box set it would be matched to);
//   - the candidate's title, subtitle and series position.
func collectedSide(book *database.Book, c *metafetch.MetadataCandidate, seq SequenceVerdict) string {
	bookColl := metafetch.IsCollectedEdition(book.Title, "", "") || anyRange(ownFieldNumbers(seq.BookNumbers))
	candColl := metafetch.IsCollectedEdition(c.Title, c.Subtitle, c.SeriesPosition)
	switch {
	case bookColl && candColl:
		return "the book and the candidate are each"
	case bookColl:
		return "the book"
	case candColl:
		return "the candidate"
	}
	return ""
}

// ownFieldNumbers keeps the numbers read from the book's stored title and
// stored series position, dropping the ones read from file and folder names.
func ownFieldNumbers(src []SourceNumber) []SourceNumber {
	var out []SourceNumber
	for _, s := range src {
		if s.Source == "title" || s.Source == "series_position" {
			out = append(out, s)
		}
	}
	return out
}

func anyRange(src []SourceNumber) bool {
	for _, s := range src {
		if n, ok := seqnum.ParsePosition(s.Number); ok && n.IsRange() {
			return true
		}
	}
	return false
}
