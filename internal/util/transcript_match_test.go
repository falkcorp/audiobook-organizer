// file: internal/util/transcript_match_test.go
// version: 1.0.0
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
// refused as transcription_mismatch.
var realReviewCases = []struct {
	name                    string
	candTitle, candAuthor   string
	heardTitle, heardAuthor string
}{
	{"Blood of Elves", "Blood of Elves", "Andrzej Sapkowski", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish"},
	{"A Cry of Honor", "A Cry of Honor (Book #4 in the Sorcerer's Ring)", "Morgan Rice", "A Cry of Honor", "Morgan Rice"},
	{"Witness to a Trial", "Witness to a Trial", "John Grisham", "Witness to a Trial A short story prequel to The Whistler", "John Grisham"},
	{"Knaves Over Queens", "Knaves Over Queens", "George R. R. Martin", "Naves Over Queens", "George R. R. Martin, assisted"},
	{"Sojourn", "Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Salvator"},
	{"This Gilded Abyss", "This Gilded Abyss", "Rebecca Thorne", "This Gilded Abyss, book one of the Gilded Abyss trilogy", "Rebecca Thorne"},
	{"Mistborn", "Mistborn", "Brandon Sanderson", "Mistborn", "Brandon Sanderson For Beth Sanderson, who's"},
}

func TestTranscriptMatch_RealReviewCasesConfirm(t *testing.T) {
	for _, tc := range realReviewCases {
		t.Run(tc.name, func(t *testing.T) {
			if !TitleAgrees(tc.candTitle, tc.heardTitle) {
				t.Errorf("TitleAgrees(%q, %q) = false", tc.candTitle, tc.heardTitle)
			}
			if !AuthorAgrees(tc.candAuthor, tc.heardAuthor, "") {
				t.Errorf("AuthorAgrees(%q, %q) = false", tc.candAuthor, tc.heardAuthor)
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

func TestTitleAgrees(t *testing.T) {
	cases := []struct {
		cand, heard string
		want        bool
	}{
		{"The Way of Kings", "The Way of Kings", true},
		{"the way of kings", "The Way of Kings", true},
		{"The Way of Kings (Stormlight 1)", "The Way of Kings", true},
		{"Blood of Elves (Unabridged)", "Blood of Elves", true},
		{"It", "It", true},
		// Must refuse: same author, different title.
		{"The Well of Ascension", "Mistborn", false},
		{"Project Hail Mary", "The Martian", false},
		// Must refuse: a different volume of the same series.
		{"Big Cats 3", "Big Cats 1", false},
		{"The Well of Ascension", "The Final Empire", false},
		// The containment guard: a one-word short title does not match a
		// long transcript just because the word occurs in it.
		{"It", "It was a dark and stormy night in the city", false},
		// Word boundaries: "Elves" is not inside "Twelves".
		{"Elves", "Twelves of the night", false},
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
		cand, heard, intro string
		want               bool
	}{
		{"Brandon Sanderson", "Sanderson", "", true},
		{"Andy Weir", "Ki", "", true}, // too short to judge
		{"Andy Weir", "", "", true},
		{"Douglas Preston & Lincoln Child", "Lincoln Child", "", true},
		{"Terry Pratchett and Neil Gaiman", "Neil Gayman", "", true},
		{"Martin Luther King Jr.", "Martin Luther King", "", true},
		// Must refuse: a completely different author.
		{"Andy Weir", "Brandon Sanderson", "", false},
		{"John Grisham", "Morgan Rice", "", false},
		// The intro transcript rescues an author Whisper mis-parsed.
		{"Andy Weir", "Written and read", "Project Hail Mary, by Andy Weir. Read by Ray Porter.", true},
		{"Andy Weir", "Written and read", "Mistborn, by Brandon Sanderson.", false},
	}
	for _, tc := range cases {
		if got := AuthorAgrees(tc.cand, tc.heard, tc.intro); got != tc.want {
			t.Errorf("AuthorAgrees(%q, %q, %q) = %v, want %v", tc.cand, tc.heard, tc.intro, got, tc.want)
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
