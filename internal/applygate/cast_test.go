// file: internal/applygate/cast_test.go
// version: 1.2.0
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
			name: "self-read (a): solo author reads their own book, no stored author",
			book: database.Book{Title: "A Promised Land", Narrator: strp("Barack Obama")},
			cand: metafetch.MetadataCandidate{Title: "A Promised Land", Author: "Barack Obama"},
		},
		{
			name: "self-read (b): stored author, narrator and candidate are the same person",
			book: database.Book{Title: "A Promised Land", Author: &database.Author{Name: "Barack Obama"}, Narrator: strp("Barack Obama")},
			cand: metafetch.MetadataCandidate{Title: "A Promised Land", Author: "Barack Obama"},
		},
		{
			name: "self-read (c): candidate names its own narrator, so the check steps aside",
			book: database.Book{Title: "A Promised Land", Narrator: strp("Barack Obama")},
			cand: metafetch.MetadataCandidate{Title: "A Promised Land", Author: "Barack Obama, Second Author", Narrator: "Barack Obama"},
		},
		{
			// Known false positive, kept on purpose (owner decision on F1): a
			// co-written book read by its FIRST author, with no stored author
			// and no candidate narrator, looks exactly like a Big Finish drama
			// credited to its writer plus the cast. A block only sends the row
			// to manual review; nothing is written.
			name: "self-read (d): co-written memoir read by its first author blocks",
			book: database.Book{Title: "Becoming Kareem", Narrator: strp("Kareem Abdul-Jabbar")},
			cand: metafetch.MetadataCandidate{Title: "Becoming Kareem", Author: "Kareem Abdul-Jabbar, Raymond Obstfeld"},
			want: ReasonCastInAuthor,
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

// TestCheckEvidence_SelfReadIsNotCast runs self-read case (a) through the
// whole evidence leg: cast_in_author must stay neutral and must not be the
// reason for any refusal.
func TestCheckEvidence_SelfReadIsNotCast(t *testing.T) {
	book := database.Book{Title: "A Promised Land", Duration: intp(104400), Narrator: strp("Barack Obama"),
		FilePath: "/lib/Barack Obama/A Promised Land/A Promised Land.m4b"}
	cand := metafetch.MetadataCandidate{Title: "A Promised Land", Author: "Barack Obama", DurationSec: 104400}
	v := CheckEvidence(&book, &cand, false)
	if v.Reason == ReasonCastInAuthor {
		t.Fatalf("self-read blocked as cast: %+v", v)
	}
	for _, ch := range v.Checks {
		if ch.Name == "cast_in_author" && ch.Outcome != OutcomeNeutral {
			t.Fatalf("cast_in_author = %+v, want neutral", ch)
		}
	}
	if !v.Pass {
		t.Fatalf("self-read with matching runtime, title and path did not pass: reason %q (%s)", v.Reason, v.Detail)
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
