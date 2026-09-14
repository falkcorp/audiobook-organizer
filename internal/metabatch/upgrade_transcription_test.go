// file: internal/metabatch/upgrade_transcription_test.go
// version: 1.2.0
// guid: 8b3e1f64-2d9a-4c07-95e8-a4f6c0d71b39
// last-edited: 2026-09-14

package metabatch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// metadata.upgrade applies with no human in the loop, and relaxes its score
// floor when the transcription confirms a candidate. Every pair the
// adversarial review measured as a false confirmation of the first shared
// matcher must refuse here.
func TestTranscriptionConfirmsCandidate_ReviewerFalsePositivesRefuse(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name                           string
		candTitle, candAuthor          string
		heardTitle, heardAuthor, intro string
	}{
		{"subtitle vs series title", "Mistborn: The Hero of Ages", "Brandon Sanderson", "Mistborn", "Brandon Sanderson", ""},
		{"series title vs subtitle", "Mistborn", "Brandon Sanderson", "Mistborn: The Hero of Ages", "Brandon Sanderson", ""},
		{"first book vs second", "Foundation", "Isaac Asimov", "Foundation and Empire", "Isaac Asimov", ""},
		{"one letter apart", "The Witches", "Roald Dahl", "The Witcher", "Roald Dahl", ""},
		{"same surname, other author", "Sleeping Beauties", "Stephen King", "Sleeping Beauties", "Owen King", ""},
		{"surname only in the intro", "The Return of the King", "Stephen King", "The Return of the King", "J. R. R. Tolkien",
			"The Return of the King, by J.R.R. Tolkien."},
		{"narrator surname in the intro", "Alex Cross", "James Patterson", "Alex Cross", "Rob Inglis",
			"Alex Cross, narrated by Patterson Joseph."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{TranscribedTitle: s(tc.heardTitle), TranscribedAuthor: s(tc.heardAuthor)}
			if tc.intro != "" {
				book.IntroTranscription = s(tc.intro)
			}
			c := &metafetch.MetadataCandidate{Title: tc.candTitle, Author: tc.candAuthor}
			if transcriptionConfirmsCandidate(book, c) {
				t.Errorf("upgrade confirmed %q/%q against heard %q/%q", tc.candTitle, tc.candAuthor, tc.heardTitle, tc.heardAuthor)
			}
		})
	}
}

// metadata.upgrade must refuse every pair util.MainTranscriptionConfirms
// refuses: the real review cases (Sojourn left this list on 2026-09-14, when
// the owner decided initials spacing is equal; see the test below) and the
// shapes where the shared matcher is looser than main (a spoken series
// trailer, a silent letter, credit text after the author, a heard volume).
func TestTranscriptionConfirmsCandidate_RefusesEveryPairMainRefused(t *testing.T) {
	s := func(v string) *string { return &v }
	pairs := []struct{ candTitle, candAuthor, pos, heardTitle, heardAuthor string }{
		{"Blood of Elves", "Andrzej Sapkowski", "", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish"},
		{"A Cry of Honor (Book #4 in the Sorcerer's Ring)", "Morgan Rice", "4", "A Cry of Honor", "Morgan Rice"},
		{"Witness to a Trial", "John Grisham", "", "Witness to a Trial A short story prequel to The Whistler", "John Grisham"},
		{"Knaves Over Queens", "George R. R. Martin", "", "Naves Over Queens", "George R. R. Martin, assisted"},
		{"This Gilded Abyss", "Rebecca Thorne", "1", "This Gilded Abyss, book one of the Gilded Abyss trilogy", "Rebecca Thorne"},
		{"Mistborn", "Brandon Sanderson", "1", "Mistborn", "Brandon Sanderson For Beth Sanderson, who's"},
		{"Dune", "Frank Herbert", "2", "Dune, book two of the Dune Chronicles", "Frank Herbert"},
		{"Harry Potter", "J. K. Rowling", "2", "Harry Potter book 2", "J. K. Rowling"},
		{"The Expanse", "James S. A. Corey", "3", "The Expanse, volume 3", "James S. A. Corey"},
		{"Ready Player One", "Ernest Cline", "", "Ready Player 1", "Ernest Cline"},
	}
	for _, p := range pairs {
		if util.MainTranscriptionConfirms(p.candTitle, p.candAuthor, p.heardTitle, p.heardAuthor) {
			t.Fatalf("fixture %q ~ %q: origin/main confirms it, so it tests nothing", p.candTitle, p.heardTitle)
		}
		book := &database.Book{TranscribedTitle: s(p.heardTitle), TranscribedAuthor: s(p.heardAuthor)}
		c := &metafetch.MetadataCandidate{Title: p.candTitle, Author: p.candAuthor, SeriesPosition: p.pos}
		if transcriptionConfirmsCandidate(book, c) {
			t.Errorf("upgrade confirmed %q ~ heard %q/%q, which origin/main refused", p.candTitle, p.heardTitle, p.heardAuthor)
		}
	}
}

// Owner decision 2026-09-14: initials spacing and punctuation are equal, so
// Sojourn (heard "R.A. Salvator", provider "R. A. Salvatore") confirms, and
// different initials still refuse.
func TestTranscriptionConfirmsCandidate_InitialsFold(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		heardAuthor string
		want        bool
	}{
		{"R.A. Salvator", true},
		{"RA Salvatore", true},
		{"J.R. Salvator", false},
		{"R.A. Smith", false},
	}
	for _, tc := range cases {
		book := &database.Book{TranscribedTitle: s("Sojourn"), TranscribedAuthor: s(tc.heardAuthor)}
		c := &metafetch.MetadataCandidate{Title: "Sojourn", Author: "R. A. Salvatore", SeriesPosition: "2"}
		if got := transcriptionConfirmsCandidate(book, c); got != tc.want {
			t.Errorf("upgrade on heard %q = %v, want %v", tc.heardAuthor, got, tc.want)
		}
	}
}
