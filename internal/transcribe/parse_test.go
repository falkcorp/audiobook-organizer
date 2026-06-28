// file: internal/transcribe/parse_test.go
// version: 1.0.0
// guid: 4f8b1d6a-9c20-4e51-bb73-1a2c3d4e5f60
// last-edited: 2026-06-28

package transcribe

import "testing"

func TestParseAudiobookIntro(t *testing.T) {
	// The real failing case from prod (book 01KNDBS8BC5D7Y6ARC9K74JY1T).
	salemsLot := "Simon and Schuster audio presents Salem's Lot by Stephen King " +
		"Read by Ron McClarty with an introduction by Stephen King No one writes a " +
		"long novel alone and I would like to take a moment of your time to thank some " +
		"of the people who helped with this one G Everett McCutchen of Hamden Academy " +
		"for his practical suggestions and encouragement Dr. John Pearson of Old Town, " +
		"Maine, medical examiner of Penobscot County, and member in good standing of " +
		"that most excellent medical specialty general practice. Father Renald Halley " +
		"of St. John's Catholic Church in Bangor, Maine, and of course my wife, whose " +
		"criticism is as tough and unflinching as ever. Although the town surrounding " +
		"Salem's lot are very real, Salem's lot exists wholly in the author's " +
		"imagination. and any resemblance between the people who live there and the " +
		"people who live in the real world is coincidental and unintended. I first " +
		"read Dracula when I was 9 or 10"

	cases := []struct {
		name                          string
		text                          string
		wantTitle, wantAuthor, wantNr string
	}{
		{
			name:       "salems_lot_publisher_presents_readby_then_acknowledgements",
			text:       salemsLot,
			wantTitle:  "Salem's Lot",
			wantAuthor: "Stephen King",
			wantNr:     "Ron McClarty",
		},
		{
			name:       "publisher_audio_presents",
			text:       "Penguin Random House Audio presents The Martian by Andy Weir Read by R.C. Bray",
			wantTitle:  "The Martian",
			wantAuthor: "Andy Weir",
			wantNr:     "R.C. Bray",
		},
		{
			name:       "bare_presents_no_audio_word",
			text:       "Macmillan presents Project Hail Mary by Andy Weir narrated by Ray Porter",
			wantTitle:  "Project Hail Mary",
			wantAuthor: "Andy Weir",
			wantNr:     "Ray Porter",
		},
		{
			name:       "no_publisher_clean_readby",
			text:       "The Name of the Wind by Patrick Rothfuss Read by Nick Podehl",
			wantTitle:  "The Name of the Wind",
			wantAuthor: "Patrick Rothfuss",
			wantNr:     "Nick Podehl",
		},
		{
			name:       "comma_punctuated_classic_case",
			text:       "The Hobbit by J.R.R. Tolkien, read by Rob Inglis.",
			wantTitle:  "The Hobbit",
			wantAuthor: "J.R.R. Tolkien",
			wantNr:     "Rob Inglis",
		},
		{
			name:       "narrator_followed_by_introduction_noise",
			text:       "Dune by Frank Herbert read by Scott Brick with an introduction by the author and a note on pronunciation",
			wantTitle:  "Dune",
			wantAuthor: "Frank Herbert",
			wantNr:     "Scott Brick",
		},
		{
			name:       "author_trailing_noise_no_readby",
			text:       "On Writing by Stephen King No one writes a long novel alone and I would like to thank everyone",
			wantTitle:  "On Writing",
			wantAuthor: "Stephen King",
			wantNr:     "",
		},
		{
			name:       "performed_by_variant",
			text:       "Lincoln in the Bardo by George Saunders performed by a full cast",
			wantTitle:  "Lincoln in the Bardo",
			wantAuthor: "George Saunders",
			wantNr:     "a full cast",
		},
		{
			name:       "two_narrators_with_and",
			text:       "Good Omens by Neil Gaiman read by Martin Jarvis and Peter Serafinowicz",
			wantTitle:  "Good Omens",
			wantAuthor: "Neil Gaiman",
			wantNr:     "Martin Jarvis and Peter Serafinowicz",
		},
		{
			name:       "empty_input",
			text:       "",
			wantTitle:  "",
			wantAuthor: "",
			wantNr:     "",
		},
		{
			name:       "no_by_at_all_title_only",
			text:       "This is Audible.",
			wantTitle:  "",
			wantAuthor: "",
			wantNr:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAudiobookIntro(tc.text)
			if got.Title != tc.wantTitle {
				t.Errorf("Title  = %q, want %q", got.Title, tc.wantTitle)
			}
			if got.Author != tc.wantAuthor {
				t.Errorf("Author = %q, want %q", got.Author, tc.wantAuthor)
			}
			if got.Narrator != tc.wantNr {
				t.Errorf("Narrator = %q, want %q", got.Narrator, tc.wantNr)
			}
		})
	}
}
