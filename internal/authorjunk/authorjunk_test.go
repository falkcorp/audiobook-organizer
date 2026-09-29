// file: internal/authorjunk/authorjunk_test.go
// version: 1.2.0
// guid: 3f2cc8c2-6a49-42d5-b173-cce4c692b577
// last-edited: 2026-09-29

package authorjunk

import "testing"

// TestClassifyName_Positives: names the owner listed, plus the shapes
// measured on the production author table on 2026-09-29.
func TestClassifyName_Positives(t *testing.T) {
	cases := []struct {
		name string
		want Class
	}{
		// series names
		{"Demon Cycle", ClassSeriesName},
		{"Night Angel Series", ClassSeriesName},
		{"Nano Mage Omnibus", ClassSeriesName},
		{"The Stormlight Archive", ClassSeriesName},
		{"The Martian Chronicles", ClassSeriesName},
		{"Forest Kingdom Saga", ClassSeriesName},
		{"Night Angel Trilogy Book 1", ClassSeriesName},
		{"Marvel Universe", ClassSeriesName},
		{"Original Series", ClassSeriesName},
		// genres
		{"Science Fiction", ClassGenre},
		{"Progression Fantasy", ClassGenre},
		{"light novel", ClassGenre},
		{"Sci-Fi", ClassGenre},
		// placeholders
		{"Unknown", ClassPlaceholder},
		{"Various", ClassPlaceholder},
		{"Various Authors", ClassPlaceholder},
		{"Unknown Author", ClassPlaceholder},
		{"- Unknown Author", ClassPlaceholder},
		{"Unknown Title", ClassPlaceholder},
		{"read by narrator", ClassPlaceholder},
		{"Anonymous", ClassPlaceholder},
		{"n/a", ClassPlaceholder},
		// publishers and studios
		{"Audible Studios", ClassPublisher},
		{"Big Finish", ClassPublisher},
		{"Big Finish Productions", ClassPublisher},
		{"GraphicAudio", ClassPublisher},
		{"GraphicAudio [R. A. Salvatore]", ClassPublisher},
		{"Brandon Sanderson (GraphicAudio)", ClassPublisher},
		{"Graphic Audio LLC.", ClassPublisher},
		{"Velox Books", ClassPublisher},
		{"full cast", ClassPublisher},
		// narrator credits
		{"Read by Stephen King", ClassNarrator},
		{"Narrated by Anne Flosnik", ClassNarrator},
		{"Read by: Peter Berkrot", ClassNarrator},
		{"Michael Kramer (narrator)", ClassNarrator},
		// work titles
		{"Book 1 (Unabridged)", ClassWorkTitle},
		{"Shadow's Edge", ClassWorkTitle},
		{"Ender's Game", ClassWorkTitle},
		{"The Restaurant at the End of the Universe", ClassWorkTitle},
		{"Jonathan Strange and Mr Norrell part 4", ClassWorkTitle},
		{"Uglies Unabridged", ClassWorkTitle},
		{"The Way of Shadows", ClassWorkTitle},
		// other shrapnel
		{"Track01", ClassOther},
		{"[PZG]", ClassOther},
		{"&#169", ClassOther},
		{"(c) 2001 Stephen Hawking", ClassOther},
		{"STAR TREK POWER KLINGON", ClassOther},
		{"Jennsen, GS_ 08 Rubicon (Amaranthe 08)", ClassOther},
		{"Z11663_048_C043", ClassOther},
		{"Heir of Novron 34-65", ClassOther},
		{"Opening Credits", ClassOther},
		{"- Rebecca Roanhorse", ClassOther},
	}
	for _, c := range cases {
		v := ClassifyName(c.name)
		if v.Class != c.want || v.Strength != Strong {
			t.Errorf("ClassifyName(%q) = %+v, want %s (strong)", c.name, v, c.want)
		}
	}
}

