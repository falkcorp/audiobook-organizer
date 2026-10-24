// file: internal/plugins/maintenance/junk_title_derive_test.go
// version: 1.2.0
// guid: 51758d2b-4a45-4c0b-9bd0-5b22cc579c19
// last-edited: 2026-10-24

package maintenance

import (
	"strings"
	"testing"
)

// 🔴 THE CORRUPTION THIS MUST NOT CAUSE. Dropping the last " - " segment after
// the credit is the obvious implementation and it silently eats real title text
// whenever the title itself contains " - " — which, for series audiobooks, is
// most of them. The author name is what makes the strip safe.
func TestTitleFromFilename_DoesNotEatTitleTextWhenAuthorIsAbsent(t *testing.T) {
	got := titleFromFilename("Dark Gallifrey - The War Master Part 2 - read by narrator.mp3", "")
	want := "Dark Gallifrey - The War Master Part 2"
	if got != want {
		t.Fatalf("got %q, want %q — with no author to match, only the credit may be removed", got, want)
	}
}

// With the author known, both the author and the credit come off.
func TestTitleFromFilename_StripsAuthorAndCredit(t *testing.T) {
	cases := []struct{ file, author, want string }{
		{"Nocturne - 3 - Umbra Mortem - JD Glasscock - read by narrator.m4b",
			"JD Glasscock", "Nocturne - 3 - Umbra Mortem"},
		{"Bobiverse - 1 - We Are Legion (We Are Bob) - Dennis E. Taylor - read by narrator.m4b",
			"Dennis E. Taylor", "Bobiverse - 1 - We Are Legion (We Are Bob)"},
	}
	for _, c := range cases {
		if got := titleFromFilename(c.file, c.author); got != c.want {
			t.Errorf("titleFromFilename(%q, %q) = %q, want %q", c.file, c.author, got, c.want)
		}
	}
}

// A filename with no credit at all is a title in its own right.
func TestTitleFromFilename_NoCreditUsesWholeStem(t *testing.T) {
	got := titleFromFilename("Kaiju Task Force_ Code Omega Season.m4b", "Some Author")
	if got != "Kaiju Task Force_ Code Omega Season" {
		t.Fatalf("got %q, want the whole stem", got)
	}
}

// Author matching is case-insensitive, because the tag case and the filename
// case disagree constantly in this library.
func TestTitleFromFilename_AuthorMatchIsCaseInsensitive(t *testing.T) {
	got := titleFromFilename("Some Book - DENNIS E. TAYLOR - read by narrator.mp3", "dennis e. taylor")
	if got != "Some Book" {
		t.Fatalf("got %q, want %q", got, "Some Book")
	}
}

// Only real audio extensions are stripped; a version number must survive.
func TestStripAudioExt_LeavesNonAudioSuffixesAlone(t *testing.T) {
	if got := stripAudioExt("Vol. 1.5"); got != "Vol. 1.5" {
		t.Fatalf("got %q, want %q — .5 is not an extension", got, "Vol. 1.5")
	}
	if got := stripAudioExt("Book.m4b"); got != "Book" {
		t.Fatalf("got %q, want %q", got, "Book")
	}
}

// 🔑 Shape B — the multi-file ID3-residue case. The folder is correct and is the
// most trustworthy evidence, so it wins over the filename (which is a track name
// like "01 Big Finish Ident.mp3" and would be worse than useless).
func TestDeriveJunkTitleReplacement_MultiFileUsesFolder(t *testing.T) {
	paths := []string{
		"/lib/Big Finish Productions/Dark Gallifrey/Dark Gallifrey - The War Master Part 2/01 Big Finish Ident.mp3",
		"/lib/Big Finish Productions/Dark Gallifrey/Dark Gallifrey - The War Master Part 2/02 The War Master Part 2 Track 01.mp3",
	}
	got, method, ok := DeriveJunkTitleReplacement("Big Finish Ident", "Big Finish Productions", paths)
	if !ok {
		t.Fatal("expected a derivation")
	}
	if got != "Dark Gallifrey - The War Master Part 2" {
		t.Fatalf("title = %q, want the folder name", got)
	}
	if method != "folder" {
		t.Fatalf("method = %q, want folder", method)
	}
}

