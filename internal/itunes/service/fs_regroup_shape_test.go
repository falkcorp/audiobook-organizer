// file: internal/itunes/service/fs_regroup_shape_test.go
// version: 1.0.0
// guid: 4d2a7f81-6c93-4e50-b8a1-0f5e2c9d3b76
// last-edited: 2026-07-13

package itunesservice

import (
	"fmt"
	"testing"
)

const shatterRoot = "/mnt/bigdata/books/audiobook-organizer"

// sb builds a primary single-file ShatterBook at a path.
func sb(id, path string) ShatterBook {
	return ShatterBook{BookID: id, FilePath: path, FileCount: 1, IsPrimary: true}
}

// findGroup returns the group whose FolderRef ends with the given suffix.
func findGroup(t *testing.T, groups []RegroupGroup, folderSuffix string) RegroupGroup {
	t.Helper()
	for _, g := range groups {
		if len(g.FolderRef) >= len(folderSuffix) && g.FolderRef[len(g.FolderRef)-len(folderSuffix):] == folderSuffix {
			return g
		}
	}
	t.Fatalf("no group with folder ending %q in %+v", folderSuffix, groups)
	return RegroupGroup{}
}

// Classic chapter shatter: `<Book>/<Book> - N/file` → confident multidisc collapse.
func TestClassify_ChapterShatter_Multidisc(t *testing.T) {
	base := shatterRoot + "/Adrian Tchaikovsky/Cage of Souls"
	var books []ShatterBook
	for i := 1; i <= 12; i++ {
		books = append(books, sb(fmt.Sprintf("c%02d", i),
			fmt.Sprintf("%s/Cage of Souls - %d/01.mp3", base, i)))
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d (stats %+v)", len(groups), st)
	}
	g := groups[0]
	if g.Kind != KindMultidisc || !g.Confident {
		t.Errorf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
	if g.FolderRef != base {
		t.Errorf("FolderRef=%q, want %q", g.FolderRef, base)
	}
	if len(g.Members) != 12 {
		t.Errorf("members=%d, want 12", len(g.Members))
	}
	if g.SurvivorTitle != "Cage of Souls" {
		t.Errorf("survivorTitle=%q, want 'Cage of Souls'", g.SurvivorTitle)
	}
	// Members ordered by chapter number.
	for i, m := range g.Members {
		want := fmt.Sprintf("c%02d", i+1)
		if m.BookID != want {
			t.Errorf("member %d = %q, want %q (ordering)", i, m.BookID, want)
		}
	}
}

// Flat multi-track: many numbered tracks directly in one book folder → confident multidisc.
func TestClassify_FlatMultitrack_Multidisc(t *testing.T) {
	base := shatterRoot + "/Neil Gaiman/Coraline"
	var books []ShatterBook
	for i := 1; i <= 8; i++ {
		books = append(books, sb(fmt.Sprintf("f%02d", i),
			fmt.Sprintf("%s/%02d - Chapter.mp3", base, i)))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Coraline")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Errorf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
	if len(g.Members) != 8 {
		t.Errorf("members=%d, want 8", len(g.Members))
	}
}

// Disc subfolders: `<Book>/Disc N/file` → confident multidisc.
func TestClassify_DiscSubfolders_Multidisc(t *testing.T) {
	base := shatterRoot + "/Patrick Rothfuss/The Name of the Wind"
	books := []ShatterBook{
		sb("d1", base+"/Disc 1/track.mp3"),
		sb("d2", base+"/Disc 2/track.mp3"),
		sb("d3", base+"/Disc 3/track.mp3"),
		sb("d4", base+"/CD 4/track.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "The Name of the Wind")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Errorf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
}

// Abridged + Unabridged editions in one folder → version-group hold (not confident).
func TestClassify_VersionGroup(t *testing.T) {
	base := shatterRoot + "/Frank Herbert/Dune"
	books := []ShatterBook{
		sb("v1", base+"/Dune (Unabridged)/01.mp3"),
		sb("v2", base+"/Dune (Abridged)/01.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Dune")
	if g.Kind != KindVersionGroup {
		t.Fatalf("kind=%q, want version-group", g.Kind)
	}
	if g.Confident {
		t.Errorf("version-group must not be confident (needs human primary pick)")
	}
}

// Anthology/omnibus marker → anthology hold.
func TestClassify_Anthology(t *testing.T) {
	base := shatterRoot + "/Isaac Asimov/Foundation Trilogy"
	books := []ShatterBook{
		sb("a1", base+"/Foundation Trilogy - 1/01.mp3"),
		sb("a2", base+"/Foundation Trilogy - 2/01.mp3"),
		sb("a3", base+"/Foundation Trilogy - 3/01.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Foundation Trilogy")
	if g.Kind != KindAnthology {
		t.Fatalf("kind=%q, want anthology", g.Kind)
	}
	if g.Confident {
		t.Errorf("anthology must not be confident")
	}
}

// A genuine single-file book (group of 1) gets NO hold.
func TestClassify_GenuineSingle_NoHold(t *testing.T) {
	books := []ShatterBook{sb("s1", shatterRoot+"/Andy Weir/The Martian/The Martian.m4b")}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("want 0 groups for a lone single file, got %d", len(groups))
	}
	if st.Singletons != 1 {
		t.Errorf("Singletons=%d, want 1 (stats %+v)", st.Singletons, st)
	}
}

// Correctly-stored series volumes under the author dir must NOT be held: grandparent
// is the author, not named after the series (mirrors fs_regroup's prefix ⊆ parent).
func TestClassify_SeriesVolumes_Skipped(t *testing.T) {
	author := shatterRoot + "/Brandon Sanderson"
	books := []ShatterBook{
		sb("m1", author+"/Mistborn - 1/book.m4b"),
		sb("m2", author+"/Mistborn - 2/book.m4b"),
		sb("m3", author+"/Mistborn - 3/book.m4b"),
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("series volumes must not be held, got %d groups: %+v", len(groups), groups)
	}
	if st.DistinctSkip != 1 {
		t.Errorf("DistinctSkip=%d, want 1 (stats %+v)", st.DistinctSkip, st)
	}
}

// A flat dump of DISTINCT books under one non-book folder must NOT be held.
func TestClassify_FlatDumpDistinctBooks_Skipped(t *testing.T) {
	dump := shatterRoot + "/abooks"
	books := []ShatterBook{
		sb("x1", dump+"/The Hobbit.m4b"),
		sb("x2", dump+"/Neuromancer.m4b"),
		sb("x3", dump+"/Snow Crash.m4b"),
	}
	groups, st := ClassifyShatteredFolders(books)
	if len(groups) != 0 {
		t.Fatalf("flat dump of distinct books must not be held, got %d: %+v", len(groups), groups)
	}
	if st.DistinctSkip != 1 {
		t.Errorf("DistinctSkip=%d, want 1 (stats %+v)", st.DistinctSkip, st)
	}
}

// A book folder whose chapter dirs carry two distinct titles → ambiguous hold.
func TestClassify_MixedIdentityChapters_Ambiguous(t *testing.T) {
	base := shatterRoot + "/Various/Split Decision"
	books := []ShatterBook{
		sb("i1", base+"/Split Decision - 1/01.mp3"),
		sb("i2", base+"/Split Decision - 2/01.mp3"),
		sb("i3", base+"/Split Decision - 3/01.mp3"),
		sb("i4", base+"/Other Work - 1/01.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Split Decision")
	if g.Kind != KindAmbiguous {
		t.Errorf("kind=%q, want ambiguous", g.Kind)
	}
	if g.Confident {
		t.Errorf("ambiguous must not be confident")
	}
}

// deriveSurvivorTitle strips a leading "NN - " track prefix, trailing parentheticals,
// and a trailing " - N" number.
func TestDeriveSurvivorTitle(t *testing.T) {
	cases := map[string]string{
		"Cage of Souls":            "Cage of Souls",
		"01 - The Fellowship":      "The Fellowship",
		"Dune (Unabridged)":        "Dune",
		"Dune (Unabridged) (2019)": "Dune",
		"The Name of the Wind - 2": "The Name of the Wind",
	}
	for in, want := range cases {
		if got := deriveSurvivorTitle(in); got != want {
			t.Errorf("deriveSurvivorTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// versionMarkers must not false-positive "abridged" inside "unabridged".
func TestVersionMarkers_UnabridgedOnly(t *testing.T) {
	if _, hasAb := versionMarkers("The Book (Unabridged)"); hasAb {
		t.Errorf("unabridged-only text wrongly flagged abridged")
	}
	hasUn, hasAb := versionMarkers("Dune Unabridged / Dune Abridged")
	if !hasUn || !hasAb {
		t.Errorf("want both markers, got unabridged=%v abridged=%v", hasUn, hasAb)
	}
}