// TestClassifyName_RealAuthors: real people, including every shape a word
// list could misfire on. None may be flagged by the name alone.
func TestClassifyName_RealAuthors(t *testing.T) {
	for _, name := range []string{
		"Junichi Saga", // "saga" is a surname
		"An Na",        // leading "An" is a name
		"A Johnston",
		"A. Merritt",
		"A. A. Milne",
		"A J Finn",
		"The Arbinger Institute", // corporate author, person-shaped
		"Ursula K. Le Guin",
		"Arthur C. Clarke",
		"Daphne du Maurier",
		"Simone de Beauvoir",
		"Preston & Child",
		"Douglas Preston, Lincoln Child",
		"Douglas Preston and Lincoln Child",
		"Brandon Sanderson",
		"Joe Abercrombie",
		"Neil Gaiman",
		"Terry Pratchett",
		"J.R.R. Tolkien",
		"George R. R. Martin",
		"Zogarth",
		"pirateaba",
		"nobody103",
		"nobody103 (Jack Voraces)",
		"RavensDagger",
		"Booker T. Washington",
		"Volker Kutscher",
		"Partha Chatterjee",
		"Christopher G. Nuttall",
		"STEPHEN KING", // two shout words are a name typed in caps
		"Émile Zola",
		"村上 春樹",
		"Harper Lee",   // "Harper" is not HarperCollins
		"Penguin Cafe", // no whole-name publisher match
		"Audrey Audible",
		"Kevin J. Anderson",
		"Ben Bova",
		"Michael Anderle",
		"Dennis E. Taylor",
		"Robin Hobb",
		"Mark Twain",
		"Jack London",
		"Mary Shelley",
		"Stephen Fry",
		"David Weber & John Ringo",
		"Zachary J. Lorang - translator",
	} {
		if v := ClassifyName(name); v.Junk() {
			t.Errorf("ClassifyName(%q) flagged a real author: %+v", name, v)
		}
		// With no library evidence Classify agrees.
		if v := Classify(name, Evidence{}); v.Junk() {
			t.Errorf("Classify(%q, {}) flagged a real author: %+v", name, v)
		}
	}
}

// TestClassify_Evidence: library evidence yields WEAK verdicts, and never
// touches a composite credit.
func TestClassify_Evidence(t *testing.T) {
	cases := []struct {
		name string
		ev   Evidence
		want Class
		str  Strength
	}{
		{"Harry Potter", Evidence{SeriesOfOtherAuthor: 7}, ClassCharacter, Weak},
		{"Jack Reacher", Evidence{SeriesOfOtherAuthor: 20}, ClassCharacter, Weak},
		{"Eldest", Evidence{SeriesOfOtherAuthor: 313}, ClassSeriesName, Weak},
		{"Before They Are Hanged", Evidence{TitleOfOtherAuthor: 5}, ClassWorkTitle, Weak},
		{"Mythos", Evidence{OwnTitles: true}, ClassWorkTitle, Strong},
		{"Camino Island", Evidence{OwnTitles: true}, ClassWorkTitle, Weak},
		// The name-only verdict wins over evidence.
		{"Demon Cycle", Evidence{SeriesOfOtherAuthor: 3}, ClassSeriesName, Strong},
		// A composite of people is never judged.
		{"Preston & Child", Evidence{SeriesOfOtherAuthor: 3, TitleOfOtherAuthor: 2}, ClassNone, StrengthNone},
		// No evidence, no verdict.
		{"Junichi Saga", Evidence{}, ClassNone, StrengthNone},
	}
	for _, c := range cases {
		v := Classify(c.name, c.ev)
		if v.Class != c.want || v.Strength != c.str {
			t.Errorf("Classify(%q, %+v) = %+v, want %s/%d", c.name, c.ev, v, c.want, c.str)
		}
	}
}

func TestIsCompositeCredit(t *testing.T) {
	for name, want := range map[string]bool{
		"Preston & Child":                true,
		"Douglas Preston, Lincoln Child": true,
		"Jonathan Smidt, Portal Books":   false, // one clause is not a person
		"Brandon Sanderson":              false,
	} {
		if got := IsCompositeCredit(name); got != want {
			t.Errorf("IsCompositeCredit(%q) = %v, want %v", name, got, want)
		}
	}
}

// The reviewer's prod-name probes (2026-09-29) that must stay unflagged.
func TestClassifyName_ReviewProbesNotFlagged(t *testing.T) {
	for _, n := range []string{"Homer", "Voltaire", "Plato", "Moebius", "Dr. Seuss", "Lemony Snicket",
		"The Brothers Grimm", "The Arbinger Institute", "An Na", "Sun Tzu", "Brandon Sanderson",
		"Joe Abercrombie", "Neil Gaiman", "E. E. Knight", "J.R.R. Tolkien", "R.A. Salvatore", "Kevin Hearne & Luke Daniels",
		"Terry Pratchett", "Stephen King", "Isaac Asimov", "C. S. Lewis", "L. E. Modesitt Jr.", "Jim Butcher", "Marie Brennan",
		"Michael J. Sullivan", "Carl Sagan", "A. A. Milne", "Captain W. E. Johns", "Grimm Brothers", "BRANDON SANDERSON", "Story, Rory"} {
		if v := ClassifyName(n); v.Junk() {
			t.Errorf("ClassifyName(%q) = %+v, want not junk", n, v)
		}
	}
}