// 🔑 Shape A — single file whose FOLDER is poisoned by the same bad title. The
// filename is the only evidence, and the folder must not be used.
func TestDeriveJunkTitleReplacement_SingleFileUsesFilenameWhenFolderIsPoisoned(t *testing.T) {
	paths := []string{
		"/lib/Nocturne/read by narrator/Nocturne - 3 - Umbra Mortem - JD Glasscock - read by narrator.m4b",
	}
	got, method, ok := DeriveJunkTitleReplacement("read by narrator", "JD Glasscock", paths)
	if !ok {
		t.Fatal("expected a derivation")
	}
	if got != "Nocturne - 3 - Umbra Mortem" {
		t.Fatalf("title = %q, want the filename-derived title", got)
	}
	if method != "filename" {
		t.Fatalf("method = %q, want filename", method)
	}
}

// A multi-file book whose folder IS junk must fall through to the filename
// rather than accept the poisoned folder name.
func TestDeriveJunkTitleReplacement_RejectsJunkFolderAndFallsThrough(t *testing.T) {
	paths := []string{
		"/lib/Nocturne/read by narrator/Nocturne - 3 - Umbra Mortem - JD Glasscock - read by narrator.m4b",
		"/lib/Nocturne/read by narrator/Nocturne - 3 - Umbra Mortem.m4b",
	}
	got, _, ok := DeriveJunkTitleReplacement("read by narrator", "JD Glasscock", paths)
	if !ok {
		t.Fatal("expected a derivation")
	}
	if IsJunkTitle(got) {
		t.Fatalf("derived another junk title: %q", got)
	}
	if got != "Nocturne - 3 - Umbra Mortem" {
		t.Fatalf("title = %q, want %q", got, "Nocturne - 3 - Umbra Mortem")
	}
}

// The ancestor walk rescues the ".../Kaiju Task/read by narrator/..." shape when
// the filename itself is unhelpful.
func TestDeriveJunkTitleReplacement_WalksUpToAnAncestorFolder(t *testing.T) {
	paths := []string{"/lib/Kaiju Task/read by narrator/read by narrator.m4b"}
	got, method, ok := DeriveJunkTitleReplacement("read by narrator", "", paths)
	if !ok {
		t.Fatal("expected a derivation from an ancestor folder")
	}
	if got != "Kaiju Task" {
		t.Fatalf("title = %q, want %q", got, "Kaiju Task")
	}
	if method != "ancestor-folder" {
		t.Fatalf("method = %q, want ancestor-folder", method)
	}
}

// 🔴 Refuse rather than invent. A book with nothing usable must be left alone —
// writing a guessed title is worse than leaving a known-bad one, because the bad
// one is at least detectable by this same op later.
func TestDeriveJunkTitleReplacement_RefusesWhenThereIsNoEvidence(t *testing.T) {
	if _, _, ok := DeriveJunkTitleReplacement("read by narrator", "", nil); ok {
		t.Fatal("derived a title from no files at all")
	}
	if _, _, ok := DeriveJunkTitleReplacement("read by narrator", "", []string{"read by narrator.m4b"}); ok {
		t.Fatal("derived a title when every candidate was junk")
	}
}

// A single-character result is not a title.
func TestDeriveJunkTitleReplacement_RejectsTooShortResults(t *testing.T) {
	if _, _, ok := DeriveJunkTitleReplacement("intro", "", []string{"/lib/A/intro.mp3"}); ok {
		t.Fatal("accepted a one-character title")
	}
}

// Never report a change when the derived value equals what is already stored.
func TestDeriveJunkTitleReplacement_RejectsNoOpRewrite(t *testing.T) {
	paths := []string{"/lib/Author/Some Book/Some Book.m4b"}
	if _, _, ok := DeriveJunkTitleReplacement("Some Book", "Author", paths); ok {
		t.Fatal("reported a change that would rewrite the title to itself")
	}
}

// Ties between directories must resolve deterministically, so a dry run and the
// apply that follows it agree.
func TestMajorityDirOf_IsDeterministicOnTies(t *testing.T) {
	paths := []string{"/b/zzz/1.mp3", "/a/aaa/2.mp3"}
	first := majorityDirOf(paths)
	for range 20 {
		if got := majorityDirOf(paths); got != first {
			t.Fatalf("majorityDirOf is unstable: %q then %q", first, got)
		}
	}
}

