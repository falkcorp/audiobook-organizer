// file: internal/applygate/unattended.go
// version: 1.0.0
// guid: 9a4e6c28-1d73-4b05-8f2a-c5b7e0d19364
// last-edited: 2026-11-16

package applygate

import (
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
// aside against the stored series position (SequenceVerdict.Corroborate) is
// confirmed, else why it is not. Confirmed means all of:
//
//   - the title check AGREES (not merely neutral), and
//   - the book's stored title is not just the series name: a title that is
//     the series' own words ("Year One" in the series "Year One") names no
//     volume, so agreeing with it proves nothing about which volume this is,
//   - the runtime check AGREES: the runtime is known and within
//     RuntimeAgreeRatio. Unknown is not confirmation: an apply that
//     overwrites nothing never needed one, which is exactly when a wrong
//     volume slips through.
func corroborationRefusal(book *database.Book, c *metafetch.MetadataCandidate, ev EvidenceVerdict) string {
	if rt := checkNamed(ev, "runtime"); rt.Outcome != OutcomeAgree {
		return "the runtime is " + outcomeWord(rt.Outcome) + ", not confirming (" + rt.Detail + ")"
	}
	if t := checkNamed(ev, "title"); t.Outcome != OutcomeAgree {
		return "the title check is " + outcomeWord(t.Outcome) + ", not agreeing (" + t.Detail + ")"
	}
	if titleIsSeriesWords(book, c) {
		return "the book's title " + quote(book.Title) + " is only the series' own words, so agreeing with it does not say which volume this is"
	}
	return ""
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
// candidate", both, or "" for neither. It reads every number the sequence leg
// read (a range anywhere on the book or the candidate), the shared
// collected-edition reader (metafetch.IsCollectedEdition) and the partial_book
// span reader (spanOf), so no spelling one of them knows slips past.
func collectedSide(book *database.Book, c *metafetch.MetadataCandidate, seq SequenceVerdict) string {
	bookColl := metafetch.IsCollectedEdition(book.Title, "", "") || spanOf(bookSide(book.Title, book.FilePath)) != "" || anyRange(seq.BookNumbers)
	candColl := metafetch.IsCollectedEdition(c.Title, c.Subtitle, c.SeriesPosition) || spanOf(c.Title+" "+c.Subtitle) != "" || anyRange(seq.CandidateNumbers)
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

func anyRange(src []SourceNumber) bool {
	for _, s := range src {
		if n, ok := seqnum.ParsePosition(s.Number); ok && n.IsRange() {
			return true
		}
	}
	return false
}