// M1: one folded key for case, punctuation, initials spacing and diacritics.
func TestFoldKey(t *testing.T) {
	for _, p := range [][2]string{
		{"J.N. Chaney", "J. N. Chaney"}, {"M.R. Forbes", "M. R. Forbes"},
		{"George R.R. Martin", "George R. R. Martin"}, {"Brandon Sanderson", "BRANDON SANDERSON"},
		{"Christopher G. Nuttall", "Christopher G Nuttall"}, {"Emma Törzs", "Emma Torzs"},
		{"Ursula K. Le Guin", "Ursula K. LeGuin"}, {"Bryce OConnor", "Bryce O'Connor"},
		{"Michael-Scott Earle", "Michael Scott Earle"},
	} {
		if FoldKey(p[0]) != FoldKey(p[1]) {
			t.Errorf("FoldKey(%q)=%q != FoldKey(%q)=%q", p[0], FoldKey(p[0]), p[1], FoldKey(p[1]))
		}
	}
	if FoldKey("Brent Weeks") == FoldKey("Brett Weeks") {
		t.Error("different names folded together")
	}
}

// M3: collective and anthology credits are real credits.
func TestIsCollectiveCredit(t *testing.T) {
	for n, want := range map[string]bool{
		"Various Authors": true, "Anonymous": true, "Anonymous (Beowulf)": true, "Anthology Editor": true,
		"Various": true, "Unknown Title": false, "Brandon Sanderson": false, "J. Anderson, Various": false,
	} {
		if got := IsCollectiveCredit(n); got != want {
			t.Errorf("IsCollectiveCredit(%q) = %v, want %v", n, got, want)
		}
	}
}

// M4: "_" between two person-shaped halves is a credit list.
func TestSplitUnderscoreCredit(t *testing.T) {
	for n, want := range map[string]bool{
		"Terry Pratchett_ Jacqueline Simpson": true, "Nick Kyme_Saul Reichlin": true,
		"Terry_Brooks": false, "J. N. Chaney_": false, "Brandon Sanderson": false, "Track_01_Intro": false,
	} {
		if _, got := SplitUnderscoreCredit(n); got != want {
			t.Errorf("SplitUnderscoreCredit(%q) = %v, want %v", n, got, want)
		}
	}
}

// M2: the person a junk row's own name carries.
// CleanedName reads a person out of the four decorations that carry one, and
// nothing else: a title or chapter label with "_" or digits stripped is not a
// person (review 2 of PR #3613: "HOR_ Prologue" -> "HOR Prologue").
func TestCleanedName(t *testing.T) {
	for n, want := range map[string]string{
		"- Arthur C. Clarke":                             "Arthur C. Clarke",
		"- Christopher G. Nuttall":                       "Christopher G. Nuttall",
		"+Brandon Sanderson":                             "Brandon Sanderson",
		"(c) 2001 Stephen Hawking":                       "Stephen Hawking",
		"© 1994 Carl Sagan":                              "Carl Sagan",
		"GraphicAudio [R. A. Salvatore]":                 "R. A. Salvatore",
		"GraphicAudio [E. E. Knight]":                    "E. E. Knight",
		"GraphicAudio [R. A. Salvatore / Some Narrator]": "R. A. Salvatore",
		"Christopher Paolini - Read by Gerard Doyle":     "Christopher Paolini",
		"Christopher Paolini - Narrated by Gerard Doyle": "Christopher Paolini",
		// Titles and chapter labels: not cleaned at all.
		"HOR_ Prologue":          "",
		"Killing Titan 01-44":    "",
		"Country Mage_":          "",
		"Glow Red_":              "",
		"The Colour Of Magic 01": "",
		"Terry_Brooks":           "",
		"J. N. Chaney_":          "",
		// Outside the four shapes.
		"Brandon Sanderson (GraphicAudio)": "",
		"[Brent Weeks / Paul Boehmer]":     "",
		"Some Title [Brent Weeks]":         "",
		// Nothing to clean, or nothing person-shaped left.
		"The Way of Shadows":                  "",
		"Brandon Sanderson":                   "",
		"GraphicAudio":                        "",
		"Book 1 (Unabridged)":                 "",
		"Terry Pratchett_ Jacqueline Simpson": "",
		"read by narrator":                    "",
		"- Book 1":                            "",
	} {
		got, ok := CleanedName(n)
		if got != want || ok != (want != "") {
			t.Errorf("CleanedName(%q) = %q, %v; want %q", n, got, ok, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"  Shadow's_Edge ": "shadow s edge",
		"Sci-Fi":           "sci fi",
		"Émile  Zola":      "émile zola",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
