// file: internal/transcribe/grammar_test.go
// version: 1.0.0
// guid: 7e03b9d5-c4a1-4268-9b7f-1d6a85f2e093
// last-edited: 2026-11-17

package transcribe

import "testing"

// All text here is invented. Titles are Batman storylines and every person is a
// Batman character; none of it is a library row.

// TestGrammarChapterMarkers is the table for the structural-marker grammar: the
// rows that must classify as a chapter with the stated number, and (below) the
// rows that look like a marker and must not.
func TestGrammarChapterMarkers(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"roman chapter", "Chapter IV. The Batcave", 4},
		{"roman uppercase heading", "CHAPTER XV THE KILLING JOKE Enough twisting steel beams were visible on the horizon, lit by fire", 15},
		{"roman book", "Book III. Gotham By Gaslight", 3},
		{"spelled compound", "Chapter Twenty One. Arkham Asylum", 21},
		{"spelled hyphen", "Chapter twenty-one", 21},
		{"spelled hundred", "Chapter one hundred and five", 105},
		{"spelled sixteen", "Chapter sixteen", 16},
		{"spelled thirty-three", "chapter thirty-three", 33},
		{"part spelled", "Part Two. The Dark Years", 2},
		{"chapter the ordinal", "Chapter the Twelfth. Wayne Manor", 12},
		{"chapter the compound ordinal", "Chapter the twenty-first", 21},
		{"part the second", "Part the Second", 2},
		{"bare number heading", "12. The Penguin's Umbrella", 12},
		{"bare number alone", "3.", 3},
		{"chapter zero", "Chapter 0", 0},
		{"disc", "Disc Two", 2},
		{"mid part includes", "Hush. This part includes chapter 2 and chapter 3", 2},
		{"mid disc continued", "The tape resumes. Disc two continued", 2},
		{"mid end of part", "End of part three", 3},
		// A heading whose body contains the word "presents" keeps its marker: the
		// publisher-lead strip must not eat it.
		{"heading then presents", "Chapter 3. Alfred presents his findings to the Justice League after the storm hits Gotham", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyIntro(tc.text, UnknownPosition)
			if c.Kind != IntroKindChapter || c.ChapterNumber != tc.want {
				t.Fatalf("ClassifyIntro(%q) = kind %s number %d, want chapter %d (reason %s)", tc.text, c.Kind, c.ChapterNumber, tc.want, c.Reason)
			}
		})
	}
}

// TestGrammarMustNotMatch is the must-not-match table: text that starts like a
// marker (or contains marker words) and is not one. Each row is paired with the
// positive rule it guards.
func TestGrammarMustNotMatch(t *testing.T) {
	long := " and Bruce walked down the long corridor of the old manor while the rain kept falling on the roof"
	cases := []struct {
		name string
		text string
	}{
		// bare "N."
		{"I. as a pronoun", "I. Bruce wanted nothing more than to leave" + long},
		{"a year", "1939. Bruce Wayne was born in Gotham" + long},
		{"a time", "10.30 in the morning, Alfred brought the tea" + long},
		{"a decimal", "3.5 million dollars vanished from Wayne Enterprises overnight" + long},
		{"four digits then dot", "2024. Gotham was quiet that year" + long},
		// part / book
		{"Part of me", "Part of me wanted to run from Gotham" + long},
		{"Book him", "Book him, Detective Gordon said, and take him to Arkham tonight" + long},
		{"Book I wanted", "Book I wanted to read was the one Alfred hid in the cellar" + long},
		{"Chapter the best", "Chapter the best Bruce ever wrote was the one about his parents" + long},
		{"Chapter the cardinal", "Chapter the one that Alfred kept" + long},
		{"Part lowercase roman", "Part iv of the plan was already in motion" + long},
		{"Book year", "Book 1939 was the year everything changed" + long},
		// roman numerals never match bare
		{"bare roman numeral", "IV. The Batcave" + long},
		// spelled numbers never match bare
		{"bare spelled number", "Twelve. Bruce counted the steps" + long},
		// "continued" alone
		{"continued in prose", "Bruce continued to walk through the rain, and the streets of Gotham were empty" + long},
		{"continued alone", "Continued."},
		{"continued with comma", "He continued, and the Joker laughed at the rooftops of Gotham" + long},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyIntro(tc.text, UnknownPosition)
			if c.Kind == IntroKindChapter {
				t.Fatalf("ClassifyIntro(%q) = chapter %d (%s); must not be a chapter", tc.text, c.ChapterNumber, c.Reason)
			}
			p := ParseClip(tc.text)
			if p.Kind == ClipKindChapter || p.Kind == ClipKindPart {
				t.Fatalf("ParseClip(%q) = %s; must not be a marker", tc.text, p.Kind)
			}
		})
	}
}

