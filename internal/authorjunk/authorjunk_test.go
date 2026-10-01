// file: internal/authorjunk/authorjunk_test.go
// version: 1.9.0
// guid: 3f2cc8c2-6a49-42d5-b173-cce4c692b577
// last-edited: 2026-10-01

package authorjunk

import (
	"testing"
	"unicode"
)

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
		// parser / form stand-ins (prod rows, 2026-09-29): "parse author"
		// was proposed as the relink target of 49 books.
		{"parse author", ClassPlaceholder},
		{"Parsed Author", ClassPlaceholder},
		{"author name", ClassPlaceholder},
		{"Test Author", ClassPlaceholder},
		{"NO IDEA", ClassPlaceholder},
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
		// a placeholder word among real ones (isPlaceholderPhrase needs all)
		"Nathan Writer",
		"Idea Vilariño",
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

// Relink TARGETS the 2026-09-29 prod trial proposed that are junk: every one
// passed the classifier until these rules (17,940-row trial, 6,874 relinks).
func TestClassifyName_TrialTargets(t *testing.T) {
	cases := []struct {
		name string
		want Class
		rule string
	}{
		{"Lesbian Romance", ClassGenre, RuleGenreLabel},
		{"A LitRPG Novel", ClassGenre, RuleGenreLabel},
		{"The Anime", ClassGenre, RuleGenreLabel},
		{"An Epic Fantasy Adventure", ClassGenre, RuleGenreLabel},
		{"the-final-strife", ClassOther, RuleSlug},
		{"The Complete", ClassWorkTitle, RuleArticleThe},
		{"The World", ClassWorkTitle, RuleArticleThe},
		{"The Stainless", ClassWorkTitle, RuleArticleThe},
		{"The Delphi", ClassWorkTitle, RuleArticleThe},
		{"The Core", ClassWorkTitle, RuleArticleThe},
		{"The Dragon", ClassWorkTitle, RuleArticleThe},
		{"The Iron", ClassWorkTitle, RuleArticleThe},
		{"The Salvation", ClassWorkTitle, RuleArticleThe},
		{"The Safanarion", ClassWorkTitle, RuleArticleThe},
		{"The Rosharan System", ClassWorkTitle, RuleArticleThe},
		{"The Threnodite System", ClassWorkTitle, RuleArticleThe},
		{"The Scadrian System", ClassWorkTitle, RuleArticleThe},
		{"The Thirteenth Doctor Adventures", ClassWorkTitle, RuleArticleThe},
		{"The Beast Realms", ClassWorkTitle, RuleArticleThe},
		{"The Ultimates", ClassWorkTitle, RuleArticleThe},
		{"The Primal Hunter", ClassWorkTitle, RuleArticleThe},
		{"The Faraway Paladin", ClassWorkTitle, RuleArticleThe},
		{"The Sapphire Crescent", ClassWorkTitle, RuleArticleThe},
		{"The Cunning Man", ClassWorkTitle, RuleArticleThe},
		{"The Seeing Stone", ClassWorkTitle, RuleArticleThe},
		{"Avatars Dance 1", ClassOther, RuleNumberWord},
		{"B01 Her", ClassOther, RuleNumberWord},
		{"B07 The", ClassOther, RuleNumberWord},
		{"Beka Cooper 2-Bloodhound", ClassOther, RuleNumberWord},
		{"203 The Key To Key To Time", ClassOther, RuleNumberWord},
		{"Star Wars", ClassSeriesName, RuleFranchise},
		{"Stargate SG-1", ClassSeriesName, RuleFranchise},
		{"Alphabet Squadron (Star Wars)", ClassSeriesName, RuleFranchise},
		{"Star Wars Full Cast Audio Drama", ClassPublisher, RuleProductionPhrase},
		{"StudyinSlaughterSchooledinMagicBook3", ClassWorkTitle, RuleGluedWords},
		{"abooks", ClassOther, RuleSiteTag},
		{"L. E. Miranda (Rise of the Last Star)", ClassSeriesName, RuleSeriesParenthetical},
		{"Phil Tucker (Dawn of the Void)", ClassSeriesName, RuleSeriesParenthetical},
		{"J. M. Alexia (Feast or Famine)", ClassSeriesName, RuleSeriesParenthetical},
	}
	for _, c := range cases {
		v := ClassifyName(c.name)
		if v.Class != c.want || v.Strength != Strong || v.Rule != c.rule {
			t.Errorf("ClassifyName(%q) = %+v, want %s/%s (strong)", c.name, v, c.want, c.rule)
		}
	}
}