func TestMajorityDirOf_PicksTheDirectoryHoldingMostFiles(t *testing.T) {
	paths := []string{"/a/x/1.mp3", "/a/y/2.mp3", "/a/y/3.mp3"}
	if got := majorityDirOf(paths); got != "/a/y" {
		t.Fatalf("got %q, want /a/y", got)
	}
}

func TestIsJunkTitle(t *testing.T) {
	for _, s := range []string{"read by narrator", "  Read By Narrator ", "INTRO", "Big Finish Ident"} {
		if !IsJunkTitle(s) {
			t.Errorf("IsJunkTitle(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"Nocturne", "The Intro Files", ""} {
		if IsJunkTitle(s) {
			t.Errorf("IsJunkTitle(%q) = true, want false", s)
		}
	}
}

// The ancestor hop escapes only a folder the organizer poisoned (the exact
// junkTitles set). A folder that merely classifies as junk ("Chapter 1") is
// not one, and climbing out of it returned the library root "lib".
func TestDeriveJunkTitleReplacement_NoHopOutOfAClassifierJunkFolder(t *testing.T) {
	for _, paths := range [][]string{
		{"/lib/Chapter 1/x.mp3"},
		{"/lib/Read by Moonlight/x.mp3"},
	} {
		if got, method, ok := DeriveJunkTitleReplacement("intro", "", paths); ok {
			t.Fatalf("%v: derived %q via %s, want nothing", paths, got, method)
		}
	}
}

// The stored author is junk on most of the rows that carry a filename credit,
// so the strip cannot depend on it: a placeholder author goes whether or not
// the stem has a "read by" credit, and so does a person of the book's own. A
// name that is only in the library goes behind a credit and for the segment
// that stands where the convention puts the author. Title text with a " - " in
// it stays. (Shapes of the 2026-10-23 review, with invented titles.)
func TestTitleFromFilenameWith_StripsAuthorSegments(t *testing.T) {
	set := func(names ...string) func(string) bool {
		m := map[string]bool{}
		for _, n := range names {
			m[strings.ToLower(n)] = true
		}
		return func(s string) bool { return m[strings.ToLower(strings.TrimSpace(s))] }
	}
	rec := authorRecognisers{own: set("Oliver Narrate"), library: set("Zed Writer", "Q. V. Example", "Salt Harbor")}
	cases := []struct {
		name, file, author, want string
	}{
		{"placeholder, no credit", "Marble Gate - Unknown Author.mp3", "", "Marble Gate"},
		{"placeholder, with credit", "Marble Gate - Unknown Author - read by narrator.mp3", "", "Marble Gate"},
		{"placeholder, stored author junk", "Marble Gate - Unknown Author.m4b", "read by narrator", "Marble Gate"},
		{"unknown alone", "Marble Gate - Unknown.mp3", "", "Marble Gate"},
		{"stored author, no credit", "Marble Gate - Someone New.mp3", "Someone New", "Marble Gate"},
		{"stored author, credit", "Marble Gate - Someone New - read by narrator.mp3", "Someone New", "Marble Gate"},
		{"the book's own person, no credit", "Marble Gate - Oliver Narrate.mp3", "", "Marble Gate"},
		{"library author behind a credit", "Marble Gate - Zed Writer - read by narrator.mp3", "", "Marble Gate"},
		{"library author with initials behind a credit", "Marble Gate 3 - Q. V. Example - read by narrator.m4b", "", "Marble Gate 3"},
		{"placeholder after the library author", "Marble Gate - Zed Writer - Unknown Author - read by narrator.mp3", "", "Marble Gate"},
		// A library name with no credit is not stripped: the library holds author
		// rows that are titles, and nothing says this segment is the author.
		{"library author, no credit", "Marble Gate - Zed Writer.mp3", "", "Marble Gate - Zed Writer"},
		{"library author with initials, no credit", "Marble Gate 3 - Q. V. Example.mp3", "", "Marble Gate 3 - Q. V. Example"},
		{"title text survives", "Dark Harbor - The Salt Wind Part 2 - Unknown Author.mp3", "", "Dark Harbor - The Salt Wind Part 2"},
		{"unknown segment that is not an author", "Dark Harbor - Salt Wind.mp3", "", "Dark Harbor - Salt Wind"},
		{"no separator", "Marble Gate.mp3", "", "Marble Gate"},
		{"only a placeholder after the separator", "Unknown Author - Marble Gate.mp3", "", "Unknown Author - Marble Gate"},
		// 🔴 A title-shaped author row ("Salt Harbor" is both) before the real
		// author: only the segment where the author stands goes, whether the
		// stored author names it or the library does.
		{"title-shaped author row, stored author", "Ember Wars 13 - Salt Harbor - Zed Writer - read by narrator.mp3", "Zed Writer", "Ember Wars 13 - Salt Harbor"},
		{"title-shaped author row, library author", "Ember Wars 13 - Salt Harbor - Zed Writer - read by narrator.mp3", "", "Ember Wars 13 - Salt Harbor"},
		{"title-shaped author row, junk stored author", "Ember Wars 13 - Salt Harbor - Zed Writer - read by narrator.mp3", "read by narrator", "Ember Wars 13 - Salt Harbor"},
		// A stored author that is junk (a chapter label in the author field) is
		// title text in the file name.
		{"junk stored author is not stripped", "Tide Log 1 0 - Prologue + Chapter 1.mp3", "Prologue + Chapter 1", "Tide Log 1 0 - Prologue + Chapter 1"},
		{"junk stored author, credit", "Tide Log 1 0 - Chapter 1 - read by narrator.mp3", "Chapter 1", "Tide Log 1 0 - Chapter 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := titleFromFilenameWith(c.file, c.author, rec); got != c.want {
				t.Errorf("titleFromFilenameWith(%q, %q) = %q, want %q", c.file, c.author, got, c.want)
			}
		})
	}
	// Without the recognisers a name that is not the stored author stays.
	if got := titleFromFilename("Marble Gate - Zed Writer - read by narrator.mp3", ""); got != "Marble Gate - Zed Writer" {
		t.Errorf("no recogniser: got %q", got)
	}
}

