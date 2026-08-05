// file: internal/plugins/maintenance/junk_title_derive_test.go
// version: 1.0.0
// guid: 51758d2b-4a45-4c0b-9bd0-5b22cc579c19
// last-edited: 2026-08-04

package maintenance

import "testing"

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
	for i := 0; i < 20; i++ {
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
