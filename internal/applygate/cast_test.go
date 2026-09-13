// file: internal/applygate/cast_test.go
// version: 1.1.0
// guid: 3f9b6d20-8e1c-4a75-b2d4-6c0e9a7f1d58
// last-edited: 2026-09-13

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// TestCheckCastInAuthor uses rows from the 2026-09-13 prod preview that passed
// the gate with a full-cast list as the author, plus near misses that must not
// block: real co-authors, a narrator tag copied from the author, and a
// provider that names its own narrator.
func TestCheckCastInAuthor(t *testing.T) {
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
		want string // "" = not blocked by this check
	}{
		{
			name: "Old Soldiers: writer tagged as narrator, cast appended to the author",
			book: database.Book{Title: "Old Soldiers", Narrator: strp("James Swallow"),
				FilePath: "/lib/Doctor Who - BF Companion Chronicles/Old Soldiers/Old Soldiers.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Old Soldiers", Author: "James Swallow, Nicholas Courtney, Toby Longworth"},
			want: ReasonCastInAuthor,
		},
		{
			name: "The Glorious Revolution: two-name cast credit",
			book: database.Book{Title: "The Glorious Revolution", Narrator: strp("Jonathan Morris")},
			cand: metafetch.MetadataCandidate{Title: "The Glorious Revolution", Author: "Jonathan Morris, Frazer Hines"},
			want: ReasonCastInAuthor,
		},
		{
			// Position in a credit list is not evidence of performing (review
			// F3); without a cast credit in the files this row is not caught.
			name: "The War Master: stored author is a non-first name, no cast credit",
			book: database.Book{Title: "The War Master", Author: &database.Author{Name: "Derek Jacobi"},
				Narrator: strp("Big Finish Productions")},
			cand: metafetch.MetadataCandidate{Title: "The War Master", Author: "James Goss, Guy Adams, Derek Jacobi, Rob Harvey"},
		},
		{
			name: "F1: co-author reads the book, stored author empty",
			book: database.Book{Title: "Good Omens", Narrator: strp("Neil Gaiman")},
			cand: metafetch.MetadataCandidate{Title: "Good Omens", Author: "Terry Pratchett, Neil Gaiman"},
		},
		{
			name: "F2: narrator shares only a surname with the first author",
			book: database.Book{Title: "The Talisman", Narrator: strp("Owen King")},
			cand: metafetch.MetadataCandidate{Title: "The Talisman", Author: "Stephen King, Peter Straub"},
		},
		{
			name: "F3: real co-author listed third",
			book: database.Book{Title: "Hellhole", Author: &database.Author{Name: "Kevin J. Anderson"}},
			cand: metafetch.MetadataCandidate{Title: "Hellhole", Author: "Frank Herbert, Brian Herbert, Kevin J. Anderson"},
		},
		{
			name: "narrator repeats the first author in full-name form",
			book: database.Book{Title: "Frostfire", Narrator: strp("Marc Platt")},
			cand: metafetch.MetadataCandidate{Title: "Frostfire", Author: "Marc Platt, Maureen O'Brien"},
			want: ReasonCastInAuthor,
		},
		{
			name: "real co-authors, narrator is someone else",
			book: database.Book{Title: "Good Omens", Narrator: strp("Martin Jarvis")},
			cand: metafetch.MetadataCandidate{Title: "Good Omens", Author: "Terry Pratchett, Neil Gaiman"},
		},
		{
			name: "co-authors stored as the author, first one listed first",
			book: database.Book{Title: "Good Omens", Author: &database.Author{Name: "Terry Pratchett"}},
			cand: metafetch.MetadataCandidate{Title: "Good Omens", Author: "Terry Pratchett, Neil Gaiman, Someone Third"},
		},
		{
			name: "narrator tag copied from the author credit",
			book: database.Book{Title: "Tin Man", Author: &database.Author{Name: "Jason Anspach, Nick Cole"},
				Narrator: strp("Jason Anspach, Nick Cole")},
			cand: metafetch.MetadataCandidate{Title: "Tin Man", Author: "Jason Anspach, Nick Cole"},
		},
		{
			name: "provider names its own narrator",
			book: database.Book{Title: "Fully Loaded Thrillers", Narrator: strp("Blake Crouch")},
			cand: metafetch.MetadataCandidate{Title: "Fully Loaded Thrillers", Author: "Luke Daniels, Blake Crouch", Narrator: "Eric G. Dove"},
		},
		{
			name: "single author who narrates the book",
			book: database.Book{Title: "The Graveyard Book", Narrator: strp("Neil Gaiman")},
			cand: metafetch.MetadataCandidate{Title: "The Graveyard Book", Author: "Neil Gaiman"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := checkCastInAuthor(&nameSource{author: bookAuthor(&c.book), narrator: bookNarrator(&c.book)}, c.cand.Author, c.cand.Narrator)
			if r.Outcome == OutcomeAgree {
				t.Fatalf("cast_in_author must never agree: %+v", r)
			}
			if r.Reason != c.want {
				t.Fatalf("reason %q (%s), want %q", r.Reason, r.Detail, c.want)
			}
		})
	}
}

// TestCheckEvidence_CastInAuthorBlocks confirms the check is wired into the
// evidence leg, on a row every other check agrees with.
func TestCheckEvidence_CastInAuthorBlocks(t *testing.T) {
	book := database.Book{Title: "Old Soldiers", Duration: intp(4789), Narrator: strp("James Swallow"),
		FilePath: "/lib/James Swallow/Old Soldiers/Old Soldiers.m4b"}
	cand := metafetch.MetadataCandidate{Title: "Old Soldiers", Author: "James Swallow, Nicholas Courtney, Toby Longworth", DurationSec: 4789}
	v := CheckEvidence(&book, &cand, false)
	if v.Pass || v.Reason != ReasonCastInAuthor {
		t.Fatalf("pass=%v reason=%q (%s), want %q", v.Pass, v.Reason, v.Detail, ReasonCastInAuthor)
	}
}