// Real people and pen names the new rules must not touch: single-token
// LitRPG pen names, CamelCase handles, initials, article-led credits and
// narrator parentheticals.
func TestClassifyName_TrialRealAuthors(t *testing.T) {
	for _, n := range []string{
		"Zogarth", "Virlyce", "Ryuto", "Bainin", "Fuurou", "JKSManga", "Nectar", "Lunadea",
		"Twoony", "Hamuo", "Inori", "Macronomicon", "Bosloe", "Strungbound", "Yorth", "Gloam",
		"Rhaegar", "Simon", "Borgy60", "SerasStreams", "RavensDagger", "AvaritiaBona", "SourpatchHero",
		"OverXelous", "XKarnation", "WillPowah", "SpaizZzer", "Chaos65", "adastra339", "randombluecat",
		"Martha Wells", "Joe Abercrombie", "Brandon Sanderson", "An Na", "The Arbinger Institute",
		"The Brothers Grimm", "The Great Courses", "Ivan Kal", "Shane Purdy",
		"Rhea Zulu", "E. E. Knight", "Jack Bryce", "Dennis E. Taylor", "Lesley L. Smith",
		"A Johnston", "A Lee Martinez", "A Merrydew", "A. Merritt", "Adam-Troy Castro",
		"Michael-Scott Earle", "K-Ming Chang", "Aer-ki Jyr", "Thurston Howell 3rd", "MacLeod Andrews",
		"Kevin Hearne (Luke Daniels)", "Kevin Hearne (Christopher Ragland)", "Robin Hobb (Anne Flosnik)",
		"nobody103 (Jack Voraces)", "Jane Doe (Editor)", "Kevin J. Anderson (with Rebecca Moesta)",
		"Jane Doe (translated by John Roe)", "Dante King (Dragon Born)", "D. B. King (War Wizard)",
		"Michael Chatfield (Science fiction author)", "Jane Doe (American novelist)",
	} {
		if v := ClassifyName(n); v.Junk() {
			t.Errorf("ClassifyName(%q) = %+v, want not junk", n, v)
		}
	}
}

// A person-shaped parenthetical is a series only when the library says so.
func TestClassifyNameInLibrary(t *testing.T) {
	series := map[string]bool{"dragon born": true, "war mage academy": true, "future reborn": true, "last reaper": true}
	isSeries := func(n string) bool { return series[n] }
	for n, want := range map[string]bool{
		"Dante King (Dragon Born)":              true,
		"Dante King(War Mage Academy)":          true,
		"Daniel Pierce(Future Reborn)":          true,
		"J. N. Chaney (Last Reaper)":            true,
		"Kevin Hearne (Luke Daniels)":           false, // a narrator, not a series here
		"Dante King":                            false,
		"Dragon Born":                           false, // no parenthetical: the series row itself is not judged here
		"L. E. Miranda (Rise of the Last Star)": true,  // name alone
	} {
		v := ClassifyNameInLibrary(n, isSeries)
		if v.Junk() != want || (want && v.Strength != Strong) {
			t.Errorf("ClassifyNameInLibrary(%q) = %+v, want junk=%v", n, v, want)
		}
	}
	if v := ClassifyNameInLibrary("Dante King (Dragon Born)", nil); v.Junk() {
		t.Errorf("nil isSeries must be ClassifyName: %+v", v)
	}
}

// CleanedName's parenthetical and "by" shapes yield the person.
func TestCleanedName_ParentheticalAndBy(t *testing.T) {
	for n, want := range map[string]string{
		"Dante King (Dragon Born)":              "Dante King",
		"Daniel Pierce(Future Reborn)":          "Daniel Pierce",
		"L. E. Miranda (Rise of the Last Star)": "L. E. Miranda",
		"C. T. Phipps (Cthulhu Armageddon)":     "C. T. Phipps",
		"D. B. King (World End)":                "D. B. King",
		"Daniel Arenson (Starship Freedom)":     "Daniel Arenson",
		"Listening to Final Strife, The (The Final Strife, Book 1) by Saara El-Arifi_7":  "Saara El-Arifi",
		"Listening to Final Strife, The (The Final Strife, Book 1) by Saara El-Arifi_15": "Saara El-Arifi",
		"The Way of Kings by Brandon Sanderson":                                          "Brandon Sanderson",
		// A narrator is never the cleaned person.
		"Michael Kramer (narrator)":               "",
		"The Way of Kings read by Michael Kramer": "",
		"The Iliad translated by Robert Fagles":   "",
		"Brandon Sanderson (GraphicAudio)":        "",
		"nobody103 (Jack Voraces)":                "", // head is not person-shaped
		"Stand by Me":                             "",
		"The Hobbit dramatised by Brian Sibley":   "",
		"Music by Tim Foster":                     "",
		"Voiced by Jim Dale":                      "",
		"Sound design by Joe Kraemer":             "",
		"Starring Tom Baker":                      "",
		"Death by Chocolate":                      "",
	} {
		got, ok := CleanedName(n)
		if got != want || ok != (want != "") {
			t.Errorf("CleanedName(%q) = %q, %v; want %q", n, got, ok, want)
		}
	}
}

