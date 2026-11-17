// file: internal/transcribe/clip_test.go
// version: 1.0.0
// guid: 0b5d7f91-3e28-4c64-a1d9-62f8c0e47b35
// last-edited: 2026-11-17

package transcribe

import (
	"strings"
	"testing"
)

// Invented text only: Batman titles, Batman characters.

func TestParseClipKinds(t *testing.T) {
	narr := " Bruce Wayne walked down the long corridor of the old manor while the rain kept falling on the roof of the cave"
	cases := []struct {
		name   string
		text   string
		want   ClipKind
		reason string
	}{
		{"empty", "", ClipKindNone, "no-transcript"},
		{"whitespace", "  \n ", ClipKindNone, "no-transcript"},
		{"sentinel", SilenceSentinel, ClipKindSilence, "silence-sentinel"},
		{"music tag", "[Music]", ClipKindSilence, "sound-tag-only"},
		{"blank audio", "[BLANK_AUDIO]", ClipKindSilence, "sound-tag-only"},
		{"music notes", "♪♪", ClipKindSilence, "sound-tag-only"},
		{"paren music", "(music)", ClipKindSilence, "sound-tag-only"},
		{"title", "The Killing Joke, written by Alfred Pennyworth, read by Selina Kyle", ClipKindTitle, "credit-grammar"},
		{"chapter", "Chapter 12. The Long Halloween." + narr, ClipKindChapter, "chapter-marker"},
		{"part", "Part Two. The Dark Years." + narr, ClipKindPart, "chapter-marker"},
		{"disc is part", "Disc 3." + narr, ClipKindPart, "chapter-marker"},
		{"track is chapter", "Track 7." + narr, ClipKindChapter, "chapter-marker"},
		{"matter", "Epilogue." + narr, ClipKindMatter, "matter-marker"},
		{"narration", narr, ClipKindNarration, "no-announcement"},
		{"jingle", "This is Audible.", ClipKindCredits, "boilerplate-only"},
		{"credits only", "Narrated by Selina Kyle. Copyright 2019 Wayne Enterprises. All rights reserved.", ClipKindCredits, "credits-only"},
		{"production", "This is a Wayne Enterprises production.", ClipKindCredits, "credits-only"},
		{"hallucination", "Thank you for watching!", ClipKindGarbage, "hallucination"},
		{"hallucination plain", "Thank you.", ClipKindGarbage, "hallucination"},
		{"repeat", "you you you you you", ClipKindGarbage, "repeated-token"},
		{"repeat vocabulary", "la la la la la la la la la la la la", ClipKindGarbage, "repeated-token"},
		{"symbols", "a ...!!!??? --- ,,, ;;;", ClipKindGarbage, "symbol-noise"},
		{"fragment", "Bruce stared at the ceiling.", ClipKindGarbage, "fragment-too-short"},
		{"publisher fragment", "Gotham Audio. For the love of stories.", ClipKindCredits, "publisher-fragment"},
		{"punctuation only", "...", ClipKindGarbage, "no-content"},
		{"punctuation run", "...!!!??? ---", ClipKindGarbage, "no-content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseClip(tc.text)
			if got.Kind != tc.want || got.Reason != tc.reason {
				t.Fatalf("ParseClip(%q) = %s/%s, want %s/%s", tc.text, got.Kind, got.Reason, tc.want, tc.reason)
			}
			if got.Kind != ClipKindNone && !got.Kind.Valid() {
				t.Fatalf("kind %q is not a storable kind", got.Kind)
			}
		})
	}
}