// A stem that is only an author credit has no title.
func TestTitleFromFilename_AuthorCreditOnlyStemHasNoTitle(t *testing.T) {
	for _, file := range []string{"read by Some Narrator.mp3", "Read By Some Narrator", "narrated by Some Narrator.m4b", "  read by narrator  .mp3"} {
		if got := titleFromFilename(file, "Some Author"); got != "" {
			t.Errorf("titleFromFilename(%q) = %q, want no title", file, got)
		}
	}
}

// The credit is found on the stem itself: lower-casing a copy first changes its
// byte length for some runes (U+0130 grows by one byte, U+212A shrinks), and the
// index it gave cut the original in the wrong place or past its end.
func TestFilenameHead_NonASCIICaseFoldingDoesNotMisalignTheCut(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"Marble Gate - Unknown Author - read by narrator.mp3", "Marble Gate - Unknown Author"},
		{strings.Repeat("\u0130", 40) + " Gate - READ BY Narrator.mp3", strings.Repeat("\u0130", 40) + " Gate"},
		{strings.Repeat("\u212a", 40) + " Gate - Read By Narrator.mp3", strings.Repeat("\u212a", 40) + " Gate"},
		{"No credit here.mp3", "No credit here"},
	} {
		head, _ := filenameHead(tc.file)
		if head != tc.want {
			t.Errorf("filenameHead(%q) = %q, want %q", tc.file, head, tc.want)
		}
	}
}

func TestDeriveJunkTitleReplacement_FilenameAuthorSuffixIsDropped(t *testing.T) {
	got, method, ok := deriveJunkTitleReplacement("read by narrator", "",
		[]string{"/lib/Books/Marble Gate - Unknown Author - read by narrator.mp3"}, authorRecognisers{})
	if !ok || got != "Marble Gate" || method != "filename" {
		t.Fatalf("got (%q, %q, %v), want (Marble Gate, filename, true)", got, method, ok)
	}
}