// Every rule added for the 2026-09-29 trial may only relink: real credits
// sit in their reach. The older rules keep their unlink behaviour.
func TestVerdict_RelinkOnly(t *testing.T) {
	for _, n := range []string{
		"The Dalai Lama", "The Rock", "The Mayo Clinic", "The Washington Post", "The Three Initiates",
		"The Venerable Bede", "The Gawain Poet", "The Beatles", "The Rolling Stones", "The Weeknd",
		"The Edge", "The Prophet Enoch", "50 Cent", "Jackson 5", "Maroon 5", "Blink 182", "Matchbox 20",
		"the-final-strife", "StudyinSlaughterSchooledinMagicBook3", "Star Wars", "abooks", "Lesbian Romance",
		"Star Wars Full Cast Audio Drama", "L. E. Miranda (Rise of the Last Star)",
	} {
		v := ClassifyName(n)
		if !v.Junk() || !v.RelinkOnly() {
			t.Errorf("ClassifyName(%q) = %+v, want a relink-only verdict", n, v)
		}
	}
	if v := ClassifyNameInLibrary("Dante King (Dragon Born)", func(n string) bool { return n == "dragon born" }); !v.RelinkOnly() {
		t.Errorf("library series parenthetical must be relink-only: %+v", v)
	}
	for _, n := range []string{"Demon Cycle", "Book 1 (Unabridged)", "GraphicAudio", "Science Fiction", "Unknown", "The Way of Shadows"} {
		if v := ClassifyName(n); !v.Junk() || v.RelinkOnly() {
			t.Errorf("ClassifyName(%q) = %+v, want junk that may unlink", n, v)
		}
	}
	// A verdict rebuilt from a stored row's rule answers the same.
	if !(Verdict{Class: ClassWorkTitle, Strength: Weak, Rule: RuleArticleThe}).RelinkOnly() {
		t.Error("RelinkOnly must read the rule")
	}
}

// A credit list with a clause only the added rules flag stays a credit list,
// exactly as before those rules: clauses are judged by the base rules.
func TestIsCompositeCredit_AddedRulesDoNotJudgeClauses(t *testing.T) {
	for _, n := range []string{
		"The Dalai Lama, Howard C. Cutler", "The Dalai Lama & Desmond Tutu", "The Dalai Lama; Howard Cutler",
		"50 Cent, Robert Greene", "The Beatles, Hunter Davies", "Jackson 5 & Fred Bronson",
	} {
		// "50 Cent" starts with a digit, so that one was never a composite
		// (IsCompositeCredit wants each clause to start with a letter); it
		// was not junk either, and still is not.
		if unicode.IsLetter([]rune(n)[0]) && !IsCompositeCredit(n) {
			t.Errorf("IsCompositeCredit(%q) = false, want true", n)
		}
		if v := ClassifyName(n); v.Junk() {
			t.Errorf("ClassifyName(%q) = %+v, want not junk (a credit list)", n, v)
		}
		if v := Classify(n, Evidence{}); v.Junk() {
			t.Errorf("Classify(%q) = %+v, want not junk", n, v)
		}
	}
}