// TestGrammarMatterMarkers: prologue/epilogue/interlude/afterword/foreword/
// introduction open front/back matter, and are NOT chapters (a prologue may open
// a book, so it is not continuation evidence).
func TestGrammarMatterMarkers(t *testing.T) {
	for _, kw := range []string{"Prologue", "Epilogue", "Interlude", "Afterword", "Foreword", "Introduction", "Preface", "Prelude", "Intermission", "Dedication", "Epigraph", "Acknowledgements"} {
		text := kw + ". Bruce Wayne stood at the window of the manor and watched the rain fall over Gotham for a long while"
		if c := ClassifyIntro(text, UnknownPosition); c.Kind == IntroKindChapter {
			t.Errorf("%s classified as a chapter; matter is not continuation evidence", kw)
		}
		p := ParseClip(text)
		if p.Kind != ClipKindMatter || p.Marker != toLower(kw) {
			t.Errorf("ParseClip(%s...) = %s marker %q, want front_back_matter %q", kw, p.Kind, p.Marker, toLower(kw))
		}
	}
	// Words that merely start with one are not matter.
	for _, text := range []string{
		"Introductions were made all around the table, and Bruce shook hands with every guest at the party in Gotham",
		"Prologues are rarely read, Alfred said, as he poured the tea for Bruce in the study of the old manor house",
	} {
		if p := ParseClip(text); p.Kind == ClipKindMatter {
			t.Errorf("ParseClip(%q) = matter; a word that merely starts with a matter word is narration", text)
		}
	}
}

// TestGrammarPresentsFix: the publisher lead comes off the title and the credit
// stays; a heading containing "presents" keeps its marker.
func TestGrammarPresentsFix(t *testing.T) {
	cases := []struct {
		name                string
		text                string
		title, author, narr string
		publisher           string
	}{
		{
			"comma after presents",
			"Wayne Enterprises Audio presents, The Long Halloween, written by Alfred Pennyworth. Read by Selina Kyle.",
			"The Long Halloween", "Alfred Pennyworth", "Selina Kyle", "Wayne Enterprises Audio",
		},
		{
			"space after presents",
			"Gotham Audio presents Year One, written by Lucius Fox. Read by Barbara Gordon.",
			"Year One", "Lucius Fox", "Barbara Gordon", "Gotham Audio",
		},
		{
			"colon after presents",
			"Arkham Audio presents: Hush written by Harvey Bullock, read by Selina Kyle",
			"Hush", "Harvey Bullock", "Selina Kyle", "Arkham Audio",
		},
		{
			"jingle then presents",
			"This is Audible. Gotham Audio presents, The Court of Owls, written by Lucius Fox, read by Barbara Gordon.",
			"The Court of Owls", "Lucius Fox", "Barbara Gordon", "Gotham Audio",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseClip(tc.text)
			if p.Kind != ClipKindTitle {
				t.Fatalf("kind = %s (%s), want title_announcement", p.Kind, p.Reason)
			}
			if p.Title != tc.title || p.Author != tc.author || p.Narrator != tc.narr || p.Publisher != tc.publisher {
				t.Fatalf("got title %q author %q narrator %q publisher %q; want %q %q %q %q",
					p.Title, p.Author, p.Narrator, p.Publisher, tc.title, tc.author, tc.narr, tc.publisher)
			}
			// ParseAudiobookIntro (the status source) must agree on the title.
			if f := ParseAudiobookIntro(tc.text); f.Title != tc.title {
				t.Fatalf("ParseAudiobookIntro title = %q, want %q", f.Title, tc.title)
			}
		})
	}
}

// TestGrammarPresentsMustNotMatch: "presents" inside narration or a chapter
// heading is not a publisher lead.
func TestGrammarPresentsMustNotMatch(t *testing.T) {
	for _, text := range []string{
		"Chapter 3. Alfred presents his findings to the Justice League after the storm hits Gotham",
		"Bruce stared at the screen. Alfred presents the evidence to the commissioner in the rain over Gotham tonight",
		"Chapter 4 The Present. The clock in the cave struck midnight and Bruce put on the cowl once more tonight",
	} {
		v := viewClip(text)
		if v.hadPresents || v.publisher != "" {
			t.Errorf("viewClip(%q) stripped a lead %q; a heading or narration is not a publisher", text, v.publisher)
		}
	}
	c := ClassifyIntro("Chapter 3. Alfred presents his findings to the Justice League after the storm hits Gotham", UnknownPosition)
	if c.Kind != IntroKindChapter || c.ChapterNumber != 3 {
		t.Errorf("heading with presents = %s %d, want chapter 3", c.Kind, c.ChapterNumber)
	}
}

// TestGrammarCreditContainingStructuralWord: a structural or matter word inside a
// credit run must not turn a title announcement into a marker or truncate the
// translator.
func TestGrammarCreditContainingStructuralWord(t *testing.T) {
	text := "The Long Halloween, written by Alfred Pennyworth, translated by Lucius Fox, introduction by Barbara Gordon, read by Selina Kyle"
	p := ParseClip(text)
	if p.Kind != ClipKindTitle {
		t.Fatalf("kind = %s, want title_announcement", p.Kind)
	}
	if p.Translator != "Lucius Fox" || p.Author != "Alfred Pennyworth" {
		t.Fatalf("translator %q author %q, want Lucius Fox / Alfred Pennyworth", p.Translator, p.Author)
	}
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
