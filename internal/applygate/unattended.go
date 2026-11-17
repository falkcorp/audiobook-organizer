// file: internal/applygate/unattended.go
// version: 1.2.0
// guid: 9a4e6c28-1d73-4b05-8f2a-c5b7e0d19364
// last-edited: 2026-11-17

package applygate

import (
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// A condition the evidence legs cannot express on their own because it
// combines legs: a collected edition (a box set, an omnibus, "Books 1-3") on
// either side applies unattended only when the runtime check AGREES. It only
// ever REFUSES, and an owner review lifts it like any certainty refusal
// (OwnerReviewOverridable). The other combined reason, sequence_unconfirmed,
// is raised by the sequence leg itself (SequenceVerdict.WeakSetAside).
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

func outcomeWord(o string) string {
	if o == "" {
		return "absent"
	}
	return o
}

// collectedRuntimeRefusal is "" unless the book or the candidate is a
// collected edition and the runtime check did not AGREE. A collected
// edition's identity is a number and a few words, and two editions of one
// author share both, so the runtime is the only thing that tells them apart;
// an unknown or merely-neutral runtime is review, never an unattended apply.
// When both sides are collected editions worded differently ("Books 1-3" and
// "Omnibus: Books 1-3") the detail says so: an owner who overrides must see
// that the two may be different editions.
func collectedRuntimeRefusal(book *database.Book, c *metafetch.MetadataCandidate, seq SequenceVerdict, ev EvidenceVerdict) string {
	if book == nil || c == nil {
		return ""
	}
	side := collectedSide(book, c, seq)
	if side == "" {
		return ""
	}
	rt := checkNamed(ev, "runtime")
	if rt.Outcome == OutcomeAgree {
		return ""
	}
	detail := side + " is a collected edition and the runtime is " + outcomeWord(rt.Outcome) + ", not confirming (" + rt.Detail + ")"
	if side == collectedBoth {
		bw, _ := spanParts(book.Title)
		cw, _ := spanParts(c.Title + " " + c.Subtitle)
		if bw != cw {
			detail += "; the edition wording differs (the book: " + wordingOrNone(bw) + ", the candidate: " + wordingOrNone(cw) +
				"), so the two may be different editions of the same range"
		}
	}
	return detail
}

func wordingOrNone(w string) string {
	if w == "" {
		return "none"
	}
	return quote(w)
}

// collectedBoth is collectedSide's answer when both sides are collected.
const collectedBoth = "the book and the candidate are each"

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
		return collectedBoth
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