// Junk-author trial 2026-09-29 (plan op 01M3QT6HZ6843PSZCZ4JNSBYPC): relink
// targets that no rule flagged. Encoder tags, bitrate release names, chapter
// labels, sort prefixes and cut-off brackets are shrapnel; each is relink-only.
func TestClassifyName_TrialShrapnelTargets(t *testing.T) {
	for n, rule := range map[string]string{
		"lavf-fate":       RuleEncoderTag,
		"lame-3.99.5":     RuleEncoderTag,
		"lame-3.100":      RuleEncoderTag,
		"Lavf58.76.100":   RuleEncoderTag,
		"chap-26-NOTES-1": RuleChapterLabel,
		"zzJim Butcher":   RuleSortPrefix,
		"Richard.Phillips-the.Rho.Agenda-Once.Dead.Nmr.64.Kbps": RuleReleaseName,
		"Graphic Audio [Jon Scieszka":                           RuleUnbalancedBracket,
	} {
		v := ClassifyName(n)
		if !v.Junk() || v.Rule != rule {
			t.Errorf("ClassifyName(%q) = %+v, want rule %s", n, v, rule)
			continue
		}
		if !v.RelinkOnly() {
			t.Errorf("ClassifyName(%q) rule %s is not relink-only", n, v.Rule)
		}
	}
	// Names near those shapes that are people or pen names.
	for _, n := range []string{"Lame", "Jim Lame", "Lame-Duck", "Chapman", "Chad Leito", "Zane Grey", "Xander Tate",
		"Trackman", "Cdric Smith", "Ian w. Sainsbury", "Stephanie 'Stephabeni' Benamati"} {
		if v := ClassifyName(n); v.Junk() {
			t.Errorf("ClassifyName(%q) = %+v, want not junk", n, v)
		}
	}
}

func TestCleanedName_TrialShapes(t *testing.T) {
	for in, want := range map[string]string{
		"zzJim Butcher":                          "Jim Butcher",
		"Graphic Audio [Jon Scieszka":            "Jon Scieszka",
		"Jennsen, GS_ 08 Rubicon (Amaranthe 08)": "G. S. Jennsen",
		"Jennsen, G.S._ 01 Starshine":            "G. S. Jennsen",
	} {
		if got, ok := CleanedName(in); !ok || got != want {
			t.Errorf("CleanedName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if !IsSurnameFirstInitials("Jennsen, GS_ 08 Rubicon (Amaranthe 08)") || IsSurnameFirstInitials("G. S. Jennsen") {
		t.Error("IsSurnameFirstInitials misjudges the surname-first shape")
	}
	// A surname-first head needs initials and a title after "_" / ":".
	for _, in := range []string{"Jennsen, Grace", "Smith, Jones_ Title", "Rubicon, 08_ Title"} {
		if got, ok := CleanedName(in); ok {
			t.Errorf("CleanedName(%q) = %q, want no clean name", in, got)
		}
	}
}

func TestPersonParentheticalHead(t *testing.T) {
	for in, want := range map[string]string{
		"Kevin Hearne (Luke Daniels)":        "Kevin Hearne",
		"Kevin Hearne (Christopher Ragland)": "Kevin Hearne",
		"Robin Hobb (Anne Flosnik)":          "Robin Hobb",
	} {
		if got, ok := PersonParentheticalHead(in); !ok || got != want {
			t.Errorf("PersonParentheticalHead(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"Kevin Hearne", "Jane Doe (Editor)", "Kevin J. Anderson (with Rebecca Moesta)",
		"nobody103 (Jack Voraces)", "Dante King (Rise of the Last Star)"} {
		if got, ok := PersonParentheticalHead(in); ok {
			t.Errorf("PersonParentheticalHead(%q) = %q, want none", in, got)
		}
	}
}

// The placeholder-phrase rule needs two or more words and is relink-only: a
// vocabulary rule never unlinks a credit on its own, and a lone head word can
// be a surname.
func TestClassifyName_PlaceholderPhraseIsRelinkOnly(t *testing.T) {
	for _, n := range []string{"parse author", "author name", "NO IDEA", "Test Author"} {
		v := ClassifyName(n)
		if v.Rule != RulePlaceholderPhrase || !v.RelinkOnly() {
			t.Errorf("ClassifyName(%q) = %+v, want relink-only %s", n, v, RulePlaceholderPhrase)
		}
	}
	for _, n := range []string{"Writer", "Name", "Idea"} {
		if v := ClassifyName(n); v.Rule == RulePlaceholderPhrase {
			t.Errorf("ClassifyName(%q) = %+v: a lone word is not a placeholder phrase", n, v)
		}
	}
}

func TestIsGenreTagline(t *testing.T) {
	for _, s := range []string{"A Novel", "A Progression LitRPG", "An Isekai LitRPG Fantasy", "A LitRPG Adventure",
		"An Epic Fantasy Adventure", "A Thriller", "Novel"} {
		if !IsGenreTagline(s) {
			t.Errorf("IsGenreTagline(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "A", "The", "A Wanted Man", "An Uncensored History", "The Tower of the Swallow",
		"A Jack Reacher Novel", "Erryn's World"} {
		if IsGenreTagline(s) {
			t.Errorf("IsGenreTagline(%q) = true, want false", s)
		}
	}
}