func TestParseClipChapterFields(t *testing.T) {
	narr := " Bruce Wayne walked down the long corridor of the old manor while the rain kept falling on the roof of the cave"
	cases := []struct {
		name    string
		text    string
		chapter int
		hasCh   bool
		title   string
		part    int
		hasPart bool
		marker  string
	}{
		{"title after dot", "Chapter 5. The Joker's Gambit." + narr, 5, true, "The Joker's Gambit", 0, false, "chapter"},
		{"title no punctuation at end of clip", "Chapter 5 The Joker's Gambit", 5, true, "The Joker's Gambit", 0, false, "chapter"},
		{"narration after number", "Chapter 5. Bruce stared at the ceiling and thought of the long night ahead." + narr, 5, true, "", 0, false, "chapter"},
		{"one word title with dot", "Chapter 5. Gotham." + narr, 5, true, "Gotham", 0, false, "chapter"},
		{"part then chapter", "Part Two. Chapter Five. The Court of Owls." + narr, 5, true, "The Court of Owls", 2, true, "part"},
		{"part only", "Part Two." + narr, 0, false, "", 2, true, "part"},
		{"bare number", "12. The Penguin's Umbrella." + narr, 12, true, "The Penguin's Umbrella", 0, false, "chapter"},
		{"chapter zero", "Chapter 0." + narr, 0, true, "", 0, false, "chapter"},
		{"roman all caps title", "CHAPTER XV THE KILLING JOKE Enough steel beams were visible on the horizon, lit by fire", 15, true, "THE KILLING JOKE", 0, false, "chapter"},
		{"title then capitalised narration is ambiguous", "Chapter 5 The Joker's Gambit" + narr, 5, true, "", 0, false, "chapter"},
		{"mid continuation", "Hush. This part includes chapter 2 and chapter 3." + narr, 2, true, "", 0, false, "continuation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseClip(tc.text)
			if got.ChapterNumber != tc.chapter || got.HasChapterNumber != tc.hasCh || got.ChapterTitle != tc.title ||
				got.PartNumber != tc.part || got.HasPartNumber != tc.hasPart || got.Marker != tc.marker {
				t.Fatalf("got chapter %d/%v title %q part %d/%v marker %q (kind %s); want %d/%v %q %d/%v %q",
					got.ChapterNumber, got.HasChapterNumber, got.ChapterTitle, got.PartNumber, got.HasPartNumber, got.Marker, got.Kind,
					tc.chapter, tc.hasCh, tc.title, tc.part, tc.hasPart, tc.marker)
			}
			// A marker confirms position, never identity.
			if got.Title != "" || got.Author != "" || got.Narrator != "" {
				t.Fatalf("marker clip carries identity: %+v", got)
			}
		})
	}
}

func TestParseClipCollectionRange(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		from, to int
	}{
		{"through", "Gotham Audio presents The Knightfall Saga, Books 1 through 3, written by Lucius Fox, read by Selina Kyle", 1, 3},
		{"spelled to", "The Knightfall Saga, Books one to three, written by Lucius Fox, read by Selina Kyle", 1, 3},
		{"list with and", "Gotham Audio presents The Knightfall Saga, Books 13, 14, and 15 of the Saga, written by Lucius Fox, read by Selina Kyle", 13, 15},
		{"thru", "The Knightfall Saga, Volumes 2 thru 4, written by Lucius Fox, read by Selina Kyle", 2, 4},
		{"two books and", "The Knightfall Saga, Books 1 and 2, written by Lucius Fox, read by Selina Kyle", 1, 2},
		{"single book is not a range", "Knightfall, Book 3, The Broken Bat, written by Lucius Fox, read by Selina Kyle", 0, 0},
		{"descending is not a range", "The Knightfall Saga, Books 3 through 1, written by Lucius Fox, read by Selina Kyle", 0, 0},
		{"no collection", "The Killing Joke, written by Alfred Pennyworth, read by Selina Kyle", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseClip(tc.text)
			if got.Kind != ClipKindTitle {
				t.Fatalf("kind = %s, want title_announcement", got.Kind)
			}
			if got.CollectionFrom != tc.from || got.CollectionTo != tc.to {
				t.Fatalf("collection = %d..%d, want %d..%d (title %q)", got.CollectionFrom, got.CollectionTo, tc.from, tc.to, got.Title)
			}
		})
	}
	// Narration that merely mentions books is not a collection.
	narr := ParseClip("Bruce put books 3 and 4 back on the shelf in the study and then walked out of the manor into the rain over Gotham")
	if narr.CollectionFrom != 0 || narr.CollectionTo != 0 {
		t.Fatalf("narration produced a collection range: %+v", narr)
	}
}

