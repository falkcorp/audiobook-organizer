// file: internal/transcribe/credit_contamination_test.go
// version: 1.0.0
// guid: 08b35dd7-71a1-4ced-9bd9-81ce163bb852
// last-edited: 2026-08-07

package transcribe

import "testing"

// TestCreditContamination_RealProdTranscripts parses the ACTUAL announcement
// text behind corrupted rows observed on prod 2026-08-07. Each case names the
// field that was wrong and what it contained.
func TestCreditContamination_RealProdTranscripts(t *testing.T) {
	cases := []struct {
		name                                string
		text                                string
		title, author, translator, narrator string
	}{
		{
			// author was: 'Alexei Asadchuk. Translated by Andrew Douglas'
			name: "translator swallowed by author",
			text: "This is Audible. Tantor Audio, a division of Recorded Books presents Bastard, " +
				"by Alexei Asadchuk. Translated by Andrew Douglas Schmidt. Narrated by Ryan Burke. " +
				"Chapter 1. Colonel, the prisoner has been delivered as you ordered.",
			title: "Bastard", author: "Alexei Asadchuk",
			translator: "Andrew Douglas Schmidt", narrator: "Ryan Burke",
		},
		{
			// title was: 'Overlord Vol. 10 ... Written'
			// author was: 'Kugane Maruyama Translated by Emily Balistreri'
			name: "welded title verb AND swallowed translator",
			text: "Yen Audio presents Overlord Vol. 10 The Ruler of Conspiracy Written by " +
				"Kugane Maruyama Translated by Emily Balistreri Cover art by SoBin " +
				"Read by Chris Guerrero Prologue When Albedo entered the room",
			title: "Overlord Vol. 10 The Ruler of Conspiracy", author: "Kugane Maruyama",
			translator: "Emily Balistreri", narrator: "Chris Guerrero",
		},
		{
			// narrator was: 'Victor Baveen. Chapter 12 Trickster Teeth'
			name: "chapter 12 leaked into narrator",
			text: "Tale Weaver presents Trickster's Luck by Marcus Kane. " +
				"Narrated by Victor Baveen. Chapter 12 Trickster Teeth. The morning came",
			title: "Trickster's Luck", author: "Marcus Kane", narrator: "Victor Baveen",
		},
		{
			// narrator was: 'Katana Jones, Chapter 24 Kongen Serven'
			name: "chapter 24 leaked into narrator",
			text: "Dragon Audio presents The Iron Vow by Helen Ward. " +
				"Narrated by Katana Jones, Chapter 24 Kongen Serven. She turned away",
			title: "The Iron Vow", author: "Helen Ward", narrator: "Katana Jones",
		},
		{
			// narrator was: 'Lisa Zimmerman and Cale Williams. Introduction'
			name: "bare Introduction leaked into narrator",
			text: "Blackstone presents The Long Quiet by Anna Reed. " +
				"Narrated by Lisa Zimmerman and Cale Williams. Introduction by the author.",
			title: "The Long Quiet", author: "Anna Reed",
			narrator: "Lisa Zimmerman and Cale Williams",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAudiobookIntro(tc.text)
			check := func(field, want, have string) {
				if want != have {
					t.Errorf("%s\n  want: %q\n  got:  %q", field, want, have)
				}
			}
			check("title", tc.title, got.Title)
			check("author", tc.author, got.Author)
			check("translator", tc.translator, got.Translator)
			check("narrator", tc.narrator, got.Narrator)
		})
	}
}

// TestCreditOrderIsIrrelevant — each role is anchored on its own verb, so a
// translator credited AFTER the narrator must parse identically.
func TestCreditOrderIsIrrelevant(t *testing.T) {
	a := ParseAudiobookIntro("Acme presents The Gate by Jon Ré. " +
		"Translated by Mia Lund. Narrated by Sam Poe. Chapter 1. It began")
	b := ParseAudiobookIntro("Acme presents The Gate by Jon Ré. " +
		"Narrated by Sam Poe. Translated by Mia Lund. Chapter 1. It began")
	for _, g := range []IntroFields{a, b} {
		if g.Author != "Jon Ré" || g.Translator != "Mia Lund" || g.Narrator != "Sam Poe" {
			t.Errorf("order changed the parse: author=%q translator=%q narrator=%q",
				g.Author, g.Translator, g.Narrator)
		}
	}
}
