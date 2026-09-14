// file: internal/metabatch/upgrade_transcription_test.go
// version: 1.0.0
// guid: 8b3e1f64-2d9a-4c07-95e8-a4f6c0d71b39
// last-edited: 2026-09-13

package metabatch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
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