func TestParseClipCreditsOnlyFields(t *testing.T) {
	got := ParseClip("Written by Alfred Pennyworth. Narrated by Selina Kyle. Published by Wayne Enterprises. All rights reserved.")
	if got.Kind != ClipKindCredits {
		t.Fatalf("kind = %s, want credits", got.Kind)
	}
	if got.Author != "Alfred Pennyworth" || got.Narrator != "Selina Kyle" || got.Publisher != "Wayne Enterprises" {
		t.Fatalf("fields = %+v", got)
	}
}

func TestParseClipJinglePublisher(t *testing.T) {
	if got := ParseClip("This is Audible."); got.Publisher != "Audible" {
		t.Fatalf("publisher = %q, want Audible", got.Publisher)
	}
}

// TestParseClipAgreesWithClassifyIntro pins the invariant the design rests on:
// the finer kind is a refinement of IntroKind, never a second opinion.
func TestParseClipAgreesWithClassifyIntro(t *testing.T) {
	check := func(t *testing.T, text string) {
		t.Helper()
		c := ClassifyIntro(text, UnknownPosition)
		p := ParseClip(text)
		if (c.Kind == IntroKindCredits) != (p.Kind == ClipKindTitle) {
			t.Fatalf("credits/title disagree for %q: intro %s clip %s", text, c.Kind, p.Kind)
		}
		if c.Kind == IntroKindChapter && p.Kind != ClipKindChapter && p.Kind != ClipKindPart {
			t.Fatalf("chapter disagrees for %q: clip %s", text, p.Kind)
		}
		if c.Kind == IntroKindCredits && c.Fields.Title != p.Title {
			t.Fatalf("title disagrees for %q: %q vs %q", text, c.Fields.Title, p.Title)
		}
	}
	for _, r := range loadCorpus(t) {
		check(t, r.Text)
	}
	for _, tc := range []string{"Chapter 3.", "[Music]", "Part Two", "Prologue", "Hush by Selina Kyle"} {
		check(t, tc)
	}
}

// TestParseClipCorpusDistribution runs the kinds over the production corpus: no
// panic, every row lands on a storable kind, and the distribution is plausible
// (a canary, not a label check).
func TestParseClipCorpusDistribution(t *testing.T) {
	counts := map[ClipKind]int{}
	for _, r := range loadCorpus(t) {
		p := ParseClip(r.Text)
		if p.Kind != ClipKindNone && !p.Kind.Valid() {
			t.Fatalf("invalid kind %q", p.Kind)
		}
		counts[p.Kind]++
		if p.Kind != ClipKindTitle && (p.Title != "" || p.ChapterTitle != "" && p.Kind != ClipKindChapter) {
			t.Fatalf("non-title kind %s carries title %q / chapter title %q", p.Kind, p.Title, p.ChapterTitle)
		}
	}
	t.Logf("corpus kinds: %v", counts)
	if counts[ClipKindTitle] < 80 {
		t.Errorf("only %d title announcements in a corpus that classifies ~89 as credits", counts[ClipKindTitle])
	}
	if counts[ClipKindChapter]+counts[ClipKindPart] < 40 {
		t.Errorf("only %d markers; the corpus has ~52 chapter-kind rows", counts[ClipKindChapter]+counts[ClipKindPart])
	}
}

// TestParseClipPublisherNeverInTitle: after the lead is stripped no title on the
// corpus starts with the publisher's own "presents".
func TestParseClipPublisherNeverInTitle(t *testing.T) {
	for _, r := range loadCorpus(t) {
		p := ParseClip(r.Text)
		if p.Kind == ClipKindTitle && strings.Contains(strings.ToLower(p.Title), " presents") {
			t.Fatalf("title %q still carries a presents lead", p.Title)
		}
	}
}

func TestParseClipDeterministic(t *testing.T) {
	for _, r := range loadCorpus(t) {
		a, b := ParseClip(r.Text), ParseClip(r.Text)
		if a != b {
			t.Fatalf("ParseClip is not a pure function of its text: %+v vs %+v", a, b)
		}
	}
}
