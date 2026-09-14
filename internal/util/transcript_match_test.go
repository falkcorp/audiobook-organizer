// file: internal/util/transcript_match_test.go
// version: 2.2.1
// guid: 0b6d3e92-4f18-4a7c-8e51-c2a7f9d0b364
// last-edited: 2026-09-14

package util

import (
	"strings"
	"testing"
)

// realReviewCases are seven books from the production review lane on
// 2026-09-13, verbatim: candidate title/author and Whisper's transcribed
// title/author. The owner clicked Apply on all of them and every one was
// refused as transcription_mismatch. confirms is what the shared matcher
// alone says (the annotation on an owner-reviewed row apply): five confirm.
// "A Cry of Honor" does not, because the candidate's own title carries
// "(Book #4 in the Sorcerer's Ring)" and trailers are stripped from the
// transcribed side only; "This Gilded Abyss" does not, because the heard
// "book one" has no series position to agree with. No unreviewed path
// confirms any of them except Sojourn: they AND MainTranscriptionConfirms,
// which is origin/main's rule plus the owner's 2026-09-14 initials fold.
// mainConfirms is that rule's answer. Sojourn was refused only because the
// heard "R.A." was spaced differently from the provider's "R. A."; the owner
// decided initials spacing and punctuation are equal, so it now confirms.
// The other six stay refused and are applied by an owner clicking Apply.
var realReviewCases = []struct {
	name                    string
	candTitle, candAuthor   string
	heardTitle, heardAuthor string
	confirms                bool
	mainConfirms            bool
}{
	{"Blood of Elves", "Blood of Elves", "Andrzej Sapkowski", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish", true, false},
	{"A Cry of Honor", "A Cry of Honor (Book #4 in the Sorcerer's Ring)", "Morgan Rice", "A Cry of Honor", "Morgan Rice", false, false},
	{"Witness to a Trial", "Witness to a Trial", "John Grisham", "Witness to a Trial A short story prequel to The Whistler", "John Grisham", true, false},
	{"Knaves Over Queens", "Knaves Over Queens", "George R. R. Martin", "Naves Over Queens", "George R. R. Martin, assisted", true, false},
	// Owner decision 2026-09-14: mainConfirms was false (initials spacing).
	{"Sojourn", "Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Salvator", true, true},
	// Heard "book one" but the review row carried no series position, so
	// nothing says this record is volume 1 (TestTitleAgrees_HeardVolume).
	{"This Gilded Abyss", "This Gilded Abyss", "Rebecca Thorne", "This Gilded Abyss, book one of the Gilded Abyss trilogy", "Rebecca Thorne", false, false},
	{"Mistborn", "Mistborn", "Brandon Sanderson", "Mistborn", "Brandon Sanderson For Beth Sanderson, who's", true, false},
}

func TestTranscriptMatch_RealReviewCases(t *testing.T) {
	for _, tc := range realReviewCases {
		t.Run(tc.name, func(t *testing.T) {
			got := TitleAgrees(tc.candTitle, "", tc.heardTitle) && AuthorAgrees(tc.candAuthor, tc.heardAuthor)
			if got != tc.confirms {
				t.Errorf("confirms = %v, want %v (title %v, author %v)", got, tc.confirms,
					TitleAgrees(tc.candTitle, "", tc.heardTitle), AuthorAgrees(tc.candAuthor, tc.heardAuthor))
			}
			// Unreviewed paths AND MainTranscriptionConfirms.
			if m := MainTranscriptionConfirms(tc.candTitle, tc.candAuthor, tc.heardTitle, tc.heardAuthor); m != tc.mainConfirms {
				t.Errorf("MainTranscriptionConfirms = %v, want %v", m, tc.mainConfirms)
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
		if TitleAgrees(p[0], "", p[1]) || TitleAgrees(p[1], "", p[0]) {
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
		// ...but a heard volume number needs a candidate position (below).
		{"Dune", "Dune, book one of the Dune Chronicles", false},
		{"Witness to a Trial", "Witness to a Trial A short story prequel to The Whistler", true},
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
		if got := TitleAgrees(tc.cand, "", tc.heard); got != tc.want {
			t.Errorf("TitleAgrees(%q, %q) = %v, want %v", tc.cand, tc.heard, got, tc.want)
		}
	}
}

// A stripped trailer's volume number is identity: "Dune, book two of the
// Dune Chronicles" is Dune Messiah. It must equal the candidate's series
// position, and a candidate with no position does not agree.
func TestTitleAgrees_HeardVolume(t *testing.T) {
	cases := []struct {
		cand, pos, heard string
		want             bool
	}{
		{"Dune", "", "Dune, book two of the Dune Chronicles", false},
		{"Dune", "1", "Dune, book two of the Dune Chronicles", false},
		{"Dune", "2", "Dune, book two of the Dune Chronicles", true},
		{"Dune", "Book Two", "Dune, book two of the Dune Chronicles", true},
		{"Harry Potter", "", "Harry Potter book 2", false},
		{"Harry Potter", "1", "Harry Potter book 2", false},
		{"Harry Potter", "02", "Harry Potter book 2", true},
		{"The Expanse", "", "The Expanse, volume 3", false},
		{"The Expanse", "3.0", "The Expanse, volume 3", true},
		{"The Expanse", "#3", "The Expanse, volume 3", true},
		{"The Expanse", "2.5", "The Expanse, volume 2", false},
		{"This Gilded Abyss", "1", "This Gilded Abyss, book one of the Gilded Abyss trilogy", true},
		// A trailer with no number needs no position.
		{"Witness to a Trial", "", "Witness to a Trial A short story prequel to The Whistler", true},
		// An unstripped exact title ignores the position.
		{"Big Cats 3", "", "Big Cats 3", true},
	}
	for _, tc := range cases {
		if got := TitleAgrees(tc.cand, tc.pos, tc.heard); got != tc.want {
			t.Errorf("TitleAgrees(%q, pos %q, %q) = %v, want %v", tc.cand, tc.pos, tc.heard, got, tc.want)
		}
	}
}

// MainTranscriptionConfirms must be origin/main's applygate rule with only
// the initials fold added: it confirms everything the old rule confirmed, and
// every extra pair it confirms is an old-rule match once initials are written
// the same way on both sides.
func TestMainTranscriptionConfirms_IsOldRulePlusInitialsFold(t *testing.T) {
	titles := []string{"Mistborn", "mistborn", "Mistborn: The Hero of Ages", "Dune", "Dune, book two of the Dune Chronicles", "Sojourn", ""}
	authors := []string{"", "Ki", "Brandon Sanderson", "Sanderson", "R.A. Salvator", "R. A. Salvatore", "RA Salvatore",
		"J.R. Salvatore", "R.A. Smith", "Debra Salvatore", "Frank Herbert", "George R. R. Martin", "George RR Martin"}
	// initialsTwin is the same name with its initials undotted and unspaced.
	initialsTwin := map[string]string{
		"R.A. Salvator": "RA Salvator", "R. A. Salvatore": "RA Salvatore",
		"J.R. Salvatore": "JR Salvatore", "R.A. Smith": "RA Smith", "George R. R. Martin": "George RR Martin",
	}
	twin := func(a string) string {
		if v, ok := initialsTwin[a]; ok {
			return v
		}
		return a
	}
	extra := 0
	for _, ct := range titles {
		for _, ht := range titles {
			for _, ca := range authors {
				for _, ha := range authors {
					old := ht != "" && oldTranscriptionRule(ct, ca, ht, ha)
					got := MainTranscriptionConfirms(ct, ca, ht, ha)
					if old && !got {
						t.Errorf("MainTranscriptionConfirms(%q,%q,%q,%q) refused a pair the old rule confirmed", ct, ca, ht, ha)
					}
					if got && !old {
						extra++
						if !oldTranscriptionRule(ct, twin(ca), ht, twin(ha)) {
							t.Errorf("MainTranscriptionConfirms(%q,%q,%q,%q) confirmed a pair that differs by more than initials", ct, ca, ht, ha)
						}
					}
				}
			}
		}
	}
	if extra == 0 {
		t.Fatal("the corpus exercises no initials fold")
	}
}

func TestFoldInitials(t *testing.T) {
	cases := []struct{ in, want string }{
		{"R.A. Salvator", "r.a. salvator"},
		{"R. A. Salvatore", "r.a. salvatore"},
		{"R A Salvatore", "r.a. salvatore"},
		{"RA Salvatore", "r.a. salvatore"},
		{"r.a salvatore", "r.a. salvatore"},
		{"George R. R. Martin, assisted", "george r.r. martin, assisted"},
		{"J.R.R. Tolkien", "j.r.r. tolkien"},
		{"JRR Tolkien", "j.r.r. tolkien"},
		{"James S. A. Corey", "james s.a. corey"},
		// A dotless token that is not all uppercase is a word, not initials.
		{"Ra Salvatore", "ra salvatore"},
		{"Ed McBain", "ed mcbain"},
		{"Kim Stanley", "kim stanley"},
		// In an all-caps name a capital run is a word; dots still fold.
		{"KIM STANLEY", "kim stanley"},
		{"R.A. SALVATORE", "r.a. salvatore"},
		// Only initials change; words and other punctuation are kept.
		{"Brandon Sanderson", "brandon sanderson"},
		{"Andrzej  Sapkowski Translated", "andrzej sapkowski translated"},
		{"Salvatore, R.", "salvatore, r."},
		{"Martin Jr.", "martin jr."},
		{"R-A Salvatore", "r-a salvatore"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := foldInitials(tc.in); got != tc.want {
			t.Errorf("foldInitials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Owner decision 2026-09-14: initials spacing and punctuation are equal, but
// different initials or a different surname still refuse.
func TestMainTranscriptionConfirms_InitialsFold(t *testing.T) {
	cases := []struct {
		cand, heard string
		want        bool
	}{
		{"R. A. Salvatore", "R.A. Salvator", true},
		{"R. A. Salvatore", "R. A. Salvatore", true},
		{"R. A. Salvatore", "RA Salvatore", true},
		{"R. A. Salvatore", "R A Salvatore", true},
		{"R.A. Salvatore", "R. A. Salvator", true},
		{"RA Salvatore", "R.A. Salvatore", true},
		{"George R. R. Martin", "George RR Martin", true},
		// Different initials.
		{"R. A. Salvatore", "J.R. Salvatore", false},
		{"R. A. Salvatore", "J. R. Salvator", false},
		{"J.R. Salvatore", "R.A. Salvatore", false},
		// Different surname.
		{"R. A. Salvatore", "R.A. Smith", false},
		{"R. A. Salvatore", "R.A. Salvadori", false},
		// Folded initials must not match inside a word: "ra salvatore" is
		// a substring of "debra salvatore", a different author. (Heard
		// "RA Salvatore" against it is confirmed by origin/main's own raw
		// substring leg, unchanged here and out of scope for the fold.)
		{"Debra Salvatore", "R. A. Salvatore", false},
		{"Debra Salvatore", "R.A. Salvatore", false},
		// A real short first name is not initials: only dotted tokens,
		// single letters and all-uppercase dotless tokens fold, classified
		// on the RAW text before lowercasing.
		{"R. A. Salvatore", "Ra Salvatore", false},
		{"Ra Salvatore", "R.A. Salvatore", false},
		{"E. D. McBain", "Ed McBain", false},
		{"J. O. Nesbo", "Jo Nesbo", false},
		{"Kim Stanley", "K. I. M. Stanley", false},
		{"A. L. Franken", "Al Franken", false},
		{"R. A. Salvatore", "JRR Salvatore", false},
		{"J. R. R. Salvatore", "JRR Salvatore", true},
		// Hyphen, apostrophe and unicode-dot tokens are never initials.
		{"R. A. Salvatore", "R-A Salvatore", false},
		{"R. A. Salvatore", "R'A Salvatore", false},
		{"R. A. Salvatore", "R․A․ Salvatore", false},
		// In an all-caps name case says nothing, so "KIM" is a word.
		{"KIM STANLEY", "K. I. M. Stanley", false},
		// A folded run must not match from its middle: "a." inside "r.a."
		// is refused by the token-start anchor. (Raw "R.A. Smith" vs "A.
		// Smith" is origin/main's own substring leg, unchanged here.)
		{"R. A. Smith", "A Smith", false},
	}
	for _, tc := range cases {
		if got := MainTranscriptionConfirms("Sojourn", tc.cand, "Sojourn", tc.heard); got != tc.want {
			t.Errorf("MainTranscriptionConfirms(Sojourn, %q, Sojourn, %q) = %v, want %v", tc.cand, tc.heard, got, tc.want)
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
