// file: internal/util/transcript_match_test.go
// version: 2.0.0
// guid: 0b6d3e92-4f18-4a7c-8e51-c2a7f9d0b364
// last-edited: 2026-09-13

package util

import (
	"strings"
	"testing"
)

// realReviewCases are seven books from the production review lane on
// 2026-09-13, verbatim: candidate title/author and Whisper's transcribed
// title/author. The owner clicked Apply on all of them and every one was
// refused as transcription_mismatch. confirms is what the STRICT matcher
// says: six confirm; "A Cry of Honor" does not, because the candidate's own
// title carries "(Book #4 in the Sorcerer's Ring)" and the matcher strips
// trailers from the transcribed side only. That book is applied by an owner
// clicking Apply on its review row.
var realReviewCases = []struct {
	name                    string
	candTitle, candAuthor   string
	heardTitle, heardAuthor string
	confirms                bool
}{
	{"Blood of Elves", "Blood of Elves", "Andrzej Sapkowski", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish", true},
	{"A Cry of Honor", "A Cry of Honor (Book #4 in the Sorcerer's Ring)", "Morgan Rice", "A Cry of Honor", "Morgan Rice", false},
	{"Witness to a Trial", "Witness to a Trial", "John Grisham", "Witness to a Trial A short story prequel to The Whistler", "John Grisham", true},
	{"Knaves Over Queens", "Knaves Over Queens", "George R. R. Martin", "Naves Over Queens", "George R. R. Martin, assisted", true},
	{"Sojourn", "Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Salvator", true},
	{"This Gilded Abyss", "This Gilded Abyss", "Rebecca Thorne", "This Gilded Abyss, book one of the Gilded Abyss trilogy", "Rebecca Thorne", true},
	{"Mistborn", "Mistborn", "Brandon Sanderson", "Mistborn", "Brandon Sanderson For Beth Sanderson, who's", true},
}

func TestTranscriptMatch_RealReviewCases(t *testing.T) {
	for _, tc := range realReviewCases {
		t.Run(tc.name, func(t *testing.T) {
			got := TitleAgrees(tc.candTitle, tc.heardTitle) && AuthorAgrees(tc.candAuthor, tc.heardAuthor)
			if got != tc.confirms {
				t.Errorf("confirms = %v, want %v (title %v, author %v)", got, tc.confirms,
					TitleAgrees(tc.candTitle, tc.heardTitle), AuthorAgrees(tc.candAuthor, tc.heardAuthor))
			}
		})
	}
}

// oldTranscriptionRule is applygate.TranscriptionConfirms as it stood on
// origin/main before this change (f071a1b05), kept here as the fail-before
// evidence: exact lower-cased title equality, then the transcribed author
// had to be a substring of the candidate author.
func oldTranscriptionRule(candTitle, candAuthor, heardTitle, heardAuthor string) bool {
	if NormalizeTitle(candTitle) != NormalizeTitle(heardTitle) {
		return false
	}
	if len(heardAuthor) <= 3 {
		return true
	}
	return strings.Contains(NormalizeAuthor(candAuthor), NormalizeAuthor(heardAuthor))
}

func TestTranscriptMatch_OldRuleRefusedAllSeven(t *testing.T) {
	for _, tc := range realReviewCases {
		if oldTranscriptionRule(tc.candTitle, tc.candAuthor, tc.heardTitle, tc.heardAuthor) {
			t.Errorf("%s: the old rule confirmed it; this case does not document the bug", tc.name)
		}
	}
}

// reviewerFalsePositives are the pairs the adversarial review of the first
// (containment + intro) matcher measured as wrongly confirmed. Every one must
// refuse, in both title orders.
var reviewerFalsePositiveTitles = [][2]string{
	{"Mistborn: The Hero of Ages", "Mistborn"},
	{"Foundation", "Foundation and Empire"},
	{"The Witches", "The Witcher"},
}

func TestTitleAgrees_ReviewerFalsePositivesRefuse(t *testing.T) {
	for _, p := range reviewerFalsePositiveTitles {
		if TitleAgrees(p[0], p[1]) || TitleAgrees(p[1], p[0]) {
			t.Errorf("TitleAgrees(%q, %q) confirmed in some order", p[0], p[1])
		}
	}
}

func TestTitleAgrees(t *testing.T) {
	cases := []struct {
		cand, heard string
		want        bool
	}{
		{"The Way of Kings", "The Way of Kings", true},
		{"the way of kings", "The Way of Kings", true},
		{"Blood of Elves (Unabridged)", "Blood of Elves", true},
		{"It", "It", true},
		// Number words are numbers.
		{"Ready Player One", "Ready Player 1", true},
		{"Big Cats 1", "Big Cats, book three", false},
		// Trailers are stripped from the transcribed side only.
		{"Dune", "Dune, book one of the Dune Chronicles", true},
		{"The Way of Kings (Stormlight 1)", "The Way of Kings", false},
		// No containment in either direction.
		{"The Way of Kings", "The Way of Kings and other stories", false},
		{"It", "It was a dark and stormy night in the city", false},
		// Must refuse: same author, different title / volume.
		{"The Well of Ascension", "Mistborn", false},
		{"Project Hail Mary", "The Martian", false},
		{"Big Cats 3", "Big Cats 1", false},
		// Fuzz: silent first letter, and one inner edit in 7+ letter words
		// with the same first and last letter; only in 3+ word titles.
		{"Knaves Over Queens", "Naves Over Queens", true},
		{"The Knight", "The Night", false},
		{"Children of Dune Messiah", "Chilren of Dune Messiah", true},
		{"The Sandersonian Way Home", "The Sandersinian Way Home", true},
		{"The Dragon Kingdom Falls", "The Dragon Kingdoms Falls", false},
		{"The Witches of Karres", "The Witcher of Karres", false},
		{"Knaves Over Kweens", "Naves Over Queens", false}, // two fuzzy words
		{"", "Mistborn", false},
		{"Mistborn", "", false},
	}
	for _, tc := range cases {
		if got := TitleAgrees(tc.cand, tc.heard); got != tc.want {
			t.Errorf("TitleAgrees(%q, %q) = %v, want %v", tc.cand, tc.heard, got, tc.want)
		}
	}
}

func TestAuthorAgrees(t *testing.T) {
	cases := []struct {
		cand, heard string
		want        bool
	}{
		{"Brandon Sanderson", "Sanderson", true},
		{"Brandon Sanderson", "Written by Brandon Sanderson", true},
		{"Andy Weir", "Ki", true}, // too short to judge
		{"Andy Weir", "", true},
		{"Douglas Preston & Lincoln Child", "Lincoln Child", true},
		{"Terry Pratchett and Neil Gaiman", "Neil Gayman", true},
		{"Martin Luther King Jr.", "Martin Luther King", true},
		{"R. A. Salvatore", "Robert Salvatore", true}, // initial vs name
		// Must refuse: a different author.
		{"Andy Weir", "Brandon Sanderson", false},
		{"John Grisham", "Morgan Rice", false},
		// The reviewer's pairs: same surname, different given name; a
		// surname that is really a first name; no typo slack under 6 letters.
		{"Stephen King", "Owen King", false},
		{"James Patterson", "Patterson Joseph", false},
		{"Stephen King", "Stephen Kind", false},
		{"Andy Weir", "Andy Weil", false},
		// The intro transcript is never consulted.
		{"Andy Weir", "Written and read", false},
	}
	for _, tc := range cases {
		if got := AuthorAgrees(tc.cand, tc.heard); got != tc.want {
			t.Errorf("AuthorAgrees(%q, %q) = %v, want %v", tc.cand, tc.heard, got, tc.want)
		}
	}
}

func TestWithinOneEdit(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"knaves", "naves", true},
		{"salvatore", "salvator", true},
		{"gaiman", "gayman", true},
		{"sanderson", "sanderson", true},
		{"sanderson", "anderso", false},
		{"martin", "marvel", false},
	}
	for _, tc := range cases {
		if got := withinOneEdit([]rune(tc.a), []rune(tc.b)); got != tc.want {
			t.Errorf("withinOneEdit(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
