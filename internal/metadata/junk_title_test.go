// file: internal/metadata/junk_title_test.go
// version: 1.6.0
// guid: 8b1e5f27-9c3a-4d60-b2e4-7f1a0c6d9e38
// last-edited: 2026-10-03

package metadata

import "testing"

func TestClassifyJunkTitle_Table(t *testing.T) {
	cases := []struct {
		title string
		want  JunkTitleKind
	}{
		// ---- chapter positions ----
		{"", JunkChapterOnly},
		{"01", JunkChapterOnly},
		{"98", JunkChapterOnly},
		{"Chapter 12", JunkChapterOnly},
		{"Disc 2", JunkChapterOnly},
		{"CD1", JunkChapterOnly},
		{"Track 07", JunkChapterOnly},
		{"3 of 12", JunkChapterOnly},
		{"Part IV", JunkChapterOnly},
		{"Chapter xii", JunkChapterOnly},
		{"Chapter One", JunkChapterOnly},
		{"Part Twelve", JunkChapterOnly},
		// ---- narrator credits ----
		{"read by narrator", JunkNarratorCredit},
		{"Narrated by the author", JunkNarratorCredit},
		{"Read by", JunkNarratorCredit},
		// ---- track tags ----
		{"Opening", JunkTrackTag},
		{"Intro", JunkTrackTag},
		{"Opening Credits", JunkTrackTag},
		{"opening credits 1", JunkTrackTag},
		{"Big Finish Ident", JunkTrackTag},
		{"End Credits", JunkTrackTag},
		// ---- placeholders ----
		{"Unknown Title", JunkPlaceholder},
		{"Unknown", JunkPlaceholder},
		{"Unknown Album", JunkPlaceholder},
		{"Unknown Author", JunkPlaceholder},
		{"Untitled", JunkPlaceholder},
		{"Audiobook", JunkPlaceholder},
		{"Audiobook 2", JunkPlaceholder},
		{"New Recording 4", JunkPlaceholder},
		{"unknown narrator", JunkPlaceholder},
		// ---- bare roman numerals ----
		{"IV", JunkRomanNumeral},
		{"XII", JunkRomanNumeral},
		{"iii", JunkRomanNumeral},

		// ---- prefixes ----
		{"01 - Eldest", JunkNumberPrefix},
		{"003. The Hobbit", JunkNumberPrefix},
		{"07_Dune", JunkNumberPrefix},
		{"01 Eldest", JunkNumberPrefix},
		{"12) Foundation", JunkNumberPrefix},
		{"- Eldest", JunkPunctuationPrefix},
		{"_Dune", JunkPunctuationPrefix},
		{"~ The Hobbit", JunkPunctuationPrefix},
		{". Foundation", JunkPunctuationPrefix},
		{"---", JunkPunctuationPrefix},

		// ---- real titles: must NOT be junk ----
		{"Discworld", JunkNone},
		{"1984", JunkNone},
		{"2001: A Space Odyssey", JunkNone},
		{"11/22/63", JunkNone},
		{"3:10 to Yuma", JunkNone},
		{"Book 3", JunkNone},
		{"I, Robot", JunkNone},
		{"l'Étranger", JunkNone},
		{"V for Vendetta", JunkNone},
		{"'Salem's Lot", JunkNone},
		{"...And Justice for All", JunkNone},
		{"1 Corinthians", JunkNone},
		{"I Corinthians", JunkNone},
		{"l Corinthians", JunkNone},
		{"7 Habits of Highly Effective People", JunkNone},
		{"20,000 Leagues Under the Sea", JunkNone},
		{"Mix", JunkNone},
		{"Dim", JunkNone},
		{"Civil War", JunkNone},
		{"Introduction to Algorithms", JunkNone},
		{"The Unknown Ajax", JunkNone},
		{"Opening Night", JunkNone},
		{"Read Between the Lines", JunkNone},
		{"Eldest", JunkNone},
		{"The Way of Kings", JunkNone},
		{"Vol 1.5", JunkNone},
		{"1.5 Degrees", JunkNone},
		// hyphenated numbers belong to the title
		{"10-Minute Toughness", JunkNone},
		{"01-Eldest", JunkNumberPrefix},
		{"3_Body", JunkNone},
		{"1. Mose", JunkNone},
		{"5. Mose", JunkNone},
		{"21-Day Sugar Detox", JunkNone},
		{"12-Step Recovery", JunkNone},
		{"1-2-3 Magic", JunkNone},
		{"4-3-2-1", JunkNone},
		{"9-11", JunkNone},
		// a numbered book's number is part of its name
		{"1. John", JunkNone},
		{"2 - Kings", JunkNone},
		{"2-Peter", JunkNone},
		// a credit naming someone is a title unless it names the narrator
		{"Read by Moonlight", JunkNone},
		{"Read by Kate Reading", JunkNone},
		// one-letter and word-like roman numerals are titles
		{"I", JunkNone},
		{"X", JunkNone},
		{"V", JunkNone},
		{"MIX", JunkNone},
		{"DIV", JunkNone},
		{"LIV", JunkNone},
		{"MD", JunkNone},
		{"DC", JunkNone},
		{"CD", JunkNone},
		{"DIM", JunkNone},
		// ---- shapes found on prod 2026-10-03 (one fixture per shape) ----
		{"1-06 Chapter 1_ Return to Peril", JunkNumberPrefix},  // disc-track prefix, chapter remainder
		{"1-08 Pteppic High In The Shadows", JunkNumberPrefix}, // disc-track prefix, real title remainder
		{"10-13 Chapter 3-8i A Hand At Cards", JunkNumberPrefix},
		{"8-02 Track 02", JunkNumberPrefix},
		{"2-04", JunkChapterOnly},     // disc-track and nothing else ("1-12" is left alone: it reads like "9-11")
		{"77_copy1", JunkChapterOnly}, // a numbered copy of a chapter file
		{"01_copy3", JunkChapterOnly},
		{"28_4", JunkChapterOnly},                   // chapter_part
		{"000m_00s__058m_32s_51h", JunkChapterOnly}, // split-tool time range
		{"495m_20s_79h__557m_15s_89h - Unknown Author", JunkChapterOnly},
		{"1987 - In the Flesh (Palmer) 64k 06.57.57 {193mb}", JunkNumberPrefix}, // year prefix
		{"2020 - To Sleep in a Sea of Stars", JunkNumberPrefix},
		// "15-Harry and Marlowe…" / "3-Father of the Groom" are NOT here: an
		// unpadded number and a bare hyphen read exactly like "10-Minute
		// Toughness"; the folder-based path owns those (34 books on prod).
		{"15-Harry and Marlowe Meet the Founder", JunkNone},
		{"01.Victory", JunkNumberPrefix}, // dot, no space
		{"01.The Kings Buccaneer", JunkNumberPrefix},
		{"12.1 Aftermath", JunkNumberPrefix}, // decimal position
		{"02.02 What Have I Done?", JunkNumberPrefix},
		{"4:Historical Crisis", JunkNumberPrefix}, // colon, 1-3 digits
		{"01: A New Beginning", JunkNumberPrefix},
		{"01/52 - Leviathan Falls", JunkNumberPrefix}, // position of total
		{"01/33 Invasive Procedures", JunkNumberPrefix},
		{"13_Whispers of the Nether", JunkNumberPrefix}, // unpadded underscore, 2+ digits
		// negatives the new shapes must leave alone
		{"1984", JunkNone},
		{"1988 Metrophage", JunkNone},       // year then a title with no separator: a title
		{"2001: A Space Odyssey", JunkNone}, // four digits before the colon
		{"3:10 to Yuma", JunkNone},          // colon then a digit
		{"11/22/63", JunkNone},
		{"9-11", JunkNone},
		{"10-Minute Toughness", JunkNone}, // a hyphenated word, not a prefix (title stays)
		{"1-2-3 Magic", JunkNone},
		{"4-3-2-1", JunkNone},
		{"86-Neon", JunkNone}, // two words only: too short to be sure
		{"3_Body", JunkNone},
		{"1.5", JunkNone},
		{"2.5 Men", JunkNone},
		{"84K", JunkNone},
		{"96 Hours", JunkNone},
	}
	for _, c := range cases {
		if got := ClassifyJunkTitle(c.title); got != c.want {
			t.Errorf("ClassifyJunkTitle(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestStripJunkTitlePrefix(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"01 - Eldest", "Eldest", true},
		{"003. The Hobbit", "The Hobbit", true},
		{"- Eldest", "Eldest", true},
		{"01 - - Eldest", "Eldest", true},
		{"01 - 02", "", false},        // remainder is itself junk
		{"01 - Chapter 3", "", false}, // remainder is itself junk
		{"02 - Chapter Two", "", false},
		{"Eldest", "", false},        // no prefix
		{"1 Corinthians", "", false}, // no separator: a title
		{"1. John", "", false},       // numbered book
		{"2 - Kings", "", false},     // numbered book
		{"10-Minute Toughness", "", false},
		{"07_Dune", "Dune", true},
		{"01-Eldest", "Eldest", true},
		{"3_Body", "", false}, // no separator: a title
		{"01 - X", "", false}, // single-rune remainder
		// shapes found on prod 2026-10-03
		{"1-08 Pteppic High In The Shadows", "Pteppic High In The Shadows", true},
		{"1-06 Chapter 1_ Return to Peril", "", false}, // remainder is a chapter
		{"8-02 Track 02", "", false},
		{"1987 - In the Flesh (Palmer) 64k 06.57.57 {193mb}", "In the Flesh (Palmer) 64k 06.57.57 {193mb}", true},
		{"01.Victory", "Victory", true},
		{"12.1 Aftermath", "Aftermath", true},
		{"4:Historical Crisis", "Historical Crisis", true},
		{"01/52 - Leviathan Falls", "Leviathan Falls", true},
		{"13_Whispers of the Nether", "Whispers of the Nether", true},
		{"77_copy1", "", false}, // chapter-only, nothing to strip to
		{"02 (138-track)", "", false},
		{"03 Chapter Two - The Hunter", "", false},
		{"02_copy1", "", false},
		{"29. Vengeful Spirit", "Vengeful Spirit", true},
		{"2001: A Space Odyssey", "", false},
		{"11/22/63", "", false},
	}
	for _, c := range cases {
		got, ok := StripJunkTitlePrefix(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("StripJunkTitlePrefix(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestClassifyJunkTitleFor_NamedCredit(t *testing.T) {
	if got := ClassifyJunkTitleFor("Read by Kate Reading", []string{"Kate Reading"}); got != JunkNarratorCredit {
		t.Errorf("credit naming the narrator = %q, want narrator_credit", got)
	}
	if got := ClassifyJunkTitleFor("Read by Moonlight", []string{"Kate Reading"}); got != JunkNone {
		t.Errorf("Read by Moonlight = %q, want a title", got)
	}
	if got := ClassifyJunkTitleFor("read by narrator", nil); got != JunkNarratorCredit {
		t.Errorf("bare credit = %q", got)
	}
}

func TestJunkTitleKindPredicates(t *testing.T) {
	if !JunkChapterOnly.IsChapterKind() || !JunkRomanNumeral.IsChapterKind() || JunkTrackTag.IsChapterKind() {
		t.Error("IsChapterKind: chapter_only and roman_numeral only")
	}
	if !JunkNumberPrefix.HasRealTitleInside() || JunkPlaceholder.HasRealTitleInside() {
		t.Error("HasRealTitleInside: prefix kinds only")
	}
}
